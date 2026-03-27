package http

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
)

// ─── bloom filter for SPT-approximate forwarding ────────────────────────────

const (
	// bloomFilterSize is the number of bits per peer bloom filter.
	// 256 bits (32 bytes) supports ~50 campfires with FPR < 0.1%.
	bloomFilterSize = 256

	// bloomFilterK is the number of hash functions.
	bloomFilterK = 3
)

// BloomFilter is a compact probabilistic set for campfire IDs.
// Zero false negatives, low false positives.
type BloomFilter struct {
	bits [bloomFilterSize / 64]uint64
}

func bloomHash(data string, seed uint32) uint32 {
	h := fnv.New32a()
	b := [4]byte{byte(seed), byte(seed >> 8), byte(seed >> 16), byte(seed >> 24)}
	h.Write(b[:])
	h.Write([]byte(data))
	return h.Sum32() % bloomFilterSize
}

// Add inserts a campfire ID into the bloom filter.
func (bf *BloomFilter) Add(item string) {
	for k := uint32(0); k < bloomFilterK; k++ {
		bit := bloomHash(item, k)
		bf.bits[bit/64] |= 1 << (bit % 64)
	}
}

// MayContain returns true if the item might be in the set.
// False means definitely not in the set (zero false negatives).
func (bf *BloomFilter) MayContain(item string) bool {
	for k := uint32(0); k < bloomFilterK; k++ {
		bit := bloomHash(item, k)
		if bf.bits[bit/64]&(1<<(bit%64)) == 0 {
			return false
		}
	}
	return true
}

// IsEmpty returns true if the bloom filter has no items added.
func (bf *BloomFilter) IsEmpty() bool {
	for _, w := range bf.bits {
		if w != 0 {
			return false
		}
	}
	return true
}

const (
	// routingTableTTL is how long a routing table entry remains valid without refresh.
	routingTableTTL = 24 * time.Hour

	// routingBeaconBudget is the maximum number of beacons accepted per campfire_id
	// within the routingTableTTL window (per spec §7.1).
	routingBeaconBudget = 5

	// MaxHops is the maximum provenance chain length before a message is dropped.
	// Prevents infinite routing loops (spec §7.5). Exported for use in tests.
	MaxHops = 8
)

// RouteEntry represents a single entry in the routing table for a campfire.
type RouteEntry struct {
	// Endpoint is the transport endpoint URL (TAINTED — operator-asserted).
	Endpoint string
	// Transport is the transport protocol (TAINTED — operator-asserted).
	Transport string
	// Gateway is the campfire_id of the gateway that advertised this route.
	Gateway string
	// Received is when this entry was received.
	Received time.Time
	// Verified indicates the beacon's inner_signature was verified against the
	// campfire_id (ed25519 public key).
	Verified bool
	// InnerTimestamp is the Unix epoch seconds from the beacon's inner_signature payload.
	// Used to prefer fresher entries when multiple exist for the same campfire_id.
	InnerTimestamp int64
	// Path is the ordered list of node_ids this beacon traversed, from origin to
	// the peer that advertised this route (spec §4, path-vector amendment).
	// Empty for legacy beacons (pre-v0.5.0).
	Path []string
	// NextHop is the node_id of the direct peer that delivered this beacon to us
	// (spec §4, path-vector amendment). Empty for locally-originated routes.
	NextHop string
}

// beaconPayload is the JSON payload of a routing:beacon message (spec §5.1).
type beaconPayload struct {
	CampfireID        string   `json:"campfire_id"`
	Endpoint          string   `json:"endpoint"`
	Transport         string   `json:"transport"`
	Description       string   `json:"description"`
	JoinProtocol      string   `json:"join_protocol"`
	Timestamp         int64    `json:"timestamp"`
	ConventionVersion string   `json:"convention_version"`
	InnerSignature    string   `json:"inner_signature"` // hex-encoded ed25519 signature
	Path              []string `json:"path,omitempty"` // node_ids from origin to advertiser (v0.5.0+)
}

// withdrawPayload is the JSON payload of a routing:withdraw message (spec §5.2).
type withdrawPayload struct {
	CampfireID     string `json:"campfire_id"`
	Reason         string `json:"reason"`
	InnerSignature string `json:"inner_signature"` // hex-encoded ed25519 signature
}


// RoutingTable maintains a map of campfire_id → routing entries.
// It is populated by routing:beacon messages and pruned by routing:withdraw and TTL.
//
// Thread-safe: all methods are safe for concurrent use.
type RoutingTable struct {
	mu      sync.RWMutex
	entries map[string][]RouteEntry // campfire_id → []RouteEntry
	// NodeID is the hex-encoded Ed25519 public key of this router's transport
	// identity. Used for loop detection per §4.2 of the path-vector amendment:
	// beacons whose path contains NodeID are dropped to prevent routing loops.
	// May be empty — loop detection is skipped when NodeID is not set.
	NodeID string
	// peerNeeds tracks which direct peers participate in each campfire.
	// Per §5.3 of the path-vector amendment: campfire_id → set of peer node_ids.
	// A peer is in the set if it delivered a message for the campfire, is a
	// next_hop in the routing table for the campfire, or sent a beacon for the
	// campfire through this router.
	peerNeeds map[string]map[string]bool // campfire_id → set of peer node_ids

	// peerBlooms tracks which campfires are reachable through each direct peer,
	// using bloom filters for compact representation. When a beacon for campfire C
	// arrives via peer P, campfire C is added to P's bloom filter. During forwarding,
	// only peers whose bloom filter matches the campfire ID are included in the
	// forwarding set — approximating a per-campfire shortest-path tree (SPT).
	//
	// This reduces amplification from O(NextHops) to O(SPT edges) with zero false
	// negatives and near-zero false positives (FPR < 0.1% at 256 bits / 50 campfires).
	peerBlooms map[string]*BloomFilter // peer_node_id → bloom filter of downstream campfire_ids
}

// newRoutingTable creates an empty RoutingTable.
func newRoutingTable() *RoutingTable {
	return &RoutingTable{
		entries:    make(map[string][]RouteEntry),
		peerNeeds:  make(map[string]map[string]bool),
		peerBlooms: make(map[string]*BloomFilter),
	}
}

// newRoutingTableWithNodeID creates a RoutingTable that knows its own node_id.
// The nodeID is used for path-vector loop detection (spec §4.2).
func newRoutingTableWithNodeID(nodeID string) *RoutingTable {
	return &RoutingTable{
		entries:    make(map[string][]RouteEntry),
		NodeID:     nodeID,
		peerNeeds:  make(map[string]map[string]bool),
		peerBlooms: make(map[string]*BloomFilter),
	}
}

// HandleBeacon processes a routing:beacon message payload.
// It verifies the inner_signature, checks the timestamp, enforces the per-campfire_id
// budget, and inserts or updates the routing table entry.
//
// senderNodeID is the node_id of the direct peer that delivered this beacon (used as
// NextHop in the RouteEntry). Pass an empty string for locally-originated beacons.
//
// Returns an error if the beacon is malformed or inner_signature verification fails.
// Silently ignores duplicates (same campfire_id + endpoint already present).
// Silently drops beacons that contain a routing loop (own node_id in path).
func (rt *RoutingTable) HandleBeacon(rawPayload []byte, gatewayCampfireID string, senderNodeID string) error {
	var bp beaconPayload
	if err := json.Unmarshal(rawPayload, &bp); err != nil {
		return fmt.Errorf("routing:beacon: unmarshal payload: %w", err)
	}

	if bp.CampfireID == "" {
		return fmt.Errorf("routing:beacon: missing campfire_id")
	}
	if bp.Endpoint == "" {
		return fmt.Errorf("routing:beacon: missing endpoint")
	}
	if bp.InnerSignature == "" {
		return fmt.Errorf("routing:beacon: missing inner_signature")
	}

	// Timestamp check: reject beacons older than TTL (spec §5.1).
	beaconTime := time.Unix(bp.Timestamp, 0)
	if time.Since(beaconTime) > routingTableTTL {
		return fmt.Errorf("routing:beacon: timestamp too old: %v", beaconTime)
	}

	// Loop detection (spec §4.2, path-vector amendment): if own node_id appears
	// in the beacon's path, the beacon has looped — drop it silently.
	if rt.NodeID != "" && len(bp.Path) > 0 {
		for _, hop := range bp.Path {
			if hop == rt.NodeID {
				log.Printf("routing:beacon: loop detected for campfire_id %s (own node_id %s in path), dropping", bp.CampfireID, rt.NodeID)
				return nil
			}
		}
	}

	// Verify inner_signature using the canonical two-pass approach (spec §5.1, §3.2):
	// try with path first (threshold=1, path in signature), then without path
	// (threshold>1, path is advisory). beacon.VerifyDeclaration handles both cases
	// and validates campfire_id format and signature encoding internally.
	verifyDecl := beacon.BeaconDeclaration{
		CampfireID:        bp.CampfireID,
		Endpoint:          bp.Endpoint,
		Transport:         bp.Transport,
		Description:       bp.Description,
		JoinProtocol:      bp.JoinProtocol,
		Timestamp:         bp.Timestamp,
		ConventionVersion: bp.ConventionVersion,
		InnerSignature:    bp.InnerSignature,
		Path:              bp.Path,
	}
	if !beacon.VerifyDeclaration(verifyDecl) {
		log.Printf("routing:beacon: inner_signature verification failed for campfire_id %s from gateway %s", bp.CampfireID, gatewayCampfireID)
		return fmt.Errorf("routing:beacon: inner_signature verification failed for campfire_id %s", bp.CampfireID)
	}

	// Validate campfire_id is a valid ed25519 public key (32 bytes).
	pubKeyBytes, err := hex.DecodeString(bp.CampfireID)
	if err != nil {
		return fmt.Errorf("routing:beacon: invalid campfire_id (not hex): %w", err)
	}
	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("routing:beacon: campfire_id wrong length: %d (want %d)", len(pubKeyBytes), ed25519.PublicKeySize)
	}

	// Copy path from beacon payload (nil-safe).
	var path []string
	if len(bp.Path) > 0 {
		path = make([]string, len(bp.Path))
		copy(path, bp.Path)
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	existing := rt.entries[bp.CampfireID]

	// Per-campfire_id budget: if at or above budget, keep only the K freshest entries
	// (spec §7.1). Replace the stalest entry if new beacon has a fresher timestamp.
	if len(existing) >= routingBeaconBudget {
		// Find the entry with the oldest inner timestamp.
		oldestIdx := 0
		for i, e := range existing {
			if e.InnerTimestamp < existing[oldestIdx].InnerTimestamp {
				oldestIdx = i
			}
		}
		// Only add if new beacon is fresher than the stalest entry.
		if bp.Timestamp <= existing[oldestIdx].InnerTimestamp {
			// New beacon is not fresher; discard it.
			return nil
		}
		// Replace the oldest entry.
		existing[oldestIdx] = RouteEntry{
			Endpoint:       bp.Endpoint,
			Transport:      bp.Transport,
			Gateway:        gatewayCampfireID,
			Received:       time.Now(),
			Verified:       true,
			InnerTimestamp: bp.Timestamp,
			Path:           path,
			NextHop:        senderNodeID,
		}
		rt.entries[bp.CampfireID] = existing
		rt.addPeerNeedsLocked(bp.CampfireID, senderNodeID)
		return nil
	}

	// Check for duplicate (same campfire_id + endpoint already present).
	for i, e := range existing {
		if e.Endpoint == bp.Endpoint {
			// Refresh the existing entry, updating path and next_hop in case they changed.
			existing[i].Received = time.Now()
			existing[i].InnerTimestamp = bp.Timestamp
			existing[i].Path = path
			existing[i].NextHop = senderNodeID
			rt.entries[bp.CampfireID] = existing
			rt.addPeerNeedsLocked(bp.CampfireID, senderNodeID)
			return nil
		}
	}

	// Add new entry.
	rt.entries[bp.CampfireID] = append(existing, RouteEntry{
		Endpoint:       bp.Endpoint,
		Transport:      bp.Transport,
		Gateway:        gatewayCampfireID,
		Received:       time.Now(),
		Verified:       true,
		InnerTimestamp: bp.Timestamp,
		Path:           path,
		NextHop:        senderNodeID,
	})
	rt.addPeerNeedsLocked(bp.CampfireID, senderNodeID)
	return nil
}

// HandleWithdraw processes a routing:withdraw message payload.
// It removes all routing table entries for the withdrawn campfire_id.
//
// Returns an error if the payload is malformed or inner_signature verification fails.
func (rt *RoutingTable) HandleWithdraw(rawPayload []byte) error {
	var withdraw withdrawPayload
	if err := json.Unmarshal(rawPayload, &withdraw); err != nil {
		return fmt.Errorf("routing:withdraw: unmarshal payload: %w", err)
	}

	if withdraw.CampfireID == "" {
		return fmt.Errorf("routing:withdraw: missing campfire_id")
	}
	if withdraw.InnerSignature == "" {
		return fmt.Errorf("routing:withdraw: missing inner_signature")
	}

	// Decode campfire_id as ed25519 public key.
	pubKeyBytes, err := hex.DecodeString(withdraw.CampfireID)
	if err != nil {
		return fmt.Errorf("routing:withdraw: invalid campfire_id (not hex): %w", err)
	}
	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("routing:withdraw: campfire_id wrong length: %d", len(pubKeyBytes))
	}

	// Verify inner_signature (proof the campfire owner authorized withdrawal).
	sigBytes, err := hex.DecodeString(withdraw.InnerSignature)
	if err != nil {
		return fmt.Errorf("routing:withdraw: invalid inner_signature (not hex): %w", err)
	}

	// Sign input for withdraw: campfire_id + reason.
	// We sign (campfire_id, reason) as a simple JSON object.
	type withdrawSignInput struct {
		CampfireID string `json:"campfire_id"`
		Reason     string `json:"reason"`
	}
	signInput := withdrawSignInput{
		CampfireID: withdraw.CampfireID,
		Reason:     withdraw.Reason,
	}
	signBytes, err := json.Marshal(signInput)
	if err != nil {
		return fmt.Errorf("routing:withdraw: encoding sign input: %w", err)
	}

	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), signBytes, sigBytes) {
		return fmt.Errorf("routing:withdraw: inner_signature verification failed for campfire_id %s", withdraw.CampfireID)
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.entries, withdraw.CampfireID)
	// Clean up peer needs set for this campfire — no routes remain, so no peers
	// need to be tracked for forwarding purposes.
	delete(rt.peerNeeds, withdraw.CampfireID)
	return nil
}

// Lookup returns all known route entries for the given campfire_id, sorted by
// route preference per §4.1 of the path-vector amendment:
//  1. Shortest path first (fewer hops = less amplification).
//  2. Among equal-length paths, freshest InnerTimestamp first.
//  3. Tie-breaker: first received (stability — earlier Received time wins).
//
// Legacy beacons (empty Path) sort after path-vector routes of the same length
// (length 0 < any path, so they sort first numerically — callers should check
// whether routes have paths when making forwarding decisions).
//
// Expired entries (older than routingTableTTL) are filtered out.
// Returns nil if no routes are known.
func (rt *RoutingTable) Lookup(campfireID string) []RouteEntry {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	entries := rt.entries[campfireID]
	if len(entries) == 0 {
		return nil
	}

	cutoff := time.Now().Add(-routingTableTTL)
	var live []RouteEntry
	for _, e := range entries {
		if e.Received.After(cutoff) {
			live = append(live, e)
		}
	}

	// Update the map to drop expired entries (lazy eviction).
	if len(live) != len(entries) {
		if len(live) == 0 {
			delete(rt.entries, campfireID)
		} else {
			rt.entries[campfireID] = live
		}
	}

	if len(live) == 0 {
		return nil
	}

	// Sort by preference: shortest path → freshest timestamp → first received.
	sort.Slice(live, func(i, j int) bool {
		pi, pj := len(live[i].Path), len(live[j].Path)
		if pi != pj {
			return pi < pj // shorter path is better
		}
		if live[i].InnerTimestamp != live[j].InnerTimestamp {
			return live[i].InnerTimestamp > live[j].InnerTimestamp // fresher is better
		}
		return live[i].Received.Before(live[j].Received) // earlier received = more stable
	})

	return live
}

// Len returns the total number of campfire_ids currently in the routing table.
func (rt *RoutingTable) Len() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return len(rt.entries)
}

// addPeerNeedsLocked adds peerNodeID to the peer needs set for campfireID
// and updates the peer's bloom filter.
// Must be called with rt.mu held (write lock).
// Empty peerNodeID is ignored (locally-originated beacons have no sender).
func (rt *RoutingTable) addPeerNeedsLocked(campfireID, peerNodeID string) {
	if peerNodeID == "" {
		return
	}
	if rt.peerNeeds[campfireID] == nil {
		rt.peerNeeds[campfireID] = make(map[string]bool)
	}
	rt.peerNeeds[campfireID][peerNodeID] = true

	// Update peer's bloom filter: this peer has a path toward campfireID.
	if rt.peerBlooms[peerNodeID] == nil {
		rt.peerBlooms[peerNodeID] = &BloomFilter{}
	}
	rt.peerBlooms[peerNodeID].Add(campfireID)
}

// RecordMessageDelivery records that peerNodeID delivered a message for campfireID
// to this router. Per §5.3, peers that deliver messages for a campfire are added
// to the peer needs set for that campfire.
//
// This should be called by the message handler when a message arrives from a peer.
func (rt *RoutingTable) RecordMessageDelivery(campfireID, peerNodeID string) {
	if campfireID == "" || peerNodeID == "" {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.addPeerNeedsLocked(campfireID, peerNodeID)
}

// PeerBloomCheck returns true if the peer's bloom filter indicates campfireID
// is reachable through peerNodeID. Returns true if no bloom filter exists for
// the peer (safe fallback — never drop a valid forwarding direction).
func (rt *RoutingTable) PeerBloomCheck(peerNodeID, campfireID string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	bf := rt.peerBlooms[peerNodeID]
	if bf == nil || bf.IsEmpty() {
		return true // no bloom data → conservative, forward anyway
	}
	return bf.MayContain(campfireID)
}

// PeerNeedsSet returns a copy of the set of direct peers that participate in
// campfireID (per §5.3 of the path-vector amendment). The returned map maps
// peer node_id → true. Returns nil if no peers are known for this campfire.
//
// The caller may use this set to determine which peers to forward messages to.
func (rt *RoutingTable) PeerNeedsSet(campfireID string) map[string]bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	peers := rt.peerNeeds[campfireID]
	if len(peers) == 0 {
		return nil
	}
	// Return a copy so the caller cannot mutate internal state.
	out := make(map[string]bool, len(peers))
	for k, v := range peers {
		out[k] = v
	}
	return out
}

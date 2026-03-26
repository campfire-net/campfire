package http

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
)

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
}

// beaconPayload is the JSON payload of a routing:beacon message (spec §5.1).
type beaconPayload struct {
	CampfireID        string `json:"campfire_id"`
	Endpoint          string `json:"endpoint"`
	Transport         string `json:"transport"`
	Description       string `json:"description"`
	JoinProtocol      string `json:"join_protocol"`
	Timestamp         int64  `json:"timestamp"`
	ConventionVersion string `json:"convention_version"`
	InnerSignature    string `json:"inner_signature"` // hex-encoded ed25519 signature
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
}

// newRoutingTable creates an empty RoutingTable.
func newRoutingTable() *RoutingTable {
	return &RoutingTable{
		entries: make(map[string][]RouteEntry),
	}
}

// HandleBeacon processes a routing:beacon message payload.
// It verifies the inner_signature, checks the timestamp, enforces the per-campfire_id
// budget, and inserts or updates the routing table entry.
//
// Returns an error if the beacon is malformed or inner_signature verification fails.
// Silently ignores duplicates (same campfire_id + endpoint already present).
func (rt *RoutingTable) HandleBeacon(rawPayload []byte, gatewayCampfireID string) error {
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

	// Decode campfire_id as ed25519 public key.
	pubKeyBytes, err := hex.DecodeString(bp.CampfireID)
	if err != nil {
		return fmt.Errorf("routing:beacon: invalid campfire_id (not hex): %w", err)
	}
	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("routing:beacon: campfire_id wrong length: %d (want %d)", len(pubKeyBytes), ed25519.PublicKeySize)
	}

	// Verify inner_signature (spec §5.1 — MUST verify before acting).
	sigBytes, err := hex.DecodeString(bp.InnerSignature)
	if err != nil {
		return fmt.Errorf("routing:beacon: invalid inner_signature (not hex): %w", err)
	}

	// Build the signed input using the canonical beacon package function.
	// This ensures signing and verification always use identical JSON encoding.
	signBytes, err := beacon.MarshalInnerSignInput(beacon.BeaconDeclaration{
		CampfireID:        bp.CampfireID,
		Endpoint:          bp.Endpoint,
		Transport:         bp.Transport,
		Description:       bp.Description,
		JoinProtocol:      bp.JoinProtocol,
		Timestamp:         bp.Timestamp,
		ConventionVersion: bp.ConventionVersion,
	})
	if err != nil {
		return fmt.Errorf("routing:beacon: encoding sign input: %w", err)
	}

	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), signBytes, sigBytes) {
		log.Printf("routing:beacon: inner_signature verification failed for campfire_id %s from gateway %s", bp.CampfireID, gatewayCampfireID)
		return fmt.Errorf("routing:beacon: inner_signature verification failed for campfire_id %s", bp.CampfireID)
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
		}
		rt.entries[bp.CampfireID] = existing
		return nil
	}

	// Check for duplicate (same campfire_id + endpoint already present).
	for i, e := range existing {
		if e.Endpoint == bp.Endpoint {
			// Refresh the existing entry.
			existing[i].Received = time.Now()
			existing[i].InnerTimestamp = bp.Timestamp
			rt.entries[bp.CampfireID] = existing
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
	})
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
	return nil
}

// Lookup returns all known route entries for the given campfire_id.
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

	return live
}

// Len returns the total number of campfire_ids currently in the routing table.
func (rt *RoutingTable) Len() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return len(rt.entries)
}

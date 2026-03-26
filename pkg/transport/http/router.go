package http

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
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

// innerBeaconSignInput is what is signed by the campfire key in a routing:beacon.
// The fields match the spec §5.1: (campfire_id, endpoint, transport, description,
// join_protocol, timestamp, convention_version).
// We use JSON-canonical encoding (sorted keys via struct tags) for determinism.
type innerBeaconSignInput struct {
	CampfireID        string `json:"campfire_id"`
	ConventionVersion string `json:"convention_version"`
	Description       string `json:"description"`
	Endpoint          string `json:"endpoint"`
	JoinProtocol      string `json:"join_protocol"`
	Timestamp         int64  `json:"timestamp"`
	Transport         string `json:"transport"`
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
	var beacon beaconPayload
	if err := json.Unmarshal(rawPayload, &beacon); err != nil {
		return fmt.Errorf("routing:beacon: unmarshal payload: %w", err)
	}

	if beacon.CampfireID == "" {
		return fmt.Errorf("routing:beacon: missing campfire_id")
	}
	if beacon.Endpoint == "" {
		return fmt.Errorf("routing:beacon: missing endpoint")
	}
	if beacon.InnerSignature == "" {
		return fmt.Errorf("routing:beacon: missing inner_signature")
	}

	// Timestamp check: reject beacons older than TTL (spec §5.1).
	beaconTime := time.Unix(beacon.Timestamp, 0)
	if time.Since(beaconTime) > routingTableTTL {
		return fmt.Errorf("routing:beacon: timestamp too old: %v", beaconTime)
	}

	// Decode campfire_id as ed25519 public key.
	pubKeyBytes, err := hex.DecodeString(beacon.CampfireID)
	if err != nil {
		return fmt.Errorf("routing:beacon: invalid campfire_id (not hex): %w", err)
	}
	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("routing:beacon: campfire_id wrong length: %d (want %d)", len(pubKeyBytes), ed25519.PublicKeySize)
	}

	// Verify inner_signature (spec §5.1 — MUST verify before acting).
	sigBytes, err := hex.DecodeString(beacon.InnerSignature)
	if err != nil {
		return fmt.Errorf("routing:beacon: invalid inner_signature (not hex): %w", err)
	}

	// Build the signed input in the same canonical form as the beacon creator.
	// Fields: campfire_id, endpoint, transport, description, join_protocol, timestamp, convention_version.
	signInput := innerBeaconSignInput{
		CampfireID:        beacon.CampfireID,
		ConventionVersion: beacon.ConventionVersion,
		Description:       beacon.Description,
		Endpoint:          beacon.Endpoint,
		JoinProtocol:      beacon.JoinProtocol,
		Timestamp:         beacon.Timestamp,
		Transport:         beacon.Transport,
	}
	signBytes, err := json.Marshal(signInput)
	if err != nil {
		return fmt.Errorf("routing:beacon: encoding sign input: %w", err)
	}

	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), signBytes, sigBytes) {
		log.Printf("routing:beacon: inner_signature verification failed for campfire_id %s from gateway %s", beacon.CampfireID, gatewayCampfireID)
		return fmt.Errorf("routing:beacon: inner_signature verification failed for campfire_id %s", beacon.CampfireID)
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	existing := rt.entries[beacon.CampfireID]

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
		if beacon.Timestamp <= existing[oldestIdx].InnerTimestamp {
			// New beacon is not fresher; discard it.
			return nil
		}
		// Replace the oldest entry.
		existing[oldestIdx] = RouteEntry{
			Endpoint:       beacon.Endpoint,
			Transport:      beacon.Transport,
			Gateway:        gatewayCampfireID,
			Received:       time.Now(),
			Verified:       true,
			InnerTimestamp: beacon.Timestamp,
		}
		rt.entries[beacon.CampfireID] = existing
		return nil
	}

	// Check for duplicate (same campfire_id + endpoint already present).
	for i, e := range existing {
		if e.Endpoint == beacon.Endpoint {
			// Refresh the existing entry.
			existing[i].Received = time.Now()
			existing[i].InnerTimestamp = beacon.Timestamp
			rt.entries[beacon.CampfireID] = existing
			return nil
		}
	}

	// Add new entry.
	rt.entries[beacon.CampfireID] = append(existing, RouteEntry{
		Endpoint:       beacon.Endpoint,
		Transport:      beacon.Transport,
		Gateway:        gatewayCampfireID,
		Received:       time.Now(),
		Verified:       true,
		InnerTimestamp: beacon.Timestamp,
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

package http

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
)

// Unit tests for RoutingTable.

// makeBeaconPayload creates a valid routing:beacon payload signed by the given campfire key.
func makeBeaconPayload(t *testing.T, campfirePriv ed25519.PrivateKey, campfirePub ed25519.PublicKey, endpoint, transport, gateway string) []byte {
	t.Helper()
	campfireIDHex := hex.EncodeToString(campfirePub)
	ts := time.Now().Unix()

	decl := beacon.BeaconDeclaration{
		CampfireID:        campfireIDHex,
		ConventionVersion: "0.4.2",
		Description:       "test campfire",
		Endpoint:          endpoint,
		JoinProtocol:      "open",
		Timestamp:         ts,
		Transport:         transport,
	}
	signBytes, err := beacon.MarshalInnerSignInput(decl)
	if err != nil {
		t.Fatalf("marshaling beacon sign input: %v", err)
	}
	sig := ed25519.Sign(campfirePriv, signBytes)

	payload := beaconPayload{
		CampfireID:        campfireIDHex,
		Endpoint:          endpoint,
		Transport:         transport,
		Description:       "test campfire",
		JoinProtocol:      "open",
		Timestamp:         ts,
		ConventionVersion: "0.4.2",
		InnerSignature:    hex.EncodeToString(sig),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshaling beacon payload: %v", err)
	}
	return b
}

// makeWithdrawPayload creates a valid routing:withdraw payload signed by the given campfire key.
func makeWithdrawPayload(t *testing.T, campfirePriv ed25519.PrivateKey, campfirePub ed25519.PublicKey, reason string) []byte {
	t.Helper()
	campfireIDHex := hex.EncodeToString(campfirePub)

	type withdrawSignInput struct {
		CampfireID string `json:"campfire_id"`
		Reason     string `json:"reason"`
	}
	signInput := withdrawSignInput{
		CampfireID: campfireIDHex,
		Reason:     reason,
	}
	signBytes, err := json.Marshal(signInput)
	if err != nil {
		t.Fatalf("marshaling withdraw sign input: %v", err)
	}
	sig := ed25519.Sign(campfirePriv, signBytes)

	payload := withdrawPayload{
		CampfireID:     campfireIDHex,
		Reason:         reason,
		InnerSignature: hex.EncodeToString(sig),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshaling withdraw payload: %v", err)
	}
	return b
}

// TestRoutingTableInsertFromBeacon verifies that a valid beacon inserts an entry.
func TestRoutingTableInsertFromBeacon(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := newRoutingTable()
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", "gateway-1")
	if err := rt.HandleBeacon(payload, "gateway-1", ""); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	campfireIDHex := hex.EncodeToString(cfPub)
	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Endpoint != "http://example.com:8080" {
		t.Errorf("endpoint = %q, want %q", routes[0].Endpoint, "http://example.com:8080")
	}
	if routes[0].Transport != "p2p-http" {
		t.Errorf("transport = %q, want %q", routes[0].Transport, "p2p-http")
	}
	if routes[0].Gateway != "gateway-1" {
		t.Errorf("gateway = %q, want %q", routes[0].Gateway, "gateway-1")
	}
	if !routes[0].Verified {
		t.Error("route should be verified")
	}
}

// TestRoutingTableRejectsInvalidInnerSignature verifies that a beacon with a bad
// inner_signature is rejected.
func TestRoutingTableRejectsInvalidInnerSignature(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := newRoutingTable()

	// Create a valid beacon then tamper with the endpoint (signature will no longer match).
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", "gateway-1")
	var p beaconPayload
	json.Unmarshal(payload, &p)
	p.Endpoint = "http://evil.example.com:9999" // tamper
	tampered, _ := json.Marshal(p)

	if err := rt.HandleBeacon(tampered, "gateway-1", ""); err == nil {
		t.Error("expected HandleBeacon to fail for tampered beacon, got nil error")
	}

	campfireIDHex := hex.EncodeToString(cfPub)
	if routes := rt.Lookup(campfireIDHex); len(routes) != 0 {
		t.Errorf("no routes should be stored for failed beacon, got %d", len(routes))
	}
}

// TestRoutingTableTTL verifies that entries expire after routingTableTTL.
// This test uses a shortened TTL injected directly into the entry.
func TestRoutingTableTTL(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := newRoutingTable()
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", "gw")
	if err := rt.HandleBeacon(payload, "gw", ""); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	campfireIDHex := hex.EncodeToString(cfPub)

	// Backdate the entry to be older than TTL.
	rt.mu.Lock()
	for i := range rt.entries[campfireIDHex] {
		rt.entries[campfireIDHex][i].Received = time.Now().Add(-routingTableTTL - time.Second)
	}
	rt.mu.Unlock()

	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 0 {
		t.Errorf("expected 0 routes after TTL, got %d", len(routes))
	}
}

// TestRoutingTableBeaconBudget verifies that at most routingBeaconBudget entries
// are kept per campfire_id and the stalest is evicted when the budget is exceeded.
func TestRoutingTableBeaconBudget(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	// Insert routingBeaconBudget beacons for different endpoints.
	// We need to manually craft payloads with different timestamps to get distinct entries.
	for i := 0; i < routingBeaconBudget; i++ {
		endpoint := "http://instance-" + string(rune('a'+i)) + ".example.com:8080"
		payload := makeBeaconPayload(t, cfPriv, cfPub, endpoint, "p2p-http", "gw")
		if err := rt.HandleBeacon(payload, "gw", ""); err != nil {
			t.Fatalf("HandleBeacon[%d]: %v", i, err)
		}
		// Small sleep to ensure distinct timestamps.
		time.Sleep(time.Millisecond)
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) != routingBeaconBudget {
		t.Fatalf("expected %d routes, got %d", routingBeaconBudget, len(routes))
	}
}

// TestRoutingTableWithdraw verifies that routing:withdraw removes all entries
// for the campfire_id.
func TestRoutingTableWithdraw(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", "gw")
	if err := rt.HandleBeacon(payload, "gw", ""); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	// Verify entry was added.
	if routes := rt.Lookup(campfireIDHex); len(routes) == 0 {
		t.Fatal("expected route after beacon, got none")
	}

	// Withdraw.
	withdrawPayloadBytes := makeWithdrawPayload(t, cfPriv, cfPub, "going offline")
	if err := rt.HandleWithdraw(withdrawPayloadBytes); err != nil {
		t.Fatalf("HandleWithdraw: %v", err)
	}

	// Entry should be gone.
	if routes := rt.Lookup(campfireIDHex); len(routes) != 0 {
		t.Errorf("expected 0 routes after withdraw, got %d", len(routes))
	}
}

// TestRoutingTableWithdrawRejectsInvalidSignature verifies that a withdraw with
// bad inner_signature is rejected.
func TestRoutingTableWithdrawRejectsInvalidSignature(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	// Insert a valid beacon first.
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", "gw")
	rt.HandleBeacon(payload, "gw", "") //nolint:errcheck

	// Craft a withdraw with a bad signature (all zeros).
	badWithdraw := withdrawPayload{
		CampfireID:     campfireIDHex,
		Reason:         "spoofed",
		InnerSignature: hex.EncodeToString(make([]byte, ed25519.SignatureSize)),
	}
	b, _ := json.Marshal(badWithdraw)
	if err := rt.HandleWithdraw(b); err == nil {
		t.Error("expected HandleWithdraw to fail for invalid signature, got nil")
	}

	// Entry should still be present.
	if routes := rt.Lookup(campfireIDHex); len(routes) == 0 {
		t.Error("entry should not have been removed by invalid withdraw")
	}
}

// TestRoutingTableLookupEmpty verifies that Lookup returns nil for unknown campfire_id.
func TestRoutingTableLookupEmpty(t *testing.T) {
	rt := newRoutingTable()
	if routes := rt.Lookup("unknown-campfire-id"); routes != nil {
		t.Errorf("expected nil for unknown campfire, got %v", routes)
	}
}

// TestRoutingTableLen verifies the Len method.
func TestRoutingTableLen(t *testing.T) {
	rt := newRoutingTable()
	if rt.Len() != 0 {
		t.Errorf("empty table should have Len 0, got %d", rt.Len())
	}

	cfPub1, cfPriv1, _ := ed25519.GenerateKey(nil)
	cfPub2, cfPriv2, _ := ed25519.GenerateKey(nil)

	rt.HandleBeacon(makeBeaconPayload(t, cfPriv1, cfPub1, "http://a.example.com", "p2p-http", "gw"), "gw", "") //nolint:errcheck
	rt.HandleBeacon(makeBeaconPayload(t, cfPriv2, cfPub2, "http://b.example.com", "p2p-http", "gw"), "gw", "") //nolint:errcheck

	if rt.Len() != 2 {
		t.Errorf("expected Len 2, got %d", rt.Len())
	}
}

// TestRoutingTableRejectsOldTimestamp verifies that a beacon with an old timestamp
// (older than routingTableTTL) is rejected.
func TestRoutingTableRejectsOldTimestamp(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	// Build a beacon with an old timestamp (25 hours ago).
	oldTS := time.Now().Add(-25 * time.Hour).Unix()
	oldDecl := beacon.BeaconDeclaration{
		CampfireID:        campfireIDHex,
		ConventionVersion: "0.4.2",
		Description:       "old campfire",
		Endpoint:          "http://old.example.com",
		JoinProtocol:      "open",
		Timestamp:         oldTS,
		Transport:         "p2p-http",
	}
	signBytes, _ := beacon.MarshalInnerSignInput(oldDecl)
	sig := ed25519.Sign(cfPriv, signBytes)

	payload := beaconPayload{
		CampfireID:        campfireIDHex,
		Endpoint:          "http://old.example.com",
		Transport:         "p2p-http",
		Description:       "old campfire",
		JoinProtocol:      "open",
		Timestamp:         oldTS,
		ConventionVersion: "0.4.2",
		InnerSignature:    hex.EncodeToString(sig),
	}
	b, _ := json.Marshal(payload)

	if err := rt.HandleBeacon(b, "gw", ""); err == nil {
		t.Error("expected HandleBeacon to reject beacon with old timestamp, got nil")
	}

	if routes := rt.Lookup(campfireIDHex); len(routes) != 0 {
		t.Errorf("no routes should be stored for old beacon, got %d", len(routes))
	}
}

// makeBeaconPayloadWithPath creates a valid routing:beacon payload with a path field.
// The path is not covered by inner_signature (matches threshold>1 advisory-path behavior,
// which is what we can test without key-material for each hop).
func makeBeaconPayloadWithPath(t *testing.T, campfirePriv ed25519.PrivateKey, campfirePub ed25519.PublicKey, endpoint, transport string, ts int64, path []string) []byte {
	t.Helper()
	campfireIDHex := hex.EncodeToString(campfirePub)

	decl := beacon.BeaconDeclaration{
		CampfireID:        campfireIDHex,
		ConventionVersion: "0.5.0",
		Description:       "test campfire",
		Endpoint:          endpoint,
		JoinProtocol:      "open",
		Timestamp:         ts,
		Transport:         transport,
	}
	signBytes, err := beacon.MarshalInnerSignInput(decl)
	if err != nil {
		t.Fatalf("marshaling beacon sign input: %v", err)
	}
	sig := ed25519.Sign(campfirePriv, signBytes)

	payload := beaconPayload{
		CampfireID:        campfireIDHex,
		Endpoint:          endpoint,
		Transport:         transport,
		Description:       "test campfire",
		JoinProtocol:      "open",
		Timestamp:         ts,
		ConventionVersion: "0.5.0",
		InnerSignature:    hex.EncodeToString(sig),
		Path:              path,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshaling beacon payload: %v", err)
	}
	return b
}

// TestRouteEntryStoresPathAndNextHop verifies that a beacon with a path field
// stores the path and next_hop in the RouteEntry.
func TestRouteEntryStoresPathAndNextHop(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	path := []string{"nodeA", "nodeB", "nodeC"}
	ts := time.Now().Unix()
	payload := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", ts, path)

	if err := rt.HandleBeacon(payload, "gw", "nodeC"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if got, want := len(routes[0].Path), 3; got != want {
		t.Errorf("path length = %d, want %d", got, want)
	}
	for i, hop := range path {
		if routes[0].Path[i] != hop {
			t.Errorf("path[%d] = %q, want %q", i, routes[0].Path[i], hop)
		}
	}
	if routes[0].NextHop != "nodeC" {
		t.Errorf("next_hop = %q, want %q", routes[0].NextHop, "nodeC")
	}
}

// TestRouteSelectionPrefersShortestPath verifies that Lookup returns routes
// sorted shortest path first.
func TestRouteSelectionPrefersShortestPath(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	ts := time.Now().Unix()

	// Insert a 3-hop route first, then a 1-hop route.
	long := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://long.example.com:8080", "p2p-http", ts, []string{"nodeA", "nodeB", "nodeC"})
	if err := rt.HandleBeacon(long, "gw", "nodeC"); err != nil {
		t.Fatalf("HandleBeacon (long path): %v", err)
	}
	time.Sleep(time.Millisecond) // ensure distinct Received times

	short := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://short.example.com:8080", "p2p-http", ts, []string{"nodeX"})
	if err := rt.HandleBeacon(short, "gw", "nodeX"); err != nil {
		t.Fatalf("HandleBeacon (short path): %v", err)
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	// Best route (index 0) should have the shortest path.
	if got := len(routes[0].Path); got != 1 {
		t.Errorf("best route path length = %d, want 1 (shortest path first)", got)
	}
	if got := len(routes[1].Path); got != 3 {
		t.Errorf("second route path length = %d, want 3", got)
	}
}

// TestRouteSelectionUsesTimestampTiebreaker verifies that among routes of equal
// path length, the freshest InnerTimestamp is preferred.
func TestRouteSelectionUsesTimestampTiebreaker(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	now := time.Now().Unix()
	older := now - 100
	newer := now

	// Insert two routes with the same path length (1) but different timestamps.
	payloadOld := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://old.example.com:8080", "p2p-http", older, []string{"nodeA"})
	if err := rt.HandleBeacon(payloadOld, "gw", "nodeA"); err != nil {
		t.Fatalf("HandleBeacon (old ts): %v", err)
	}

	payloadNew := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://new.example.com:8080", "p2p-http", newer, []string{"nodeB"})
	if err := rt.HandleBeacon(payloadNew, "gw", "nodeB"); err != nil {
		t.Fatalf("HandleBeacon (new ts): %v", err)
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	// Best route should have the fresher timestamp.
	if routes[0].InnerTimestamp != newer {
		t.Errorf("best route InnerTimestamp = %d, want %d (fresher)", routes[0].InnerTimestamp, newer)
	}
	if routes[1].InnerTimestamp != older {
		t.Errorf("second route InnerTimestamp = %d, want %d (older)", routes[1].InnerTimestamp, older)
	}
}

// TestLoopDetectionDropsBeaconWithOwnNodeID verifies that when a beacon's path
// contains this router's own node_id, the beacon is silently dropped.
func TestLoopDetectionDropsBeaconWithOwnNodeID(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)

	const ownNodeID = "my-own-node-id"
	rt := newRoutingTableWithNodeID(ownNodeID)

	// A beacon whose path includes our own node_id — this is a loop.
	path := []string{"nodeA", ownNodeID, "nodeB"}
	ts := time.Now().Unix()
	payload := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://loop.example.com:8080", "p2p-http", ts, path)

	// HandleBeacon must return nil (silent drop, not an error).
	if err := rt.HandleBeacon(payload, "gw", "nodeB"); err != nil {
		t.Errorf("loop-detected beacon should be silently dropped (nil error), got: %v", err)
	}

	// No route should have been installed.
	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 0 {
		t.Errorf("looped beacon should not install route, got %d routes", len(routes))
	}
}

// TestLoopDetectionAllowsBeaconWithoutOwnNodeID verifies that a beacon whose
// path does NOT contain own node_id passes through normally.
func TestLoopDetectionAllowsBeaconWithoutOwnNodeID(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)

	const ownNodeID = "my-own-node-id"
	rt := newRoutingTableWithNodeID(ownNodeID)

	// A beacon whose path does NOT include our node_id.
	path := []string{"nodeA", "nodeB", "nodeC"}
	ts := time.Now().Unix()
	payload := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://ok.example.com:8080", "p2p-http", ts, path)

	if err := rt.HandleBeacon(payload, "gw", "nodeC"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
}

// TestPeerNeedsSet_PopulatedByBeacon verifies that HandleBeacon adds the
// senderNodeID to the peer needs set for the campfire (§5.3, source: peers
// that sent a beacon for C through this router).
func TestPeerNeedsSet_PopulatedByBeacon(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)

	rt := newRoutingTableWithNodeID("self-node")
	ts := time.Now().Unix()

	// senderNodeID is "peerA"; nextHop is also "peerA" (the beacon sender and next_hop are the same peer).
	path := []string{"originNode", "peerA"}
	payload := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", ts, path)
	if err := rt.HandleBeacon(payload, "gw", "peerA"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	needs := rt.PeerNeedsSet(campfireIDHex)
	if needs == nil {
		t.Fatal("PeerNeedsSet should not be nil after beacon from peerA")
	}
	if !needs["peerA"] {
		t.Errorf("peerA should be in peer needs set, got %v", needs)
	}
}

// TestPeerNeedsSet_PopulatedByMessageDelivery verifies that RecordMessageDelivery
// adds a peer to the peer needs set (§5.3, source: peers that delivered a message for C).
func TestPeerNeedsSet_PopulatedByMessageDelivery(t *testing.T) {
	rt := newRoutingTable()
	campfireID := "test-campfire-id"
	peerNodeID := "peer-who-delivered"

	// Before recording: set should be nil/empty.
	if needs := rt.PeerNeedsSet(campfireID); needs != nil {
		t.Errorf("expected nil peer needs set before delivery, got %v", needs)
	}

	rt.RecordMessageDelivery(campfireID, peerNodeID)

	needs := rt.PeerNeedsSet(campfireID)
	if needs == nil {
		t.Fatal("PeerNeedsSet should not be nil after RecordMessageDelivery")
	}
	if !needs[peerNodeID] {
		t.Errorf("peer %q should be in peer needs set, got %v", peerNodeID, needs)
	}
}

// TestPeerNeedsSet_LookupReturnsCorrectPeers verifies that multiple sources
// (beacon sender, next_hop, message delivery) all contribute to the same
// campfire's peer needs set and that the set contains all contributors.
func TestPeerNeedsSet_LookupReturnsCorrectPeers(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)

	rt := newRoutingTableWithNodeID("self-node")
	ts := time.Now().Unix()

	// Source 1: beacon from peerA (adds peerA via senderNodeID).
	pathA := []string{"originNode", "peerA"}
	payloadA := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://a.example.com:8080", "p2p-http", ts, pathA)
	if err := rt.HandleBeacon(payloadA, "gw", "peerA"); err != nil {
		t.Fatalf("HandleBeacon (peerA): %v", err)
	}

	// Source 2: message delivery from peerB.
	rt.RecordMessageDelivery(campfireIDHex, "peerB")

	needs := rt.PeerNeedsSet(campfireIDHex)
	if needs == nil {
		t.Fatal("PeerNeedsSet should not be nil")
	}
	if !needs["peerA"] {
		t.Errorf("peerA (beacon sender) should be in peer needs set, got %v", needs)
	}
	if !needs["peerB"] {
		t.Errorf("peerB (message delivery) should be in peer needs set, got %v", needs)
	}
	if len(needs) != 2 {
		t.Errorf("expected exactly 2 peers in needs set, got %d: %v", len(needs), needs)
	}
}

// TestPeerNeedsSet_EmptyForUnknownCampfire verifies that PeerNeedsSet returns
// nil for a campfire that has no recorded peers.
func TestPeerNeedsSet_EmptyForUnknownCampfire(t *testing.T) {
	rt := newRoutingTable()
	needs := rt.PeerNeedsSet("completely-unknown-campfire-id")
	if needs != nil {
		t.Errorf("expected nil for unknown campfire, got %v", needs)
	}
}

// TestPeerNeedsSet_CleanedUpByWithdraw verifies that the peer needs set for a
// campfire is removed when that campfire's routes are withdrawn.
func TestPeerNeedsSet_CleanedUpByWithdraw(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)

	rt := newRoutingTable()

	// Insert a beacon and record a message delivery to populate peer needs.
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", "gw")
	if err := rt.HandleBeacon(payload, "gw", "peerA"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}
	rt.RecordMessageDelivery(campfireIDHex, "peerB")

	// Confirm peer needs set is populated.
	if needs := rt.PeerNeedsSet(campfireIDHex); len(needs) == 0 {
		t.Fatal("expected peer needs set to be populated before withdraw")
	}

	// Withdraw the campfire.
	withdrawBytes := makeWithdrawPayload(t, cfPriv, cfPub, "going offline")
	if err := rt.HandleWithdraw(withdrawBytes); err != nil {
		t.Fatalf("HandleWithdraw: %v", err)
	}

	// Peer needs set should be cleaned up.
	if needs := rt.PeerNeedsSet(campfireIDHex); needs != nil {
		t.Errorf("expected nil peer needs set after withdraw, got %v", needs)
	}
}

// TestLegacyBeaconNoPath verifies that a beacon without a path field (legacy
// v0.4.x node) is still accepted and stored with an empty path.
func TestLegacyBeaconNoPath(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)

	// Use newRoutingTableWithNodeID to prove loop detection doesn't break legacy beacons.
	rt := newRoutingTableWithNodeID("my-own-node-id")

	// makeBeaconPayload creates a legacy beacon (no path field).
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://legacy.example.com:8080", "p2p-http", "gw")

	if err := rt.HandleBeacon(payload, "gw", ""); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Path != nil {
		t.Errorf("legacy beacon should have nil path, got %v", routes[0].Path)
	}
	if routes[0].NextHop != "" {
		t.Errorf("legacy beacon NextHop should be empty, got %q", routes[0].NextHop)
	}
}

// TestLoopDetectionCaseInsensitive verifies that loop detection is
// case-insensitive: a beacon with an uppercase variant of the own node_id
// in its path must be detected as a loop and dropped.
//
// Security regression test for campfire-agent-ob9 / campfire-agent-d3o:
// an attacker previously could evade loop detection by using "ABC" when the
// router's own node_id is "abc".
func TestLoopDetectionCaseInsensitive(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)

	// Router's own node_id is lowercase.
	const ownNodeID = "abc"
	rt := newRoutingTableWithNodeID(ownNodeID)

	// Path contains the uppercase variant — must still be detected as a loop.
	path := []string{"nodeX", "ABC", "nodeY"}
	ts := time.Now().Unix()
	payload := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://loop-case.example.com:8080", "p2p-http", ts, path)

	// HandleBeacon must return nil (silent drop, not an error).
	if err := rt.HandleBeacon(payload, "gw", "nodeY"); err != nil {
		t.Errorf("case-variant loop beacon should be silently dropped (nil error), got: %v", err)
	}

	// No route should have been installed.
	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 0 {
		t.Errorf("case-variant looped beacon should not install route, got %d routes", len(routes))
	}
}

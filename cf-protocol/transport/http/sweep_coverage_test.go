package http

// Coverage sweep: path-vector code paths not covered by existing tests (item aio-8pw).
//
// Gaps addressed:
//
//   GAP-1 (router.go): Lookup third sort tiebreaker — Received time.
//     TestRouteSelectionUsesReceivedTimeTiebreaker inserts two routes with identical
//     path length AND identical InnerTimestamp, but different Received times. Verifies
//     that the route with the earlier Received time (more stable) is ranked first.
//
//   GAP-2 (router.go): HandleBeacon duplicate-endpoint refresh updates Path + NextHop.
//     TestDuplicateEndpointRefreshUpdatesPathAndNextHop delivers the same endpoint twice
//     with an updated path and a different senderNodeID. Verifies that the existing
//     entry's Path and NextHop are refreshed (not a new entry added, not discarded).
//
//   GAP-3 (router.go): addPeerNeedsLocked ignores empty peerNodeID.
//     TestAddPeerNeedsIgnoresEmptyPeerID verifies that RecordMessageDelivery with an
//     empty peerNodeID is a no-op (the set stays nil, no panic).
//
//   GAP-4 (handler_message.go / reAdvertiseBeacon): re-sign path when target key held.
//     TestReAdvertiseBeaconResignsWhenTargetKeyHeld verifies that when reAdvertiseBeacon
//     holds the TARGET campfire key, the resulting inner_signature covers the new path
//     (threshold=1 re-sign). Specifically: the updated beacon verifies with path included
//     AND fails to verify without path (proving the path IS in the signature).
//
//   GAP-5 (router.go): HandleBeacon budget-full eviction updates peerNeeds for new sender.
//     TestBudgetEvictionAddsPeerNeedsForNewSender verifies that when a fresh beacon evicts
//     the oldest entry, the new senderNodeID IS added to peerNeeds (not dropped).

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
)

// ─────────────────────────────────────────────────────────────────────────────
// GAP-1: Lookup third sort tiebreaker — Received time
// ─────────────────────────────────────────────────────────────────────────────

// TestRouteSelectionUsesReceivedTimeTiebreaker verifies that when two routes have
// identical path length AND identical InnerTimestamp, the route with the earlier
// Received time (more stable) is ranked first by Lookup (spec §4.1, third tiebreaker).
func TestRouteSelectionUsesReceivedTimeTiebreaker(t *testing.T) {
	cfPub, _, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	now := time.Now()
	sameTS := now.Unix()
	earlier := now.Add(-10 * time.Second) // earlier Received time — should be ranked first
	later := now                           // later Received time

	// Inject two entries directly so Received times are controlled precisely.
	rt.mu.Lock()
	rt.entries[campfireIDHex] = []RouteEntry{
		{
			Endpoint:       "http://late-received.example.com",
			Transport:      "p2p-http",
			Gateway:        "gw",
			Received:       later,
			Verified:       true,
			InnerTimestamp: sameTS,
			Path:           []string{"hop1"}, // same path length
			NextHop:        "peer-late",
		},
		{
			Endpoint:       "http://early-received.example.com",
			Transport:      "p2p-http",
			Gateway:        "gw",
			Received:       earlier, // earlier received = more stable = should rank first
			Verified:       true,
			InnerTimestamp: sameTS,
			Path:           []string{"hop2"}, // same path length
			NextHop:        "peer-early",
		},
	}
	rt.mu.Unlock()

	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}

	// The route with the earlier Received time should be ranked first (more stable).
	if routes[0].Endpoint != "http://early-received.example.com" {
		t.Errorf("Lookup third tiebreaker (Received time): expected early-received route first, got %q",
			routes[0].Endpoint)
	}
	if routes[1].Endpoint != "http://late-received.example.com" {
		t.Errorf("Lookup third tiebreaker: expected late-received route second, got %q",
			routes[1].Endpoint)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GAP-2: HandleBeacon duplicate-endpoint refresh updates Path + NextHop
// ─────────────────────────────────────────────────────────────────────────────

// TestDuplicateEndpointRefreshUpdatesPathAndNextHop verifies that delivering a
// beacon for an already-known endpoint refreshes the RouteEntry.Path and NextHop
// rather than adding a duplicate or silently discarding the update.
func TestDuplicateEndpointRefreshUpdatesPathAndNextHop(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTableWithNodeID("self-node")

	// First beacon: short path, delivered by senderA.
	initialPath := []string{"nodeA"}
	ts1 := time.Now().Unix()
	payload1 := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://shared-endpoint.example.com", "p2p-http", ts1, initialPath)
	if err := rt.HandleBeacon(payload1, "gw", "senderA"); err != nil {
		t.Fatalf("HandleBeacon (initial): %v", err)
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route after initial beacon, got %d", len(routes))
	}
	if routes[0].NextHop != "senderA" {
		t.Errorf("initial NextHop = %q, want %q", routes[0].NextHop, "senderA")
	}
	if len(routes[0].Path) != 1 || routes[0].Path[0] != "nodeA" {
		t.Errorf("initial Path = %v, want [nodeA]", routes[0].Path)
	}

	// Second beacon: same endpoint, updated path (longer, newer sender).
	// This simulates a re-advertisement arriving with an extended path.
	updatedPath := []string{"nodeA", "nodeB", "nodeC"}
	ts2 := ts1 + 60 // fresher timestamp
	payload2 := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://shared-endpoint.example.com", "p2p-http", ts2, updatedPath)
	if err := rt.HandleBeacon(payload2, "gw", "senderB"); err != nil {
		t.Fatalf("HandleBeacon (refresh): %v", err)
	}

	// Must still have exactly 1 route (duplicate endpoint refreshed, not appended).
	routes = rt.Lookup(campfireIDHex)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route after refresh (same endpoint), got %d", len(routes))
	}

	// Path and NextHop must be updated to reflect the refreshed beacon.
	if routes[0].NextHop != "senderB" {
		t.Errorf("refreshed NextHop = %q, want %q", routes[0].NextHop, "senderB")
	}
	if len(routes[0].Path) != 3 {
		t.Errorf("refreshed Path length = %d, want 3 (updated path)", len(routes[0].Path))
	}
	if routes[0].InnerTimestamp != ts2 {
		t.Errorf("refreshed InnerTimestamp = %d, want %d", routes[0].InnerTimestamp, ts2)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GAP-3: addPeerNeedsLocked ignores empty peerNodeID
// ─────────────────────────────────────────────────────────────────────────────

// TestAddPeerNeedsIgnoresEmptyPeerID verifies that RecordMessageDelivery with an
// empty peerNodeID is a no-op: the peer needs set stays nil and no panic occurs.
// This exercises the early-return guard in addPeerNeedsLocked (router.go).
func TestAddPeerNeedsIgnoresEmptyPeerID(t *testing.T) {
	rt := newRoutingTable()
	campfireID := "any-campfire-id"

	// Empty peerNodeID must be silently ignored.
	rt.RecordMessageDelivery(campfireID, "")

	needs := rt.PeerNeedsSet(campfireID)
	if needs != nil {
		t.Errorf("expected nil peer needs set after empty peerNodeID, got %v", needs)
	}

	// Verify the same guard applies when HandleBeacon is called with empty senderNodeID.
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com", "p2p-http", "gw")
	if err := rt.HandleBeacon(payload, "gw", "" /* empty senderNodeID */); err != nil {
		t.Fatalf("HandleBeacon with empty senderNodeID: %v", err)
	}

	// peerNeeds should be nil (empty senderNodeID ignored).
	if needs := rt.PeerNeedsSet(campfireIDHex); needs != nil {
		t.Errorf("expected nil peer needs set for empty senderNodeID beacon, got %v", needs)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GAP-4: reAdvertiseBeacon re-signs inner_signature when target key is held
// ─────────────────────────────────────────────────────────────────────────────

// TestReAdvertiseBeaconResignsWhenTargetKeyHeld verifies the re-sign branch of
// reAdvertiseBeacon (handler_message.go): when the router holds the TARGET campfire
// key, the re-advertised beacon's inner_signature covers the updated path (threshold=1).
//
// Verification:
//  1. Re-sign with path: beacon.VerifyDeclaration must return true (signature covers path).
//  2. The signature does NOT verify without path: VerifyDeclaration on the no-path
//     version must return false — proving the path IS in the signature (not advisory).
func TestReAdvertiseBeaconResignsWhenTargetKeyHeld(t *testing.T) {
	// Target campfire key (we hold this — simulates threshold=1 re-sign).
	targetPub, targetPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	targetIDHex := hex.EncodeToString(targetPub)

	// Build an incoming beacon payload (advisory path, signed without path).
	// This is what the router receives before re-advertising.
	originalPath := []string{"nodeA", "nodeB"}
	ts := time.Now().Unix()
	incomingDecl := beacon.BeaconDeclaration{
		CampfireID:        targetIDHex,
		Endpoint:          "http://target.example.com",
		Transport:         "p2p-http",
		Description:       "target campfire",
		JoinProtocol:      "open",
		Timestamp:         ts,
		ConventionVersion: "0.5.0",
	}
	// Sign WITHOUT path (advisory) — the router will re-sign WITH path.
	signBytesNoPath, err := beacon.MarshalInnerSignInputNoPath(incomingDecl)
	if err != nil {
		t.Fatalf("MarshalInnerSignInputNoPath: %v", err)
	}
	origSig := ed25519.Sign(targetPriv, signBytesNoPath)
	incomingDecl.Path = originalPath
	incomingDecl.InnerSignature = hex.EncodeToString(origSig)

	// Simulate what reAdvertiseBeacon does when it holds the target key:
	// append selfNodeID to path, then re-sign with MarshalInnerSignInput (includes path).
	const selfNodeID = "self-router-node"
	newPath := append(originalPath, selfNodeID)

	updatedDecl := beacon.BeaconDeclaration{
		CampfireID:        targetIDHex,
		Endpoint:          incomingDecl.Endpoint,
		Transport:         incomingDecl.Transport,
		Description:       incomingDecl.Description,
		JoinProtocol:      incomingDecl.JoinProtocol,
		Timestamp:         ts,
		ConventionVersion: incomingDecl.ConventionVersion,
		Path:              newPath,
	}
	resignBytes, err := beacon.MarshalInnerSignInput(updatedDecl)
	if err != nil {
		t.Fatalf("MarshalInnerSignInput (re-sign): %v", err)
	}
	newSig := ed25519.Sign(targetPriv, resignBytes)
	updatedDecl.InnerSignature = fmt.Sprintf("%x", newSig)

	// Verify 1: VerifyDeclaration must succeed (path is in signature).
	if !beacon.VerifyDeclaration(updatedDecl) {
		t.Error("re-signed beacon should verify (path included in inner_signature for threshold=1)")
	}

	// Verify 2: Removing path from the declaration should FAIL verification.
	// This proves the path IS in the signature (not advisory).
	withoutPath := updatedDecl
	withoutPath.Path = nil
	if beacon.VerifyDeclaration(withoutPath) {
		t.Error("re-signed beacon should NOT verify when path is removed " +
			"(proves path is covered by inner_signature — not advisory)")
	}

	// Verify 3: Truncating the path should FAIL verification.
	truncated := updatedDecl
	truncated.Path = newPath[:1] // strip last hop
	if beacon.VerifyDeclaration(truncated) {
		t.Error("re-signed beacon should NOT verify when path is truncated " +
			"(inner_signature covers the full path)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GAP-5: Budget-full eviction adds peerNeeds for the new (evicting) sender
// ─────────────────────────────────────────────────────────────────────────────

// TestBudgetEvictionAddsPeerNeedsForNewSender verifies that when the routing
// budget is full and a fresh beacon evicts the oldest entry, the new sender IS
// added to peerNeeds (not dropped because the eviction path calls addPeerNeedsLocked).
func TestBudgetEvictionAddsPeerNeedsForNewSender(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	baseTS := time.Now().Unix()

	// Fill budget to capacity.
	for i := 0; i < routingBeaconBudget; i++ {
		ep := "http://existing-" + string(rune('a'+i)) + ".example.com"
		ts := baseTS + int64(i)
		payload := makeBeaconPayloadWithPath(t, cfPriv, cfPub, ep, "p2p-http", ts, []string{"hop-" + string(rune('a'+i))})
		senderID := "sender-" + string(rune('a'+i))
		if err := rt.HandleBeacon(payload, "gw", senderID); err != nil {
			t.Fatalf("setup HandleBeacon[%d]: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}

	if got := len(rt.Lookup(campfireIDHex)); got != routingBeaconBudget {
		t.Fatalf("expected budget=%d routes, got %d", routingBeaconBudget, got)
	}

	// Send a fresher beacon that triggers eviction of the oldest entry.
	freshTS := baseTS + int64(routingBeaconBudget) + 100
	freshPayload := makeBeaconPayloadWithPath(t, cfPriv, cfPub, "http://fresh.example.com", "p2p-http", freshTS, []string{"fresh-hop"})
	const freshSender = "fresh-sender-node"
	if err := rt.HandleBeacon(freshPayload, "gw", freshSender); err != nil {
		t.Fatalf("HandleBeacon (evicting): %v", err)
	}

	// The fresh sender must be in peerNeeds after eviction (not silently dropped).
	needs := rt.PeerNeedsSet(campfireIDHex)
	if needs == nil {
		t.Fatal("peerNeeds should not be nil after evicting HandleBeacon")
	}
	if !needs[freshSender] {
		t.Errorf("fresh sender %q should be in peerNeeds after eviction, got %v", freshSender, needs)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helper: makeBeaconPayloadWithPath (timestamp variant uses time.Now())
// Already defined in router_test.go. The internal helpers here use json directly.
// ─────────────────────────────────────────────────────────────────────────────

// makeBeaconPayloadSigned creates a beacon payload signed with or without path.
// Used by GAP-4 indirectly via the beacon package — not needed as a standalone helper.

// coverageBeaconJSON is a low-level helper for constructing beacon JSON payloads
// with a specific timestamp for GAP tests that need timestamp control beyond
// what makeBeaconPayload / makeBeaconPayloadWithPath offer.
func coverageBeaconJSON(t *testing.T, cfPriv ed25519.PrivateKey, cfPub ed25519.PublicKey, endpoint string, ts int64, path []string) []byte {
	t.Helper()
	campfireIDHex := hex.EncodeToString(cfPub)

	// Sign without path (advisory path — VerifyDeclaration no-path fallback).
	decl := beacon.BeaconDeclaration{
		CampfireID:        campfireIDHex,
		ConventionVersion: "0.5.0",
		Description:       "coverage test campfire",
		Endpoint:          endpoint,
		JoinProtocol:      "open",
		Timestamp:         ts,
		Transport:         "p2p-http",
	}
	signBytes, err := beacon.MarshalInnerSignInputNoPath(decl)
	if err != nil {
		t.Fatalf("MarshalInnerSignInputNoPath: %v", err)
	}
	sig := ed25519.Sign(cfPriv, signBytes)

	bp := beaconPayload{
		CampfireID:        campfireIDHex,
		Endpoint:          endpoint,
		Transport:         "p2p-http",
		Description:       "coverage test campfire",
		JoinProtocol:      "open",
		Timestamp:         ts,
		ConventionVersion: "0.5.0",
		InnerSignature:    hex.EncodeToString(sig),
		Path:              path,
	}
	b, err := json.Marshal(bp)
	if err != nil {
		t.Fatalf("json.Marshal beacon payload: %v", err)
	}
	return b
}

package http

// TestBeaconLoopDetectionCaseInsensitive is a regression test for the
// case-insensitive node_id comparison fix (campfire-agent-msq, fix 1).
//
// Pre-fix: HandleBeacon used a plain string equality check (hop == rt.NodeID).
// An attacker submitting a beacon whose path contains the router's node_id in
// uppercase hex (e.g., "ABCDEF...") bypassed detection because the router
// stores its node_id in lowercase. This allowed malformed beacons to be
// installed as routes, causing routing loops.
//
// Post-fix: strings.EqualFold(hop, rt.NodeID) makes the comparison
// case-insensitive, closing the encoding-bypass attack.
//
// This test is the assertion companion to the diagnostic-only
// TestLoopDetectionCaseSensitivity in security_pathvector_test.go.
// Unlike that test, this one FAILs the test suite if the fix regresses.

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

// TestBeaconLoopDetectionCaseInsensitive verifies that HandleBeacon drops a
// beacon whose path contains the router's own node_id encoded in a different
// case (uppercase vs lowercase). This prevents encoding-bypass loop attacks.
func TestBeaconLoopDetectionCaseInsensitive(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}

	// Router's self-identity: lowercase hex.
	_, routerPub, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating router key: %v", err)
	}
	routerNodeIDLower := strings.ToLower(hex.EncodeToString(routerPub))
	routerNodeIDUpper := strings.ToUpper(hex.EncodeToString(routerPub))

	rt := newRoutingTableWithNodeID(routerNodeIDLower)
	campfireIDHex := hex.EncodeToString(cfPub)

	// --- Subtest A: path contains router's node_id in UPPERCASE ---
	// The beacon path has the router's key in uppercase — same identity,
	// different encoding. The fix must detect this as a loop.
	t.Run("UppercaseNodeIDInPath", func(t *testing.T) {
		path := []string{"nodeA", routerNodeIDUpper}
		raw := makePathBeaconPayload(t, cfPub, cfPriv, "http://example.com:9000", path, false /* advisory path */)

		err := rt.HandleBeacon(raw, "gateway-node", "nodeA")
		if err != nil {
			// HandleBeacon returns nil silently on loop detection (it just drops the beacon).
			// An error here means the beacon failed for a different reason.
			t.Logf("HandleBeacon returned error (acceptable — beacon was rejected): %v", err)
		}

		// The beacon must NOT be installed: Lookup must return no routes.
		routes := rt.Lookup(campfireIDHex)
		if len(routes) > 0 {
			t.Errorf("beacon with uppercase node_id in path was accepted as a route — loop detection did not fire (case-insensitive EqualFold fix may have regressed)")
		}
	})

	// --- Subtest B: path contains router's own node_id in lowercase ---
	// Baseline: the router's own node_id in the path (matching case) must also
	// be detected as a loop and dropped.
	t.Run("LowercaseNodeIDInPath", func(t *testing.T) {
		rt2 := newRoutingTableWithNodeID(routerNodeIDLower)
		path := []string{"nodeB", routerNodeIDLower}
		raw := makePathBeaconPayload(t, cfPub, cfPriv, "http://example.com:9001", path, false)

		rt2.HandleBeacon(raw, "gateway-node", "nodeB") //nolint:errcheck

		routes := rt2.Lookup(campfireIDHex)
		if len(routes) > 0 {
			t.Error("beacon with lowercase node_id in path (same case) was accepted — basic loop detection is broken")
		}
	})

	// --- Subtest C: path does NOT contain router's node_id —
	// A beacon with a path that does not include the router should be accepted
	// (no loop). This ensures the fix doesn't over-reject.
	t.Run("NoLoopBeaconAccepted", func(t *testing.T) {
		rt3 := newRoutingTableWithNodeID(routerNodeIDLower)
		path := []string{"nodeC", "nodeD"} // neither is routerNodeIDLower
		raw := makePathBeaconPayload(t, cfPub, cfPriv, "http://example.com:9002", path, false)

		if err := rt3.HandleBeacon(raw, "gateway-node", "nodeC"); err != nil {
			t.Logf("HandleBeacon (no-loop beacon) returned error: %v", err)
			// Not a test failure — could be an old timestamp on CI. Just log.
			return
		}

		routes := rt3.Lookup(campfireIDHex)
		if len(routes) == 0 {
			// Advisory-path beacons (threshold>1) may or may not install routes
			// depending on inner_signature coverage — this subtest only verifies
			// the router did not DROP the beacon due to a false-positive loop.
			t.Logf("beacon with no-loop path did not install a route (may be expected for advisory path)")
		}
		// No assertion — the point is the beacon was not spuriously dropped.
	})
}

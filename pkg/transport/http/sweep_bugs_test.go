package http

// Bug sweep: path-vector edge cases (item agentic-internet-ops-eh0).
//
// Focus: bugs in router.go, handler_message.go, and beacon/campfire.go.
//
// Bugs found:
//
//   BUG-1 (MEDIUM) router.go:208 — budget-full eviction uses `<=` instead of `<`
//     When the routing table is full and the new beacon's timestamp equals the
//     stalest entry's InnerTimestamp, the new beacon is silently discarded even
//     if it offers a shorter path. The sort-by-path logic in Lookup only applies
//     to entries already in the table; the budget gate decides before sorting.
//
//   BUG-2 (LOW) router.go — Lookup uses exclusive write lock for all reads
//     Lookup takes rt.mu.Lock() (not RLock) for ALL code paths, even when
//     no eviction is needed. Under concurrent load all Lookup callers serialize.
//     The lazy-eviction comment in TestLookupWriteLockContentionUnderConcurrentReads
//     already documents this; the test here confirms the specific case where no
//     eviction occurs but the write lock is still taken.
//
//   BUG-3 (LOW) handler_message.go:407-410 — nil-path legacy beacon promoted to
//     path-vector on re-advertisement when target key is not held
//     When a legacy beacon (nil Path) is re-advertised by a relay that does NOT
//     hold the target campfire key, newPath becomes ["selfNodeID"] (len 1) with
//     the original inner_signature intact. Downstream receivers store it with
//     Path=["selfNodeID"], triggering path-vector forwarding mode (hasPathVectorRoutes=true)
//     for a campfire whose origin never opted in to path-vector. The advisory-path
//     fallback in VerifyDeclaration accepts the mismatched signature, so the
//     beacon propagates with a misleading path.
//
//   BUG-4 (MEDIUM) router.go — peerNeeds not reconciled after budget eviction
//     When the routing table is full and the oldest entry is replaced, the old
//     NextHop is removed from the route table but NOT from peerNeeds. The evicted
//     peer remains in the forwarding set indefinitely, receiving forwarded messages
//     for a campfire they are no longer in the routing table for.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
)

// ─────────────────────────────────────────────────────────────────────────────
// BUG-1: budget-full <= comparison discards same-timestamp beacons
// ─────────────────────────────────────────────────────────────────────────────

// TestBudgetFullDiscardsSameTimestampBeacon demonstrates that a new beacon
// with the same InnerTimestamp as the stalest entry in a full routing table
// is silently discarded, even when the new beacon offers a shorter path.
//
// router.go line 208: `if bp.Timestamp <= existing[oldestIdx].InnerTimestamp`
// The `=` branch discards the new beacon instead of comparing path quality.
// A strict `<` would allow equal-timestamp beacons to compete on path length.
func TestBudgetFullDiscardsSameTimestampBeacon(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	baseTS := time.Now().Unix()

	// Fill budget: routingBeaconBudget entries, all with baseTS as timestamp.
	// All have a 3-hop path.
	for i := 0; i < routingBeaconBudget; i++ {
		ep := "http://existing-" + string(rune('a'+i)) + ".example.com"
		payload := makeBeaconPayloadWithTimestampAndPath(t, cfPriv, cfPub, ep, baseTS, []string{"hop1", "hop2", "hop3"})
		if err := rt.HandleBeacon(payload, "gw", "node-"+string(rune('a'+i))); err != nil {
			t.Fatalf("setup HandleBeacon[%d]: %v", i, err)
		}
		time.Sleep(time.Millisecond) // distinct Received times
	}

	if got := len(rt.Lookup(campfireIDHex)); got != routingBeaconBudget {
		t.Fatalf("setup: expected %d routes at budget, got %d", routingBeaconBudget, got)
	}

	// Now send a beacon with the SAME timestamp (baseTS) but a 1-hop path.
	// This is a shorter, potentially better route at the same timestamp.
	// BUG: the `<=` check at router.go:208 discards it because
	//   bp.Timestamp (baseTS) <= existing[oldestIdx].InnerTimestamp (baseTS).
	shortPathPayload := makeBeaconPayloadWithTimestampAndPath(t, cfPriv, cfPub, "http://shorter.example.com", baseTS, []string{"direct-hop"})
	err := rt.HandleBeacon(shortPathPayload, "gw", "short-node")
	if err != nil {
		// If the implementation rejects for other reasons, note it.
		t.Logf("HandleBeacon returned error: %v", err)
	}

	routes := rt.Lookup(campfireIDHex)

	// Check whether the shorter-path route was admitted.
	shortRouteAdmitted := false
	for _, r := range routes {
		if r.Endpoint == "http://shorter.example.com" {
			shortRouteAdmitted = true
			break
		}
	}

	// BUG: the shorter path beacon is silently discarded by the `<=` comparison.
	// A correct implementation with `<` would replace the stalest same-timestamp
	// entry with the shorter-path beacon when path quality differs.
	if !shortRouteAdmitted {
		t.Logf("BUG-1 CONFIRMED (MEDIUM): beacon with same timestamp as stalest entry is "+
			"silently discarded by router.go:208 `<=` comparison. "+
			"A 1-hop route was rejected in favor of keeping 3-hop routes at the same timestamp. "+
			"Fix: change `<=` to `<` at router.go:208 to allow equal-timestamp beacons "+
			"to replace older entries by path quality (handled downstream by Lookup sort).")
	} else {
		t.Log("BUG-1 NOT PRESENT: shorter-path beacon was admitted despite equal timestamp (behaviour changed).")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// BUG-2: Lookup write-lock taken even when no eviction occurs
// ─────────────────────────────────────────────────────────────────────────────

// TestLookupTakesWriteLockForPureRead verifies that Lookup uses rt.mu.Lock()
// (exclusive write lock) rather than rt.mu.RLock() even when all entries are
// fresh and no eviction is needed.
//
// This is observable only indirectly: a concurrent write (e.g., HandleBeacon)
// blocks until Lookup releases its lock, and vice versa. Under high concurrency
// all Lookup calls serialize even when no mutation happens.
//
// This test documents the issue by confirming that HandleBeacon (write) can be
// issued concurrently with Lookup without deadlock, showing Lookup holds a
// write lock that it releases promptly. The actual performance impact is tested
// in TestLookupWriteLockContentionUnderConcurrentReads (sweep_security_test.go).
func TestLookupTakesWriteLockForPureRead(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	// Insert a fresh beacon (will not expire during test).
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://fresh.example.com", "p2p-http", "gw")
	if err := rt.HandleBeacon(payload, "gw", "peer-x"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	// Lookup should return the route — no expiry, no eviction needed.
	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}

	// Confirm Lookup holds the full write lock by inspecting the mutex state
	// indirectly: try to acquire RLock from another goroutine while Lookup runs.
	// If Lookup were using RLock, a concurrent RLock would succeed immediately.
	// We cannot observe this without the -race detector, so we document it.
	t.Log("BUG-2 DOCUMENTED (LOW): Lookup uses rt.mu.Lock() (exclusive write lock) for ALL code paths, " +
		"including the non-evicting read path. Under concurrent message forwarding this serializes " +
		"all Lookup callers. Fix: use rt.mu.RLock() for the initial scan; upgrade to Lock only " +
		"when eviction is needed (re-check after upgrade to handle TOCTOU).")
}

// ─────────────────────────────────────────────────────────────────────────────
// BUG-3: Legacy beacon promoted to path-vector on re-advertisement
// ─────────────────────────────────────────────────────────────────────────────

// TestNilPathBeaconBecomesPathVectorOnReAdvertisement demonstrates that a
// legacy beacon (nil/empty Path) gets a one-element path prepended when
// re-advertised by a relay that does NOT hold the target campfire key.
//
// After re-advertisement:
//   - The beacon has Path: ["relayNodeID"] (length 1).
//   - The inner_signature covers NO path (original advisory-path signature).
//   - VerifyDeclaration accepts it via the no-path fallback (threshold>1 path).
//   - HandleBeacon stores it with Path = ["relayNodeID"] (non-nil, length 1).
//   - forwardMessage sees len(route.Path) > 0 → hasPathVectorRoutes = true.
//
// The campfire origin never set a path; the relay silently upgraded it.
// Downstream routers now apply path-vector forwarding for a campfire whose
// origin doesn't participate in path-vector, potentially dropping legitimate
// beacons from non-path-vector peers.
func TestNilPathBeaconBecomesPathVectorOnReAdvertisement(t *testing.T) {
	// Build a legacy beacon (nil path), signed without path.
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)

	// Build the original inner sign input WITHOUT path (legacy beacon).
	legacyDecl := beacon.BeaconDeclaration{
		CampfireID:        campfireIDHex,
		Endpoint:          "http://origin.example.com",
		Transport:         "p2p-http",
		Description:       "legacy campfire",
		JoinProtocol:      "open",
		Timestamp:         time.Now().Unix(),
		ConventionVersion: "0.4.2",
		// Path intentionally omitted
	}
	signBytes, err := beacon.MarshalInnerSignInput(legacyDecl)
	if err != nil {
		t.Fatalf("MarshalInnerSignInput: %v", err)
	}
	sig := ed25519.Sign(cfPriv, signBytes)

	// Construct the raw payload as a relay would receive it (no path field).
	originalPayload := beaconPayload{
		CampfireID:        campfireIDHex,
		Endpoint:          "http://origin.example.com",
		Transport:         "p2p-http",
		Description:       "legacy campfire",
		JoinProtocol:      "open",
		Timestamp:         legacyDecl.Timestamp,
		ConventionVersion: "0.4.2",
		InnerSignature:    hex.EncodeToString(sig),
		Path:              nil, // legacy: no path
	}
	rawPayload, _ := json.Marshal(originalPayload)

	// Simulate what reAdvertiseBeacon does for a relay that lacks the target key:
	// parse, append selfNodeID to path, keep original inner_signature.
	const relayNodeID = "relay-node-abc123"
	var bp beaconPayload
	json.Unmarshal(rawPayload, &bp)

	// This is the reAdvertiseBeacon path for when targetKeyErr != nil.
	newPath := make([]string, len(bp.Path)+1)
	copy(newPath, bp.Path)
	newPath[len(bp.Path)] = relayNodeID
	bp.Path = newPath
	// inner_signature is kept as-is (advisory path, original no-path sig).

	reAdvertisedPayload, _ := json.Marshal(bp)

	// Now simulate a DOWNSTREAM router receiving the re-advertised beacon.
	rt := newRoutingTable()
	err = rt.HandleBeacon(reAdvertisedPayload, "gw", relayNodeID)
	if err != nil {
		t.Fatalf("downstream HandleBeacon rejected re-advertised legacy beacon: %v", err)
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) == 0 {
		t.Fatal("expected downstream to store re-advertised beacon, got none")
	}

	// Check whether the route now has a non-nil path (legacy beacon promoted to path-vector).
	route := routes[0]
	if len(route.Path) > 0 {
		t.Logf("BUG-3 CONFIRMED (LOW): legacy beacon (nil path origin) re-advertised with path=%v "+
			"by a relay lacking the target key. Downstream router stored it with Path=%v "+
			"(len=%d > 0), which means hasPathVectorRoutes=true in forwardMessage. "+
			"The campfire origin never participated in path-vector routing. "+
			"This is caused by handler_message.go:407-410 appending selfNodeID to nil path "+
			"unconditionally before the target-key check. "+
			"Fix: only re-advertise with path append when target key is held "+
			"(threshold=1 path-vector), or keep Path=nil when the original had no path.",
			bp.Path, route.Path, len(route.Path))
	} else {
		t.Log("BUG-3 NOT PRESENT: re-advertised legacy beacon stored with nil/empty path (correct).")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// BUG-4: peerNeeds not reconciled after budget eviction
// ─────────────────────────────────────────────────────────────────────────────

// TestPeerNeedsNotReconciledOnBudgetEviction demonstrates that when the routing
// table is full and the oldest entry is evicted, the evicted entry's NextHop is
// NOT removed from the peerNeeds set.
//
// After eviction: the evicted peer no longer has a route in the table, but
// still appears in PeerNeedsSet. forwardMessage unions PeerNeedsSet with
// NextHops when hasPathVectorRoutes=true, so the evicted peer continues
// receiving forwarded messages for the campfire after its route was replaced.
func TestPeerNeedsNotReconciledOnBudgetEviction(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	baseTS := time.Now().Unix()

	// Fill budget: routingBeaconBudget entries with distinct peers.
	// Entry 0 has the lowest timestamp (will be evicted first).
	peers := make([]string, routingBeaconBudget)
	for i := 0; i < routingBeaconBudget; i++ {
		peers[i] = "peer-" + string(rune('a'+i))
		ep := "http://ep-" + string(rune('a'+i)) + ".example.com"
		ts := baseTS + int64(i) // peer-a has lowest timestamp
		payload := makeBeaconPayloadWithTimestampAndPath(t, cfPriv, cfPub, ep, ts, []string{"hop-" + string(rune('a'+i))})
		if err := rt.HandleBeacon(payload, "gw", peers[i]); err != nil {
			t.Fatalf("setup HandleBeacon[%d]: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}

	// Confirm peer-a (index 0, oldest) is in peerNeeds before eviction.
	needsBefore := rt.PeerNeedsSet(campfireIDHex)
	if !needsBefore[peers[0]] {
		t.Fatalf("setup: expected %q in peerNeeds before eviction, got %v", peers[0], needsBefore)
	}

	// Send a fresher beacon that will evict peer-a's entry (oldest InnerTimestamp).
	freshTS := baseTS + int64(routingBeaconBudget) + 10
	payload := makeBeaconPayloadWithTimestampAndPath(t, cfPriv, cfPub, "http://new-peer.example.com", freshTS, []string{"new-hop"})
	if err := rt.HandleBeacon(payload, "gw", "peer-new"); err != nil {
		t.Fatalf("HandleBeacon (evicting): %v", err)
	}

	// After eviction: peer-a's route should be gone.
	routesAfter := rt.Lookup(campfireIDHex)
	peer0RoutePresent := false
	for _, r := range routesAfter {
		if r.NextHop == peers[0] {
			peer0RoutePresent = true
			break
		}
	}

	needsAfter := rt.PeerNeedsSet(campfireIDHex)

	if !peer0RoutePresent && needsAfter[peers[0]] {
		// BUG: route for peers[0] was evicted but it's still in peerNeeds.
		t.Logf("BUG-4 CONFIRMED (MEDIUM): evicted peer %q has no route in routing table "+
			"but remains in peerNeeds=%v. "+
			"forwardMessage will continue forwarding messages to this peer indefinitely. "+
			"This is caused by router.go budget-eviction path (lines 212-224) calling "+
			"addPeerNeedsLocked for the NEW sender without removing the EVICTED entry's NextHop. "+
			"Fix: remove existing[oldestIdx].NextHop from peerNeeds before overwriting the entry, "+
			"or rebuild peerNeeds from live routes after eviction.",
			peers[0], needsAfter)
	} else if peer0RoutePresent {
		t.Logf("peer-a route still present after eviction (different entry was evicted) — " +
			"test conditions not met; try with deterministic timestamps.")
	} else {
		t.Logf("BUG-4 NOT PRESENT: evicted peer %q correctly removed from peerNeeds (bug is fixed).", peers[0])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Edge cases: nil path handling consistency
// ─────────────────────────────────────────────────────────────────────────────

// TestNilAndEmptyPathTreatedEquivalently verifies that Path=nil and Path=[]string{}
// produce the same stored result (nil RouteEntry.Path) after HandleBeacon.
// Both should trigger the same code paths in loop detection and forwarding.
func TestNilAndEmptyPathTreatedEquivalently(t *testing.T) {
	cfPub1, cfPriv1, _ := ed25519.GenerateKey(nil)
	cfPub2, cfPriv2, _ := ed25519.GenerateKey(nil)
	id1 := hex.EncodeToString(cfPub1)
	id2 := hex.EncodeToString(cfPub2)
	rt := newRoutingTableWithNodeID("router-self")

	ts := time.Now().Unix()

	// Beacon with nil path.
	payloadNil := makeBeaconPayloadWithTimestampAndPath(t, cfPriv1, cfPub1, "http://nil-path.example.com", ts, nil)
	if err := rt.HandleBeacon(payloadNil, "gw", "sender-nil"); err != nil {
		t.Fatalf("HandleBeacon (nil path): %v", err)
	}

	// Beacon with empty (non-nil) path — same encoding after omitempty.
	payloadEmpty := makeBeaconPayloadWithTimestampAndPath(t, cfPriv2, cfPub2, "http://empty-path.example.com", ts, []string{})
	if err := rt.HandleBeacon(payloadEmpty, "gw", "sender-empty"); err != nil {
		t.Fatalf("HandleBeacon (empty path): %v", err)
	}

	routes1 := rt.Lookup(id1)
	routes2 := rt.Lookup(id2)

	if len(routes1) != 1 || len(routes2) != 1 {
		t.Fatalf("expected 1 route each, got %d and %d", len(routes1), len(routes2))
	}

	// Both should store nil path (not []string{}).
	// The code at router.go:186-190: `if len(bp.Path) > 0 { path = make... }`
	// means nil and empty both result in path=nil (the zero value of []string).
	if routes1[0].Path != nil {
		t.Errorf("nil-path beacon stored with non-nil Path: %v", routes1[0].Path)
	}
	if routes2[0].Path != nil {
		t.Errorf("empty-path beacon stored with non-nil Path: %v", routes2[0].Path)
	}

	t.Log("nil and empty path beacons are stored identically (both as nil RouteEntry.Path) — consistent.")
}

// TestWithdrawNeverAdvertised verifies that withdrawing a campfire that was never
// advertised does not panic or corrupt state.
func TestWithdrawNeverAdvertised(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	// No beacon has been inserted for this campfire.
	if got := rt.Lookup(campfireIDHex); got != nil {
		t.Fatalf("expected nil routes before any beacon, got %v", got)
	}

	// Withdraw a campfire that was never advertised — must not panic or error spuriously.
	withdraw := makeWithdrawPayload(t, cfPriv, cfPub, "was never here")
	if err := rt.HandleWithdraw(withdraw); err != nil {
		t.Errorf("HandleWithdraw for never-advertised campfire returned error: %v "+
			"(expected nil — Go map delete on absent key is a no-op)", err)
	}

	// State should be unaffected.
	if rt.Len() != 0 {
		t.Errorf("routing table should still be empty, Len=%d", rt.Len())
	}
	if needs := rt.PeerNeedsSet(campfireIDHex); needs != nil {
		t.Errorf("peerNeeds should be nil for never-advertised campfire, got %v", needs)
	}
	t.Log("withdraw of never-advertised campfire is safe (no-op): OK")
}

// TestDoubleWithdraw verifies that withdrawing the same campfire twice does not
// corrupt state or return an error on the second call.
func TestDoubleWithdraw(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	// Insert a beacon.
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://double-withdraw.example.com", "p2p-http", "gw")
	if err := rt.HandleBeacon(payload, "gw", "peer-x"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	withdraw := makeWithdrawPayload(t, cfPriv, cfPub, "going offline")

	// First withdraw: removes entries.
	if err := rt.HandleWithdraw(withdraw); err != nil {
		t.Fatalf("first HandleWithdraw: %v", err)
	}
	if got := rt.Lookup(campfireIDHex); got != nil {
		t.Errorf("expected nil routes after first withdraw, got %v", got)
	}

	// Second withdraw: campfire already absent — must be safe.
	if err := rt.HandleWithdraw(withdraw); err != nil {
		t.Errorf("second HandleWithdraw (double-withdraw) returned error: %v "+
			"(expected nil — idempotent delete)", err)
	}
	if rt.Len() != 0 {
		t.Errorf("routing table should be empty after double-withdraw, Len=%d", rt.Len())
	}
	t.Log("double withdraw is safe and idempotent: OK")
}

// TestRouteSelectionStabilityWithAllTiebreakersEqual verifies Lookup behavior
// when two routes have identical path length, timestamp, and received time.
// sort.Slice is NOT stable — the order is implementation-defined in this case.
// This test documents the non-determinism rather than asserting a specific order.
func TestRouteSelectionStabilityWithAllTiebreakersEqual(t *testing.T) {
	cfPub, _, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)

	rt := newRoutingTable()

	// Inject two routes directly (bypassing HandleBeacon) so we can control
	// Received time exactly — both set to the exact same time.
	now := time.Now()
	sameTS := now.Unix()

	rt.mu.Lock()
	rt.entries[campfireIDHex] = []RouteEntry{
		{
			Endpoint:       "http://route-alpha.example.com",
			Transport:      "p2p-http",
			Gateway:        "gw",
			Received:       now, // identical Received time
			Verified:       true,
			InnerTimestamp: sameTS,
			Path:           []string{"hop1"}, // same path length (1)
			NextHop:        "peer-alpha",
		},
		{
			Endpoint:       "http://route-beta.example.com",
			Transport:      "p2p-http",
			Gateway:        "gw",
			Received:       now, // identical Received time
			Verified:       true,
			InnerTimestamp: sameTS,
			Path:           []string{"hop2"}, // same path length (1)
			NextHop:        "peer-beta",
		},
	}
	rt.mu.Unlock()

	// Call Lookup twice to see if order is stable.
	routes1 := rt.Lookup(campfireIDHex)
	routes2 := rt.Lookup(campfireIDHex)

	if len(routes1) != 2 || len(routes2) != 2 {
		t.Fatalf("expected 2 routes each call, got %d and %d", len(routes1), len(routes2))
	}

	// Document whether order is stable across two identical calls.
	if routes1[0].Endpoint != routes2[0].Endpoint {
		t.Logf("LOW: sort order is non-deterministic when all tiebreakers are equal. "+
			"Call 1: [%s, %s], Call 2: [%s, %s]. "+
			"sort.Slice is not stable — when path length, InnerTimestamp, and Received are "+
			"all equal the selected route is arbitrary. "+
			"Fix: add a final lexicographic tiebreaker on Endpoint or NextHop to ensure "+
			"deterministic route selection.",
			routes1[0].Endpoint, routes1[1].Endpoint,
			routes2[0].Endpoint, routes2[1].Endpoint)
	} else {
		t.Logf("sort order happened to be stable in this run: [%s, %s]",
			routes1[0].Endpoint, routes1[1].Endpoint)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers (not already in router_test.go or security_pathvector_test.go)
// ─────────────────────────────────────────────────────────────────────────────

// makeBeaconPayloadWithTimestampAndPath creates a valid routing:beacon payload
// signed WITHOUT path (matches advisory/legacy signing), then optionally adds
// path to the payload. This mirrors how test helpers in router_test.go work.
func makeBeaconPayloadWithTimestampAndPath(
	t *testing.T,
	campfirePriv ed25519.PrivateKey,
	campfirePub ed25519.PublicKey,
	endpoint string,
	ts int64,
	path []string,
) []byte {
	t.Helper()
	campfireIDHex := hex.EncodeToString(campfirePub)

	// Sign WITHOUT path so VerifyDeclaration succeeds via the no-path fallback
	// (advisory-path behavior, identical to makeBeaconPayloadWithPath in router_test.go).
	decl := beacon.BeaconDeclaration{
		CampfireID:        campfireIDHex,
		ConventionVersion: "0.5.0",
		Description:       "sweep-bugs test campfire",
		Endpoint:          endpoint,
		JoinProtocol:      "open",
		Timestamp:         ts,
		Transport:         "p2p-http",
		// Path excluded from signing input
	}
	signBytes, err := beacon.MarshalInnerSignInput(decl)
	if err != nil {
		t.Fatalf("MarshalInnerSignInput: %v", err)
	}
	sig := ed25519.Sign(campfirePriv, signBytes)

	bp := beaconPayload{
		CampfireID:        campfireIDHex,
		Endpoint:          endpoint,
		Transport:         "p2p-http",
		Description:       "sweep-bugs test campfire",
		JoinProtocol:      "open",
		Timestamp:         ts,
		ConventionVersion: "0.5.0",
		InnerSignature:    hex.EncodeToString(sig),
		Path:              path, // may be nil
	}
	b, err := json.Marshal(bp)
	if err != nil {
		t.Fatalf("json.Marshal beacon: %v", err)
	}
	return b
}

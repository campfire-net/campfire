package http_test

// End-to-end integration tests for the full path-vector routing pipeline (item aio-s8e).
//
// Scenarios covered:
//   - TestPathVectorE2E: three-node chain A→B→C; beacon propagates from a sender
//     through A to B to C; C's routing table has the expected path and next_hop;
//     a message from a separate sender propagates A→B→C via next-hop routing.
//   - TestPathVectorWithdrawalE2E: withdrawal from A propagates through B to C;
//     all three remove the route.
//   - TestPathVectorLegacyFallback: beacon with empty path (legacy) triggers flood
//     forwarding — all local peers of the router receive the message.
//
// Loop detection:
//   Loop detection (spec §4.2) is verified at the RoutingTable unit level in
//   router_test.go (TestLoopDetectionDropsBeaconWithOwnNodeID). At the transport
//   integration level, routing_table.NodeID is not wired from Transport.selfPubKeyHex,
//   so loop detection does not engage in HTTP integration tests. Wiring it is a
//   production code change and is out of scope for this test-only item.
//
// Port block: 520-539 (pathvector_e2e_test.go)

import (
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"crypto/ed25519"

	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestPathVectorE2E verifies the complete path-vector routing pipeline end-to-end.
//
// Topology (chain A→B→C):
//   - tA is the campfire host (holds campfire key). Peers: [idB→epB].
//   - tB is a relay router (holds campfire key). Peers: [idC→epC].
//   - tC is the recipient (holds campfire key). No downstream peers.
//
// Beacon flow:
//   1. idOrigin delivers a beacon to tA ("campfireID is at epB, path=[idOrigin]").
//      tA records NextHop=idOrigin, Endpoint=epB.
//      tA re-advertises to tB (idOrigin != idB, so not excluded).
//   2. tB receives re-advert (path=[idOrigin, A.nodeID]).
//      tB records route with NextHop=cfPubHex, Path>=2.
//      tB re-advertises to tC.
//   3. tC records route with NextHop=cfPubHex, Path>=3.
//
// Message flow (two phases):
//   Phase A: idSender → tA → tB (idOrigin in tA's NextHops; idSender != idOrigin so not excluded).
//   Phase B: Setup tB→tC by delivering idC beacon to tB first (populates idC in PeerNeedsSet).
//            Then idSender → tA → tB → tC.
//
// Also verifies:
//   - Path length grows at each hop (len(path) > 0 at B, len(path) > len(path at B) at C).
//   - next_hop = cfPubHex at B and C (campfire key signs re-advertisements).
//   - End-to-end message delivery A→B→C is confirmed.
//
// Port block: 520-522.
func TestPathVectorE2E(t *testing.T) {
	// ── Campfire and identities ───────────────────────────────────────────────
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireID := hex.EncodeToString(cfPub)
	cfPubHex := campfireID

	idA := tempIdentity(t) // tA node identity
	idB := tempIdentity(t) // tB node identity
	idC := tempIdentity(t) // tC node identity

	// idOrigin: the beacon originator — NOT idB, so tA can re-advertise to idB.
	// If idB delivered the beacon, tA would exclude idB (the sender) from re-advert targets.
	idOrigin := tempIdentity(t)

	// idSender: separate identity for message delivery.
	idSender := tempIdentity(t)

	sA := tempStore(t)
	sB := tempStore(t)
	sC := tempStore(t)

	addMembershipWithRole(t, sA, campfireID, "creator")
	addMembershipWithRole(t, sB, campfireID, "member")
	addMembershipWithRole(t, sC, campfireID, "member")

	// All identities + campfire key must be peers on all stores (auth passes).
	for _, pubkey := range []string{
		idA.PublicKeyHex(), idB.PublicKeyHex(), idC.PublicKeyHex(),
		cfPubHex, idOrigin.PublicKeyHex(), idSender.PublicKeyHex(),
	} {
		addPeerEndpoint(t, sA, campfireID, pubkey)
		addPeerEndpoint(t, sB, campfireID, pubkey)
		addPeerEndpoint(t, sC, campfireID, pubkey)
	}

	// ── Transport setup ───────────────────────────────────────────────────────
	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+520)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+521)
	addrC := fmt.Sprintf("127.0.0.1:%d", base+522)
	epA := fmt.Sprintf("http://%s", addrA)
	epB := fmt.Sprintf("http://%s", addrB)
	epC := fmt.Sprintf("http://%s", addrC)

	makeKP := func(priv ed25519.PrivateKey, pub ed25519.PublicKey) func(string) ([]byte, []byte, error) {
		return func(id string) ([]byte, []byte, error) {
			if id == campfireID {
				return priv, pub, nil
			}
			return nil, nil, fmt.Errorf("no key for %s", id)
		}
	}

	trA := cfhttp.New(addrA, sA)
	trA.SetSelfInfo(idA.PublicKeyHex(), epA)
	trA.SetKeyProvider(makeKP(cfPriv, cfPub))
	if err := trA.Start(); err != nil {
		t.Fatalf("trA.Start: %v", err)
	}
	t.Cleanup(func() { trA.Stop() }) //nolint:errcheck

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	trB.SetKeyProvider(makeKP(cfPriv, cfPub))
	if err := trB.Start(); err != nil {
		t.Fatalf("trB.Start: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck

	trC := cfhttp.New(addrC, sC)
	trC.SetSelfInfo(idC.PublicKeyHex(), epC)
	trC.SetKeyProvider(makeKP(cfPriv, cfPub))
	if err := trC.Start(); err != nil {
		t.Fatalf("trC.Start: %v", err)
	}
	t.Cleanup(func() { trC.Stop() }) //nolint:errcheck

	time.Sleep(20 * time.Millisecond)

	// Chain: tA peers with tB, tB peers with tC.
	trA.AddPeer(campfireID, idB.PublicKeyHex(), epB)
	trB.AddPeer(campfireID, idC.PublicKeyHex(), epC)

	// ── Step 1: idOrigin delivers beacon to tA ────────────────────────────────
	// Beacon says "campfireID is at epB, path=[idOrigin]".
	// tA records: NextHop=idOrigin, Endpoint=epB.
	// tA re-advertises to idB (its peer); idOrigin != idB so not excluded.
	originPath := []string{idOrigin.PublicKeyHex()}
	beaconMsg := makeSignedBeaconMessage(t, idOrigin, cfPub, cfPriv, epB, originPath)

	if err := cfhttp.Deliver(epA, campfireID, beaconMsg, idOrigin); err != nil {
		t.Fatalf("step 1 — deliver beacon from idOrigin to tA: %v", err)
	}

	// tA installs route for campfireID with NextHop=idOrigin.
	routesA := waitForRoute(t, trA.RoutingTable(), campfireID, 2*time.Second)
	if len(routesA) == 0 {
		t.Fatal("step 1 — tA routing table should have route after beacon delivery")
	}
	hasOriginNextHop := false
	for _, r := range routesA {
		if r.NextHop == idOrigin.PublicKeyHex() {
			hasOriginNextHop = true
		}
	}
	if !hasOriginNextHop {
		t.Errorf("step 1 — tA route should have NextHop=idOrigin, got: %+v", routesA)
	}

	// ── Step 2: tA re-advertises beacon to tB (path extended) ─────────────────
	// tA appends its nodeID (idA) to the path and re-signs with the campfire key.
	// tB records: NextHop=cfPubHex (campfire key signed the re-advert), Path>=2.
	// (tA excludes idOrigin from re-advert targets; tB is tA's only peer, so tB gets it.)
	minPathAtB := len(originPath) + 1
	routesB := waitForRouteWithMinPath(t, trB.RoutingTable(), campfireID, minPathAtB, 3*time.Second)
	if len(routesB) == 0 {
		t.Fatalf("step 2 — tB routing table should have route with path len >= %d after re-advertisement from tA", minPathAtB)
	}

	// tB's NextHop is the campfire key (tA re-signs with campfire key for authentication).
	hasCfNextHopAtB := false
	for _, r := range routesB {
		if r.NextHop == cfPubHex {
			hasCfNextHopAtB = true
		}
	}
	if !hasCfNextHopAtB {
		t.Errorf("step 2 — tB route should have NextHop=cfPubHex (campfire key signs re-advertisements), got: %+v", routesB)
	}

	// ── Step 3: tB re-advertises beacon to tC (path extended again) ───────────
	// tB appends its nodeID (idB) and re-signs with the campfire key.
	// tC records: NextHop=cfPubHex, Path>=3.
	minPathAtC := len(originPath) + 2
	routesC := waitForRouteWithMinPath(t, trC.RoutingTable(), campfireID, minPathAtC, 5*time.Second)
	if len(routesC) == 0 {
		t.Fatalf("step 3 — tC routing table should have route with path len >= %d after 2-hop re-advertisement", minPathAtC)
	}

	// tC's NextHop is the campfire key.
	hasCfNextHopAtC := false
	for _, r := range routesC {
		if r.NextHop == cfPubHex {
			hasCfNextHopAtC = true
		}
	}
	if !hasCfNextHopAtC {
		t.Errorf("step 3 — tC route should have NextHop=cfPubHex, got: %+v", routesC)
	}

	// Verify path grew at each hop.
	maxPathAtC := 0
	for _, r := range routesC {
		if len(r.Path) > maxPathAtC {
			maxPathAtC = len(r.Path)
		}
	}
	if maxPathAtC < minPathAtC {
		t.Errorf("step 3 — tC path len = %d, want >= %d (path should grow at each hop)", maxPathAtC, minPathAtC)
	}

	// ── Step 4: idSender delivers a message to tA ─────────────────────────────
	// tA path-vector forwarding:
	//   - NextHops = {idOrigin} (from the beacon in step 1)
	//   - PeerNeedsSet = {idOrigin} (idOrigin delivered the beacon)
	//   - sender = idSender (not idOrigin, so idOrigin not excluded)
	//   - ForwardingSet = {idOrigin}
	//   - idOrigin is NOT a local peer → check route.Endpoint for NextHop=idOrigin
	//   - route.Endpoint = epB (from the beacon in step 1) → forward to epB ✓
	msg := newTestMessage(t, idSender)
	if err := cfhttp.Deliver(epA, campfireID, msg, idSender); err != nil {
		t.Fatalf("step 4 — deliver message from idSender to tA: %v", err)
	}

	// tB should receive the message (tA→tB via path-vector routing; route.Endpoint=epB).
	if !waitForMessage(t, sB, campfireID, msg.ID, 3*time.Second) {
		t.Error("step 4 — tB should receive message forwarded from tA via path-vector (route.Endpoint=epB)")
	}

	// ── Step 5: Enable tB→tC forwarding and verify end-to-end ────────────────
	// tB receives the forwarded message signed by campfire key (senderHex = cfPubHex).
	// tB's forwarding set = (NextHops ∪ PeerNeedsSet) - sender.
	// Currently: NextHops = {cfPubHex}, PeerNeedsSet = {cfPubHex} → ForwardingSet = {} (empty).
	//
	// To enable tB→tC forwarding, idC must be in tB's PeerNeedsSet. We achieve this
	// by having idC deliver a beacon directly to tB. This adds idC to PeerNeedsSet
	// and installs a route for idC at tB, enabling forwarding to epC.
	beaconFromC := makeSignedBeaconMessage(t, idC, cfPub, cfPriv, epC, []string{idC.PublicKeyHex()})
	if err := cfhttp.Deliver(epB, campfireID, beaconFromC, idC); err != nil {
		t.Fatalf("step 5 setup — deliver idC beacon to tB: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // let tB update PeerNeedsSet

	// Deliver a second message from idSender to tA — now tB can forward to tC.
	// tB forwarding: NextHops = {cfPubHex, idC}, PeerNeedsSet = {cfPubHex, idC}
	//   - sender = cfPubHex
	//   - ForwardingSet = {cfPubHex, idC} - {cfPubHex} = {idC}
	//   - nodeToEndpoint[idC] = epC (local peer of tB) → forward to epC ✓
	msg2 := newTestMessage(t, idSender)
	if err := cfhttp.Deliver(epA, campfireID, msg2, idSender); err != nil {
		t.Fatalf("step 5 — deliver second message from idSender to tA: %v", err)
	}

	// tB should receive msg2.
	if !waitForMessage(t, sB, campfireID, msg2.ID, 3*time.Second) {
		t.Error("step 5 — tB should have msg2 (forwarded from tA)")
	}

	// tC should receive msg2 (forwarded A→B→C via path-vector).
	if !waitForMessage(t, sC, campfireID, msg2.ID, 5*time.Second) {
		t.Error("step 5 — tC should receive message forwarded from tA via tB (A→B→C path-vector next-hop forwarding)")
	}
}

// TestPathVectorWithdrawalE2E verifies that a routing:withdraw from A propagates
// through B to C, causing all three nodes to remove the route for the campfire.
//
// Topology: A → B → C (chain). Beacon propagates first, then withdrawal from A.
//
// Port block: 523-525.
func TestPathVectorWithdrawalE2E(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireID := hex.EncodeToString(cfPub)
	cfPubHex := campfireID

	idA := tempIdentity(t)
	idB := tempIdentity(t)
	idC := tempIdentity(t)

	sA := tempStore(t)
	sB := tempStore(t)
	sC := tempStore(t)

	addMembershipWithRole(t, sA, campfireID, "creator")
	addMembershipWithRole(t, sB, campfireID, "member")
	addMembershipWithRole(t, sC, campfireID, "member")

	for _, pubkey := range []string{idA.PublicKeyHex(), idB.PublicKeyHex(), idC.PublicKeyHex(), cfPubHex} {
		addPeerEndpoint(t, sA, campfireID, pubkey)
		addPeerEndpoint(t, sB, campfireID, pubkey)
		addPeerEndpoint(t, sC, campfireID, pubkey)
	}

	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+523)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+524)
	addrC := fmt.Sprintf("127.0.0.1:%d", base+525)
	epA := fmt.Sprintf("http://%s", addrA)
	epB := fmt.Sprintf("http://%s", addrB)
	epC := fmt.Sprintf("http://%s", addrC)

	makeKP := func(priv ed25519.PrivateKey, pub ed25519.PublicKey) func(string) ([]byte, []byte, error) {
		return func(id string) ([]byte, []byte, error) {
			if id == campfireID {
				return priv, pub, nil
			}
			return nil, nil, fmt.Errorf("no key for %s", id)
		}
	}

	trA := cfhttp.New(addrA, sA)
	trA.SetSelfInfo(idA.PublicKeyHex(), epA)
	trA.SetKeyProvider(makeKP(cfPriv, cfPub))
	if err := trA.Start(); err != nil {
		t.Fatalf("trA.Start: %v", err)
	}
	t.Cleanup(func() { trA.Stop() }) //nolint:errcheck

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	trB.SetKeyProvider(makeKP(cfPriv, cfPub))
	if err := trB.Start(); err != nil {
		t.Fatalf("trB.Start: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck

	trC := cfhttp.New(addrC, sC)
	trC.SetSelfInfo(idC.PublicKeyHex(), epC)
	trC.SetKeyProvider(makeKP(cfPriv, cfPub))
	if err := trC.Start(); err != nil {
		t.Fatalf("trC.Start: %v", err)
	}
	t.Cleanup(func() { trC.Stop() }) //nolint:errcheck

	time.Sleep(20 * time.Millisecond)

	trA.AddPeer(campfireID, idB.PublicKeyHex(), epB)
	trB.AddPeer(campfireID, idC.PublicKeyHex(), epC)

	// ── Phase 1: propagate beacon A→B→C ──────────────────────────────────────
	originPath := []string{idA.PublicKeyHex()}
	beaconMsg := makeSignedBeaconMessage(t, idA, cfPub, cfPriv, epA, originPath)
	if err := cfhttp.Deliver(epA, campfireID, beaconMsg, idA); err != nil {
		t.Fatalf("deliver beacon to tA: %v", err)
	}

	// Wait for tC to receive the route (2 hops from tA via tA→tB re-advert, tB→tC re-advert).
	routesC := waitForRoute(t, trC.RoutingTable(), campfireID, 5*time.Second)
	if len(routesC) == 0 {
		t.Fatal("prerequisite: tC should have route before withdrawal propagation test")
	}
	t.Logf("prerequisite passed: tC has %d route(s) for campfireID", len(routesC))

	// ── Phase 2: idA sends a withdrawal ──────────────────────────────────────
	withdrawMsg := makeSignedWithdrawMessage(t, idA, cfPub, cfPriv, "going offline")
	if err := cfhttp.Deliver(epA, campfireID, withdrawMsg, idA); err != nil {
		t.Fatalf("deliver withdrawal to tA: %v", err)
	}

	// tA removes the route immediately (withdrawal is processed synchronously).
	if routes := trA.RoutingTable().Lookup(campfireID); len(routes) != 0 {
		t.Errorf("tA: expected 0 routes after withdrawal, got %d", len(routes))
	}

	// tB removes its route after receiving the propagated withdrawal from tA.
	if !waitForNoRoute(t, trB.RoutingTable(), campfireID, 3*time.Second) {
		remaining := trB.RoutingTable().Lookup(campfireID)
		t.Errorf("tB: expected 0 routes after withdrawal propagation, still has %d route(s)", len(remaining))
	}

	// tC removes its route after withdrawal propagates A→B→C.
	if !waitForNoRoute(t, trC.RoutingTable(), campfireID, 4*time.Second) {
		remaining := trC.RoutingTable().Lookup(campfireID)
		t.Errorf("tC: expected 0 routes after 2-hop withdrawal propagation, still has %d route(s)", len(remaining))
	}
}

// TestPathVectorLegacyFallback verifies that when all beacons for a campfire have
// empty paths (legacy pre-v0.5.0 behavior), message forwarding falls back to flooding
// all known local peers rather than using path-vector next-hop selection.
//
// Setup: router tA has two local peers (tB and tC). A legacy beacon (empty path) arrives.
// Expected: both tB and tC receive a subsequent message (flood — no path-vector constraint).
//
// Port block: 526-528.
func TestPathVectorLegacyFallback(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireID := hex.EncodeToString(cfPub)
	cfPubHex := campfireID

	idA := tempIdentity(t)
	idB := tempIdentity(t)
	idC := tempIdentity(t)

	sA := tempStore(t)
	sB := tempStore(t)
	sC := tempStore(t)

	addMembershipWithRole(t, sA, campfireID, "creator")
	addMembershipWithRole(t, sB, campfireID, "member")
	addMembershipWithRole(t, sC, campfireID, "member")

	for _, pubkey := range []string{idA.PublicKeyHex(), idB.PublicKeyHex(), idC.PublicKeyHex(), cfPubHex} {
		addPeerEndpoint(t, sA, campfireID, pubkey)
		addPeerEndpoint(t, sB, campfireID, pubkey)
		addPeerEndpoint(t, sC, campfireID, pubkey)
	}

	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+526)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+527)
	addrC := fmt.Sprintf("127.0.0.1:%d", base+528)
	epA := fmt.Sprintf("http://%s", addrA)
	epB := fmt.Sprintf("http://%s", addrB)
	epC := fmt.Sprintf("http://%s", addrC)

	trA := startTransportWithKey(t, addrA, sA, idA, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addrB, sB, idB, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addrC, sC, idC, campfireID, cfPriv, cfPub)

	// Both tB and tC are local peers of tA.
	trA.AddPeer(campfireID, idB.PublicKeyHex(), epB)
	trA.AddPeer(campfireID, idC.PublicKeyHex(), epC)

	// Deliver a LEGACY beacon (nil path = empty path = pre-v0.5.0 behavior).
	// An empty-path beacon installs a route with no Path, triggering flood fallback.
	legacyBeacon := makeSignedBeaconMessage(t, idA, cfPub, cfPriv, epA, nil /* empty path */)
	if err := cfhttp.Deliver(epA, campfireID, legacyBeacon, idA); err != nil {
		t.Fatalf("deliver legacy beacon: %v", err)
	}

	// Wait for the beacon to be processed and route to be installed.
	time.Sleep(50 * time.Millisecond)

	// Verify the installed route has empty path (confirming legacy behavior).
	routesA := trA.RoutingTable().Lookup(campfireID)
	if len(routesA) > 0 {
		for _, r := range routesA {
			if len(r.Path) > 0 {
				t.Errorf("legacy beacon should install empty-path route; got non-empty path: %v", r.Path)
			}
		}
	}

	// Deliver a message from a separate sender (not idA, so idA is not excluded as sender).
	idSender := tempIdentity(t)
	addPeerEndpoint(t, sA, campfireID, idSender.PublicKeyHex())
	msg := newTestMessage(t, idSender)
	if err := cfhttp.Deliver(epA, campfireID, msg, idSender); err != nil {
		t.Fatalf("deliver message to tA: %v", err)
	}

	// Both tB and tC should receive the message — flood fallback (no path-vector next_hops).
	if !waitForMessage(t, sB, campfireID, msg.ID, 2*time.Second) {
		t.Error("legacy fallback: tB should receive message via flood (empty-path beacon, no path-vector routes)")
	}
	if !waitForMessage(t, sC, campfireID, msg.ID, 2*time.Second) {
		t.Error("legacy fallback: tC should receive message via flood (empty-path beacon, no path-vector routes)")
	}
}

// TestPathVectorBeaconChainPathGrowth verifies that the beacon path grows correctly
// as it propagates A→B→C, with each router appending its own nodeID.
//
// This is a focused sub-test of the path accumulation aspect of TestPathVectorE2E,
// using different senders and cleaner assertions.
//
// Port block: 529-531.
func TestPathVectorBeaconChainPathGrowth(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireID := hex.EncodeToString(cfPub)
	cfPubHex := campfireID

	idA := tempIdentity(t)
	idB := tempIdentity(t)
	idC := tempIdentity(t)
	idOrigin := tempIdentity(t) // beacon originator (separate from A, B, C)

	sA := tempStore(t)
	sB := tempStore(t)
	sC := tempStore(t)

	addMembershipWithRole(t, sA, campfireID, "member")
	addMembershipWithRole(t, sB, campfireID, "member")
	addMembershipWithRole(t, sC, campfireID, "member")

	for _, pubkey := range []string{idA.PublicKeyHex(), idB.PublicKeyHex(), idC.PublicKeyHex(), cfPubHex, idOrigin.PublicKeyHex()} {
		addPeerEndpoint(t, sA, campfireID, pubkey)
		addPeerEndpoint(t, sB, campfireID, pubkey)
		addPeerEndpoint(t, sC, campfireID, pubkey)
	}

	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+529)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+530)
	addrC := fmt.Sprintf("127.0.0.1:%d", base+531)
	epA := fmt.Sprintf("http://%s", addrA)
	epB := fmt.Sprintf("http://%s", addrB)
	epC := fmt.Sprintf("http://%s", addrC)

	makeKP := func(priv ed25519.PrivateKey, pub ed25519.PublicKey) func(string) ([]byte, []byte, error) {
		return func(id string) ([]byte, []byte, error) {
			if id == campfireID {
				return priv, pub, nil
			}
			return nil, nil, fmt.Errorf("no key for %s", id)
		}
	}

	trA := cfhttp.New(addrA, sA)
	trA.SetSelfInfo(idA.PublicKeyHex(), epA)
	trA.SetKeyProvider(makeKP(cfPriv, cfPub))
	if err := trA.Start(); err != nil {
		t.Fatalf("trA.Start: %v", err)
	}
	t.Cleanup(func() { trA.Stop() }) //nolint:errcheck

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	trB.SetKeyProvider(makeKP(cfPriv, cfPub))
	if err := trB.Start(); err != nil {
		t.Fatalf("trB.Start: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck

	trC := cfhttp.New(addrC, sC)
	trC.SetSelfInfo(idC.PublicKeyHex(), epC)
	trC.SetKeyProvider(makeKP(cfPriv, cfPub))
	if err := trC.Start(); err != nil {
		t.Fatalf("trC.Start: %v", err)
	}
	t.Cleanup(func() { trC.Stop() }) //nolint:errcheck

	time.Sleep(20 * time.Millisecond)

	// Chain: A→B→C.
	trA.AddPeer(campfireID, idB.PublicKeyHex(), epB)
	trB.AddPeer(campfireID, idC.PublicKeyHex(), epC)

	// Deliver beacon from idOrigin to tA. originPath = [idOrigin].
	originPath := []string{idOrigin.PublicKeyHex()}
	beaconMsg := makeSignedBeaconMessage(t, idOrigin, cfPub, cfPriv, "http://origin.example.com:9090", originPath)
	if err := cfhttp.Deliver(epA, campfireID, beaconMsg, idOrigin); err != nil {
		t.Fatalf("deliver beacon to tA: %v", err)
	}

	// tA should have the route.
	routesA := waitForRoute(t, trA.RoutingTable(), campfireID, 2*time.Second)
	if len(routesA) == 0 {
		t.Fatal("tA: routing table should have route after beacon delivery")
	}
	t.Logf("tA route: path=%v", routesA[0].Path)

	// tB should have a route with path len >= 2 (A appends its nodeID).
	minPathB := len(originPath) + 1
	routesB := waitForRouteWithMinPath(t, trB.RoutingTable(), campfireID, minPathB, 3*time.Second)
	if len(routesB) == 0 {
		t.Fatalf("tB: routing table should have route with path len >= %d; got 0 routes", minPathB)
	}
	maxPathB := 0
	for _, r := range routesB {
		if len(r.Path) > maxPathB {
			maxPathB = len(r.Path)
		}
	}
	t.Logf("tB route: path len=%d (want >= %d)", maxPathB, minPathB)

	// tC should have a route with path len >= 3 (A appends, then B appends).
	minPathC := len(originPath) + 2
	routesC := waitForRouteWithMinPath(t, trC.RoutingTable(), campfireID, minPathC, 5*time.Second)
	if len(routesC) == 0 {
		t.Fatalf("tC: routing table should have route with path len >= %d; got 0 routes", minPathC)
	}
	maxPathC := 0
	for _, r := range routesC {
		if len(r.Path) > maxPathC {
			maxPathC = len(r.Path)
		}
	}
	t.Logf("tC route: path len=%d (want >= %d)", maxPathC, minPathC)

	// Path length must have grown at each hop.
	if maxPathB < minPathB {
		t.Errorf("tB path len=%d, want >= %d (A should append its nodeID)", maxPathB, minPathB)
	}
	if maxPathC < minPathC {
		t.Errorf("tC path len=%d, want >= %d (A and B should each append their nodeID)", maxPathC, minPathC)
	}
}

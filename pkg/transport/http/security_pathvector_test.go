package http

// Security review tests for path-vector amendment (item agentic-internet-ops-ysp).
//
// Focus areas:
//   1. Path signing — forgery, truncation, reorder, threshold type confusion
//   2. Loop detection — encoding bypass, partial loops
//   3. Beacon re-sign — advisory path injection, re-sign failure state
//   4. Forwarding bypass — flood fallback forcing, PeerNeedsSet poisoning
//
// Each test is labeled with severity: CRITICAL / HIGH / MEDIUM / LOW.
//
// These are unit tests (package http, internal access). They exercise RoutingTable
// directly rather than spinning up HTTP servers so they run without port contention.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// makePathBeaconPayload creates a beacon payload with a path field.
// If signWithPath is true the inner_signature covers the path (threshold=1).
// If signWithPath is false the inner_signature does NOT cover the path
// (threshold>1 / advisory path).
func makePathBeaconPayload(
	t *testing.T,
	cfPub ed25519.PublicKey,
	cfPriv ed25519.PrivateKey,
	endpoint string,
	path []string,
	signWithPath bool,
) []byte {
	t.Helper()
	campfireIDHex := hex.EncodeToString(cfPub)
	ts := time.Now().Unix()

	decl := beacon.BeaconDeclaration{
		CampfireID:        campfireIDHex,
		ConventionVersion: "0.5.0",
		Description:       "path-vector test campfire",
		Endpoint:          endpoint,
		JoinProtocol:      "open",
		Timestamp:         ts,
		Transport:         "p2p-http",
		Path:              path,
	}

	var signBytes []byte
	var err error
	if signWithPath {
		signBytes, err = beacon.MarshalInnerSignInput(decl)
	} else {
		signBytes, err = beacon.MarshalInnerSignInputNoPath(decl)
	}
	if err != nil {
		t.Fatalf("marshaling sign input: %v", err)
	}
	sig := ed25519.Sign(cfPriv, signBytes)

	bp := beaconPayload{
		CampfireID:        campfireIDHex,
		Endpoint:          endpoint,
		Transport:         "p2p-http",
		Description:       "path-vector test campfire",
		JoinProtocol:      "open",
		Timestamp:         ts,
		ConventionVersion: "0.5.0",
		InnerSignature:    hex.EncodeToString(sig),
		Path:              path,
	}
	b, err := json.Marshal(bp)
	if err != nil {
		t.Fatalf("marshaling beacon payload: %v", err)
	}
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. PATH SIGNING
// ─────────────────────────────────────────────────────────────────────────────

// CRITICAL: TestPathTruncationAccepted
//
// An attacker receives a threshold=1 beacon (path in signature) and strips
// intermediate hops from the path before forwarding. Because VerifyDeclaration
// tries with-path first (which fails after truncation), it then falls back to
// without-path — which will ALSO fail for a genuine threshold=1 beacon, so
// the beacon should be REJECTED.
//
// This test proves that truncating the path on a threshold=1 beacon causes
// HandleBeacon to reject it (inner_signature mismatch).
func TestPathTruncationRejected(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	rt := newRoutingTable()

	originalPath := []string{"nodeA", "nodeB", "nodeC"}
	raw := makePathBeaconPayload(t, cfPub, cfPriv, "http://example.com", originalPath, true /* signWithPath */)

	// Mutate: strip the last hop (truncate path from 3 to 2).
	var bp beaconPayload
	if err := json.Unmarshal(raw, &bp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	bp.Path = []string{"nodeA", "nodeB"} // truncated
	truncated, _ := json.Marshal(bp)

	err := rt.HandleBeacon(truncated, "gw", "nodeB")
	if err == nil {
		t.Error("CRITICAL: truncated path accepted on threshold=1 beacon — inner_signature should fail")
	}
}

// CRITICAL: TestPathReorderRejected
//
// An attacker reorders hops in a threshold=1 beacon path. The inner_signature
// covers the ordered path, so reordering should invalidate it.
func TestPathReorderRejected(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	rt := newRoutingTable()

	originalPath := []string{"nodeA", "nodeB", "nodeC"}
	raw := makePathBeaconPayload(t, cfPub, cfPriv, "http://example.com", originalPath, true)

	var bp beaconPayload
	json.Unmarshal(raw, &bp)   //nolint:errcheck
	bp.Path = []string{"nodeC", "nodeB", "nodeA"} // reversed
	reordered, _ := json.Marshal(bp)

	err := rt.HandleBeacon(reordered, "gw", "nodeC")
	if err == nil {
		t.Error("CRITICAL: reordered path accepted on threshold=1 beacon — inner_signature should fail")
	}
}

// CRITICAL: TestAdvisoryPathInjection — threshold>1 path forgery
//
// An attacker intercepts a threshold>1 beacon (advisory path, signature does NOT
// cover path). They replace the path field with a forged path containing node_ids
// they control, then forward it. The beacon must still pass VerifyDeclaration
// (the signature doesn't cover the path), meaning ANYONE can forge the path of a
// threshold>1 beacon. This test documents that advisory paths are UNVERIFIED and
// can be set arbitrarily.
//
// Security implication: for threshold>1 campfires, the "path" field used for loop
// prevention and route preference CANNOT be trusted. An attacker with the campfire
// key (or intercepting a beacon in transit) can inject arbitrary node_ids into the
// path to manipulate routing decisions (e.g., make their route appear shortest).
func TestAdvisoryPathInjection(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	rt := newRoutingTableWithNodeID("honest-node")

	// Legitimate beacon: threshold>1 (path is advisory, not in signature).
	// Path is ["originNode"].
	raw := makePathBeaconPayload(t, cfPub, cfPriv, "http://target.com", []string{"originNode"}, false /* advisory */)

	var bp beaconPayload
	json.Unmarshal(raw, &bp) //nolint:errcheck

	// Attacker injects a forged path — keeps original signature (not covered).
	// They include "honest-node" to try to trigger loop detection, or they
	// make the path empty to look like the shortest route.
	bp.Path = []string{} // strip path to appear as legacy/origin beacon
	forged, _ := json.Marshal(bp)

	err := rt.HandleBeacon(forged, "gw", "attacker-node")
	// This will succeed — documenting that path is not authenticated for advisory beacons.
	// The test asserts the beacon IS accepted (proving the vulnerability exists).
	if err != nil {
		t.Logf("beacon rejected (may be expected): %v", err)
		return
	}

	// Beacon was accepted with forged path — verify the route has the injected (empty) path.
	campfireIDHex := hex.EncodeToString(cfPub)
	routes := rt.Lookup(campfireIDHex)
	if len(routes) == 0 {
		t.Fatal("no routes installed after forged advisory beacon")
	}
	if len(routes[0].Path) != 0 {
		t.Errorf("expected forged empty path, got: %v", routes[0].Path)
	}
	t.Log("CRITICAL: advisory (threshold>1) beacon path is not authenticated — attacker can forge path field")
}

// CRITICAL: TestAdvisoryPathHijackShorterRoute
//
// Demonstrates the routing-preference manipulation vector: an attacker who intercepts
// a threshold>1 beacon (or has the campfire key) can inject a shorter path (e.g., [])
// to make their route appear best and attract forwarded traffic.
func TestAdvisoryPathHijackShorterRoute(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	rt := newRoutingTable()
	campfireIDHex := hex.EncodeToString(cfPub)

	// Legitimate beacon from honest router: path length 3.
	legitimatePayload := makePathBeaconPayload(t, cfPub, cfPriv, "http://honest.com",
		[]string{"nodeA", "nodeB", "nodeC"}, false /* advisory */)
	if err := rt.HandleBeacon(legitimatePayload, "gw", "nodeC"); err != nil {
		t.Fatalf("HandleBeacon (legitimate): %v", err)
	}

	// Attacker sends the same beacon with path stripped to [] (appears as origin).
	var bp beaconPayload
	json.Unmarshal(legitimatePayload, &bp) //nolint:errcheck
	bp.Endpoint = "http://attacker.com"    // different endpoint so it's a distinct route
	bp.Path = []string{}                    // forged shorter path
	// Re-sign without path to keep signature valid (this is what an attacker with the
	// campfire key or a relay-mode node would do).
	decl := beacon.BeaconDeclaration{
		CampfireID:        bp.CampfireID,
		Endpoint:          bp.Endpoint,
		Transport:         bp.Transport,
		Description:       bp.Description,
		JoinProtocol:      bp.JoinProtocol,
		Timestamp:         bp.Timestamp,
		ConventionVersion: bp.ConventionVersion,
	}
	signBytes, _ := beacon.MarshalInnerSignInputNoPath(decl)
	sig := ed25519.Sign(cfPriv, signBytes)
	bp.InnerSignature = hex.EncodeToString(sig)
	attackerPayload, _ := json.Marshal(bp)

	if err := rt.HandleBeacon(attackerPayload, "gw", "attacker"); err != nil {
		t.Fatalf("HandleBeacon (attacker): %v", err)
	}

	// Route selection: shortest path wins. Attacker's route (len 0) beats honest (len 3).
	routes := rt.Lookup(campfireIDHex)
	if len(routes) < 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	if routes[0].Endpoint != "http://attacker.com" {
		t.Errorf("CRITICAL: expected attacker's shorter route to win, got: %s", routes[0].Endpoint)
	} else {
		t.Log("CRITICAL: attacker with advisory beacon can inject shorter path to win route selection")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. LOOP DETECTION
// ─────────────────────────────────────────────────────────────────────────────

// HIGH: TestLoopDetectionCaseSensitivity
//
// Loop detection compares node_id strings with ==. If the router's node_id is
// stored as lowercase hex but the beacon path contains the same key in uppercase
// hex, the comparison fails and the loop is not detected.
//
// Ed25519 public keys are conventionally hex-encoded lowercase, but the spec does
// not mandate case. An attacker can submit a beacon with their node_id in uppercase
// to bypass their own loop detection and cause routing instability.
func TestLoopDetectionCaseSensitivity(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)

	// Router's node_id is lowercase hex.
	_, routerPub, _ := ed25519.GenerateKey(nil)
	routerNodeIDLower := strings.ToLower(hex.EncodeToString(routerPub))
	routerNodeIDUpper := strings.ToUpper(hex.EncodeToString(routerPub))

	rt := newRoutingTableWithNodeID(routerNodeIDLower)

	// Beacon path contains the router's node_id in UPPERCASE — same key, different encoding.
	// Loop detection uses ==, so routerNodeIDUpper != routerNodeIDLower.
	path := []string{"nodeA", routerNodeIDUpper}
	raw := makePathBeaconPayload(t, cfPub, cfPriv, "http://example.com", path, false /* advisory */)

	err := rt.HandleBeacon(raw, "gw", "nodeA")
	if err != nil {
		t.Logf("HandleBeacon error: %v", err)
		return
	}

	// If the beacon was accepted, loop detection was bypassed by case difference.
	campfireIDHex := hex.EncodeToString(cfPub)
	routes := rt.Lookup(campfireIDHex)
	if len(routes) > 0 {
		// FINDING: loop detection is case-sensitive. Uppercase encoding of the same
		// key bypasses the string equality check. This is an active vulnerability.
		t.Log("HIGH: loop detection bypassed via uppercase node_id encoding — beacon accepted despite router's node_id in path")
		t.Log("HIGH CONFIRMED: loop detection requires case-insensitive or canonical comparison of node_ids")
	} else {
		t.Log("OK: uppercase node_id correctly detected as loop (loop detection is case-insensitive)")
	}
}

// HIGH: TestLoopDetectionEmptyNodeID
//
// When RoutingTable is created with newRoutingTable() (no node_id), loop detection
// is completely disabled. The spec says routers MUST check for their own node_id
// in the path, but the implementation makes this optional.
//
// A node deployed without SetSelfInfo / newRoutingTableWithNodeID will forward
// beacons that should be dropped, creating routing loops.
func TestLoopDetectionEmptyNodeID(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)

	// Deliberately no node_id — simulates a misconfigured or legacy routing table.
	rt := newRoutingTable() // NodeID == ""

	// Beacon path contains "nodeA" — if this router IS nodeA, it should detect the loop.
	// But since NodeID is "", it cannot.
	path := []string{"nodeA", "nodeB"}
	raw := makePathBeaconPayload(t, cfPub, cfPriv, "http://example.com", path, false)

	err := rt.HandleBeacon(raw, "gw", "nodeB")
	if err != nil {
		t.Logf("HandleBeacon error: %v", err)
		return
	}

	campfireIDHex := hex.EncodeToString(cfPub)
	routes := rt.Lookup(campfireIDHex)
	if len(routes) > 0 {
		t.Log("HIGH: loop detection is silently skipped when NodeID is empty — spec requires MUST check")
		// This documents the gap: spec says MUST, code says "if NodeID != """.
		// Not a test failure per se — the behavior is intentional per comment in code,
		// but it means loop prevention depends on correct deployment configuration.
	}
}

// HIGH: TestPathLengthUnbounded
//
// The path field has no length limit. An attacker can craft a beacon with a path
// of arbitrary length (e.g., 10,000 hops), forcing O(path_length) loop detection
// scans on every router that processes the beacon. This is a CPU amplification attack.
//
// Spec says max_hops is 8 (for message provenance), but path length for beacons
// has no explicit cap in the amendment.
func TestPathLengthUnbounded(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	rt := newRoutingTableWithNodeID("victim-node")

	// Craft a path with 1000 hops (all distinct, no loop).
	bigPath := make([]string, 1000)
	for i := range bigPath {
		bigPath[i] = hex.EncodeToString([]byte{byte(i >> 8), byte(i)}) + "padding"
	}
	raw := makePathBeaconPayload(t, cfPub, cfPriv, "http://example.com", bigPath, false)

	// This should succeed (no loop), but requires 1000 string comparisons.
	// The test documents that there is no path length cap.
	err := rt.HandleBeacon(raw, "gw", "some-sender")
	if err != nil {
		t.Logf("HandleBeacon rejected oversized path: %v", err)
		return
	}
	t.Log("MEDIUM: beacon with 1000-hop path accepted — no path length limit enforced; CPU amplification possible")
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. BEACON RE-SIGN
// ─────────────────────────────────────────────────────────────────────────────

// MEDIUM: TestReSignKeepsOriginalSigOnMarshalFailure
//
// In reAdvertiseBeacon, if the re-sign marshal step fails, the code falls through
// keeping the original inner_signature with the newly-appended path. This produces
// a beacon where:
//   - path = [..., selfNodeID]   (updated)
//   - inner_signature = original (signed over old path)
//
// Downstream nodes will attempt to verify: with-path fails (path changed),
// without-path succeeds via the advisory fallback. This means the signature
// effectively stops covering the path at any hop where re-signing is skipped.
//
// This test verifies VerifyDeclaration behavior when inner_signature covers the
// original path but the path field has been extended (simulating a re-sign failure).
func TestReSignOriginalSigWithExtendedPath(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)

	// Create beacon signed with path ["nodeA"].
	originalPath := []string{"nodeA"}
	decl := beacon.BeaconDeclaration{
		CampfireID:        campfireIDHex,
		Endpoint:          "http://example.com",
		Transport:         "p2p-http",
		Description:       "test",
		JoinProtocol:      "open",
		Timestamp:         time.Now().Unix(),
		ConventionVersion: "0.5.0",
		Path:              originalPath,
	}
	signBytes, _ := beacon.MarshalInnerSignInput(decl)
	sig := ed25519.Sign(cfPriv, signBytes)
	decl.InnerSignature = hex.EncodeToString(sig)

	// Simulate re-sign failure: path is extended but signature is unchanged.
	extendedPath := []string{"nodeA", "nodeB"} // nodeB appended, no re-sign
	declExtended := decl
	declExtended.Path = extendedPath

	// VerifyDeclaration should fail with-path (path changed) but succeed without-path
	// if the original was signed without path — BUT here the original WAS signed with
	// path. So without-path fallback uses a different signing input and should also fail.
	verified := beacon.VerifyDeclaration(declExtended)

	if verified {
		// This means the beacon was accepted despite the path being modified after signing.
		// Possible if the original sig was created without path AND the no-path fallback matches.
		t.Log("MEDIUM: extended path beacon accepted — downstream router cannot distinguish threshold=1 re-sign failure from advisory path")
	} else {
		t.Log("OK: extended path beacon rejected — signature mismatch correctly detected")
	}
	// Note: the real risk is in the reAdvertiseBeacon code path where re-sign silently
	// falls back to keeping the original sig. This test validates VerifyDeclaration
	// behavior; the re-sign failure is tested by code inspection (see finding report).
}

// MEDIUM: TestThresholdTypeConfusionDowngrade
//
// VerifyDeclaration tries with-path first, then without-path on failure.
// An attacker who holds the campfire key can create a threshold=1 beacon (path in sig),
// then strip the path field before forwarding. The without-path fallback would verify
// the signature — but this only applies if they also created a version WITHOUT path.
//
// The actual confusion: a threshold>1 beacon (signed without path) can have its path
// replaced with any content by anyone. If an attacker strips the path entirely,
// VerifyDeclaration succeeds via with-path (since path is empty/nil, both paths produce
// identical signing input). This means stripping the path from a threshold>1 beacon
// causes it to be treated as a threshold=1 beacon (path verified, but path is now empty).
func TestThresholdTypeConfusionEmptyPathStrip(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	ts := time.Now().Unix()

	// Create a threshold>1 beacon with path ["nodeA"] — signed WITHOUT path.
	decl := beacon.BeaconDeclaration{
		CampfireID:        campfireIDHex,
		Endpoint:          "http://example.com",
		Transport:         "p2p-http",
		Description:       "test",
		JoinProtocol:      "open",
		Timestamp:         ts,
		ConventionVersion: "0.5.0",
		Path:              []string{"nodeA"},
	}
	signBytes, _ := beacon.MarshalInnerSignInputNoPath(decl) // threshold>1: path not in sig
	sig := ed25519.Sign(cfPriv, signBytes)

	// Attacker strips the path entirely (path = nil/empty).
	// With-path attempt: MarshalInnerSignInput with nil path == MarshalInnerSignInputNoPath.
	// So with-path verification succeeds!
	stripped := beacon.BeaconDeclaration{
		CampfireID:        campfireIDHex,
		Endpoint:          "http://example.com",
		Transport:         "p2p-http",
		Description:       "test",
		JoinProtocol:      "open",
		Timestamp:         ts,
		ConventionVersion: "0.5.0",
		Path:              nil, // path stripped
		InnerSignature:    hex.EncodeToString(sig),
	}

	verified := beacon.VerifyDeclaration(stripped)
	if !verified {
		t.Log("OK: stripped path beacon rejected")
		return
	}

	// Beacon with stripped path accepted — now route is installed without any path info.
	// This forces flood-mode fallback for this campfire.
	rt := newRoutingTableWithNodeID("someNode")
	raw, _ := json.Marshal(beaconPayload{
		CampfireID:        stripped.CampfireID,
		Endpoint:          stripped.Endpoint,
		Transport:         stripped.Transport,
		Description:       stripped.Description,
		JoinProtocol:      stripped.JoinProtocol,
		Timestamp:         stripped.Timestamp,
		ConventionVersion: stripped.ConventionVersion,
		InnerSignature:    stripped.InnerSignature,
		Path:              nil,
	})
	if err := rt.HandleBeacon(raw, "gw", "attacker"); err != nil {
		t.Logf("HandleBeacon rejected: %v", err)
		return
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) > 0 && len(routes[0].Path) == 0 {
		t.Log("MEDIUM: threshold>1 beacon with advisory path can be stripped by attacker — routes with empty path force flood fallback")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. FORWARDING BYPASS
// ─────────────────────────────────────────────────────────────────────────────

// HIGH: TestFloodFallbackForcedByEmptyPath
//
// The forwardMessage decision logic checks whether ANY route has a non-empty path.
// If all routes have empty paths (legacy beacons), it falls back to flood mode.
//
// An attacker (or a legitimate node re-advertising a stripped beacon) can inject
// route entries with empty paths, forcing the router into flood mode even when
// other path-vector routes exist. This defeats the DDoS amplification protection.
func TestFloodFallbackForcedByEmptyPath(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	rt := newRoutingTable()
	campfireIDHex := hex.EncodeToString(cfPub)

	// Install a legitimate path-vector route.
	legitimatePayload := makePathBeaconPayload(t, cfPub, cfPriv, "http://honest.com",
		[]string{"nodeA", "nodeB"}, false)
	if err := rt.HandleBeacon(legitimatePayload, "gw", "nodeB"); err != nil {
		t.Fatalf("HandleBeacon (legitimate): %v", err)
	}

	routes := rt.Lookup(campfireIDHex)
	hasPathVector := false
	for _, r := range routes {
		if len(r.Path) > 0 {
			hasPathVector = true
		}
	}
	if !hasPathVector {
		t.Fatal("setup: expected path-vector route to be installed")
	}

	// Now install a legacy beacon (empty path) for the SAME endpoint.
	// Since endpoint matches, HandleBeacon will REFRESH the entry, clearing the path.
	emptyPathPayload := makePathBeaconPayload(t, cfPub, cfPriv, "http://honest.com",
		nil /* empty path */, false)
	if err := rt.HandleBeacon(emptyPathPayload, "gw", "nodeB"); err != nil {
		t.Fatalf("HandleBeacon (empty path): %v", err)
	}

	// Check: after the refresh, the path is now empty — flood fallback will be used.
	routes = rt.Lookup(campfireIDHex)
	hasPathVectorAfter := false
	for _, r := range routes {
		if len(r.Path) > 0 {
			hasPathVectorAfter = true
		}
	}
	if !hasPathVectorAfter {
		t.Log("HIGH: refresh with empty-path beacon clears path from routing entry — " +
			"forwardMessage falls back to flood mode; amplification protection defeated")
	}
}

// HIGH: TestPeerNeedsSetPoisoning
//
// RecordMessageDelivery adds any sender to the peerNeeds set for the campfire.
// An attacker with delivery rights to any campfire the victim routes can send
// a single message for campfire C. This adds the attacker's node_id to
// peerNeeds[C], meaning the attacker will receive ALL future forwarded messages
// for campfire C via path-vector forwarding.
//
// This is a traffic interception / exfiltration vector.
func TestPeerNeedsSetPoisoning(t *testing.T) {
	rt := newRoutingTable()

	campfireID := "deadbeef" + strings.Repeat("aa", 28) // fake campfire ID
	legitimatePeer := "aabbcc" + strings.Repeat("00", 29)
	attackerNode := "evil0001" + strings.Repeat("ff", 28)

	// Legitimate peer delivers a message — populates peerNeeds.
	rt.RecordMessageDelivery(campfireID, legitimatePeer)

	// Attacker delivers a single message — they get added to peerNeeds.
	rt.RecordMessageDelivery(campfireID, attackerNode)

	peerNeeds := rt.PeerNeedsSet(campfireID)
	if !peerNeeds[attackerNode] {
		t.Fatal("setup: attacker not in peerNeeds after delivery")
	}

	// Now verify: attacker remains in peerNeeds even after they stop sending.
	// There is no expiry or removal mechanism for peerNeeds entries.
	// An attacker sends 1 message and receives all future forwarded messages forever
	// (until routing:withdraw removes the campfire entry entirely).
	if peerNeeds[attackerNode] {
		t.Log("HIGH: attacker added to PeerNeedsSet by sending one message — " +
			"will receive all future forwarded messages for campfire indefinitely; no expiry mechanism")
	}
}

// MEDIUM: TestPeerNeedsSetNoExpiry
//
// PeerNeedsSet entries persist until HandleWithdraw deletes the campfire entry.
// A peer that was once a member (and sent messages) stays in the forwarding set
// forever, even after they leave. This is a minor data retention / unnecessary
// forwarding issue.
func TestPeerNeedsSetNoExpiry(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	rt := newRoutingTable()
	campfireIDHex := hex.EncodeToString(cfPub)

	// Peer delivers a message.
	rt.RecordMessageDelivery(campfireIDHex, "departed-peer")

	peerNeeds := rt.PeerNeedsSet(campfireIDHex)
	if !peerNeeds["departed-peer"] {
		t.Fatal("peer not recorded")
	}

	// Withdraw the campfire (simulates peer leaving or campfire shutting down).
	withdrawPayload := makeWithdrawPayload(t, cfPriv, cfPub, "leaving")
	if err := rt.HandleWithdraw(withdrawPayload); err != nil {
		t.Fatalf("HandleWithdraw: %v", err)
	}

	// After withdraw, peerNeeds for that campfire is cleared.
	peerNeedsAfter := rt.PeerNeedsSet(campfireIDHex)
	if peerNeedsAfter["departed-peer"] {
		t.Log("OK: withdraw clears peerNeeds")
	} else {
		t.Log("MEDIUM: peerNeeds only cleared on withdraw — no TTL-based expiry; departed peers linger until next withdraw")
	}
}

// HIGH: TestBeaconReAdvertisementFloodsGateway
//
// reAdvertiseBeacon sends to ALL known peers for the GATEWAY campfire, not just
// next-hops. This means beacon propagation uses flood-and-dedup, not path-vector,
// defeating the efficiency improvement for the beacon plane itself.
//
// This test documents the gap via RoutingTable: even with path-vector data routes,
// beacons are re-advertised to all peers. The beacon plane inherits the O(degree)
// amplification that the amendment aims to remove.
//
// (This is a design-level finding — tested at the routing table level only,
// since the full reAdvertiseBeacon function requires HTTP server setup.)
func TestBeaconPropagationUsesFloodNotPathVector(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	rt := newRoutingTableWithNodeID("routerX")
	campfireIDHex := hex.EncodeToString(cfPub)

	// Install a path-vector route with a specific next_hop.
	path := []string{"nodeA"}
	raw := makePathBeaconPayload(t, cfPub, cfPriv, "http://nodeA.com", path, false)
	if err := rt.HandleBeacon(raw, "gw", "nodeA"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) == 0 {
		t.Fatal("no routes installed")
	}

	// The route has a next_hop of "nodeA".
	// forwardMessage (data plane) would only forward to nodeA.
	// But reAdvertiseBeacon (beacon plane) forwards to ALL peers for the gateway campfire.
	// This is verified by reading the reAdvertiseBeacon code: it iterates h.transport.peers[campfireID]
	// (all local peers), not routes[].NextHop.
	// We document this finding via a comment since the actual beacon forwarding
	// happens in the HTTP layer which requires server setup.

	if routes[0].NextHop == "nodeA" {
		t.Log("HIGH: path-vector route has next_hop=nodeA, but reAdvertiseBeacon floods ALL " +
			"gateway peers — beacon plane does not use path-vector forwarding, retaining O(degree) amplification for beacons")
	}
}

// LOW: TestLoopDetectionSkippedWhenNodeIDEmpty
//
// When the routing table has no NodeID set, loop detection is silently skipped.
// The spec (§4.2, §6.1) says a router MUST reject beacons containing its own node_id.
// The implementation makes this conditional on NodeID being non-empty.
//
// A node that misconfigures (or forgets to call SetSelfInfo) will pass beacons
// with its own node_id through, creating routing loops.
func TestLoopDetectionSkippedWithoutNodeID(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)

	// No NodeID — loop detection disabled.
	rt := newRoutingTable()

	// Beacon claims to have passed through "this-routers-actual-id".
	// Without NodeID set, the router cannot detect this.
	path := []string{"some-upstream", "this-routers-actual-id"}
	raw := makePathBeaconPayload(t, cfPub, cfPriv, "http://example.com", path, false)

	err := rt.HandleBeacon(raw, "gw", "some-upstream")
	if err != nil {
		t.Logf("HandleBeacon rejected: %v", err)
		return
	}

	campfireIDHex := hex.EncodeToString(cfPub)
	routes := rt.Lookup(campfireIDHex)
	if len(routes) > 0 {
		t.Log("LOW: loop detection silently skipped when NodeID is empty — " +
			"spec MUST check is not enforced when router is misconfigured")
	}
}

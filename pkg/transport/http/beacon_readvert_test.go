package http_test

// Integration tests for beacon re-advertisement and withdrawal propagation (o5e).
//
// Scenarios covered:
//   - TestBeaconReAdvertisement: beacon received by router A is re-advertised to router B
//     with A's node_id appended to the path.
//   - TestBeaconReAdvertisementPathGrows: a 2-hop re-advertisement correctly extends the path.
//   - TestWithdrawPropagation: routing:withdraw received by router A is propagated to router B.
//
// Port block: 500-519 (beacon_readvert_test.go)

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"crypto/ed25519"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// makeSignedBeaconMessage creates a routing:beacon message ready for delivery.
// The inner_signature is signed WITHOUT the path (advisory path / threshold>1 mode).
// This matches the approach in router_test.go: the path is in the wire payload but not
// in the signing input. VerifyDeclaration will try with-path (fail) then without-path (succeed).
// When re-advertised, routers append their node_id and keep the original inner_signature
// (advisory path behavior, since they don't hold the target campfire key).
func makeSignedBeaconMessage(
	t *testing.T,
	senderID *identity.Identity,
	targetCfPub ed25519.PublicKey,
	targetCfPriv ed25519.PrivateKey,
	endpoint string,
	path []string,
) *message.Message {
	t.Helper()

	targetCfIDHex := hex.EncodeToString(targetCfPub)
	ts := time.Now().Unix()

	// Sign WITHOUT path (advisory path / threshold>1 behavior).
	// The inner_signature covers campfire_id, endpoint, transport, etc. but NOT path.
	// This allows the beacon to be re-advertised with appended path without re-signing.
	declNoPath := beacon.BeaconDeclaration{
		CampfireID:        targetCfIDHex,
		Endpoint:          endpoint,
		Transport:         "p2p-http",
		Description:       "readvert test campfire",
		JoinProtocol:      "open",
		Timestamp:         ts,
		ConventionVersion: "0.5.0",
	}
	signBytes, err := beacon.MarshalInnerSignInput(declNoPath)
	if err != nil {
		t.Fatalf("MarshalInnerSignInput: %v", err)
	}
	sig := ed25519.Sign(targetCfPriv, signBytes)

	// Wire payload includes path but signature was computed without path.
	decl := beacon.BeaconDeclaration{
		CampfireID:        targetCfIDHex,
		Endpoint:          endpoint,
		Transport:         "p2p-http",
		Description:       "readvert test campfire",
		JoinProtocol:      "open",
		Timestamp:         ts,
		ConventionVersion: "0.5.0",
		InnerSignature:    hex.EncodeToString(sig),
		Path:              path,
	}

	payloadBytes, err := json.Marshal(decl)
	if err != nil {
		t.Fatalf("marshaling beacon decl: %v", err)
	}

	msg, err := message.NewMessage(senderID.PrivateKey, senderID.PublicKey, payloadBytes, []string{"routing:beacon"}, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return msg
}

// makeSignedWithdrawMessage creates a routing:withdraw message ready for delivery.
func makeSignedWithdrawMessage(
	t *testing.T,
	senderID *identity.Identity,
	targetCfPub ed25519.PublicKey,
	targetCfPriv ed25519.PrivateKey,
	reason string,
) *message.Message {
	t.Helper()

	targetCfIDHex := hex.EncodeToString(targetCfPub)

	type withdrawSignInput struct {
		CampfireID string `json:"campfire_id"`
		Reason     string `json:"reason"`
	}
	signInput := withdrawSignInput{CampfireID: targetCfIDHex, Reason: reason}
	signBytes, err := json.Marshal(signInput)
	if err != nil {
		t.Fatalf("marshal withdraw sign input: %v", err)
	}
	sig := ed25519.Sign(targetCfPriv, signBytes)

	payload := map[string]string{
		"campfire_id":     targetCfIDHex,
		"reason":          reason,
		"inner_signature": hex.EncodeToString(sig),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal withdraw payload: %v", err)
	}

	msg, err := message.NewMessage(senderID.PrivateKey, senderID.PublicKey, payloadBytes, []string{"routing:withdraw"}, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return msg
}

// waitForRoute polls the routing table until a route for campfireID appears or times out.
func waitForRoute(t *testing.T, rt *cfhttp.RoutingTable, campfireIDHex string, timeout time.Duration) []cfhttp.RouteEntry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		routes := rt.Lookup(campfireIDHex)
		if len(routes) > 0 {
			return routes
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// waitForRouteWithMinPath polls the routing table until a route for campfireID appears
// with a path of at least minPathLen, or times out.
func waitForRouteWithMinPath(t *testing.T, rt *cfhttp.RoutingTable, campfireIDHex string, minPathLen int, timeout time.Duration) []cfhttp.RouteEntry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		routes := rt.Lookup(campfireIDHex)
		if len(routes) > 0 {
			// Check if any route has at least minPathLen path length.
			for _, r := range routes {
				if len(r.Path) >= minPathLen {
					return routes
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// waitForNoRoute polls the routing table until all routes for campfireID are gone.
func waitForNoRoute(t *testing.T, rt *cfhttp.RoutingTable, campfireIDHex string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		routes := rt.Lookup(campfireIDHex)
		if len(routes) == 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestBeaconReAdvertisement verifies that when router A receives a routing:beacon from
// a peer (id1), it re-advertises the beacon to router B with A's node_id appended to the path.
//
// Topology: id1 → tr1 (A) → tr2 (B)
// Expected: tr2 receives the beacon and its routing table has a route with A's node_id in path.
func TestBeaconReAdvertisement(t *testing.T) {
	// The campfire being advertised via beacon.
	targetCfPub, targetCfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	targetCfIDHex := hex.EncodeToString(targetCfPub)

	// The gateway campfire (what the beacon is delivered into).
	gatewayCfPub, gatewayCfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	gatewayCfIDHex := hex.EncodeToString(gatewayCfPub)

	id1 := tempIdentity(t) // the original beacon sender
	id2 := tempIdentity(t) // identity for tr2

	s1 := tempStore(t)
	s2 := tempStore(t)

	addMembershipWithRole(t, s1, gatewayCfIDHex, "member")
	addMembershipWithRole(t, s2, gatewayCfIDHex, "member")

	// id1 is a peer on s1 (to pass auth on tr1).
	addPeerEndpoint(t, s1, gatewayCfIDHex, id1.PublicKeyHex())
	// campfire key is a peer on s2 (tr1 forwards signed as campfire).
	addPeerEndpoint(t, s2, gatewayCfIDHex, hex.EncodeToString(gatewayCfPub))
	addPeerEndpoint(t, s2, gatewayCfIDHex, id2.PublicKeyHex())

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+500)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+501)
	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)

	tr1 := cfhttp.New(addr1, s1)
	tr1.SetSelfInfo(id1.PublicKeyHex(), ep1)
	tr1.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == gatewayCfIDHex {
			return gatewayCfPriv, gatewayCfPub, nil
		}
		return nil, nil, fmt.Errorf("no key for %s", id)
	})
	if err := tr1.Start(); err != nil {
		t.Fatalf("tr1.Start: %v", err)
	}
	t.Cleanup(func() { tr1.Stop() }) //nolint:errcheck

	tr2 := cfhttp.New(addr2, s2)
	tr2.SetSelfInfo(id2.PublicKeyHex(), ep2)
	if err := tr2.Start(); err != nil {
		t.Fatalf("tr2.Start: %v", err)
	}
	t.Cleanup(func() { tr2.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Register tr2 as a peer of tr1 for the gateway campfire.
	tr1.AddPeer(gatewayCfIDHex, id2.PublicKeyHex(), ep2)

	// Deliver a routing:beacon for targetCampfire to tr1. The beacon originates from
	// id1 (path = [id1.PublicKeyHex()]).
	originPath := []string{id1.PublicKeyHex()}
	beaconMsg := makeSignedBeaconMessage(t, id1, targetCfPub, targetCfPriv, "http://origin.example.com:9090", originPath)

	if err := cfhttp.Deliver(ep1, gatewayCfIDHex, beaconMsg, id1); err != nil {
		t.Fatalf("deliver beacon to tr1: %v", err)
	}

	// tr1 should install the route in its own routing table.
	routes1 := waitForRoute(t, tr1.RoutingTable(), targetCfIDHex, 2*time.Second)
	if len(routes1) == 0 {
		t.Fatal("tr1 routing table should have route for target campfire after beacon")
	}

	// tr2 should receive the re-advertised beacon and install a route with extended path.
	// We wait for a route with path length >= 2 (original path [id1] + tr1's node_id appended).
	// forwardMessage also delivers the original message (path=[id1]) to tr2, but the
	// re-advertised beacon (path=[id1, tr1.nodeID]) should arrive and update the route.
	minPath := len(originPath) + 1
	routes2 := waitForRouteWithMinPath(t, tr2.RoutingTable(), targetCfIDHex, minPath, 3*time.Second)
	if len(routes2) == 0 {
		t.Fatalf("tr2 should have a route with path length >= %d (re-advertisement should append tr1's node_id)", minPath)
	}
	foundExtended := false
	for _, r := range routes2 {
		if len(r.Path) >= minPath {
			foundExtended = true
			break
		}
	}
	if !foundExtended {
		t.Errorf("tr2 routes have max path len %d, want >= %d (tr1 should append its node_id)", func() int {
			max := 0
			for _, r := range routes2 {
				if len(r.Path) > max {
					max = len(r.Path)
				}
			}
			return max
		}(), minPath)
	}
}

// TestBeaconReAdvertisementPathGrows verifies the path-vector extension across two hops:
// origin → tr1 → tr2. The path should grow at each hop.
//
// Topology: tr1 originates beacon (path=[tr1.nodeID]), delivers to tr2.
//           tr2 re-advertises to tr3 with path=[tr1.nodeID, tr2.nodeID].
func TestBeaconReAdvertisementPathGrows(t *testing.T) {
	targetCfPub, targetCfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	targetCfIDHex := hex.EncodeToString(targetCfPub)

	gatewayCfPub, gatewayCfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	gatewayCfIDHex := hex.EncodeToString(gatewayCfPub)

	id1 := tempIdentity(t)
	id2 := tempIdentity(t)
	id3 := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)
	s3 := tempStore(t)

	addMembershipWithRole(t, s1, gatewayCfIDHex, "member")
	addMembershipWithRole(t, s2, gatewayCfIDHex, "member")
	addMembershipWithRole(t, s3, gatewayCfIDHex, "member")

	cfPubHex := hex.EncodeToString(gatewayCfPub)

	// Auth setup: each store needs the campfire key and neighbor IDs as peers.
	addPeerEndpoint(t, s1, gatewayCfIDHex, id1.PublicKeyHex())
	addPeerEndpoint(t, s1, gatewayCfIDHex, cfPubHex)
	addPeerEndpoint(t, s2, gatewayCfIDHex, id2.PublicKeyHex())
	addPeerEndpoint(t, s2, gatewayCfIDHex, cfPubHex)
	addPeerEndpoint(t, s3, gatewayCfIDHex, id3.PublicKeyHex())
	addPeerEndpoint(t, s3, gatewayCfIDHex, cfPubHex)

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+502)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+503)
	addr3 := fmt.Sprintf("127.0.0.1:%d", base+504)
	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)
	ep3 := fmt.Sprintf("http://%s", addr3)

	makeKeyProvider := func(priv ed25519.PrivateKey, pub ed25519.PublicKey) func(string) ([]byte, []byte, error) {
		return func(id string) ([]byte, []byte, error) {
			if id == gatewayCfIDHex {
				return priv, pub, nil
			}
			return nil, nil, fmt.Errorf("no key for %s", id)
		}
	}

	tr1 := cfhttp.New(addr1, s1)
	tr1.SetSelfInfo(id1.PublicKeyHex(), ep1)
	tr1.SetKeyProvider(makeKeyProvider(gatewayCfPriv, gatewayCfPub))
	if err := tr1.Start(); err != nil {
		t.Fatalf("tr1.Start: %v", err)
	}
	t.Cleanup(func() { tr1.Stop() }) //nolint:errcheck

	tr2 := cfhttp.New(addr2, s2)
	tr2.SetSelfInfo(id2.PublicKeyHex(), ep2)
	tr2.SetKeyProvider(makeKeyProvider(gatewayCfPriv, gatewayCfPub))
	if err := tr2.Start(); err != nil {
		t.Fatalf("tr2.Start: %v", err)
	}
	t.Cleanup(func() { tr2.Stop() }) //nolint:errcheck

	tr3 := cfhttp.New(addr3, s3)
	tr3.SetSelfInfo(id3.PublicKeyHex(), ep3)
	tr3.SetKeyProvider(makeKeyProvider(gatewayCfPriv, gatewayCfPub))
	if err := tr3.Start(); err != nil {
		t.Fatalf("tr3.Start: %v", err)
	}
	t.Cleanup(func() { tr3.Stop() }) //nolint:errcheck

	time.Sleep(20 * time.Millisecond)

	// Chain: tr1 → tr2 → tr3.
	tr1.AddPeer(gatewayCfIDHex, id2.PublicKeyHex(), ep2)
	tr2.AddPeer(gatewayCfIDHex, id3.PublicKeyHex(), ep3)
	// tr3 has no downstream peers.

	// tr1 delivers a beacon with path=[id1]. tr1 should re-advertise to tr2 with path=[id1, id1]
	// (tr1's self node_id appended). Then tr2 re-advertises to tr3 with path=[id1, id1, id2].
	originPath := []string{id1.PublicKeyHex()}
	beaconMsg := makeSignedBeaconMessage(t, id1, targetCfPub, targetCfPriv, "http://origin.example.com:7070", originPath)

	if err := cfhttp.Deliver(ep1, gatewayCfIDHex, beaconMsg, id1); err != nil {
		t.Fatalf("deliver beacon to tr1: %v", err)
	}

	// tr3 is 2 hops away (tr1→tr2→tr3). Wait longer for propagation.
	// We expect at least 2 path extensions (tr1 appends, tr2 appends).
	minPathAt3 := len(originPath) + 2
	routes3 := waitForRouteWithMinPath(t, tr3.RoutingTable(), targetCfIDHex, minPathAt3, 5*time.Second)
	if len(routes3) == 0 {
		t.Fatal("tr3 routing table should have route after 2-hop re-advertisement")
	}
	foundExtended3 := false
	for _, r := range routes3 {
		if len(r.Path) >= minPathAt3 {
			foundExtended3 = true
			break
		}
	}
	if !foundExtended3 {
		maxLen := 0
		for _, r := range routes3 {
			if len(r.Path) > maxLen {
				maxLen = len(r.Path)
			}
		}
		t.Errorf("path at tr3 max len = %d, want >= %d (should grow at each hop)", maxLen, minPathAt3)
	}
}

// TestWithdrawPropagation verifies that when tr1 receives a routing:withdraw, it
// propagates the withdrawal to tr2, which removes the route from its routing table.
//
// Topology: id1 → tr1 (installs route) → tr2 (gets re-advertised route).
//           id1 → tr1 (withdraw) → tr2 (propagated withdraw, route removed).
func TestWithdrawPropagation(t *testing.T) {
	targetCfPub, targetCfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	targetCfIDHex := hex.EncodeToString(targetCfPub)

	gatewayCfPub, gatewayCfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	gatewayCfIDHex := hex.EncodeToString(gatewayCfPub)

	id1 := tempIdentity(t)
	id2 := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)

	addMembershipWithRole(t, s1, gatewayCfIDHex, "member")
	addMembershipWithRole(t, s2, gatewayCfIDHex, "member")

	cfPubHex := hex.EncodeToString(gatewayCfPub)
	addPeerEndpoint(t, s1, gatewayCfIDHex, id1.PublicKeyHex())
	addPeerEndpoint(t, s1, gatewayCfIDHex, cfPubHex)
	addPeerEndpoint(t, s2, gatewayCfIDHex, cfPubHex)
	addPeerEndpoint(t, s2, gatewayCfIDHex, id2.PublicKeyHex())

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+505)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+506)
	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)

	makeKeyProvider := func(priv ed25519.PrivateKey, pub ed25519.PublicKey) func(string) ([]byte, []byte, error) {
		return func(id string) ([]byte, []byte, error) {
			if id == gatewayCfIDHex {
				return priv, pub, nil
			}
			return nil, nil, fmt.Errorf("no key for %s", id)
		}
	}

	tr1 := cfhttp.New(addr1, s1)
	tr1.SetSelfInfo(id1.PublicKeyHex(), ep1)
	tr1.SetKeyProvider(makeKeyProvider(gatewayCfPriv, gatewayCfPub))
	if err := tr1.Start(); err != nil {
		t.Fatalf("tr1.Start: %v", err)
	}
	t.Cleanup(func() { tr1.Stop() }) //nolint:errcheck

	tr2 := cfhttp.New(addr2, s2)
	tr2.SetSelfInfo(id2.PublicKeyHex(), ep2)
	tr2.SetKeyProvider(makeKeyProvider(gatewayCfPriv, gatewayCfPub))
	if err := tr2.Start(); err != nil {
		t.Fatalf("tr2.Start: %v", err)
	}
	t.Cleanup(func() { tr2.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	tr1.AddPeer(gatewayCfIDHex, id2.PublicKeyHex(), ep2)

	// Step 1: Deliver a beacon so both tr1 and tr2 have routes.
	originPath := []string{id1.PublicKeyHex()}
	beaconMsg := makeSignedBeaconMessage(t, id1, targetCfPub, targetCfPriv, "http://origin.example.com:8080", originPath)
	if err := cfhttp.Deliver(ep1, gatewayCfIDHex, beaconMsg, id1); err != nil {
		t.Fatalf("deliver beacon: %v", err)
	}

	// Wait for tr2 to have the route (via re-advertisement from tr1).
	routes2 := waitForRoute(t, tr2.RoutingTable(), targetCfIDHex, 3*time.Second)
	if len(routes2) == 0 {
		t.Fatal("tr2 should have route after re-advertisement (prerequisite for withdraw propagation test)")
	}

	// Step 2: Deliver a withdraw to tr1.
	withdrawMsg := makeSignedWithdrawMessage(t, id1, targetCfPub, targetCfPriv, "going offline")
	if err := cfhttp.Deliver(ep1, gatewayCfIDHex, withdrawMsg, id1); err != nil {
		t.Fatalf("deliver withdraw: %v", err)
	}

	// tr1 should remove the route immediately.
	if routes := tr1.RoutingTable().Lookup(targetCfIDHex); len(routes) != 0 {
		t.Errorf("tr1 should have removed route after withdraw, got %d routes", len(routes))
	}

	// tr2 should also remove the route after receiving the propagated withdrawal.
	if !waitForNoRoute(t, tr2.RoutingTable(), targetCfIDHex, 3*time.Second) {
		routes := tr2.RoutingTable().Lookup(targetCfIDHex)
		t.Errorf("tr2 should have removed route after propagated withdraw, still has %d routes", len(routes))
	}
}

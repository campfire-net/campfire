package http_test

// Integration tests for router forwarding (campfire-agent-y0c).
//
// Scenarios covered:
//   - Message posted to instance 1 is forwarded to instance 2 (unidirectional).
//   - Message posted to instance 2 is forwarded to instance 1 (bidirectional).
//   - Mutual membership (A member of B, B member of A) — no infinite loops.
//   - Dedup: duplicate delivery is silently dropped, no double-forwarding.
//   - Max hops: message with provenance chain >= maxHops is dropped.
//   - Provenance hop is added on forward (signed by campfire key).
//   - Forwarding skipped when no key provider (default transport).
//   - Path-vector forwarding: next_hop only, not all peers (wew).
//   - Path-vector fallback: legacy beacons → flood all peers (wew).
//   - Sender excluded from forwarding set (wew).
//   - PeerNeedsSet ∪ NextHops combined in forwarding set (wew).
//
// Port block: 460-499 (forwarding_test.go)

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// startTransportWithKey starts a transport with a key provider that returns the
// given campfire keypair for the given campfire ID.
func startTransportWithKey(
	t *testing.T,
	addr string,
	s store.Store,
	selfID *identity.Identity,
	campfireID string,
	cfPriv ed25519.PrivateKey,
	cfPub ed25519.PublicKey,
) *cfhttp.Transport {
	t.Helper()
	tr := cfhttp.New(addr, s)
	ep := fmt.Sprintf("http://%s", addr)
	tr.SetSelfInfo(selfID.PublicKeyHex(), ep)
	tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("campfire not found: %s", id)
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport on %s: %v", addr, err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	return tr
}

// addMembershipWithRole inserts a campfire membership with the given role.
func addMembershipWithRole(t *testing.T, s store.Store, campfireID, role string) {
	t.Helper()
	err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "open",
		Role:         role,
		JoinedAt:     time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
}

// waitForMessage polls the store until a message with the given ID appears or timeout.
func waitForMessage(t *testing.T, s store.Store, campfireID, msgID string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msgs, err := s.ListMessages(campfireID, 0)
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		for _, m := range msgs {
			if m.ID == msgID {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// doSignedPost builds and sends an authenticated POST request. Used for low-level
// tests that need direct control over message content (e.g. max_hops test).
func doSignedPost(t *testing.T, url string, body []byte, id *identity.Identity) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	signTestRequest(req, id, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http request to %s: %v", url, err)
	}
	return resp
}

// TestForwardOnReceiveUnidirectional verifies that when instance 1 receives a message,
// it forwards it to instance 2 (which is a known peer for the campfire).
// Done condition: message appears on instance 2's store after being posted to instance 1.
func TestForwardOnReceiveUnidirectional(t *testing.T) {
	campfireID := "forward-unidirectional"
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	id1 := tempIdentity(t)
	id2 := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)

	// Both instances know about the campfire.
	addMembershipWithRole(t, s1, campfireID, "creator")
	addMembershipWithRole(t, s2, campfireID, "member")

	// id1 is a member on both stores (to pass auth on both sides).
	addPeerEndpoint(t, s1, campfireID, id1.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, id1.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, id2.PublicKeyHex())

	// Also allow the campfire key (which tr1 signs forwarded messages as) on s2.
	addPeerEndpoint(t, s2, campfireID, hex.EncodeToString(cfPub))

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+460)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+461)
	ep2 := fmt.Sprintf("http://%s", addr2)

	tr1 := startTransportWithKey(t, addr1, s1, id1, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addr2, s2, id2, campfireID, cfPriv, cfPub)

	// Register instance 2 as a peer of instance 1 for this campfire.
	tr1.AddPeer(campfireID, id2.PublicKeyHex(), ep2)

	// id1 delivers a message to instance 1.
	msg := newTestMessage(t, id1)
	ep1 := fmt.Sprintf("http://%s", addr1)
	if err := cfhttp.Deliver(ep1, campfireID, msg, id1); err != nil {
		t.Fatalf("deliver to instance 1 failed: %v", err)
	}

	// Message should appear on instance 2 (forwarded by router).
	if !waitForMessage(t, s2, campfireID, msg.ID, 2*time.Second) {
		t.Errorf("message %s not forwarded to instance 2 within 2s", msg.ID)
	}
}

// TestForwardOnReceiveBidirectional verifies that forwarding works in both directions:
// - Message posted to instance 1 arrives at instance 2.
// - Message posted to instance 2 arrives at instance 1.
func TestForwardOnReceiveBidirectional(t *testing.T) {
	campfireID := "forward-bidirectional"
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	id1 := tempIdentity(t)
	id2 := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)

	addMembershipWithRole(t, s1, campfireID, "creator")
	addMembershipWithRole(t, s2, campfireID, "member")

	cfPubHex := hex.EncodeToString(cfPub)

	// Both agents + campfire key are members on both stores.
	addPeerEndpoint(t, s1, campfireID, id1.PublicKeyHex())
	addPeerEndpoint(t, s1, campfireID, id2.PublicKeyHex())
	addPeerEndpoint(t, s1, campfireID, cfPubHex)
	addPeerEndpoint(t, s2, campfireID, id1.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, id2.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, cfPubHex)

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+462)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+463)
	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)

	tr1 := startTransportWithKey(t, addr1, s1, id1, campfireID, cfPriv, cfPub)
	tr2 := startTransportWithKey(t, addr2, s2, id2, campfireID, cfPriv, cfPub)

	// Mutual peer registration.
	tr1.AddPeer(campfireID, id2.PublicKeyHex(), ep2)
	tr2.AddPeer(campfireID, id1.PublicKeyHex(), ep1)

	// --- Direction 1: id1 → instance 1 → instance 2 ---
	msg1 := newTestMessage(t, id1)
	if err := cfhttp.Deliver(ep1, campfireID, msg1, id1); err != nil {
		t.Fatalf("deliver msg1 to instance 1: %v", err)
	}
	if !waitForMessage(t, s2, campfireID, msg1.ID, 2*time.Second) {
		t.Errorf("msg1 not forwarded to instance 2 within 2s")
	}

	// --- Direction 2: id2 → instance 2 → instance 1 ---
	msg2 := newTestMessage(t, id2)
	if err := cfhttp.Deliver(ep2, campfireID, msg2, id2); err != nil {
		t.Fatalf("deliver msg2 to instance 2: %v", err)
	}
	if !waitForMessage(t, s1, campfireID, msg2.ID, 2*time.Second) {
		t.Errorf("msg2 not forwarded to instance 1 within 2s")
	}
}

// TestForwardNoLoopMutualMembership verifies that mutual membership (A peered with B,
// B peered with A) does not cause infinite loops. Each message should appear exactly
// once at each instance.
func TestForwardNoLoopMutualMembership(t *testing.T) {
	campfireID := "forward-no-loop"
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	id1 := tempIdentity(t)
	id2 := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)

	addMembershipWithRole(t, s1, campfireID, "creator")
	addMembershipWithRole(t, s2, campfireID, "member")

	cfPubHex := hex.EncodeToString(cfPub)
	addPeerEndpoint(t, s1, campfireID, id1.PublicKeyHex())
	addPeerEndpoint(t, s1, campfireID, id2.PublicKeyHex())
	addPeerEndpoint(t, s1, campfireID, cfPubHex)
	addPeerEndpoint(t, s2, campfireID, id1.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, id2.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, cfPubHex)

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+464)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+465)
	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)

	tr1 := startTransportWithKey(t, addr1, s1, id1, campfireID, cfPriv, cfPub)
	tr2 := startTransportWithKey(t, addr2, s2, id2, campfireID, cfPriv, cfPub)

	// Mutual peering (A→B and B→A).
	tr1.AddPeer(campfireID, id2.PublicKeyHex(), ep2)
	tr2.AddPeer(campfireID, id1.PublicKeyHex(), ep1)

	// Post a message to instance 1.
	msg := newTestMessage(t, id1)
	if err := cfhttp.Deliver(ep1, campfireID, msg, id1); err != nil {
		t.Fatalf("deliver to instance 1: %v", err)
	}

	// Wait for message to arrive at instance 2.
	if !waitForMessage(t, s2, campfireID, msg.ID, 2*time.Second) {
		t.Errorf("message not forwarded to instance 2")
	}

	// Wait a bit longer to ensure no bouncing.
	time.Sleep(300 * time.Millisecond)

	// Count occurrences on each instance — should be exactly 1.
	s1Msgs, _ := s1.ListMessages(campfireID, 0)
	s2Msgs, _ := s2.ListMessages(campfireID, 0)

	s1Count := 0
	s2Count := 0
	for _, m := range s1Msgs {
		if m.ID == msg.ID {
			s1Count++
		}
	}
	for _, m := range s2Msgs {
		if m.ID == msg.ID {
			s2Count++
		}
	}

	if s1Count != 1 {
		t.Errorf("instance 1 has %d copies of message, want 1 (dedup should prevent loops)", s1Count)
	}
	if s2Count != 1 {
		t.Errorf("instance 2 has %d copies of message, want 1 (dedup should prevent loops)", s2Count)
	}
}

// TestDedupDropsDuplicate verifies that delivering the same message twice results
// in it being stored only once (dedup table prevents double-store and double-forward).
func TestDedupDropsDuplicate(t *testing.T) {
	campfireID := "forward-dedup"
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	id1 := tempIdentity(t)

	s1 := tempStore(t)
	addMembershipWithRole(t, s1, campfireID, "creator")
	addPeerEndpoint(t, s1, campfireID, id1.PublicKeyHex())

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+466)
	ep1 := fmt.Sprintf("http://%s", addr1)

	_ = startTransportWithKey(t, addr1, s1, id1, campfireID, cfPriv, cfPub)

	// Deliver the same message twice.
	msg := newTestMessage(t, id1)
	if err := cfhttp.Deliver(ep1, campfireID, msg, id1); err != nil {
		t.Fatalf("first deliver: %v", err)
	}
	// Second deliver of the same message — should be accepted (200) but deduplicated.
	if err := cfhttp.Deliver(ep1, campfireID, msg, id1); err != nil {
		t.Fatalf("second deliver (dedup): %v", err)
	}

	// Give any async goroutines time to complete.
	time.Sleep(50 * time.Millisecond)

	// Store should contain exactly 1 copy.
	msgs, err := s1.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	count := 0
	for _, m := range msgs {
		if m.ID == msg.ID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 copy of message in store, got %d (dedup failed)", count)
	}
}

// TestMaxHopsEnforced verifies that a message with provenance chain length >= maxHops
// is dropped and not stored.
func TestMaxHopsEnforced(t *testing.T) {
	campfireID := "forward-maxhops"
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	id1 := tempIdentity(t)
	s1 := tempStore(t)
	addMembershipWithRole(t, s1, campfireID, "creator")
	addPeerEndpoint(t, s1, campfireID, id1.PublicKeyHex())

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+467)
	ep1 := fmt.Sprintf("http://%s", addr1)

	_ = startTransportWithKey(t, addr1, s1, id1, campfireID, cfPriv, cfPub)

	// Build a message and add maxHops provenance hops.
	msg, err := message.NewMessage(id1.PrivateKey, id1.PublicKey, []byte("at hop limit"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}

	// Add exactly maxHops hops (should be dropped: len >= maxHops).
	for i := 0; i < cfhttp.MaxHops; i++ {
		hopPub, hopPriv, _ := ed25519.GenerateKey(nil)
		if err := msg.AddHop(hopPriv, hopPub, nil, 1, "open", nil, ""); err != nil {
			t.Fatalf("AddHop[%d]: %v", i, err)
		}
	}

	if len(msg.Provenance) != cfhttp.MaxHops {
		t.Fatalf("precondition: expected %d hops, got %d", cfhttp.MaxHops, len(msg.Provenance))
	}

	// Encode and deliver.
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("encoding message: %v", err)
	}
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep1, campfireID)
	resp := doSignedPost(t, url, body, id1)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200 for max-hops drop, got %d", resp.StatusCode)
	}

	// Give time for any async processing.
	time.Sleep(30 * time.Millisecond)

	msgs, err := s1.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range msgs {
		if m.ID == msg.ID {
			t.Error("message with maxHops provenance should have been dropped, but was stored")
		}
	}
}

// TestProvenanceHopAddedOnForward verifies that when a message is forwarded,
// a provenance hop signed by the campfire key is added to the forwarded copy.
// After instance 1 forwards to instance 2, the message on instance 2 should have
// one more provenance hop than the original.
func TestProvenanceHopAddedOnForward(t *testing.T) {
	campfireID := "forward-provenance"
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	id1 := tempIdentity(t)
	id2 := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)

	addMembershipWithRole(t, s1, campfireID, "creator")
	addMembershipWithRole(t, s2, campfireID, "member")

	cfPubHex := hex.EncodeToString(cfPub)
	addPeerEndpoint(t, s1, campfireID, id1.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, id1.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, id2.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, cfPubHex)

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+468)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+469)
	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)

	tr1 := startTransportWithKey(t, addr1, s1, id1, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addr2, s2, id2, campfireID, cfPriv, cfPub)
	tr1.AddPeer(campfireID, id2.PublicKeyHex(), ep2)

	// Deliver a fresh message (0 provenance hops) to instance 1.
	msg := newTestMessage(t, id1)
	if len(msg.Provenance) != 0 {
		t.Fatalf("new message should have 0 provenance hops, got %d", len(msg.Provenance))
	}

	if err := cfhttp.Deliver(ep1, campfireID, msg, id1); err != nil {
		t.Fatalf("deliver to instance 1: %v", err)
	}

	// Wait for forwarded message on instance 2.
	if !waitForMessage(t, s2, campfireID, msg.ID, 2*time.Second) {
		t.Fatalf("message not forwarded to instance 2 within 2s")
	}

	// Fetch the message from instance 2's store and check provenance.
	msgs2, err := s2.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages on s2: %v", err)
	}
	var fwdRec *store.MessageRecord
	for i := range msgs2 {
		if msgs2[i].ID == msg.ID {
			fwdRec = &msgs2[i]
			break
		}
	}
	if fwdRec == nil {
		t.Fatalf("forwarded message not found in s2")
	}

	// The forwarded message should have exactly 1 provenance hop (added by tr1's router).
	if len(fwdRec.Provenance) != 1 {
		t.Errorf("forwarded message should have 1 provenance hop, got %d", len(fwdRec.Provenance))
	}

	if len(fwdRec.Provenance) >= 1 {
		hopCampfireIDHex := hex.EncodeToString(fwdRec.Provenance[0].CampfireID)
		expectedHex := hex.EncodeToString(cfPub)
		if hopCampfireIDHex != expectedHex {
			t.Errorf("provenance hop CampfireID = %s, want %s", hopCampfireIDHex, expectedHex)
		}

		// Verify the hop signature.
		if !message.VerifyHop(msg.ID, fwdRec.Provenance[0]) {
			t.Error("provenance hop signature verification failed")
		}
	}
}

// TestRoutingBeaconUpdatesRoutingTable verifies that a routing:beacon message
// received by handleDeliver updates the routing table.
func TestRoutingBeaconUpdatesRoutingTable(t *testing.T) {
	// The campfire being advertised.
	targetCfPub, targetCfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	targetCampfireIDHex := hex.EncodeToString(targetCfPub)

	// The gateway campfire (what we deliver the beacon into).
	gatewayCfPub, gatewayCfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	gatewayCampfireID := hex.EncodeToString(gatewayCfPub)

	id1 := tempIdentity(t)
	s1 := tempStore(t)
	addMembershipWithRole(t, s1, gatewayCampfireID, "member")
	addPeerEndpoint(t, s1, gatewayCampfireID, id1.PublicKeyHex())

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+470)
	ep1 := fmt.Sprintf("http://%s", addr1)

	tr1 := startTransportWithKey(t, addr1, s1, id1, gatewayCampfireID, gatewayCfPriv, gatewayCfPub)

	// Build and sign a routing:beacon payload for targetCampfire.
	ts := time.Now().Unix()
	beaconDecl := beacon.BeaconDeclaration{
		CampfireID:        targetCampfireIDHex,
		ConventionVersion: "0.4.2",
		Description:       "test campfire",
		Endpoint:          "http://remote.example.com:9090",
		JoinProtocol:      "open",
		Timestamp:         ts,
		Transport:         "p2p-http",
	}
	signBytes, _ := beacon.MarshalInnerSignInput(beaconDecl)
	innerSig := ed25519.Sign(targetCfPriv, signBytes)

	beaconPayload := map[string]interface{}{
		"campfire_id":        targetCampfireIDHex,
		"endpoint":           "http://remote.example.com:9090",
		"transport":          "p2p-http",
		"description":        "test campfire",
		"join_protocol":      "open",
		"timestamp":          ts,
		"convention_version": "0.4.2",
		"inner_signature":    hex.EncodeToString(innerSig),
	}
	payloadBytes, _ := json.Marshal(beaconPayload)

	// Deliver the routing:beacon message to the gateway campfire.
	beaconMsg, err := message.NewMessage(id1.PrivateKey, id1.PublicKey, payloadBytes, []string{"routing:beacon"}, nil)
	if err != nil {
		t.Fatalf("creating beacon message: %v", err)
	}

	if err := cfhttp.Deliver(ep1, gatewayCampfireID, beaconMsg, id1); err != nil {
		t.Fatalf("deliver beacon: %v", err)
	}

	// Give the handler time to process the beacon.
	time.Sleep(50 * time.Millisecond)

	// The routing table should now contain an entry for targetCampfire.
	routes := tr1.RoutingTable().Lookup(targetCampfireIDHex)
	if len(routes) == 0 {
		t.Error("routing table should contain entry for target campfire after beacon")
	}
	if len(routes) > 0 && routes[0].Endpoint != "http://remote.example.com:9090" {
		t.Errorf("route endpoint = %q, want %q", routes[0].Endpoint, "http://remote.example.com:9090")
	}
}

// TestForwardSkippedWithoutKeyProvider verifies that a transport without a key provider
// does not attempt forwarding (no errors, message stored locally only).
func TestForwardSkippedWithoutKeyProvider(t *testing.T) {
	campfireID := "forward-no-keyprovider"
	id1 := tempIdentity(t)
	id2 := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)

	addMembership(t, s1, campfireID)
	addPeerEndpoint(t, s1, campfireID, id1.PublicKeyHex())
	addMembership(t, s2, campfireID)
	addPeerEndpoint(t, s2, campfireID, id1.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, id2.PublicKeyHex())

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+471)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+472)
	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)

	// No key provider set on tr1.
	tr1 := startTransport(t, addr1, s1)
	tr1.AddPeer(campfireID, id2.PublicKeyHex(), ep2)
	_ = startTransport(t, addr2, s2)

	// Deliver a message to tr1.
	msg := newTestMessage(t, id1)
	if err := cfhttp.Deliver(ep1, campfireID, msg, id1); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	// Wait — message should NOT be forwarded (no key provider).
	time.Sleep(100 * time.Millisecond)

	// Message should be stored on instance 1.
	if !waitForMessage(t, s1, campfireID, msg.ID, time.Second) {
		t.Error("message should be stored on instance 1")
	}

	// Message should NOT appear on instance 2 (no forwarding without key provider).
	msgs2, _ := s2.ListMessages(campfireID, 0)
	for _, m := range msgs2 {
		if m.ID == msg.ID {
			t.Error("message should NOT have been forwarded without key provider")
		}
	}
}

// TestForwardingUsesNextHop verifies that when a path-vector route exists for a campfire,
// messages are forwarded only to the next_hop peer and not to all known peers.
//
// Setup: 3 instances — router (tr1), next_hop peer (tr2), bystander (tr3).
// Router has a path-vector route for campfireID with NextHop=id2 (tr2's node_id).
// tr3 is a locally known peer but NOT in the forwarding set.
//
// The beacon is delivered into a separate gateway campfire (gwCampfireID), which is
// the campfire that messages are forwarded through. The beacon advertises campfireID
// (same as gwCampfireID in this test) so that the route appears in tr1's routing table
// for the same campfire_id that messages are delivered to.
//
// Done condition: message appears on tr2 but NOT on tr3 after being posted to tr1.
func TestForwardingUsesNextHop(t *testing.T) {
	// cfPub is the campfire key. campfireID = hex(cfPub).
	// The beacon advertises campfireID itself (self-advertisement): this installs
	// a path-vector route for campfireID in tr1's routing table.
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireID := hex.EncodeToString(cfPub)

	id1 := tempIdentity(t) // router (tr1)
	id2 := tempIdentity(t) // next_hop peer (tr2)
	id3 := tempIdentity(t) // bystander (NOT in forwarding set)

	s1 := tempStore(t)
	s2 := tempStore(t)
	s3 := tempStore(t)

	addMembershipWithRole(t, s1, campfireID, "creator")
	addMembershipWithRole(t, s2, campfireID, "member")
	addMembershipWithRole(t, s3, campfireID, "member")

	cfPubHex := hex.EncodeToString(cfPub)

	// Auth: all participants are known to all stores.
	for _, s := range []store.Store{s1, s2, s3} {
		addPeerEndpoint(t, s, campfireID, id1.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, id2.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, id3.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, cfPubHex)
	}

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+473)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+474)
	addr3 := fmt.Sprintf("127.0.0.1:%d", base+475)
	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)
	ep3 := fmt.Sprintf("http://%s", addr3)

	tr1 := startTransportWithKey(t, addr1, s1, id1, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addr2, s2, id2, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addr3, s3, id3, campfireID, cfPriv, cfPub)

	// Register both id2 and id3 as peers of tr1. Both are locally known.
	tr1.AddPeer(campfireID, id2.PublicKeyHex(), ep2)
	tr1.AddPeer(campfireID, id3.PublicKeyHex(), ep3)

	// Deliver a routing:beacon from id2 to tr1. The beacon advertises campfireID itself
	// at ep2 (self-advertisement: tr2 says "I host campfireID at ep2").
	// The beacon has a non-empty path [id2] → path-vector route with NextHop=id2.
	beaconMsg := makeSignedBeaconMessage(t, id2, cfPub, cfPriv, ep2, []string{id2.PublicKeyHex()})
	if err := cfhttp.Deliver(ep1, campfireID, beaconMsg, id2); err != nil {
		t.Fatalf("deliver beacon: %v", err)
	}
	time.Sleep(80 * time.Millisecond) // let routing table update

	// Verify routing table has a path-vector route for campfireID with NextHop=id2.
	routes := tr1.RoutingTable().Lookup(campfireID)
	if len(routes) == 0 {
		t.Fatal("routing table should contain route for campfireID after beacon")
	}
	hasPathVectorWithID2 := false
	for _, r := range routes {
		if len(r.Path) > 0 && r.NextHop == id2.PublicKeyHex() {
			hasPathVectorWithID2 = true
		}
	}
	if !hasPathVectorWithID2 {
		t.Fatalf("expected path-vector route with NextHop=id2, got: %+v", routes)
	}

	// Now deliver a regular message for campfireID to tr1 from a fresh sender.
	// tr1 should forward using path-vector: only to id2 (next_hop), not to id3 (bystander).
	idSender := tempIdentity(t)
	addPeerEndpoint(t, s1, campfireID, idSender.PublicKeyHex())
	msg := newTestMessage(t, idSender)

	if err := cfhttp.Deliver(ep1, campfireID, msg, idSender); err != nil {
		t.Fatalf("deliver message: %v", err)
	}

	// id2's store (next_hop) should receive the message.
	if !waitForMessage(t, s2, campfireID, msg.ID, 2*time.Second) {
		t.Errorf("message should be forwarded to next_hop peer (id2) via path-vector routing")
	}

	// id3's store (bystander) should NOT receive the message.
	time.Sleep(150 * time.Millisecond)
	s3Msgs, _ := s3.ListMessages(campfireID, 0)
	for _, m := range s3Msgs {
		if m.ID == msg.ID {
			t.Error("message should NOT be forwarded to bystander peer (id3) — path-vector restricts forwarding to next_hops + peer_needs_set only")
		}
	}
}

// TestForwardingFallsBackToFlood verifies that when no path-vector routes exist
// (all beacons have empty Path fields — legacy behavior), messages are forwarded
// to all peers except the sender (v0.4.2 flood behavior).
//
// Done condition: both peers receive the message when no path-vector routes exist.
func TestForwardingFallsBackToFlood(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireID := hex.EncodeToString(cfPub)

	id1 := tempIdentity(t) // router
	id2 := tempIdentity(t) // peer A
	id3 := tempIdentity(t) // peer B

	s1 := tempStore(t)
	s2 := tempStore(t)
	s3 := tempStore(t)

	addMembershipWithRole(t, s1, campfireID, "creator")
	addMembershipWithRole(t, s2, campfireID, "member")
	addMembershipWithRole(t, s3, campfireID, "member")

	cfPubHex := hex.EncodeToString(cfPub)
	for _, s := range []store.Store{s1, s2, s3} {
		addPeerEndpoint(t, s, campfireID, id1.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, id2.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, id3.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, cfPubHex)
	}

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+476)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+477)
	addr3 := fmt.Sprintf("127.0.0.1:%d", base+478)
	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)
	ep3 := fmt.Sprintf("http://%s", addr3)

	tr1 := startTransportWithKey(t, addr1, s1, id1, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addr2, s2, id2, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addr3, s3, id3, campfireID, cfPriv, cfPub)

	// Register id2 and id3 as local peers of tr1. No path-vector routes exist —
	// only local peers, no routing beacons delivered.
	tr1.AddPeer(campfireID, id2.PublicKeyHex(), ep2)
	tr1.AddPeer(campfireID, id3.PublicKeyHex(), ep3)

	// Deliver a message as id1 (NOT id2 or id3, so neither is excluded as sender).
	idSender := tempIdentity(t)
	addPeerEndpoint(t, s1, campfireID, idSender.PublicKeyHex())
	msg := newTestMessage(t, idSender)

	if err := cfhttp.Deliver(ep1, campfireID, msg, idSender); err != nil {
		t.Fatalf("deliver message: %v", err)
	}

	// Both peers should receive the message (flood fallback — no path-vector routes).
	if !waitForMessage(t, s2, campfireID, msg.ID, 2*time.Second) {
		t.Errorf("message should be flooded to peer id2 when no path-vector routes exist")
	}
	if !waitForMessage(t, s3, campfireID, msg.ID, 2*time.Second) {
		t.Errorf("message should be flooded to peer id3 when no path-vector routes exist")
	}
}

// TestForwardingExcludesSender verifies that the message sender is never included
// in the forwarding set, even if the sender appears in the peer-needs-set or
// as a routing next_hop.
//
// Done condition: a message delivered by id2 is NOT forwarded back to id2,
// even when id2 is a registered local peer.
func TestForwardingExcludesSender(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireID := hex.EncodeToString(cfPub)

	id1 := tempIdentity(t) // router
	id2 := tempIdentity(t) // sender (should be excluded from forwarding)
	id3 := tempIdentity(t) // other peer (should receive the message)

	s1 := tempStore(t)
	s2 := tempStore(t)
	s3 := tempStore(t)

	addMembershipWithRole(t, s1, campfireID, "creator")
	addMembershipWithRole(t, s2, campfireID, "member")
	addMembershipWithRole(t, s3, campfireID, "member")

	cfPubHex := hex.EncodeToString(cfPub)
	for _, s := range []store.Store{s1, s2, s3} {
		addPeerEndpoint(t, s, campfireID, id1.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, id2.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, id3.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, cfPubHex)
	}

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+479)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+480)
	addr3 := fmt.Sprintf("127.0.0.1:%d", base+481)
	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)
	ep3 := fmt.Sprintf("http://%s", addr3)

	tr1 := startTransportWithKey(t, addr1, s1, id1, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addr2, s2, id2, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addr3, s3, id3, campfireID, cfPriv, cfPub)

	// Both id2 and id3 are registered as local peers of tr1.
	tr1.AddPeer(campfireID, id2.PublicKeyHex(), ep2)
	tr1.AddPeer(campfireID, id3.PublicKeyHex(), ep3)

	// Deliver a message FROM id2 to tr1. id2 is the sender — must be excluded.
	msg := newTestMessage(t, id2)
	if err := cfhttp.Deliver(ep1, campfireID, msg, id2); err != nil {
		t.Fatalf("deliver message from id2: %v", err)
	}

	// id3 should receive the forwarded message (it's not the sender).
	if !waitForMessage(t, s3, campfireID, msg.ID, 2*time.Second) {
		t.Errorf("message should be forwarded to id3 (not the sender)")
	}

	// id2 should NOT receive an echo (it's the sender — excluded from forwarding set).
	// Wait briefly to confirm no echo arrives.
	time.Sleep(200 * time.Millisecond)
	s2Msgs, _ := s2.ListMessages(campfireID, 0)
	echoCount := 0
	for _, m := range s2Msgs {
		if m.ID == msg.ID {
			echoCount++
		}
	}
	// id2 sent the message, so it's in s1 after delivery. But tr1 should NOT forward back to ep2.
	// s2 should have 0 copies (it only receives what tr1 forwards, and tr1 excludes sender).
	if echoCount > 0 {
		t.Errorf("message should NOT be echoed back to sender id2 (got %d copies)", echoCount)
	}
}

// TestForwardingCombinesPeerNeedsAndNextHops verifies that the forwarding set is
// the union of PeerNeedsSet and routing next_hops, not just one or the other.
//
// Setup:
//   - id2 is the routing next_hop (has a path-vector route for campfireID).
//   - id3 is in the peer-needs-set (it previously delivered a message for campfireID).
//   - Both should receive forwarded messages.
//
// Done condition: both id2 and id3 receive the message.
func TestForwardingCombinesPeerNeedsAndNextHops(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireID := hex.EncodeToString(cfPub)

	id1 := tempIdentity(t) // router
	id2 := tempIdentity(t) // routing next_hop
	id3 := tempIdentity(t) // peer-needs-set member (delivered a prior message)

	s1 := tempStore(t)
	s2 := tempStore(t)
	s3 := tempStore(t)

	addMembershipWithRole(t, s1, campfireID, "creator")
	addMembershipWithRole(t, s2, campfireID, "member")
	addMembershipWithRole(t, s3, campfireID, "member")

	cfPubHex := hex.EncodeToString(cfPub)
	for _, s := range []store.Store{s1, s2, s3} {
		addPeerEndpoint(t, s, campfireID, id1.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, id2.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, id3.PublicKeyHex())
		addPeerEndpoint(t, s, campfireID, cfPubHex)
	}

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+482)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+483)
	addr3 := fmt.Sprintf("127.0.0.1:%d", base+484)
	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)
	ep3 := fmt.Sprintf("http://%s", addr3)

	tr1 := startTransportWithKey(t, addr1, s1, id1, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addr2, s2, id2, campfireID, cfPriv, cfPub)
	_ = startTransportWithKey(t, addr3, s3, id3, campfireID, cfPriv, cfPub)

	// Register both as local peers of tr1.
	tr1.AddPeer(campfireID, id2.PublicKeyHex(), ep2)
	tr1.AddPeer(campfireID, id3.PublicKeyHex(), ep3)

	// 1. Populate routing table with path-vector route for campfireID: NextHop = id2.
	//    Deliver a routing:beacon from id2 with a non-empty path, advertising campfireID itself.
	beaconMsg := makeSignedBeaconMessage(t, id2, cfPub, cfPriv, ep2, []string{id2.PublicKeyHex()})
	if err := cfhttp.Deliver(ep1, campfireID, beaconMsg, id2); err != nil {
		t.Fatalf("deliver beacon: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	// Verify path-vector route is installed with NextHop=id2.
	routes := tr1.RoutingTable().Lookup(campfireID)
	if len(routes) == 0 {
		t.Fatal("path-vector route not installed for campfireID")
	}
	hasPathVectorWithID2 := false
	for _, r := range routes {
		if len(r.Path) > 0 && r.NextHop == id2.PublicKeyHex() {
			hasPathVectorWithID2 = true
		}
	}
	if !hasPathVectorWithID2 {
		t.Fatalf("expected path-vector route with NextHop=id2, got: %+v", routes)
	}

	// 2. Populate peer-needs-set for campfireID with id3 by having id3 deliver a prior message.
	//    RecordMessageDelivery is called in handleDeliver, so we deliver a message from id3.
	priorMsg := newTestMessage(t, id3)
	if err := cfhttp.Deliver(ep1, campfireID, priorMsg, id3); err != nil {
		t.Fatalf("deliver prior message from id3: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Verify peer-needs-set contains id3.
	pns := tr1.RoutingTable().PeerNeedsSet(campfireID)
	if !pns[id3.PublicKeyHex()] {
		t.Fatalf("peer-needs-set should contain id3 after it delivered a message for campfireID")
	}

	// 3. Now deliver the test message from a fresh sender (not id2 or id3).
	idSender := tempIdentity(t)
	addPeerEndpoint(t, s1, campfireID, idSender.PublicKeyHex())
	msg := newTestMessage(t, idSender)
	if err := cfhttp.Deliver(ep1, campfireID, msg, idSender); err != nil {
		t.Fatalf("deliver message: %v", err)
	}

	// Both id2 (next_hop) and id3 (peer-needs-set) should receive the forwarded message.
	if !waitForMessage(t, s2, campfireID, msg.ID, 2*time.Second) {
		t.Errorf("next_hop peer (id2) should receive forwarded message via routing next_hops")
	}
	if !waitForMessage(t, s3, campfireID, msg.ID, 2*time.Second) {
		t.Errorf("peer-needs-set member (id3) should receive forwarded message via PeerNeedsSet")
	}
}

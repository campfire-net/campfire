package http_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

func tempStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func tempIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	return id
}

// addMembership inserts a campfire membership so messages have a valid campfire_id.
// SQLite FK enforcement is off by default, but we add it for correctness.
func addMembership(t *testing.T, s store.Store, campfireID string) {
	t.Helper()
	err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "http",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
}

func startTransport(t *testing.T, addr string, s store.Store) *cfhttp.Transport {
	t.Helper()
	tr := cfhttp.New(addr, s)
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport on %s: %v", addr, err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	return tr
}

func newTestMessage(t *testing.T, id *identity.Identity) *message.Message {
	t.Helper()
	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("hello from test"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	return msg
}

// portBase returns a per-process base port to avoid conflicts between parallel test runs.
// Each test file is assigned a dedicated non-overlapping block of offsets. When adding a
// new test, use the next free offset within the owning file's block. When adding a new
// file, claim the next free block (increment by 20) and register it here.
//
// Port allocation table (offset blocks, each 20 wide):
//
//	  0 -  19: http_test.go               (used:  0-13)
//	 20 -  39: send_read_test.go          (used: 20-26)
//	 40 -  59: frost_sign_e2e_test.go     (used: 40-41)
//	 60 -  79: handler_sign_test.go       (used: 60-69)
//	 80 -  99: sign_race_test.go          (used: 80-82)
//	100 - 119: poll_handler_test.go       (used: 100-112)
//	120 - 139: poll_client_test.go        (used: 120-124)
//	140 - 159: auth_test.go              (used: 140-152)
//	160 - 179: evict_auth_test.go         (used: 160-163)
//	180 - 219: security_test.go          (used: 180-202; 40-port block, extra tests)
//	220 - 249: rekey_test.go             (used: 220-230; 226+threshold covers 227-228)
//	250 - 259: rekey_replay_test.go      (used: 250-251)
//	260 - 279: replay_protection_test.go  (used: 260-266)
//	280 - 299: join_peer_disclosure_test.go (used: 280-283)
//	300 - 319: join_pubkey_test.go        (used: 300, 305)
//	320 - 339: (reserved)
//	340 - 359: invite_only_join_test.go   (used: 340, 345, 350, 355)
//	360 - 379: membership_event_test.go   (used: 360-362)
//	380 - 399: handler_message_params_test.go (used: 380-382)
//	400 - 419: handler_sync_malformed_test.go (used: 400-401)
//	420 - 439: ssrf_test.go               (used: 420)
//	440 - 459: handler_message_test.go    (used: 440-444)
//	460 - 499: forwarding_test.go         (used: 460-472)
//	500 - 519: beacon_readvert_test.go    (used: 500-506)
func portBase() int {
	return 19000 + (os.Getpid() % 500)
}

// TestDeliverAndSync verifies that two transports can exchange messages.
func TestDeliverAndSync(t *testing.T) {
	campfireID := "test-campfire-1"
	id1 := tempIdentity(t)
	id2 := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)
	addMembership(t, s1, campfireID)
	addMembership(t, s2, campfireID)
	// Add both identities as members so membership checks pass.
	addPeerEndpoint(t, s2, campfireID, id1.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, id2.PublicKeyHex())

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+0)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+1)

	startTransport(t, addr1, s1)
	startTransport(t, addr2, s2)

	ep2 := fmt.Sprintf("http://%s", addr2)

	// id1 delivers a message to transport 2
	msg := newTestMessage(t, id1)
	if err := cfhttp.Deliver(ep2, campfireID, msg, id1); err != nil {
		t.Fatalf("deliver failed: %v", err)
	}

	// id2 syncs from transport 2 — should see the message
	msgs, err := cfhttp.Sync(ep2, campfireID, 0, id2)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].ID != msg.ID {
		t.Errorf("message ID mismatch: got %s, want %s", msgs[0].ID, msg.ID)
	}
}

// TestSyncSince verifies the since timestamp filter.
func TestSyncSince(t *testing.T) {
	campfireID := "test-campfire-since"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+2)
	startTransport(t, addr, s)
	ep := fmt.Sprintf("http://%s", addr)

	msg1 := newTestMessage(t, id)
	if err := cfhttp.Deliver(ep, campfireID, msg1, id); err != nil {
		t.Fatalf("deliver msg1: %v", err)
	}

	cutoff := time.Now().UnixNano()
	time.Sleep(2 * time.Millisecond)

	msg2 := newTestMessage(t, id)
	if err := cfhttp.Deliver(ep, campfireID, msg2, id); err != nil {
		t.Fatalf("deliver msg2: %v", err)
	}

	// Sync since cutoff should return only msg2
	msgs, err := cfhttp.Sync(ep, campfireID, cutoff, id)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message since cutoff, got %d", len(msgs))
	}
	if msgs[0].ID != msg2.ID {
		t.Errorf("expected msg2 (%s), got %s", msg2.ID, msgs[0].ID)
	}
}

// TestInvalidSignatureRejected verifies that a mismatched signature gets 401.
func TestInvalidSignatureRejected(t *testing.T) {
	campfireID := "test-campfire-auth"
	id1 := tempIdentity(t)
	id2 := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+3)
	startTransport(t, addr, s)
	ep := fmt.Sprintf("http://%s", addr)

	// Build request body signed by id2 but claiming sender is id1 → 401
	msg := newTestMessage(t, id1)
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("encoding message: %v", err)
	}
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)

	// Sign with id2 but claim sender is id1 — signature will fail to verify.
	// Use signTestRequest with id2, then overwrite the Sender header.
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	signTestRequest(req, id2, body)
	req.Header.Set("X-Campfire-Sender", id1.PublicKeyHex()) // override to wrong key

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestMissingSignatureHeadersRejected verifies requests without auth headers get 401.
func TestMissingSignatureHeadersRejected(t *testing.T) {
	campfireID := "test-campfire-noheader"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+4)
	startTransport(t, addr, s)
	ep := fmt.Sprintf("http://%s", addr)

	msg := newTestMessage(t, id)
	body, _ := cfencoding.Marshal(msg)
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	// No signature headers

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestDeliverToAll verifies parallel fan-out to multiple peers.
func TestDeliverToAll(t *testing.T) {
	campfireID := "test-campfire-fanout"
	id := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)
	addMembership(t, s1, campfireID)
	addMembership(t, s2, campfireID)
	// Add id as a member on both stores so membership checks pass.
	addPeerEndpoint(t, s1, campfireID, id.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, id.PublicKeyHex())

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+5)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+6)
	startTransport(t, addr1, s1)
	startTransport(t, addr2, s2)

	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)

	msg := newTestMessage(t, id)
	errs := cfhttp.DeliverToAll([]string{ep1, ep2}, campfireID, msg, id)
	for i, err := range errs {
		if err != nil {
			t.Errorf("DeliverToAll[%d]: %v", i, err)
		}
	}

	// Both peers should have the message
	for _, ep := range []string{ep1, ep2} {
		msgs, err := cfhttp.Sync(ep, campfireID, 0, id)
		if err != nil {
			t.Errorf("sync from %s: %v", ep, err)
			continue
		}
		if len(msgs) != 1 {
			t.Errorf("expected 1 message from %s, got %d", ep, len(msgs))
		}
	}
}

// TestMembershipNotification verifies join/leave events update the peer list.
func TestMembershipNotification(t *testing.T) {
	// Loopback endpoints are used in this integration test; bypass SSRF validation.
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	campfireID := "test-campfire-membership"
	id1 := tempIdentity(t)
	id2 := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)
	addMembership(t, s1, campfireID)
	addMembership(t, s2, campfireID)
	// id2 must be a member of s1's campfire to send membership notifications.
	addPeerEndpoint(t, s1, campfireID, id2.PublicKeyHex())

	base := portBase()
	addr1 := fmt.Sprintf("127.0.0.1:%d", base+7)
	addr2 := fmt.Sprintf("127.0.0.1:%d", base+8)
	tr1 := startTransport(t, addr1, s1)
	startTransport(t, addr2, s2)

	ep1 := fmt.Sprintf("http://%s", addr1)
	ep2 := fmt.Sprintf("http://%s", addr2)

	// id2 notifies tr1 it joined
	joinEvent := cfhttp.MembershipEvent{
		Event:    "join",
		Member:   id2.PublicKeyHex(),
		Endpoint: ep2,
	}
	if err := cfhttp.NotifyMembership(ep1, campfireID, joinEvent, id2); err != nil {
		t.Fatalf("notify join: %v", err)
	}

	peers := tr1.Peers(campfireID)
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after join, got %d", len(peers))
	}
	if peers[0].PubKeyHex != id2.PublicKeyHex() {
		t.Errorf("peer pubkey mismatch")
	}

	// id2 notifies leave
	leaveEvent := cfhttp.MembershipEvent{
		Event:  "leave",
		Member: id2.PublicKeyHex(),
	}
	if err := cfhttp.NotifyMembership(ep1, campfireID, leaveEvent, id2); err != nil {
		t.Fatalf("notify leave: %v", err)
	}

	peers = tr1.Peers(campfireID)
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers after leave, got %d", len(peers))
	}

	// suppress unused warning for id1
	_ = id1
}

// TestJoinKeyExchange verifies that a joiner receives the campfire private key
// encrypted via ECDH and can decrypt it.
func TestJoinKeyExchange(t *testing.T) {
	campfireID := "test-campfire-join"

	// Generate campfire identity (the campfire's own Ed25519 keypair).
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	// Agent A (creator/admitting member) and Agent B (joiner).
	idA := tempIdentity(t)
	idB := tempIdentity(t)

	sA := tempStore(t)
	sB := tempStore(t)

	// Add campfire membership to Agent A's store so the join handler can serve metadata.
	err = sA.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+9)
	epA := fmt.Sprintf("http://%s", addrA)

	// Start transport for Agent A with a key provider that returns the campfire keypair.
	trA := cfhttp.New(addrA, sA)
	trA.SetSelfInfo(idA.PublicKeyHex(), epA)
	trA.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("campfire not found: %s", id)
	})
	if err := trA.Start(); err != nil {
		t.Fatalf("starting transport A: %v", err)
	}
	t.Cleanup(func() { trA.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Agent B (joiner) sends join request.
	result, err := cfhttp.Join(epA, campfireID, idB, "")
	if err != nil {
		t.Fatalf("join failed: %v", err)
	}

	// Verify the campfire public key matches.
	if fmt.Sprintf("%x", result.CampfirePubKey) != fmt.Sprintf("%x", cfPub) {
		t.Errorf("campfire public key mismatch: got %x, want %x", result.CampfirePubKey, cfPub)
	}

	// Verify the decrypted private key matches.
	if fmt.Sprintf("%x", result.CampfirePrivKey) != fmt.Sprintf("%x", cfPriv) {
		t.Errorf("campfire private key mismatch after decryption")
	}

	// Verify join protocol and threshold.
	if result.JoinProtocol != "open" {
		t.Errorf("join_protocol = %s, want open", result.JoinProtocol)
	}
	if result.Threshold != 1 {
		t.Errorf("threshold = %d, want 1", result.Threshold)
	}

	// Verify that Agent A (admitting member) appears in the peers list.
	foundA := false
	for _, p := range result.Peers {
		if p.PubKeyHex == idA.PublicKeyHex() && p.Endpoint == epA {
			foundA = true
		}
	}
	if !foundA {
		t.Errorf("admitting member %s not found in peers list: %+v", idA.PublicKeyHex(), result.Peers)
	}

	// Verify joiner's endpoint was stored in Agent A's peer list.
	// (joiner provided no endpoint, so nothing to check there)

	// Suppress unused
	_ = sB
	_ = idA
}

// TestMembershipJoinIdentityInjectionRejected verifies that a join event where
// event.Member != X-Campfire-Sender is rejected with 400.
func TestMembershipJoinIdentityInjectionRejected(t *testing.T) {
	campfireID := "test-campfire-injection"
	attacker := tempIdentity(t)
	victim := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	// Add attacker as a member so membership check passes.
	s.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireID, MemberPubkey: attacker.PublicKeyHex(), Endpoint: "http://127.0.0.1:1"}) //nolint:errcheck

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+10)
	ep := fmt.Sprintf("http://%s", addr)
	startTransport(t, addr, s)

	body, err := json.Marshal(cfhttp.MembershipEvent{
		Event:    "join",
		Member:   victim.PublicKeyHex(),
		Endpoint: ep,
	})
	if err != nil {
		t.Fatalf("marshaling event: %v", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/membership", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	signTestRequest(req, attacker, body)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for mismatched join member, got %d", resp.StatusCode)
	}
}

// TestMembershipJoinValidSender verifies a well-formed join event is accepted.
func TestMembershipJoinValidSender(t *testing.T) {
	// Loopback endpoints are used in this integration test; bypass SSRF validation.
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	campfireID := "test-campfire-valid-join"
	joiner := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	// Add joiner as a known member so membership check passes.
	s.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireID, MemberPubkey: joiner.PublicKeyHex(), Endpoint: "http://127.0.0.1:1"}) //nolint:errcheck

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+11)
	ep := fmt.Sprintf("http://%s", addr)
	tr := startTransport(t, addr, s)

	joinEvent := cfhttp.MembershipEvent{
		Event:    "join",
		Member:   joiner.PublicKeyHex(),
		Endpoint: ep,
	}
	if err := cfhttp.NotifyMembership(ep, campfireID, joinEvent, joiner); err != nil {
		t.Fatalf("valid join should be accepted: %v", err)
	}

	peers := tr.Peers(campfireID)
	found := false
	for _, p := range peers {
		if p.PubKeyHex == joiner.PublicKeyHex() {
			found = true
		}
	}
	if !found {
		t.Errorf("joiner not found in peer list after valid join")
	}
}

// TestMembershipLeaveIdentityMismatchRejected verifies a leave event where
// event.Member != sender is rejected.
func TestMembershipLeaveIdentityMismatchRejected(t *testing.T) {
	campfireID := "test-campfire-leave-mismatch"
	attacker := tempIdentity(t)
	target := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	// Add attacker as member so membership check passes.
	s.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireID, MemberPubkey: attacker.PublicKeyHex(), Endpoint: "http://127.0.0.1:1"}) //nolint:errcheck

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+12)
	ep := fmt.Sprintf("http://%s", addr)
	startTransport(t, addr, s)

	body, err := json.Marshal(cfhttp.MembershipEvent{
		Event:  "leave",
		Member: target.PublicKeyHex(),
	})
	if err != nil {
		t.Fatalf("marshaling event: %v", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/membership", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	signTestRequest(req, attacker, body)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for leave with mismatched member, got %d", resp.StatusCode)
	}
}

// TestMembershipJoinSSRFEndpointRejected verifies that a join event containing
// a private/internal endpoint (SSRF attempt) is rejected with 400.
func TestMembershipJoinSSRFEndpointRejected(t *testing.T) {
	cfhttp.RestoreValidateJoinerEndpoint()
	t.Cleanup(cfhttp.OverrideValidateJoinerEndpointForTest)

	campfireID := "test-campfire-ssrf-join"
	attacker := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	// Attacker is a legitimate member — auth check passes; only endpoint validation
	// should reject the request.
	s.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireID, MemberPubkey: attacker.PublicKeyHex(), Endpoint: "http://127.0.0.1:1"}) //nolint:errcheck

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+13)
	ep := fmt.Sprintf("http://%s", addr)
	startTransport(t, addr, s)

	// Attacker announces itself with a private metadata-service endpoint.
	body, err := json.Marshal(cfhttp.MembershipEvent{
		Event:    "join",
		Member:   attacker.PublicKeyHex(),
		Endpoint: "http://169.254.169.254/latest/meta-data/",
	})
	if err != nil {
		t.Fatalf("marshaling event: %v", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/membership", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	signTestRequest(req, attacker, body)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for SSRF endpoint in join event, got %d", resp.StatusCode)
	}
}

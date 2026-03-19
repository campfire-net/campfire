package http_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
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

func tempStore(t *testing.T) *store.Store {
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
func addMembership(t *testing.T, s *store.Store, campfireID string) {
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

func startTransport(t *testing.T, addr string, s *store.Store) *cfhttp.Transport {
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

	sig := id2.Sign(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("X-Campfire-Sender", id1.PublicKeyHex())
	req.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))

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
	campfireID := "test-campfire-membership"
	id1 := tempIdentity(t)
	id2 := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)
	addMembership(t, s1, campfireID)
	addMembership(t, s2, campfireID)

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

// TestJoinThresholdShareDistributed verifies that when threshold>1 and a pending DKG
// share is available, the joiner receives ThresholdShareData in the JoinResponse and
// the joiner's participant ID is correctly persisted in peer_endpoints.
func TestJoinThresholdShareDistributed(t *testing.T) {
	campfireID := "test-campfire-threshold-join"

	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	idA := tempIdentity(t) // admitting member
	idB := tempIdentity(t) // joiner

	sA := tempStore(t)

	// threshold=2 campfire.
	err = sA.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    2,
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Store a pending DKG share for participant 2 (the joiner).
	pendingShare := []byte("fake-dkg-share-for-participant-2")
	const joinerParticipantID = uint32(2)
	if err := sA.StorePendingThresholdShare(campfireID, joinerParticipantID, pendingShare); err != nil {
		t.Fatalf("storing pending threshold share: %v", err)
	}

	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+10)
	epA := fmt.Sprintf("http://%s", addrA)

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

	joinerEndpoint := "http://127.0.0.1:19999"
	result, err := cfhttp.Join(epA, campfireID, idB, joinerEndpoint)
	if err != nil {
		t.Fatalf("join failed: %v", err)
	}

	// Joiner should receive the decrypted DKG share.
	if len(result.ThresholdShareData) == 0 {
		t.Fatal("expected ThresholdShareData to be non-empty, got empty")
	}
	if string(result.ThresholdShareData) != string(pendingShare) {
		t.Errorf("ThresholdShareData mismatch: got %q, want %q", result.ThresholdShareData, pendingShare)
	}

	// Joiner should receive the correct participant ID.
	if result.MyParticipantID != joinerParticipantID {
		t.Errorf("MyParticipantID = %d, want %d", result.MyParticipantID, joinerParticipantID)
	}

	// No private key should be returned for threshold>1.
	if len(result.CampfirePrivKey) != 0 {
		t.Error("expected CampfirePrivKey to be nil for threshold>1")
	}

	// Joiner's participant ID should be persisted in peer_endpoints.
	peers, err := sA.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("listing peer endpoints: %v", err)
	}
	var found bool
	for _, p := range peers {
		if p.MemberPubkey == idB.PublicKeyHex() {
			found = true
			if p.ParticipantID != joinerParticipantID {
				t.Errorf("peer_endpoints participant_id = %d, want %d", p.ParticipantID, joinerParticipantID)
			}
			if p.Endpoint != joinerEndpoint {
				t.Errorf("peer_endpoints endpoint = %q, want %q", p.Endpoint, joinerEndpoint)
			}
		}
	}
	if !found {
		t.Errorf("joiner %s not found in peer_endpoints after join", idB.PublicKeyHex())
	}

	// The pending share should have been consumed (claimed).
	pid, remaining, err := sA.ClaimPendingThresholdShare(campfireID)
	if err != nil {
		t.Fatalf("checking remaining pending shares: %v", err)
	}
	if remaining != nil {
		t.Errorf("expected no remaining pending shares after join, got pid=%d data=%q", pid, remaining)
	}
}

// TestJoinThresholdNoPendingShare verifies that when threshold>1 but no pending DKG
// share is available, the handler returns 200 without ThresholdShareData (no panic, no 500).
func TestJoinThresholdNoPendingShare(t *testing.T) {
	campfireID := "test-campfire-threshold-noshare"

	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	idA := tempIdentity(t) // admitting member
	idB := tempIdentity(t) // joiner

	sA := tempStore(t)

	err = sA.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    3,
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
	// Intentionally do NOT store any pending threshold share.

	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+11)
	epA := fmt.Sprintf("http://%s", addrA)

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

	result, err := cfhttp.Join(epA, campfireID, idB, "")
	if err != nil {
		t.Fatalf("join failed (expected success with empty share): %v", err)
	}

	// No share data should be present.
	if len(result.ThresholdShareData) != 0 {
		t.Errorf("expected ThresholdShareData to be empty when no pending share, got %d bytes", len(result.ThresholdShareData))
	}

	// No participant ID should be assigned.
	if result.MyParticipantID != 0 {
		t.Errorf("expected MyParticipantID=0 when no pending share, got %d", result.MyParticipantID)
	}

	// No private key either.
	if len(result.CampfirePrivKey) != 0 {
		t.Error("expected CampfirePrivKey to be nil for threshold>1")
	}

	// Threshold should be reported correctly.
	if result.Threshold != 3 {
		t.Errorf("threshold = %d, want 3", result.Threshold)
	}

	_ = idA
}

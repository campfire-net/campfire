package protocol_test

// Tests for campfireagent-432: syncFromHTTPPeers must reject messages with
// invalid Ed25519 signatures, matching the verification behaviour of
// syncIfFilesystem. Without this fix a compromised peer can inject arbitrary
// forged messages into the local store.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// newValidSyncMessage creates a properly signed message with a valid provenance
// hop — accepted by VerifySignature() and VerifyProvenance().
func newValidSyncMessage(t *testing.T, cfID *identity.Identity, payload string) *message.Message {
	t.Helper()

	senderID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating sender identity: %v", err)
	}

	msg, err := message.NewMessage(
		message.MustNewEd25519Signer(senderID.PrivateKey, senderID.PublicKey),
		[]byte(payload),
		[]string{"status"},
		[]string{},
	)
	if err != nil {
		t.Fatalf("creating valid message: %v", err)
	}

	if err := msg.AddHop(cfID.PrivateKey, cfID.PublicKey, []byte("testhash"), 1, "open", []string{}, "full"); err != nil {
		t.Fatalf("adding provenance hop: %v", err)
	}

	return msg
}

// newTamperedSyncMessage creates a valid message then tampers with the payload
// so VerifySignature() returns false. The Signature field is left untouched —
// it no longer matches the tampered payload.
func newTamperedSyncMessage(t *testing.T, cfID *identity.Identity) *message.Message {
	t.Helper()

	msg := newValidSyncMessage(t, cfID, "original payload before tampering")

	// Overwrite the payload after signing. The signature is now invalid.
	msg.Payload = []byte("TAMPERED by compromised peer — should be rejected")

	// Confirm the message is indeed invalid before using it in the test.
	if msg.VerifySignature() {
		t.Fatal("test setup error: tampered message still passes VerifySignature — tampering failed")
	}

	return msg
}

// setupHTTPSyncPeer starts an httptest.Server that responds to
//
//	GET /campfire/{id}/sync
//
// with a CBOR-encoded []message.Message containing the provided messages.
// Other paths return 404.
func setupHTTPSyncPeer(t *testing.T, msgs []*message.Message) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only respond to the sync endpoint.
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Encode the messages as CBOR.
		flat := make([]message.Message, len(msgs))
		for i, m := range msgs {
			flat[i] = *m
		}
		body, err := cfencoding.Marshal(flat)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/cbor")
		w.WriteHeader(http.StatusOK)
		w.Write(body) //nolint:errcheck
	}))

	return server
}

// setupHTTPPeerCampfire creates a p2p-http membership and registers peerEndpoint
// as a peer. Returns the campfireID.
func setupHTTPPeerCampfire(t *testing.T, agentID *identity.Identity, s store.Store, cfID *identity.Identity, peerEndpoint string) string {
	t.Helper()

	campfireID := cfID.PublicKeyHex()

	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  t.TempDir(), // unused for p2p-http but field is required
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "p2p-http",
	}); err != nil {
		t.Fatalf("adding p2p-http membership: %v", err)
	}

	// Register a peer with an endpoint distinct from the agent's own key.
	peerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating peer identity: %v", err)
	}
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: peerID.PublicKeyHex(),
		Endpoint:     peerEndpoint,
	}); err != nil {
		t.Fatalf("registering peer endpoint: %v", err)
	}

	return campfireID
}

// newNoOpSyncer returns a Syncer that does nothing (a no-op). Used to satisfy
// the condition in Read() that gates readFromHTTPPeers: the syncer being non-nil
// signals CLI mode (vs. MCP server mode, which is push-based). The actual HTTP
// peer sync is done by readFromHTTPPeers itself via cfhttp.Sync, not via the
// injected Syncer.
func newNoOpSyncer() protocol.Syncer {
	return &countingSyncer{}
}

// TestReadFromHTTPPeers_RejectsInvalidSignature is the primary regression test
// for campfireagent-432: a message returned by a peer with an invalid Ed25519
// signature must be silently dropped and must NOT appear in Read results.
//
// This test exercises the real crypto path — no mocking of VerifySignature.
func TestReadFromHTTPPeers_RejectsInvalidSignature(t *testing.T) {
	agentID, s, _ := setupTestEnv(t)
	defer s.Close()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	// Build a tampered message that a compromised peer would send.
	tampered := newTamperedSyncMessage(t, cfID)

	// Peer server serves only the tampered message.
	peer := setupHTTPSyncPeer(t, []*message.Message{tampered})
	defer peer.Close()

	campfireID := setupHTTPPeerCampfire(t, agentID, s, cfID, peer.URL)

	client := protocol.New(s, agentID)
	// SetSyncer is required: Read() only calls readFromHTTPPeers when c.syncer != nil
	// (the non-nil syncer signals CLI mode vs MCP server push-based mode).
	client.SetSyncer(newNoOpSyncer())

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	for _, m := range result.Messages {
		if m.ID == tampered.ID {
			t.Errorf("tampered message %q with invalid signature was accepted — signature verification missing in HTTP sync path", m.ID)
		}
	}
}

// TestReadFromHTTPPeers_AcceptsValidSignature verifies the positive path:
// a validly signed message from a peer IS returned by Read.
// Without this we cannot distinguish "verification rejects all" from "verification
// correctly rejects only invalid messages".
func TestReadFromHTTPPeers_AcceptsValidSignature(t *testing.T) {
	agentID, s, _ := setupTestEnv(t)
	defer s.Close()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	valid := newValidSyncMessage(t, cfID, "legitimate peer message")

	// Confirm the message is genuinely valid before using it in the test.
	if !valid.VerifySignature() {
		t.Fatal("test setup error: valid message fails VerifySignature")
	}
	if !valid.VerifyProvenance() {
		t.Fatal("test setup error: valid message fails VerifyProvenance")
	}

	peer := setupHTTPSyncPeer(t, []*message.Message{valid})
	defer peer.Close()

	campfireID := setupHTTPPeerCampfire(t, agentID, s, cfID, peer.URL)

	client := protocol.New(s, agentID)
	client.SetSyncer(newNoOpSyncer())

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	found := false
	for _, m := range result.Messages {
		if m.ID == valid.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("valid peer message %q was not returned by Read — expected it to be accepted (got %d messages)", valid.ID, len(result.Messages))
	}
}

// TestReadFromHTTPPeers_RejectsInvalidProvenance verifies that a message with a
// valid body signature but missing/invalid provenance hop is also rejected,
// consistent with syncIfFilesystem behaviour.
func TestReadFromHTTPPeers_RejectsInvalidProvenance(t *testing.T) {
	agentID, s, _ := setupTestEnv(t)
	defer s.Close()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	// Create a message without any provenance hop — VerifyProvenance returns false.
	senderID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating sender identity: %v", err)
	}
	noHop, err := message.NewMessage(
		message.MustNewEd25519Signer(senderID.PrivateKey, senderID.PublicKey),
		[]byte("no provenance hop"),
		[]string{"status"},
		[]string{},
	)
	if err != nil {
		t.Fatalf("creating no-hop message: %v", err)
	}

	// Body signature is valid; provenance is absent.
	if !noHop.VerifySignature() {
		t.Fatal("test setup error: no-hop message should have valid body signature")
	}
	if noHop.VerifyProvenance() {
		t.Fatal("test setup error: message without provenance hops should fail VerifyProvenance")
	}

	peer := setupHTTPSyncPeer(t, []*message.Message{noHop})
	defer peer.Close()

	campfireID := setupHTTPPeerCampfire(t, agentID, s, cfID, peer.URL)

	client := protocol.New(s, agentID)
	client.SetSyncer(newNoOpSyncer())

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	for _, m := range result.Messages {
		if m.ID == noHop.ID {
			t.Errorf("message %q with no provenance hops was accepted — VerifyProvenance check missing in HTTP sync path", m.ID)
		}
	}
}

// TestReadFromHTTPPeers_MixedValid_InvalidBatch verifies that when a peer sends
// a batch containing both valid and tampered messages, only the valid ones appear.
func TestReadFromHTTPPeers_MixedValid_InvalidBatch(t *testing.T) {
	agentID, s, _ := setupTestEnv(t)
	defer s.Close()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	validA := newValidSyncMessage(t, cfID, "valid A")
	tampered := newTamperedSyncMessage(t, cfID)
	validB := newValidSyncMessage(t, cfID, "valid B")

	peer := setupHTTPSyncPeer(t, []*message.Message{validA, tampered, validB})
	defer peer.Close()

	campfireID := setupHTTPPeerCampfire(t, agentID, s, cfID, peer.URL)

	client := protocol.New(s, agentID)
	client.SetSyncer(newNoOpSyncer())

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	ids := make(map[string]bool, len(result.Messages))
	for _, m := range result.Messages {
		ids[m.ID] = true
	}

	if !ids[validA.ID] {
		t.Errorf("valid message A %q should appear in results", validA.ID)
	}
	if !ids[validB.ID] {
		t.Errorf("valid message B %q should appear in results", validB.ID)
	}
	if ids[tampered.ID] {
		t.Errorf("tampered message %q must not appear in results", tampered.ID)
	}
}

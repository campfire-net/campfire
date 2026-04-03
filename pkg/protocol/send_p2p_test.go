package protocol_test

// TestSendP2PHTTP verifies that protocol.Client.Send() dispatches through
// sendP2PHTTP for a p2p-http membership with threshold=1, signs the provenance
// hop directly with the campfire private key (no FROST), stores the message in
// the local store, and delivers the CBOR message to the fake peer via HTTP POST.
//
// Covered bead: campfire-agent-bu8

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// setupP2PHTPCampfire creates a P2P HTTP campfire for tests:
//   - Generates a campfire identity
//   - Writes {campfireID}.cbor to tmpDir
//   - Records a p2p-http membership (threshold=1) in the store
//   - Registers a peer endpoint pointing to peerEndpoint
//
// Returns the campfireID (hex public key).
func setupP2PHTTPCampfire(
	t *testing.T,
	agentID *identity.Identity,
	s store.Store,
	tmpDir string,
	peerEndpoint string,
) string {
	t.Helper()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	// Write campfire state CBOR file to tmpDir/{campfireID}.cbor
	cfState := campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(&cfState)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	statePath := filepath.Join(tmpDir, campfireID+".cbor")
	if err := os.WriteFile(statePath, stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	// Add p2p-http membership (threshold=1, TransportDir = tmpDir)
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tmpDir,
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "p2p-http",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Register a peer endpoint for another member so delivery is exercised.
	// Use a distinct (fake) pubkey for the peer so it isn't filtered out.
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

// TestSendP2PHTTP is the primary test: threshold=1, single peer, message delivered.
func TestSendP2PHTTP(t *testing.T) {
	// Track delivery: count requests received and capture body.
	var deliveryCount int32
	var deliveredBody []byte

	// Fake peer server — accepts POST /campfire/{id}/deliver, returns 200.
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		deliveredBody = body
		atomic.AddInt32(&deliveryCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer peer.Close()

	agentID, s, tmpDir := setupTestEnv(t)
	campfireID := setupP2PHTTPCampfire(t, agentID, s, tmpDir, peer.URL)

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("test p2p payload"),
		Tags:       []string{"status"},
		Instance:   "implementer",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msg == nil {
		t.Fatal("Send returned nil message")
	}
	if msg.ID == "" {
		t.Fatal("message has empty ID")
	}

	// Assert: message signature is valid.
	if !msg.VerifySignature() {
		t.Error("message signature is invalid")
	}

	// Assert: sender matches agent identity.
	if fmt.Sprintf("%x", msg.Sender) != agentID.PublicKeyHex() {
		t.Errorf("sender mismatch: got %x, want %s", msg.Sender, agentID.PublicKeyHex())
	}

	// Assert: exactly 1 provenance hop.
	if len(msg.Provenance) != 1 {
		t.Fatalf("expected 1 provenance hop, got %d", len(msg.Provenance))
	}

	// Assert: hop signature verifies against the campfire key.
	hop := msg.Provenance[0]
	if !message.VerifyHop(msg.ID, hop) {
		t.Error("provenance hop signature is invalid")
	}

	// Assert: message stored in local store.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.ID == msg.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("message %s not found in local store (got %d messages)", msg.ID, len(msgs))
	}

	// Assert: fake peer received exactly 1 HTTP delivery.
	if got := atomic.LoadInt32(&deliveryCount); got != 1 {
		t.Errorf("expected 1 delivery to peer, got %d", got)
	}

	// Assert: peer received a valid CBOR-encoded message.
	if len(deliveredBody) == 0 {
		t.Fatal("peer received empty body")
	}
	var delivered message.Message
	if err := cfencoding.Unmarshal(deliveredBody, &delivered); err != nil {
		t.Errorf("peer received non-CBOR body: %v", err)
	} else if delivered.ID != msg.ID {
		t.Errorf("delivered message ID %s != sent message ID %s", delivered.ID, msg.ID)
	}
}

// TestSendP2PHTTP_NoPeers verifies that Send succeeds even when there are no
// peer endpoints registered (solo campfire). Message must still be stored locally.
func TestSendP2PHTTP_NoPeers(t *testing.T) {
	agentID, s, tmpDir := setupTestEnv(t)

	// Set up campfire with no peer endpoints.
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	cfState := campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(&cfState)
	if err != nil {
		t.Fatalf("marshalling state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, campfireID+".cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing state: %v", err)
	}
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tmpDir,
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "p2p-http",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("solo campfire"),
	})
	if err != nil {
		t.Fatalf("Send (no peers): %v", err)
	}
	if msg == nil {
		t.Fatal("Send returned nil message")
	}

	// Hop must still be present and valid.
	if len(msg.Provenance) != 1 {
		t.Fatalf("expected 1 provenance hop, got %d", len(msg.Provenance))
	}
	if !message.VerifyHop(msg.ID, msg.Provenance[0]) {
		t.Error("provenance hop signature invalid for solo campfire")
	}

	// Message must be in local store.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Error("message not found in local store (no peers)")
	}
}

// TestSendP2PHTTP_PathTraversal verifies that Send rejects membership records
// whose TransportDir contains path traversal sequences. A malicious or
// corrupted store record must not cause reads or writes outside the intended
// campfire transport directory.
//
// Regression test for campfire-agent-zde.
func TestSendP2PHTTP_PathTraversal(t *testing.T) {
	agentID, s, tmpDir := setupTestEnv(t)

	// Generate a campfire identity so we have a valid campfireID.
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	// Craft a traversal path: a raw string with ".." components that would escape
	// the intended transport directory if used directly in filepath.Join.
	// This simulates a tampered SQLite store record — the string is stored as-is,
	// not processed through filepath.Join/Clean when written.
	traversalDir := tmpDir + "/../../etc"

	// Write a membership record with the traversal TransportDir.
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  traversalDir,
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      0,
		Threshold:     1,
		TransportType: "p2p-http",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	client := protocol.New(s, agentID)
	_, err = client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("should be rejected"),
	})
	if err == nil {
		t.Fatal("expected Send to fail with path traversal TransportDir, but it succeeded")
	}
	// The error must mention transport dir validation — not a generic file-not-found.
	if !strings.Contains(err.Error(), "transport dir") {
		t.Errorf("expected 'transport dir' in error, got: %v", err)
	}
}

// TestSendP2PHTTP_PeerDeliveryFailure verifies that a peer delivery failure is
// non-fatal: Send must succeed and the message must be in the local store even
// when the peer HTTP server returns 500.
func TestSendP2PHTTP_PeerDeliveryFailure(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("simulated error")) //nolint:errcheck
	}))
	defer peer.Close()

	agentID, s, tmpDir := setupTestEnv(t)
	campfireID := setupP2PHTTPCampfire(t, agentID, s, tmpDir, peer.URL)

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("fail delivery"),
	})
	// Delivery failure is non-fatal (logged to stderr).
	if err != nil {
		t.Fatalf("Send should succeed despite peer failure: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}

	// Message still reaches local store.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.ID == msg.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("message not in local store after failed delivery")
	}
}

// TestMain for the protocol package — overrides the SSRF-safe HTTP client used
// by pkg/transport/http so that outbound calls to loopback httptest servers succeed.
// Without this, cfhttp.Deliver() blocks connections to 127.0.0.1.
func init() {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
}

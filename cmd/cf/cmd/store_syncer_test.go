package cmd

// Regression tests for campfireagent-9c4: StoreSyncer.Sync swallows transport errors.
//
// Before the fix: syncCampfire returned void, so StoreSyncer.Sync always returned nil
// after the membership lookup, even when the filesystem transport directory had been
// deleted. This meant the Subscribe error path in subscribe.go:154 was unreachable
// when a StoreSyncer was installed — subscriptions on deleted transports ran forever.
//
// Fix: syncFromFilesystem now returns an error when ListMessages fails.
// syncCampfire propagates filesystem errors and returns error.
// StoreSyncer.Sync propagates the error from syncCampfire.
//
// Also contains regression tests for campfireagent-e23: syncFromHTTPPeers must
// verify Ed25519 signatures before storing peer messages.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestStoreSyncer_ReturnsErrorOnBrokenTransport verifies that StoreSyncer.Sync
// returns a non-nil error when the filesystem transport directory has been deleted.
// Before the fix, it always returned nil.
func TestStoreSyncer_ReturnsErrorOnBrokenTransport(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, campfire.RoleFull)

	// Verify Sync works before deleting the transport dir.
	syncer := NewStoreSyncer(agentID, s)
	if err := syncer.Sync(campfireID); err != nil {
		t.Fatalf("Sync before deletion: %v", err)
	}

	// Delete the transport directory.
	cfDir := filepath.Join(transportBaseDir, campfireID)
	if err := os.RemoveAll(cfDir); err != nil {
		t.Fatalf("removing transport dir: %v", err)
	}

	// Sync must now return an error — this is the core of the bug fix.
	if err := syncer.Sync(campfireID); err == nil {
		t.Error("StoreSyncer.Sync returned nil after transport dir deletion; expected non-nil error")
	}
}

// TestStoreSyncer_Subscribe_TerminatesOnBrokenTransport verifies that a Subscribe
// call using a StoreSyncer closes the Messages() channel when the filesystem
// transport directory is deleted. Before the fix, the subscription ran forever
// because StoreSyncer.Sync always returned nil, so subscribe.go:154 was never
// reached.
func TestStoreSyncer_Subscribe_TerminatesOnBrokenTransport(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, campfire.RoleFull)

	// Install a StoreSyncer — this is the production code path used by cmd/cf.
	client := protocol.New(s, agentID)
	client.SetSyncer(NewStoreSyncer(agentID, s))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   campfireID,
		PollInterval: 50 * time.Millisecond,
	})

	// Let the subscription start polling.
	time.Sleep(100 * time.Millisecond)

	// Delete the transport directory to inject a permanent transport failure.
	cfDir := filepath.Join(transportBaseDir, campfireID)
	if err := os.RemoveAll(cfDir); err != nil {
		t.Fatalf("removing transport dir: %v", err)
	}

	// The subscription MUST close Messages() within 2 seconds.
	// Before the fix, it ran forever because StoreSyncer.Sync returned nil.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range sub.Messages() {
		}
	}()

	select {
	case <-done:
		// Channel closed — correct. Err() must be non-nil.
		if err := sub.Err(); err == nil {
			t.Error("sub.Err() is nil after transport dir deletion; expected non-nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Messages() channel did not close within 2 seconds after transport dir deletion: subscription ran forever (StoreSyncer.Sync is still swallowing errors)")
	}
}

// TestStoreSyncer_HTTPPeer_TamperedMessageRejected verifies that syncFromHTTPPeers
// does not store a message whose Ed25519 signature has been tampered with.
//
// Before the fix (campfireagent-e23): syncFromHTTPPeers called s.AddMessage without
// calling VerifySignature or VerifyProvenance, so a compromised peer could inject
// forged messages into the local store. This test uses a real httptest.Server to serve
// a tampered message and confirms it does NOT appear in the local store after sync.
func TestStoreSyncer_HTTPPeer_TamperedMessageRejected(t *testing.T) {
	// Override HTTP client so loopback (127.0.0.1) is reachable.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	// Create a valid signed message, then tamper with its payload so the
	// Ed25519 signature no longer verifies.
	authorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating author identity: %v", err)
	}
	validMsg := newTestMessage(t, authorID.PrivateKey, authorID.PublicKey,
		[]byte("legitimate payload"), []string{"test"}, nil)
	// Tamper: overwrite payload after signing — signature is now invalid.
	validMsg.Payload = []byte("tampered payload")

	// Stand up a real httptest.Server that serves the tampered message on /sync.
	peer := newMockHTTPPeer()
	peer.addSyncMessage(*validMsg)
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	// Create local store and register a p2p-http membership for the campfire.
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	// Use a campfire ID that is a valid hex string (64 chars).
	campfireID := "aabbccdd00112233aabbccdd00112233aabbccdd00112233aabbccdd00112233"

	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: "",
		TransportType: "p2p-http",
		JoinProtocol: "open",
		Role:         campfire.RoleFull,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Register the test server as the only peer for this campfire.
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: authorID.PublicKeyHex(),
		Endpoint:     srv.URL,
	}); err != nil {
		t.Fatalf("upserting peer endpoint: %v", err)
	}

	// Run syncFromHTTPPeers — the function under test.
	syncFromHTTPPeers(campfireID, agentID, s)

	// The tampered message must NOT be in the local store.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages in store after tampered-signature sync, got %d: "+
			"syncFromHTTPPeers is storing messages without verifying Ed25519 signatures", len(msgs))
	}
}

// TestStoreSyncer_HTTPPeer_ValidMessageAccepted verifies that syncFromHTTPPeers
// stores a correctly signed message from a peer — regression guard ensuring the
// verification fix does not reject legitimate messages.
func TestStoreSyncer_HTTPPeer_ValidMessageAccepted(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	authorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating author identity: %v", err)
	}

	// Create a valid signed message with a provenance hop so VerifyProvenance passes.
	validMsg := newTestMessage(t, authorID.PrivateKey, authorID.PublicKey,
		[]byte("valid payload"), []string{"test"}, nil)
	addTestProvenance(t, validMsg)

	peer := newMockHTTPPeer()
	peer.addSyncMessage(*validMsg)
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	campfireID := "ccddee0011223344ccddee0011223344ccddee0011223344ccddee0011223344"

	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: "",
		TransportType: "p2p-http",
		JoinProtocol: "open",
		Role:         campfire.RoleFull,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: authorID.PublicKeyHex(),
		Endpoint:     srv.URL,
	}); err != nil {
		t.Fatalf("upserting peer endpoint: %v", err)
	}

	syncFromHTTPPeers(campfireID, agentID, s)

	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("expected 1 message in store after valid-signature sync, got %d", len(msgs))
	}
}


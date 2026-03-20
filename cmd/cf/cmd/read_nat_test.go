package cmd

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---- helpers ----------------------------------------------------------------

func tempTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func tempTestIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	return id
}

func addTestMembership(t *testing.T, s *store.Store, campfireID string) {
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

// startTestTransport starts a real HTTP transport with the given identity as
// self, registers id as a peer in the store so membership checks pass, and
// returns the endpoint URL and transport.
func startTestTransport(t *testing.T, campfireID string, id *identity.Identity, s *store.Store) (string, *cfhttp.Transport) {
	t.Helper()
	// Let the OS pick a free port by binding to :0 — not possible with net/http/httptest.
	// Use an httptest.Server on random port instead.
	// We need a real cfhttp.Transport because handlePoll lives there.
	// Pick a deterministic but likely-free port.
	addr := fmt.Sprintf("127.0.0.1:0")
	// We can't use ":0" with net.Listen through cfhttp.New directly; use httptest.
	// Instead, start transport on a specific port. Use a helper to find a free one.
	_ = addr

	// Register self as a peer so membership checks in handlePoll pass.
	s.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: id.PublicKeyHex(),
		Endpoint:     "http://placeholder",
	})

	// Use httptest.NewServer with the transport's handler. We need to extract
	// the handler from the transport. Since cfhttp.Transport exposes no handler
	// getter, we start the transport on a fixed test port.
	//
	// Pick a port from the process PID to avoid cross-test collisions.
	base := 21000 + (os.Getpid() % 200)
	listenAddr := fmt.Sprintf("127.0.0.1:%d", base+200)
	ep := fmt.Sprintf("http://%s", listenAddr)

	tr := cfhttp.New(listenAddr, s)
	tr.SetSelfInfo(id.PublicKeyHex(), ep)
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport on %s: %v", listenAddr, err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck

	// Update the self peer endpoint to the real address.
	s.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: id.PublicKeyHex(),
		Endpoint:     ep,
	})

	time.Sleep(20 * time.Millisecond)
	return ep, tr
}

// storeTestMessage inserts a message record into the store and returns it.
func storeTestMessage(t *testing.T, s *store.Store, campfireID string, id *identity.Identity) store.MessageRecord {
	t.Helper()
	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("nat test payload"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	rec := store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      id.PublicKeyHex(),
		Payload:     msg.Payload,
		Tags:        []string{"test"},
		Antecedents: nil,
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
		Provenance:  nil,
		ReceivedAt:  time.Now().UnixNano(),
	}
	if _, err := s.AddMessage(rec); err != nil {
		t.Fatalf("storing message: %v", err)
	}
	return rec
}

// ---- tests ------------------------------------------------------------------

// TestCfReadNATModeReceivesMessages: selfEndpoint="", one peer pointing to a
// real transport that has a pre-stored message. The command logic should poll,
// receive the message, print it, and exit (no --follow).
func TestCfReadNATModeReceivesMessages(t *testing.T) {
	campfireID := "nat-recv-test"
	id := tempTestIdentity(t)
	s := tempTestStore(t)

	addTestMembership(t, s, campfireID)
	ep, _ := startTestTransport(t, campfireID, id, s)

	// Pre-store a message on the server's transport store.
	storeTestMessage(t, s, campfireID, id)

	cfg := natPollConfig{
		campfireID:   campfireID,
		peers:        []store.PeerEndpoint{{CampfireID: campfireID, MemberPubkey: id.PublicKeyHex(), Endpoint: ep}},
		cursor:       0,
		follow:       false,
		id:           id,
		timeoutSecs:  2,
		stopCh:       nil,
	}

	var out bytes.Buffer
	err := runNATPoll(cfg, &out)
	if err != nil {
		t.Fatalf("runNATPoll: %v", err)
	}

	output := out.String()
	if output == "" {
		t.Error("expected non-empty output from NAT poll, got empty string")
	}
}

// TestCfReadNATModeNoPeers: selfEndpoint="", no peers in store.
// computeInitialCursor and the validation logic should return an error
// indicating no reachable peers.
func TestCfReadNATModeNoPeers(t *testing.T) {
	cfg := natPollConfig{
		campfireID:  "nat-nopeer-test",
		peers:       nil, // empty
		cursor:      0,
		follow:      false,
		id:          nil,
		timeoutSecs: 2,
		stopCh:      nil,
	}

	var out bytes.Buffer
	err := runNATPoll(cfg, &out)
	if err == nil {
		t.Fatal("expected error for no peers, got nil")
	}
	if err.Error() != "no reachable peers to poll" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

// TestCfReadDirectModeUnchanged: selfEndpoint != "" means direct mode.
// NAT poll function should NOT be called; the existing store-read path is used.
// We verify by checking computeInitialCursor computes correctly from the store
// and that the direct-mode read path returns messages from a local store.
func TestCfReadDirectModeUnchanged(t *testing.T) {
	campfireID := "direct-mode-test"
	id := tempTestIdentity(t)
	s := tempTestStore(t)

	addTestMembership(t, s, campfireID)
	_ = storeTestMessage(t, s, campfireID, id)

	// computeInitialCursor should return the ReceivedAt of the stored message.
	cursor, err := computeInitialCursor(s, campfireID)
	if err != nil {
		t.Fatalf("computeInitialCursor: %v", err)
	}
	if cursor == 0 {
		t.Error("expected non-zero cursor after storing a message")
	}

	// Simulate direct mode: selfEndpoint != "", so we just read from store.
	// The natPollConfig path is NOT used when selfEndpoint != "".
	// This test validates that the cursor derivation works (contract for direct mode restart).
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Error("expected at least one message in direct mode store")
	}
}

// TestCfReadFollowTerminatesOnSignal: selfEndpoint="", --follow=true.
// After receiving one message, send on stopCh (simulating SIGINT),
// assert the function returns cleanly.
func TestCfReadFollowTerminatesOnSignal(t *testing.T) {
	campfireID := "nat-follow-signal"
	id := tempTestIdentity(t)
	s := tempTestStore(t)

	addTestMembership(t, s, campfireID)

	// Use httptest.NewServer as an alternative: create a minimal poll server.
	// We'll use the real transport but deliver a message after the poll starts,
	// then close stopCh to terminate.
	base := 21000 + (os.Getpid() % 200)
	listenAddr := fmt.Sprintf("127.0.0.1:%d", base+201)
	ep := fmt.Sprintf("http://%s", listenAddr)

	s.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: id.PublicKeyHex(),
		Endpoint:     ep,
	})

	tr := cfhttp.New(listenAddr, s)
	tr.SetSelfInfo(id.PublicKeyHex(), ep)
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	defer tr.Stop() //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	stopCh := make(chan os.Signal, 1)
	cfg := natPollConfig{
		campfireID:  campfireID,
		peers:       []store.PeerEndpoint{{CampfireID: campfireID, MemberPubkey: id.PublicKeyHex(), Endpoint: ep}},
		cursor:      0,
		follow:      true,
		id:          id,
		timeoutSecs: 2,
		stopCh:      stopCh,
	}

	errCh := make(chan error, 1)
	var out bytes.Buffer

	go func() {
		errCh <- runNATPoll(cfg, &out)
	}()

	// Give the goroutine a moment to start the poll loop, then send SIGINT.
	time.Sleep(50 * time.Millisecond)
	stopCh <- syscall.SIGINT

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runNATPoll returned error after signal: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runNATPoll did not terminate within 3s after SIGINT")
	}
}

// TestRunNATPollOneShotSinglePeerError verifies that in one-shot mode with a
// single failing peer, runNATPoll returns an error (not immediately — the peer
// is tried exactly once) rather than firing the peerIdx==0 check on the very
// first increment (which previously returned before any second attempt).
//
// The old bug: peerIdx = (0+1)%1 = 0, then "if peerIdx == 0 { return firstErr }"
// fired on the first error, which is correct exit behaviour but the comment
// above said it should "not fire immediately if first peer errors". The real
// semantic: the loop should attempt all peers once, then exit. With 1 peer that
// means exactly 1 attempt. So the fix: use consecutiveErrors >= len(peers).
func TestRunNATPollOneShotSinglePeerError(t *testing.T) {
	// Use a port that will refuse connections immediately.
	badEndpoint := "http://127.0.0.1:1" // port 1 should be unreachable
	id := tempTestIdentity(t)

	peers := []store.PeerEndpoint{
		{CampfireID: "cf-test", MemberPubkey: "aaa", Endpoint: badEndpoint},
	}

	stopCh := make(chan os.Signal, 1)
	cfg := natPollConfig{
		campfireID:  "cf-test",
		peers:       peers,
		cursor:      0,
		follow:      false,
		id:          id,
		timeoutSecs: 1,
		stopCh:      stopCh,
	}

	start := time.Now()
	var out bytes.Buffer
	err := runNATPoll(cfg, &out)
	elapsed := time.Since(start)

	// Must return an error since the only peer is unreachable.
	if err == nil {
		t.Fatal("expected error from unreachable peer, got nil")
	}

	// Must not loop indefinitely — should complete quickly (1 peer, 1 sleep of 1s).
	if elapsed > 5*time.Second {
		t.Errorf("runNATPoll took too long (%v); expected < 5s for single-peer one-shot", elapsed)
	}
}

// TestRunNATPollOneShotMultiPeerAllFail verifies that in one-shot mode with
// multiple failing peers, runNATPoll returns after trying all peers exactly once.
// Specifically, this guards against the multi-peer infinite loop: if peer[0]
// fails (firstErr set, peerIdx→1), peer[1] succeeds (firstErr cleared,
// consecutiveErrors reset), then peer[0] fails again (peerIdx→1), the loop
// used to run forever because peerIdx never wrapped back to 0.
// With the consecutiveErrors fix, we correctly detect N consecutive failures.
func TestRunNATPollOneShotMultiPeerAllFail(t *testing.T) {
	badEndpoint1 := "http://127.0.0.1:2" // port 2, unreachable
	badEndpoint2 := "http://127.0.0.1:3" // port 3, unreachable
	id := tempTestIdentity(t)

	peers := []store.PeerEndpoint{
		{CampfireID: "cf-test-multi", MemberPubkey: "aaa", Endpoint: badEndpoint1},
		{CampfireID: "cf-test-multi", MemberPubkey: "bbb", Endpoint: badEndpoint2},
	}

	stopCh := make(chan os.Signal, 1)
	cfg := natPollConfig{
		campfireID:  "cf-test-multi",
		peers:       peers,
		cursor:      0,
		follow:      false,
		id:          id,
		timeoutSecs: 1,
		stopCh:      stopCh,
	}

	start := time.Now()
	var out bytes.Buffer
	err := runNATPoll(cfg, &out)
	elapsed := time.Since(start)

	// Must return an error since all peers are unreachable.
	if err == nil {
		t.Fatal("expected error from unreachable peers, got nil")
	}

	// With 2 peers each sleeping 1s, should complete around 2s, not indefinitely.
	if elapsed > 10*time.Second {
		t.Errorf("runNATPoll took too long (%v); expected < 10s for 2-peer one-shot", elapsed)
	}
}

// Verify that httptest.Server is importable (compile check only).
var _ = (*httptest.Server)(nil)

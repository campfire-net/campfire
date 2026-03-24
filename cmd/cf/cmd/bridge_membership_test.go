package cmd

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// mockMembershipPeer is a test HTTP server that records delivered messages
// and membership events, and serves sync responses.
type mockMembershipPeer struct {
	mu     sync.Mutex
	events []cfhttp.MembershipEvent
}

func newMockMembershipPeer() *mockMembershipPeer {
	return &mockMembershipPeer{}
}

func (m *mockMembershipPeer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/campfire/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case pathEndsWith(path, "/membership") && r.Method == http.MethodPost:
			m.handleMembership(w, r)
		case pathEndsWith(path, "/sync") && r.Method == http.MethodGet:
			// Empty sync response — no messages.
			w.Header().Set("Content-Type", "application/cbor")
			w.WriteHeader(http.StatusOK)
			// Write empty CBOR array.
			w.Write([]byte{0x80}) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func (m *mockMembershipPeer) handleMembership(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	var event cfhttp.MembershipEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "decode error", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.events = append(m.events, event)
	m.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (m *mockMembershipPeer) getEvents() []cfhttp.MembershipEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]cfhttp.MembershipEvent, len(m.events))
	copy(out, m.events)
	return out
}

// setupMembershipTest creates the common test fixtures: fs transport, store, campfire dir.
// Returns (tmpDir, campfireID, fsTransport, store, agentID).
func setupMembershipTest(t *testing.T, campfireIDSuffix string) (string, string, *fs.Transport, store.Store, *identity.Identity) {
	t.Helper()
	tmpDir := t.TempDir()
	campfireID := "membership-test-" + campfireIDSuffix
	cfDir := filepath.Join(tmpDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	fsTransport := fs.New(tmpDir)
	return tmpDir, campfireID, fsTransport, s, agentID
}

// TestSyncHTTPToFS verifies that an HTTP peer known in the store appears in the
// fs members/ directory after one sync cycle.
func TestSyncHTTPToFS(t *testing.T) {
	_, campfireID, fsTransport, s, _ := setupMembershipTest(t, "http-to-fs-00000000000000")

	// Register a remote HTTP peer in the store (simulates a peer that joined via HTTP).
	remotePeer, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	remotePubHex := remotePeer.PublicKeyHex()
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: remotePubHex,
		Endpoint:     "http://remote.example.com:9000",
	}); err != nil {
		t.Fatal(err)
	}

	state := newMembershipSyncState()

	// Run HTTP→fs sync.
	syncHTTPToFS(campfireID, fsTransport, s, state)

	// Verify the member appears in the fs members/ directory.
	fsMembers, err := fsTransport.ListMembers(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fsMembers) != 1 {
		t.Fatalf("expected 1 fs member after HTTP→fs sync, got %d", len(fsMembers))
	}

	gotPubHex := hex.EncodeToString(fsMembers[0].PublicKey)
	if gotPubHex != remotePubHex {
		t.Errorf("fs member pubkey = %s, want %s", gotPubHex[:8], remotePubHex[:8])
	}
}

// TestSyncHTTPToFSIdempotent verifies that running HTTP→fs sync multiple times
// does not create duplicate member records.
func TestSyncHTTPToFSIdempotent(t *testing.T) {
	_, campfireID, fsTransport, s, _ := setupMembershipTest(t, "http-to-fs-idem-00000000000")

	remotePeer, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: remotePeer.PublicKeyHex(),
		Endpoint:     "http://remote.example.com:9000",
	}); err != nil {
		t.Fatal(err)
	}

	state := newMembershipSyncState()

	// Run sync three times.
	syncHTTPToFS(campfireID, fsTransport, s, state)
	syncHTTPToFS(campfireID, fsTransport, s, state)
	syncHTTPToFS(campfireID, fsTransport, s, state)

	// Should still be exactly 1 member record.
	fsMembers, err := fsTransport.ListMembers(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fsMembers) != 1 {
		t.Errorf("expected 1 fs member after repeated sync, got %d", len(fsMembers))
	}
}

// TestSyncFSToHTTP verifies that a bridge agent's own fs member record triggers
// a join announcement to the HTTP side.
func TestSyncFSToHTTP(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	peer := newMockMembershipPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	_, campfireID, fsTransport, _, agentID := setupMembershipTest(t, "fs-to-http-0000000000000000")

	// Write the bridge agent's own member record to fs.
	bridgePubBytes, err := hex.DecodeString(agentID.PublicKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	bridgeMember := campfire.MemberRecord{
		PublicKey: bridgePubBytes,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}
	if err := fsTransport.WriteMember(campfireID, bridgeMember); err != nil {
		t.Fatal(err)
	}

	state := newMembershipSyncState()

	// Run fs→HTTP sync.
	syncFSToHTTP(campfireID, fsTransport, agentID, srv.URL, state)

	// Verify the join event was sent to the HTTP peer.
	events := peer.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 membership event, got %d", len(events))
	}
	if events[0].Event != "join" {
		t.Errorf("expected event type 'join', got %q", events[0].Event)
	}
	if events[0].Member != agentID.PublicKeyHex() {
		t.Errorf("expected member %s, got %s", agentID.PublicKeyHex()[:8], events[0].Member[:8])
	}
}

// TestSyncFSToHTTPIdempotent verifies that the same member is not announced twice.
func TestSyncFSToHTTPIdempotent(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	peer := newMockMembershipPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	_, campfireID, fsTransport, _, agentID := setupMembershipTest(t, "fs-to-http-idem-000000000000")

	bridgePubBytes, err := hex.DecodeString(agentID.PublicKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	bridgeMember := campfire.MemberRecord{
		PublicKey: bridgePubBytes,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}
	if err := fsTransport.WriteMember(campfireID, bridgeMember); err != nil {
		t.Fatal(err)
	}

	state := newMembershipSyncState()

	// Run sync three times.
	syncFSToHTTP(campfireID, fsTransport, agentID, srv.URL, state)
	syncFSToHTTP(campfireID, fsTransport, agentID, srv.URL, state)
	syncFSToHTTP(campfireID, fsTransport, agentID, srv.URL, state)

	// Should only have sent 1 event.
	events := peer.getEvents()
	if len(events) != 1 {
		t.Errorf("expected 1 membership event (idempotent), got %d", len(events))
	}
}

// TestSyncMembershipFullCycle verifies the full syncMembership function:
// HTTP peer appears in fs, and bridge agent in fs gets announced to HTTP.
func TestSyncMembershipFullCycle(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	peer := newMockMembershipPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	_, campfireID, fsTransport, s, agentID := setupMembershipTest(t, "full-cycle-000000000000000000")

	// Register an HTTP peer in the store.
	remotePeer, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: remotePeer.PublicKeyHex(),
		Endpoint:     "http://remote.example.com:9000",
	}); err != nil {
		t.Fatal(err)
	}

	// Write the bridge agent's own member record to fs.
	bridgePubBytes, err := hex.DecodeString(agentID.PublicKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTransport.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: bridgePubBytes,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatal(err)
	}

	state := newMembershipSyncState()
	syncMembership(campfireID, fsTransport, s, agentID, srv.URL, state)

	// Verify HTTP peer appeared in fs.
	fsMembers, err := fsTransport.ListMembers(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	// Expect 2 members: the bridge agent (written above) + the HTTP peer.
	found := make(map[string]bool)
	for _, m := range fsMembers {
		found[hex.EncodeToString(m.PublicKey)] = true
	}
	if !found[remotePeer.PublicKeyHex()] {
		t.Errorf("expected HTTP peer %s in fs members after sync", remotePeer.PublicKeyHex()[:8])
	}

	// Verify bridge agent join was announced to HTTP.
	events := peer.getEvents()
	if len(events) != 1 {
		t.Errorf("expected 1 membership event for bridge agent, got %d", len(events))
	}
	if len(events) > 0 && events[0].Member != agentID.PublicKeyHex() {
		t.Errorf("expected bridge agent pubkey in event, got %s", events[0].Member[:8])
	}
}

// TestSyncMembershipExistingMembersNotReannounced verifies that members already
// present on both sides are not re-announced on subsequent cycles.
func TestSyncMembershipExistingMembersNotReannounced(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	peer := newMockMembershipPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	_, campfireID, fsTransport, s, agentID := setupMembershipTest(t, "no-reannounce-0000000000000000")

	remotePeer, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: remotePeer.PublicKeyHex(),
		Endpoint:     "http://remote.example.com:9000",
	}); err != nil {
		t.Fatal(err)
	}

	bridgePubBytes, err := hex.DecodeString(agentID.PublicKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTransport.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: bridgePubBytes,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatal(err)
	}

	state := newMembershipSyncState()

	// First cycle: both syncs run, member written to fs, bridge agent announced.
	syncMembership(campfireID, fsTransport, s, agentID, srv.URL, state)
	eventsAfterFirst := len(peer.getEvents())

	// Second cycle: nothing new should happen.
	syncMembership(campfireID, fsTransport, s, agentID, srv.URL, state)
	eventsAfterSecond := len(peer.getEvents())

	if eventsAfterSecond != eventsAfterFirst {
		t.Errorf("expected no new membership events on second cycle (got %d → %d)", eventsAfterFirst, eventsAfterSecond)
	}

	// fs member count should still be 2 (bridge + remote peer), not grow.
	fsMembers, err := fsTransport.ListMembers(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fsMembers) != 2 {
		t.Errorf("expected 2 fs members after two cycles, got %d", len(fsMembers))
	}
}

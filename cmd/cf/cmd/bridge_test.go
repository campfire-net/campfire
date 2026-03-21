package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// mockHTTPPeer is a test HTTP server that records delivered messages
// and serves messages for sync requests.
type mockHTTPPeer struct {
	mu        sync.Mutex
	delivered []message.Message
	toServe   []message.Message
}

func newMockHTTPPeer() *mockHTTPPeer {
	return &mockHTTPPeer{}
}

func (m *mockHTTPPeer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/campfire/", func(w http.ResponseWriter, r *http.Request) {
		// Route based on path suffix.
		path := r.URL.Path
		switch {
		case len(path) > len("/campfire/") && pathEndsWith(path, "/deliver"):
			m.handleDeliver(w, r)
		case len(path) > len("/campfire/") && pathEndsWith(path, "/sync"):
			m.handleSync(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func pathEndsWith(path, suffix string) bool {
	return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
}

func (m *mockHTTPPeer) handleDeliver(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	var msg message.Message
	if err := cfencoding.Unmarshal(body, &msg); err != nil {
		http.Error(w, "decode error", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.delivered = append(m.delivered, msg)
	m.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (m *mockHTTPPeer) handleSync(w http.ResponseWriter, r *http.Request) {
	sinceStr := r.URL.Query().Get("since")
	var since int64
	if sinceStr != "" {
		if v, err := strconv.ParseInt(sinceStr, 10, 64); err == nil {
			since = v
		}
	}

	m.mu.Lock()
	all := m.toServe
	m.toServe = nil
	m.mu.Unlock()

	// Filter by creation timestamp, matching handleSync server semantics.
	var msgs []message.Message
	for _, msg := range all {
		if msg.Timestamp > since {
			msgs = append(msgs, msg)
		}
	}
	if msgs == nil {
		msgs = []message.Message{}
	}

	data, err := cfencoding.Marshal(msgs)
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	w.Write(data) //nolint:errcheck
}

func (m *mockHTTPPeer) addSyncMessage(msg message.Message) {
	m.mu.Lock()
	m.toServe = append(m.toServe, msg)
	m.mu.Unlock()
}

func (m *mockHTTPPeer) getDelivered() []message.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]message.Message, len(m.delivered))
	copy(out, m.delivered)
	return out
}

// TestBridgeFSToHTTP verifies that a message written to the filesystem transport
// is picked up by the bridge and delivered to the HTTP endpoint.
func TestBridgeFSToHTTP(t *testing.T) {
	// Override HTTP client for loopback.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	// Set up mock HTTP peer.
	peer := newMockHTTPPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	// Set up filesystem transport.
	tmpDir := t.TempDir()
	campfireID := "bridge-test-fs-to-http-campfire-00000000000000000000000000000000"
	cfDir := filepath.Join(tmpDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Create store.
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Create identity.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	// Write a message to the filesystem.
	fsTransport := fs.New(tmpDir)
	msg1, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte("hello from fs"), []string{"test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTransport.WriteMessage(campfireID, msg1); err != nil {
		t.Fatal(err)
	}

	// Build forwarded set (empty — nothing in store yet).
	forwarded := buildForwardedSet(campfireID, fsTransport, s)
	if len(forwarded) != 0 {
		t.Fatalf("expected empty forwarded set, got %d", len(forwarded))
	}

	// Run one pump cycle.
	pumpFSToHTTP(campfireID, fsTransport, s, agentID, srv.URL, forwarded, nil)

	// Verify the message was delivered to the HTTP peer.
	delivered := peer.getDelivered()
	if len(delivered) != 1 {
		t.Fatalf("expected 1 delivered message, got %d", len(delivered))
	}
	if string(delivered[0].Payload) != "hello from fs" {
		t.Errorf("expected payload 'hello from fs', got %q", string(delivered[0].Payload))
	}

	// Verify the message is in the store.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message in store, got %d", len(msgs))
	}

	// Verify the message ID is in the forwarded set.
	if !forwarded[msg1.ID] {
		t.Error("expected message ID in forwarded set")
	}
}

// TestBridgeHTTPToFS verifies that a message from the HTTP endpoint is written
// to the filesystem transport.
func TestBridgeHTTPToFS(t *testing.T) {
	// Override HTTP client for loopback.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	// Create an identity for the remote sender.
	remoteID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	// Create a message from the remote agent.
	remoteMsg, err := message.NewMessage(remoteID.PrivateKey, remoteID.PublicKey, []byte("hello from http"), []string{"test"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Set up mock HTTP peer with the message to serve.
	peer := newMockHTTPPeer()
	peer.addSyncMessage(*remoteMsg)
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	// Set up filesystem transport.
	tmpDir := t.TempDir()
	campfireID := "bridge-test-http-to-fs-campfire-00000000000000000000000000000000"
	cfDir := filepath.Join(tmpDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Create store.
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Create bridge identity.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	fsTransport := fs.New(tmpDir)

	// Run one pump cycle.
	newCursor := pumpHTTPToFS(campfireID, fsTransport, s, agentID, srv.URL, 0)

	// Verify the message is in the store.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message in store, got %d", len(msgs))
	}
	if string(msgs[0].Payload) != "hello from http" {
		t.Errorf("expected payload 'hello from http', got %q", string(msgs[0].Payload))
	}

	// Verify the message was written to the filesystem.
	fsMessages, err := fsTransport.ListMessages(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fsMessages) != 1 {
		t.Fatalf("expected 1 fs message, got %d", len(fsMessages))
	}
	if string(fsMessages[0].Payload) != "hello from http" {
		t.Errorf("expected fs payload 'hello from http', got %q", string(fsMessages[0].Payload))
	}

	// Cursor should have advanced.
	if newCursor == 0 {
		t.Error("expected cursor to advance past 0")
	}
}

// TestBridgeHTTPToFSCursorSemantic verifies that pumpHTTPToFS advances the cursor
// using message.Timestamp (creation time), not the local wall clock, so that a
// subsequent sync with the returned cursor is correctly filtered by the server.
//
// Regression for: bridge advanced cursor in received_at (NowNano) space while
// handleSync filters by afterTimestamp (message.Timestamp), causing messages whose
// creation timestamp < bridge wall clock to be silently dropped after the first batch.
func TestBridgeHTTPToFSCursorSemantic(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	remoteID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	// msg1 has Timestamp=1000; msg2 has Timestamp=2000.
	// After syncing msg1, the cursor must advance to 1000 so that msg2 (Timestamp=2000)
	// is returned on the next sync. If the cursor were set to NowNano() instead,
	// and NowNano() > 2000 (which is always true for nanosecond wall-clock values),
	// msg2 would be silently dropped.
	msg1 := makeMessageWithTimestamp(t, remoteID, []byte("msg-one"), 1000)
	msg2 := makeMessageWithTimestamp(t, remoteID, []byte("msg-two"), 2000)

	peer := newMockHTTPPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	tmpDir := t.TempDir()
	campfireID := "bridge-test-cursor-semantic-campfire-0000000000000000000000000000000"
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
	defer s.Close()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	fsTransport := fs.New(tmpDir)

	// First pump: serve msg1 only.
	peer.addSyncMessage(*msg1)
	cursor := pumpHTTPToFS(campfireID, fsTransport, s, agentID, srv.URL, 0)

	// Cursor must equal msg1.Timestamp, not NowNano().
	if cursor != msg1.Timestamp {
		t.Fatalf("after first pump: cursor = %d, want %d (msg1.Timestamp)", cursor, msg1.Timestamp)
	}

	// Second pump: serve msg2 with Timestamp > cursor.
	// The mock now filters by 'since', so msg2 is returned only if Timestamp > cursor.
	peer.addSyncMessage(*msg2)
	cursor = pumpHTTPToFS(campfireID, fsTransport, s, agentID, srv.URL, cursor)

	if cursor != msg2.Timestamp {
		t.Fatalf("after second pump: cursor = %d, want %d (msg2.Timestamp)", cursor, msg2.Timestamp)
	}

	// Both messages must be in the filesystem transport.
	fsMessages, err := fsTransport.ListMessages(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fsMessages) != 2 {
		t.Fatalf("expected 2 fs messages, got %d (second message was dropped)", len(fsMessages))
	}
}

// makeMessageWithTimestamp creates a signed message with an explicit Timestamp value.
// message.NewMessage always uses time.Now(), so we patch the Timestamp field directly
// after construction; the signature covers the other fields and the test verifies
// round-trip through the store/fs, not signature correctness.
func makeMessageWithTimestamp(t *testing.T, id *identity.Identity, payload []byte, ts int64) *message.Message {
	t.Helper()
	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, payload, []string{"test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	msg.Timestamp = ts
	return msg
}

// TestBridgeDedup verifies that the same message is not delivered twice to HTTP.
func TestBridgeDedup(t *testing.T) {
	// Override HTTP client for loopback.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	peer := newMockHTTPPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	tmpDir := t.TempDir()
	campfireID := "bridge-test-dedup-campfire-00000000000000000000000000000000000"
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
	defer s.Close()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	fsTransport := fs.New(tmpDir)
	msg1, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte("dedup test"), []string{"test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTransport.WriteMessage(campfireID, msg1); err != nil {
		t.Fatal(err)
	}

	forwarded := buildForwardedSet(campfireID, fsTransport, s)

	// First pump — should deliver.
	pumpFSToHTTP(campfireID, fsTransport, s, agentID, srv.URL, forwarded, nil)
	if len(peer.getDelivered()) != 1 {
		t.Fatalf("expected 1 delivery after first pump, got %d", len(peer.getDelivered()))
	}

	// Second pump — same message should NOT be re-delivered.
	pumpFSToHTTP(campfireID, fsTransport, s, agentID, srv.URL, forwarded, nil)
	if len(peer.getDelivered()) != 1 {
		t.Fatalf("expected still 1 delivery after second pump (dedup), got %d", len(peer.getDelivered()))
	}
}

// TestBridgeForwardedSetRebuildsOnRestart verifies that the forwarded-ID set
// is correctly rebuilt from the store on restart (simulated).
func TestBridgeForwardedSetRebuildsOnRestart(t *testing.T) {
	// Override HTTP client for loopback.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	peer := newMockHTTPPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	tmpDir := t.TempDir()
	campfireID := "bridge-test-rebuild-campfire-000000000000000000000000000000000"
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
	defer s.Close()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	fsTransport := fs.New(tmpDir)
	msg1, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte("pre-restart"), []string{"test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTransport.WriteMessage(campfireID, msg1); err != nil {
		t.Fatal(err)
	}

	// First run: pump once.
	forwarded := buildForwardedSet(campfireID, fsTransport, s)
	pumpFSToHTTP(campfireID, fsTransport, s, agentID, srv.URL, forwarded, nil)
	if len(peer.getDelivered()) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(peer.getDelivered()))
	}

	// Simulate restart: rebuild forwarded set from scratch.
	forwarded2 := buildForwardedSet(campfireID, fsTransport, s)

	// The message should be in the rebuilt set (it's in the store).
	if !forwarded2[msg1.ID] {
		t.Error("expected message ID in rebuilt forwarded set")
	}

	// Pumping again should not re-deliver.
	pumpFSToHTTP(campfireID, fsTransport, s, agentID, srv.URL, forwarded2, nil)
	if len(peer.getDelivered()) != 1 {
		t.Fatalf("expected still 1 delivery after restart rebuild, got %d", len(peer.getDelivered()))
	}
}

// TestDiscoverFSCampfires verifies that discoverFSCampfires finds campfire directories.
func TestDiscoverFSCampfires(t *testing.T) {
	tmpDir := t.TempDir()

	// Create two campfire directories.
	for _, cfID := range []string{"campfire-aaa", "campfire-bbb"} {
		for _, sub := range []string{"members", "messages"} {
			if err := os.MkdirAll(filepath.Join(tmpDir, cfID, sub), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Create a non-campfire directory (no messages/ subdir).
	if err := os.MkdirAll(filepath.Join(tmpDir, "not-a-campfire"), 0o755); err != nil {
		t.Fatal(err)
	}

	ids := discoverFSCampfires(tmpDir)
	if len(ids) != 2 {
		t.Fatalf("expected 2 campfires, got %d: %v", len(ids), ids)
	}

	// Verify both are found (order may vary).
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found["campfire-aaa"] || !found["campfire-bbb"] {
		t.Errorf("expected campfire-aaa and campfire-bbb, got %v", ids)
	}
}

// TestBridgeCmdFlagValidation verifies that the bridge command rejects invalid flag combos.
func TestBridgeCmdFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		flags   map[string]string
		wantErr string
	}{
		{
			name:    "no --to flag",
			args:    []string{"some-id"},
			flags:   map[string]string{},
			wantErr: "--to is required",
		},
		{
			name:    "no campfire-id and no --all",
			args:    []string{},
			flags:   map[string]string{"to": "http://localhost:9000"},
			wantErr: "either provide a campfire-id or use --all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset flags.
			for k, v := range tt.flags {
				bridgeCmd.Flags().Set(k, v) //nolint:errcheck
			}
			// Set --all to false explicitly for non-all tests.
			if _, ok := tt.flags["all"]; !ok {
				bridgeCmd.Flags().Set("all", "false") //nolint:errcheck
			}
			if _, ok := tt.flags["to"]; !ok {
				bridgeCmd.Flags().Set("to", "") //nolint:errcheck
			}

			err := bridgeCmd.RunE(bridgeCmd, tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !containsStr(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got: %s", tt.wantErr, err.Error())
			}
		})
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Ensure json import is used (needed by mockHTTPPeer).
var _ = json.Marshal

package protocol_test

// Tests for protocol.Client.Send() — GitHub transport path.
// Uses httptest.NewServer to fake the GitHub API — no real network calls.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// fakeGitHubServer records POST /repos/{owner}/{repo}/issues/{number}/comments
// calls and returns 201 Created with a minimal JSON response.
type fakeGitHubServer struct {
	mu       sync.Mutex
	comments []string // request bodies received
}

func (f *fakeGitHubServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.comments = append(f.comments, string(body))
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":1,"body":"ok","created_at":"%s"}`, time.Now().UTC().Format(time.RFC3339))
		return
	}
	// Any GET returns 200 with an empty array (no existing comments).
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]")) //nolint:errcheck
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// setupGitHubCampfire creates a store membership record for a GitHub-transport
// campfire pointing at the given fake server URL.
// Returns the campfire ID (hex public key of a freshly generated campfire identity).
// The campfire private key is stored in the membership so sendGitHub can add
// provenance hops (campfire-agent-64l fix).
func setupGitHubCampfire(t *testing.T, s store.Store, baseURL string) string {
	t.Helper()

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("generating campfire: %v", err)
	}
	campfireID := cf.PublicKeyHex()

	meta, err := json.Marshal(map[string]interface{}{
		"repo":         "test/repo",
		"issue_number": 1,
		"base_url":     baseURL,
	})
	if err != nil {
		t.Fatalf("marshalling transport meta: %v", err)
	}
	transportDir := "github:" + string(meta)

	if err := s.AddMembership(store.Membership{
		CampfireID:      campfireID,
		TransportDir:    transportDir,
		TransportType:   "github",
		JoinProtocol:    "open",
		Role:            campfire.RoleFull,
		JoinedAt:        time.Now().UnixNano(),
		Threshold:       1,
		CampfirePrivKey: fmt.Sprintf("%x", cf.PrivateKey),
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	return campfireID
}

// TestSendGitHub verifies that Client.Send() delivers a message via the GitHub
// transport and mirrors it to the local store.
func TestSendGitHub(t *testing.T) {
	fake := &fakeGitHubServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	campfireID := setupGitHubCampfire(t, s, srv.URL)

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     []byte("hello from github transport"),
		Tags:        []string{"status"},
		GitHubToken: "fake-token",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msg == nil {
		t.Fatal("Send returned nil message")
	}

	// Message must have a valid ID.
	if msg.ID == "" {
		t.Error("message ID is empty")
	}

	// Message signature must be valid.
	if !msg.VerifySignature() {
		t.Error("message signature is invalid")
	}

	// Sender must be the agent's public key.
	if fmt.Sprintf("%x", msg.Sender) != agentID.PublicKeyHex() {
		t.Errorf("sender mismatch: got %x, want %s", msg.Sender, agentID.PublicKeyHex())
	}

	// Fake server must have received exactly one POST (the comment).
	fake.mu.Lock()
	commentCount := len(fake.comments)
	fake.mu.Unlock()
	if commentCount != 1 {
		t.Errorf("fake server received %d POST requests, want 1", commentCount)
	}

	// Message must be mirrored in the local store.
	records, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("listing stored messages: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("message not mirrored to local store")
	}
	found := false
	for _, r := range records {
		if r.ID == msg.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("sent message %s not found in local store", msg.ID)
	}
}

// TestSendGitHub_Tags verifies that tags are propagated correctly.
func TestSendGitHub_Tags(t *testing.T) {
	fake := &fakeGitHubServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	campfireID := setupGitHubCampfire(t, s, srv.URL)

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     []byte("tagged message"),
		Tags:        []string{"finding", "blocker"},
		GitHubToken: "fake-token",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(msg.Tags) != 2 || msg.Tags[0] != "finding" || msg.Tags[1] != "blocker" {
		t.Errorf("tags mismatch: got %v, want [finding blocker]", msg.Tags)
	}
}

// TestSendGitHub_Instance verifies that the Instance field is propagated.
func TestSendGitHub_Instance(t *testing.T) {
	fake := &fakeGitHubServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	campfireID := setupGitHubCampfire(t, s, srv.URL)

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     []byte("msg"),
		Instance:    "implementer",
		GitHubToken: "fake-token",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msg.Instance != "implementer" {
		t.Errorf("instance mismatch: got %q, want %q", msg.Instance, "implementer")
	}
}

// TestSendGitHub_ProvenanceHop is the regression test for campfire-agent-64l.
// Before the fix, sendGitHub skipped the AddHop call and msg.Provenance was
// empty. After the fix, every message sent via GitHub transport must carry at
// least one provenance hop signed by the campfire key that passes VerifyHop.
func TestSendGitHub_ProvenanceHop(t *testing.T) {
	fake := &fakeGitHubServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	campfireID := setupGitHubCampfire(t, s, srv.URL)

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     []byte("provenance hop regression test"),
		Tags:        []string{"status"},
		GitHubToken: "fake-token",
	})
	if err != nil {
		t.Fatalf("Send via GitHub transport: %v", err)
	}

	// Primary assertion: provenance hop must be present (campfire-agent-64l fix).
	if len(msg.Provenance) == 0 {
		t.Fatal("regression campfire-agent-64l: GitHub-transport message has no provenance hop")
	}

	// Secondary assertion: the hop signature must be valid.
	hop := msg.Provenance[0]
	if !message.VerifyHop(msg.ID, hop) {
		t.Error("provenance hop signature is invalid")
	}
}

// TestSendGitHub_MissingToken verifies that Send returns an error when no token
// is provided (neither in request nor via env).
func TestSendGitHub_MissingToken(t *testing.T) {
	fake := &fakeGitHubServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	campfireID := setupGitHubCampfire(t, s, srv.URL)

	// Ensure GITHUB_TOKEN is not set in the environment for this test.
	t.Setenv("GITHUB_TOKEN", "")

	client := protocol.New(s, agentID)
	_, err = client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("msg"),
		// GitHubToken intentionally omitted
	})
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}
}

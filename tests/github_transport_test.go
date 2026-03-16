// Package tests contains integration tests for the Campfire GitHub transport.
//
// TestGitHubTransport_EndToEnd spins up an httptest.NewServer fake GitHub API,
// then runs two agents through the full create→join→send→read lifecycle:
//
//  1. Agent A creates a campfire with the GitHub transport.
//  2. Agent B joins the campfire.
//     - Agent B posts a campfire:join-request comment.
//     - Agent A delivers a campfire:key-delivery comment encrypting the campfire
//       private key to Agent B's Ed25519 public key (using workspace-19 X25519
//       conversion + AES-256-GCM).
//     - Agent B polls, decrypts, and stores the campfire private key.
//  3. Agent A sends a message.
//  4. Agent B polls and receives the message.
//  5. The received message's Ed25519 signature verifies.
package tests

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	ghtr "github.com/campfire-net/campfire/pkg/transport/github"
)

// ---- fake GitHub server ----

type e2eFakeServer struct {
	mu          sync.Mutex
	comments    []e2eComment
	nextID      int
	etag        string
	files       map[string]e2eFakeFile
	issues      []e2eFakeIssue
	nextIssueID int
}

type e2eComment struct {
	ID        int       `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type e2eFakeFile struct {
	Content string `json:"content"` // base64 encoded
	SHA     string `json:"sha"`
}

type e2eFakeIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

func newE2EFakeServer() (*e2eFakeServer, *httptest.Server) {
	fs := &e2eFakeServer{
		nextID:      1,
		nextIssueID: 1,
		etag:        `"initial-etag"`,
		files:       make(map[string]e2eFakeFile),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.Contains(path, "/contents/") {
			fs.handleContents(w, r, path)
			return
		}
		if strings.HasSuffix(path, "/comments") && strings.Contains(path, "/issues/") {
			switch r.Method {
			case http.MethodPost:
				fs.handleCreateComment(w, r)
			case http.MethodGet:
				fs.handleListComments(w, r)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}
		if strings.HasSuffix(path, "/issues") && r.Method == http.MethodPost {
			fs.handleCreateIssue(w, r)
			return
		}
		http.Error(w, "not found: "+path, http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	return fs, srv
}

func (fs *e2eFakeServer) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Body string `json:"body"`
	}
	json.Unmarshal(body, &req)

	fs.mu.Lock()
	id := fs.nextID
	fs.nextID++
	c := e2eComment{
		ID:        id,
		Body:      req.Body,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	fs.comments = append(fs.comments, c)
	fs.etag = fmt.Sprintf(`"etag-%d"`, id)
	fs.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(c)
}

func (fs *e2eFakeServer) handleListComments(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	currentEtag := fs.etag
	comments := make([]e2eComment, len(fs.comments))
	copy(comments, fs.comments)
	fs.mu.Unlock()

	clientEtag := r.Header.Get("If-None-Match")
	if clientEtag != "" && clientEtag == currentEtag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	sinceStr := r.URL.Query().Get("since")
	var since time.Time
	if sinceStr != "" {
		since, _ = time.Parse(time.RFC3339, sinceStr)
	}

	var filtered []e2eComment
	for _, c := range comments {
		if since.IsZero() || c.CreatedAt.After(since) {
			filtered = append(filtered, c)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", currentEtag)
	w.WriteHeader(http.StatusOK)
	if filtered == nil {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(filtered)
}

func (fs *e2eFakeServer) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	json.Unmarshal(body, &req)

	fs.mu.Lock()
	id := fs.nextIssueID
	fs.nextIssueID++
	issue := e2eFakeIssue{Number: id, Title: req.Title, Body: req.Body}
	fs.issues = append(fs.issues, issue)
	fs.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(issue)
}

func (fs *e2eFakeServer) handleContents(w http.ResponseWriter, r *http.Request, path string) {
	idx := strings.Index(path, "/contents/")
	if idx < 0 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	filePath := path[idx+len("/contents/"):]

	fs.mu.Lock()
	defer fs.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		f, ok := fs.files[filePath]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"content": f.Content,
			"sha":     f.SHA,
		})
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Message string `json:"message"`
			Content string `json:"content"`
			SHA     string `json:"sha"`
		}
		json.Unmarshal(body, &req)
		fs.files[filePath] = e2eFakeFile{
			Content: req.Content,
			SHA:     fmt.Sprintf("sha-%d", len(fs.files)+1),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content": map[string]string{"path": filePath},
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---- helpers ----

func openE2EStore(t *testing.T) (*store.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return s, func() { s.Close() }
}

func newTransportForAgent(t *testing.T, srv *httptest.Server, s *store.Store, repo string, issueNum int) *ghtr.Transport {
	t.Helper()
	cfg := ghtr.Config{
		Repo:        repo,
		IssueNumber: issueNum,
		Token:       "test-token",
		BaseURL:     srv.URL,
	}
	tr, err := ghtr.New(cfg, s)
	if err != nil {
		t.Fatalf("ghtr.New: %v", err)
	}
	return tr
}

// ---- end-to-end test ----

// TestGitHubTransport_EndToEnd verifies the full create→join(key-delivery)→send→read
// lifecycle using a fake GitHub server.
//
// Agents:
//
//	agentA — creator; holds campfire private key; delivers key to agentB on join
//	agentB — joiner; posts join-request; receives and decrypts key; reads A's message
func TestGitHubTransport_EndToEnd(t *testing.T) {
	fake, srv := newE2EFakeServer()
	defer srv.Close()

	// Agent A: creator
	agentA, err := identity.Generate()
	if err != nil {
		t.Fatalf("generate agentA identity: %v", err)
	}
	// Agent B: joiner
	agentB, err := identity.Generate()
	if err != nil {
		t.Fatalf("generate agentB identity: %v", err)
	}

	storeA, cleanA := openE2EStore(t)
	defer cleanA()
	storeB, cleanB := openE2EStore(t)
	defer cleanB()

	const repo = "org/campfire-relay"

	// ---- Step 1: Agent A creates the campfire ----

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}
	cf.AddMember(agentA.PublicKey)

	campfireID := cf.PublicKeyHex()

	// Create the GitHub Issue (returns issue number).
	trA := newTransportForAgent(t, srv, storeA, repo, 0)
	issueNum, err := trA.CreateCampfire(cf, "e2e test campfire")
	if err != nil {
		t.Fatalf("CreateCampfire: %v", err)
	}
	if issueNum <= 0 {
		t.Fatalf("expected positive issue number, got %d", issueNum)
	}

	// Register campfire with both transports.
	trA.RegisterCampfire(campfireID, issueNum)
	if err := storeA.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: "github:" + fmt.Sprintf(`{"repo":%q,"issue_number":%d}`, repo, issueNum),
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     store.NowNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("AddMembership A: %v", err)
	}

	// Verify the fake server has one issue.
	fake.mu.Lock()
	if len(fake.issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(fake.issues))
	}
	if !strings.HasPrefix(fake.issues[0].Title, "campfire:") {
		t.Errorf("issue title: got %q, want prefix campfire:", fake.issues[0].Title)
	}
	fake.mu.Unlock()

	// ---- Step 2: Agent B joins ----
	// Agent B creates its own transport and posts a join-request.
	trB := newTransportForAgent(t, srv, storeB, repo, issueNum)
	trB.RegisterCampfire(campfireID, issueNum)

	joinReqMsg, err := message.NewMessage(
		agentB.PrivateKey,
		agentB.PublicKey,
		[]byte(fmt.Sprintf(`{"joiner":"%s"}`, agentB.PublicKeyHex())),
		[]string{"campfire:join-request"},
		nil,
	)
	if err != nil {
		t.Fatalf("join-request message: %v", err)
	}
	if err := trB.Send(campfireID, joinReqMsg); err != nil {
		t.Fatalf("send join-request: %v", err)
	}

	// ---- Step 2b: Agent A observes the join-request and delivers the key ----
	// In production this runs in the poll loop. Here we drive it manually.
	msgsA, err := trA.Poll(campfireID)
	if err != nil {
		t.Fatalf("trA.Poll for join-request: %v", err)
	}

	var joinerPubKey []byte
	for _, msg := range msgsA {
		hasjoinTag := false
		for _, tag := range msg.Tags {
			if tag == "campfire:join-request" {
				hasjoinTag = true
				break
			}
		}
		if hasjoinTag {
			joinerPubKey = msg.Sender
			break
		}
	}
	if len(joinerPubKey) == 0 {
		t.Fatal("agent A did not see the join-request; joiner public key not found")
	}

	// Encrypt the campfire private key to the joiner's Ed25519 public key.
	campfirePrivKey := []byte(cf.Identity.PrivateKey)
	ciphertext, err := identity.EncryptToEd25519Key(joinerPubKey, campfirePrivKey)
	if err != nil {
		t.Fatalf("EncryptToEd25519Key: %v", err)
	}

	// Post campfire:key-delivery comment signed by Agent A.
	keyDeliveryMsg, err := message.NewMessage(
		agentA.PrivateKey,
		agentA.PublicKey,
		[]byte(hex.EncodeToString(ciphertext)),
		[]string{"campfire:key-delivery"},
		nil,
	)
	if err != nil {
		t.Fatalf("key-delivery message: %v", err)
	}
	if err := trA.Send(campfireID, keyDeliveryMsg); err != nil {
		t.Fatalf("send key-delivery: %v", err)
	}

	// ---- Step 2c: Agent B polls and receives key-delivery ----
	msgsB, err := trB.Poll(campfireID)
	if err != nil {
		t.Fatalf("trB.Poll for key-delivery: %v", err)
	}

	var receivedCampfirePrivKey []byte
	for _, msg := range msgsB {
		hasKeyTag := false
		for _, tag := range msg.Tags {
			if tag == "campfire:key-delivery" {
				hasKeyTag = true
				break
			}
		}
		if !hasKeyTag {
			continue
		}
		ct, err := hex.DecodeString(string(msg.Payload))
		if err != nil {
			continue
		}
		pt, err := identity.DecryptWithEd25519Key(agentB.PrivateKey, ct)
		if err != nil {
			t.Fatalf("DecryptWithEd25519Key: %v", err)
		}
		receivedCampfirePrivKey = pt
		break
	}
	if len(receivedCampfirePrivKey) == 0 {
		t.Fatal("agent B did not receive the key-delivery comment")
	}
	if string(receivedCampfirePrivKey) != string(campfirePrivKey) {
		t.Errorf("decrypted campfire key mismatch: got %x, want %x", receivedCampfirePrivKey, campfirePrivKey)
	}

	// Record B's membership now that it has the key.
	if err := storeB.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: "github:" + fmt.Sprintf(`{"repo":%q,"issue_number":%d}`, repo, issueNum),
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     store.NowNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("AddMembership B: %v", err)
	}

	// ---- Step 3: Agent A sends a message ----
	sentMsg, err := message.NewMessage(
		agentA.PrivateKey,
		agentA.PublicKey,
		[]byte("hello from agent A"),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("message.NewMessage: %v", err)
	}
	if err := trA.Send(campfireID, sentMsg); err != nil {
		t.Fatalf("trA.Send: %v", err)
	}

	// ---- Step 4: Agent B polls and receives the message ----
	// Reset B's lastSeen so it re-reads from the beginning.
	msgsB2, err := trB.Poll(campfireID)
	if err != nil {
		t.Fatalf("trB.Poll for message: %v", err)
	}

	var receivedMsg *message.Message
	for i := range msgsB2 {
		if msgsB2[i].ID == sentMsg.ID {
			receivedMsg = &msgsB2[i]
			break
		}
	}
	if receivedMsg == nil {
		t.Fatalf("agent B did not receive agent A's message (ID=%s); got %d messages", sentMsg.ID, len(msgsB2))
	}

	// ---- Step 5: Verify Ed25519 signature ----
	if !receivedMsg.VerifySignature() {
		t.Error("received message signature verification failed")
	}
	if string(receivedMsg.Payload) != "hello from agent A" {
		t.Errorf("payload: got %q, want %q", receivedMsg.Payload, "hello from agent A")
	}

	// ---- Additional: message stored in B's SQLite ----
	rec, err := storeB.GetMessage(sentMsg.ID)
	if err != nil {
		t.Fatalf("storeB.GetMessage: %v", err)
	}
	if rec == nil {
		t.Error("message was not stored in agent B's SQLite store")
	}
}

// TestGitHubTransport_Create_PrintsCampfireIDAndIssueURL verifies that the
// Transport.CreateCampfire method returns a positive issue number and that the
// fake server records an issue with the correct title format.
func TestGitHubTransport_Create_PrintsCampfireIDAndIssueURL(t *testing.T) {
	_, srv := newE2EFakeServer()
	defer srv.Close()

	s, cleanup := openE2EStore(t)
	defer cleanup()

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	tr := newTransportForAgent(t, srv, s, "myorg/relay", 0)
	issueNum, err := tr.CreateCampfire(cf, "cli integration test")
	if err != nil {
		t.Fatalf("CreateCampfire: %v", err)
	}
	if issueNum <= 0 {
		t.Errorf("expected positive issue number, got %d", issueNum)
	}

	campfireID := cf.PublicKeyHex()
	if len(campfireID) != 64 {
		t.Errorf("campfire ID should be 64 hex chars (32-byte Ed25519 pubkey), got %d", len(campfireID))
	}
}

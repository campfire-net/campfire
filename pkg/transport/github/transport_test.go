package github

import (
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/3dl-dev/campfire/pkg/campfire"
	"github.com/3dl-dev/campfire/pkg/message"
	"github.com/3dl-dev/campfire/pkg/store"
)

// openTestStore creates a temporary SQLite store for tests.
func openTestStore(t *testing.T) (*store.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	return s, func() { s.Close() }
}

// mustNewCampfire creates a campfire and registers it in the store.
func mustNewCampfire(t *testing.T, s *store.Store) *campfire.Campfire {
	t.Helper()
	c, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}
	if err := s.AddMembership(store.Membership{
		CampfireID:   c.PublicKeyHex(),
		TransportDir: "github",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	return c
}

// mustNewMessage creates and signs a test message using the campfire's identity.
func mustNewMessage(t *testing.T, c *campfire.Campfire, payload string) *message.Message {
	t.Helper()
	msg, err := message.NewMessage(
		c.Identity.PrivateKey,
		c.Identity.PublicKey,
		[]byte(payload),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("message.NewMessage: %v", err)
	}
	return msg
}

// newTestTransport builds a Transport pointed at the given test server.
func newTestTransport(t *testing.T, srv *httptest.Server, s *store.Store) *Transport {
	t.Helper()
	cfg := Config{
		Repo:        "org/repo",
		IssueNumber: 1,
		Token:       "token",
		BaseURL:     srv.URL,
	}
	tr, err := New(cfg, s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

// --- Tests ---

// TestNew_EncryptAtRest_Fails verifies that requesting EncryptAtRest=true returns
// ErrEncryptAtRestNotSupported from New(), so callers fail fast rather than
// silently sending plaintext when they expected encryption.
func TestNew_EncryptAtRest_Fails(t *testing.T) {
	s, cleanup := openTestStore(t)
	defer cleanup()

	cfg := Config{
		Repo:          "org/repo",
		IssueNumber:   1,
		Token:         "token",
		EncryptAtRest: true,
	}
	_, err := New(cfg, s)
	if err == nil {
		t.Fatal("New with EncryptAtRest=true: expected error, got nil")
	}
	if !errors.Is(err, ErrEncryptAtRestNotSupported) {
		t.Errorf("New with EncryptAtRest=true: got %v, want ErrEncryptAtRestNotSupported", err)
	}
}

// TestNew_Defaults verifies that New() applies sensible default for PollIntervalSecs.
func TestNew_Defaults(t *testing.T) {
	s, cleanup := openTestStore(t)
	defer cleanup()

	cfg := Config{
		Repo:        "org/repo",
		IssueNumber: 1,
		Token:       "token",
	}
	tr, err := New(cfg, s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tr.cfg.PollIntervalSecs != defaultPollIntervalSecs {
		t.Errorf("PollIntervalSecs: got %d, want %d", tr.cfg.PollIntervalSecs, defaultPollIntervalSecs)
	}
}

// TestSend_PostsCommentAndPollDecodes is the main send/poll integration test.
//
// Verifies:
//   - Send posts a correctly-encoded campfire-msg-v1: comment to the fake GitHub server
//   - Poll fetches, decodes, verifies the Ed25519 signature, and returns the message
//   - The message is stored in the local SQLite store
func TestSend_PostsCommentAndPollDecodes(t *testing.T) {
	fs, srv := newFakeServer()
	_ = fs
	defer srv.Close()

	s, cleanup := openTestStore(t)
	defer cleanup()

	c := mustNewCampfire(t, s)
	msg := mustNewMessage(t, c, "integration test message")

	tr := newTestTransport(t, srv, s)
	tr.RegisterCampfire(c.PublicKeyHex(), 1)

	// Send a message.
	if err := tr.Send(c.PublicKeyHex(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Verify the fake server received a campfire-msg-v1: comment.
	fs.mu.Lock()
	if len(fs.comments) != 1 {
		t.Fatalf("fakeServer: expected 1 comment, got %d", len(fs.comments))
	}
	body := fs.comments[0].Body
	fs.mu.Unlock()

	if len(body) < len(commentPrefix) || body[:len(commentPrefix)] != commentPrefix {
		t.Errorf("Send: comment body does not start with %q: %q", commentPrefix, body)
	}

	// Poll should return the message.
	msgs, err := tr.Poll(c.PublicKeyHex())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("Poll: expected 1 message, got %d", len(msgs))
	}

	got := msgs[0]
	if got.ID != msg.ID {
		t.Errorf("Poll: message ID: got %q, want %q", got.ID, msg.ID)
	}
	if string(got.Payload) != "integration test message" {
		t.Errorf("Poll: payload: got %q", got.Payload)
	}

	// Ed25519 signature must still verify on the decoded message.
	if !got.VerifySignature() {
		t.Error("Poll: message signature verification failed")
	}

	// Message must be stored in SQLite.
	rec, err := s.GetMessage(msg.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if rec == nil {
		t.Error("Poll: message was not stored in SQLite")
	}
}

// TestPoll_304_AdvancesNoState verifies that a 304 response leaves lastSeen and
// etagCache unchanged and returns an empty slice (no error).
func TestPoll_304_AdvancesNoState(t *testing.T) {
	_, srv := newFakeServer()
	defer srv.Close()

	s, cleanup := openTestStore(t)
	defer cleanup()

	c := mustNewCampfire(t, s)
	tr := newTestTransport(t, srv, s)
	tr.RegisterCampfire(c.PublicKeyHex(), 1)

	// First poll: populates ETag from server.
	if _, err := tr.Poll(c.PublicKeyHex()); err != nil {
		t.Fatalf("Poll 1: %v", err)
	}

	tr.mu.RLock()
	etag1 := tr.etagCache[c.PublicKeyHex()]
	lastSeen1 := tr.lastSeen[c.PublicKeyHex()]
	tr.mu.RUnlock()

	// Second poll: server returns 304 (same ETag sent back).
	msgs2, err := tr.Poll(c.PublicKeyHex())
	if err != nil {
		t.Fatalf("Poll 2: %v", err)
	}
	if len(msgs2) != 0 {
		t.Errorf("Poll 2 (304): expected 0 messages, got %d", len(msgs2))
	}

	tr.mu.RLock()
	etag2 := tr.etagCache[c.PublicKeyHex()]
	lastSeen2 := tr.lastSeen[c.PublicKeyHex()]
	tr.mu.RUnlock()

	if etag2 != etag1 {
		t.Errorf("304: etagCache changed: %q → %q", etag1, etag2)
	}
	if !lastSeen2.Equal(lastSeen1) {
		t.Errorf("304: lastSeen changed: %v → %v", lastSeen1, lastSeen2)
	}
}

// TestPoll_DuplicateNotStoredTwice verifies that the same message arriving twice
// from the GitHub API is stored only once in SQLite (AddMessage INSERT OR IGNORE).
func TestPoll_DuplicateNotStoredTwice(t *testing.T) {
	fs, srv := newFakeServer()
	defer srv.Close()

	s, cleanup := openTestStore(t)
	defer cleanup()

	c := mustNewCampfire(t, s)
	msg := mustNewMessage(t, c, "dedup test")

	tr := newTestTransport(t, srv, s)
	tr.RegisterCampfire(c.PublicKeyHex(), 1)

	// Send the message so it lands in the fake server.
	if err := tr.Send(c.PublicKeyHex(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Poll 1: picks up the message.
	if _, err := tr.Poll(c.PublicKeyHex()); err != nil {
		t.Fatalf("Poll 1: %v", err)
	}

	// Reset poll state so the next poll re-fetches the same comment.
	tr.mu.Lock()
	tr.lastSeen[c.PublicKeyHex()] = time.Time{}
	tr.etagCache[c.PublicKeyHex()] = ""
	tr.mu.Unlock()

	// Rotate fake-server ETag so it returns 200 (not 304) with the same comment.
	fs.mu.Lock()
	fs.etag = `"forced-new-etag"`
	fs.mu.Unlock()

	// Poll 2: same message delivered again by server.
	if _, err := tr.Poll(c.PublicKeyHex()); err != nil {
		t.Fatalf("Poll 2: %v", err)
	}

	// The message must appear exactly once in the store.
	records, err := s.ListMessages(c.PublicKeyHex(), 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	count := 0
	for _, r := range records {
		if r.ID == msg.ID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("dedup: message stored %d time(s), expected exactly 1", count)
	}
}

// TestStartStop_LifecycleGoroutineExitsCleanly verifies that Start() launches the
// poll loop and Stop() shuts it down within a reasonable deadline.
func TestStartStop_LifecycleGoroutineExitsCleanly(t *testing.T) {
	_, srv := newFakeServer()
	defer srv.Close()

	s, cleanup := openTestStore(t)
	defer cleanup()

	cfg := Config{
		Repo:             "org/repo",
		IssueNumber:      1,
		Token:            "token",
		BaseURL:          srv.URL,
		PollIntervalSecs: 60, // long interval so goroutine just blocks on ticker
	}
	tr, err := New(cfg, s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := tr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the goroutine a moment to start.
	time.Sleep(10 * time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- tr.Stop() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Stop() hung — goroutine did not exit within 2s")
	}
}

// TestStart_DoubleStartReturnsError verifies that calling Start() twice returns an error.
func TestStart_DoubleStartReturnsError(t *testing.T) {
	_, srv := newFakeServer()
	defer srv.Close()

	s, cleanup := openTestStore(t)
	defer cleanup()

	cfg := Config{
		Repo:             "org/repo",
		IssueNumber:      1,
		Token:            "token",
		BaseURL:          srv.URL,
		PollIntervalSecs: 60,
	}
	tr, _ := New(cfg, s)
	if err := tr.Start(); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer tr.Stop() //nolint:errcheck

	if err := tr.Start(); err == nil {
		t.Error("second Start() should return an error, got nil")
	}
}

// TestCreateCampfire_CreatesGitHubIssue verifies that CreateCampfire calls the
// GitHub issues endpoint and returns a positive issue number.
func TestCreateCampfire_CreatesGitHubIssue(t *testing.T) {
	fs, srv := newFakeServer()
	defer srv.Close()

	s, cleanup := openTestStore(t)
	defer cleanup()

	c, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	cfg := Config{
		Repo:        "org/repo",
		IssueNumber: 0,
		Token:       "token",
		BaseURL:     srv.URL,
	}
	tr, err := New(cfg, s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	issueNum, err := tr.CreateCampfire(c, "test campfire description")
	if err != nil {
		t.Fatalf("CreateCampfire: %v", err)
	}
	if issueNum <= 0 {
		t.Errorf("CreateCampfire: expected positive issue number, got %d", issueNum)
	}

	// The fake server should have one issue with the correct title format.
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.issues) != 1 {
		t.Fatalf("fakeServer: expected 1 issue, got %d", len(fs.issues))
	}
	title := fs.issues[0].Title
	expectedPrefix := "campfire:"
	if len(title) < len(expectedPrefix) || title[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("CreateCampfire: issue title %q does not start with %q", title, expectedPrefix)
	}
}

// TestRegisterCampfire_InitialisesStateMaps verifies that RegisterCampfire
// initialises all internal maps so that Poll and the poll loop work correctly.
func TestRegisterCampfire_InitialisesStateMaps(t *testing.T) {
	_, srv := newFakeServer()
	defer srv.Close()

	s, cleanup := openTestStore(t)
	defer cleanup()

	tr := newTestTransport(t, srv, s)

	const campfireID = "deadbeef01234567"
	tr.RegisterCampfire(campfireID, 42)

	tr.mu.RLock()
	_, hasLastSeen := tr.lastSeen[campfireID]
	_, hasEtag := tr.etagCache[campfireID]
	issueNum, hasIssue := tr.issueNumbers[campfireID]
	tr.mu.RUnlock()

	if !hasLastSeen {
		t.Error("RegisterCampfire: lastSeen entry missing")
	}
	if !hasEtag {
		t.Error("RegisterCampfire: etagCache entry missing")
	}
	if !hasIssue {
		t.Error("RegisterCampfire: issueNumbers entry missing")
	}
	if issueNum != 42 {
		t.Errorf("RegisterCampfire: issueNumbers[campfireID] = %d, want 42", issueNum)
	}
}

// TestPoll_InvalidSignatureDropped verifies that messages failing Ed25519 verification
// are silently dropped and not stored.
func TestPoll_InvalidSignatureDropped(t *testing.T) {
	fs, srv := newFakeServer()
	defer srv.Close()

	s, cleanup := openTestStore(t)
	defer cleanup()

	c := mustNewCampfire(t, s)
	msg := mustNewMessage(t, c, "original payload")

	// Tamper: change payload after signing — signature will be invalid.
	msg.Payload = []byte("tampered payload — signature now invalid")

	encoded, err := EncodeComment(msg)
	if err != nil {
		t.Fatalf("EncodeComment: %v", err)
	}

	// Inject tampered comment directly into the fake server.
	fs.mu.Lock()
	id := fs.nextID
	fs.nextID++
	fs.comments = append(fs.comments, fakeComment{
		ID:        id,
		Body:      encoded,
		CreatedAt: time.Now().UTC(),
	})
	fs.etag = `"etag-tampered"`
	fs.mu.Unlock()

	tr := newTestTransport(t, srv, s)
	tr.RegisterCampfire(c.PublicKeyHex(), 1)

	msgs, err := tr.Poll(c.PublicKeyHex())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("Poll: tampered message should be dropped, got %d messages", len(msgs))
	}

	// Must not be in the store.
	rec, _ := s.GetMessage(msg.ID)
	if rec != nil {
		t.Error("tampered message was stored — should have been dropped")
	}
}

// TestPoll_NonCampfireCommentIgnored verifies human comments (no campfire-msg-v1: prefix)
// are silently skipped without error.
func TestPoll_NonCampfireCommentIgnored(t *testing.T) {
	fs, srv := newFakeServer()
	defer srv.Close()

	s, cleanup := openTestStore(t)
	defer cleanup()

	// Inject a human comment into the fake server.
	fs.mu.Lock()
	fs.comments = append(fs.comments, fakeComment{
		ID:        fs.nextID,
		Body:      "This is a human comment about the project, not a campfire message.",
		CreatedAt: time.Now().UTC(),
	})
	fs.nextID++
	fs.etag = `"etag-human"`
	fs.mu.Unlock()

	c := mustNewCampfire(t, s)
	tr := newTestTransport(t, srv, s)
	tr.RegisterCampfire(c.PublicKeyHex(), 1)

	msgs, err := tr.Poll(c.PublicKeyHex())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("Poll: expected 0 messages (human comment skipped), got %d", len(msgs))
	}
}

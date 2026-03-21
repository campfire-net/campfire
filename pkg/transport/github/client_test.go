package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is a test helper that simulates the GitHub REST API.
type fakeServer struct {
	mu       sync.Mutex
	comments []fakeComment
	nextID   int
	etag     string
	files    map[string]fakeFile // path -> file
	issues   []fakeIssue
	nextIssueID int
}

type fakeComment struct {
	ID        int       `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type fakeIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

type fakeFile struct {
	Content string `json:"content"` // base64 encoded
	SHA     string `json:"sha"`
}

func newFakeServer() (*fakeServer, *httptest.Server) {
	fs := &fakeServer{
		nextID:      1,
		nextIssueID: 1,
		etag:        `"initial-etag"`,
		files:       make(map[string]fakeFile),
	}
	mux := http.NewServeMux()

	// POST /repos/{owner}/{repo}/issues/{number}/comments
	// GET  /repos/{owner}/{repo}/issues/{number}/comments
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Dispatch on method + path pattern
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
		// POST /repos/{owner}/{repo}/issues
		if strings.HasSuffix(path, "/issues") {
			if r.Method == http.MethodPost {
				fs.handleCreateIssue(w, r)
				return
			}
		}
		http.Error(w, "not found: "+path, http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	return fs, srv
}

func (fs *fakeServer) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Body string `json:"body"`
	}
	json.Unmarshal(body, &req)

	fs.mu.Lock()
	id := fs.nextID
	fs.nextID++
	// Truncate to second so RFC3339 round-trip is lossless.
	c := fakeComment{
		ID:        id,
		Body:      req.Body,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	fs.comments = append(fs.comments, c)
	// Rotate etag so next GET sees new content
	fs.etag = fmt.Sprintf(`"etag-%d"`, id)
	fs.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(c)
}

func (fs *fakeServer) handleListComments(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	currentEtag := fs.etag
	comments := make([]fakeComment, len(fs.comments))
	copy(comments, fs.comments)
	fs.mu.Unlock()

	// Conditional GET: If-None-Match
	clientEtag := r.Header.Get("If-None-Match")
	if clientEtag != "" && clientEtag == currentEtag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Filter by since
	sinceStr := r.URL.Query().Get("since")
	var since time.Time
	if sinceStr != "" {
		since, _ = time.Parse(time.RFC3339, sinceStr)
	}

	var filtered []fakeComment
	for _, c := range comments {
		if since.IsZero() || !c.CreatedAt.Before(since) {
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

func (fs *fakeServer) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	json.Unmarshal(body, &req)

	fs.mu.Lock()
	id := fs.nextIssueID
	fs.nextIssueID++
	issue := fakeIssue{Number: id, Title: req.Title, Body: req.Body}
	fs.issues = append(fs.issues, issue)
	fs.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(issue)
}

func (fs *fakeServer) handleContents(w http.ResponseWriter, r *http.Request, path string) {
	// Extract file path from URL: /repos/{owner}/{repo}/contents/{file-path}
	// Find the /contents/ portion
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
			Content string `json:"content"` // base64
			SHA     string `json:"sha"`     // required for update
		}
		json.Unmarshal(body, &req)
		fs.files[filePath] = fakeFile{
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

// assertAuthHeader checks that the request included the expected Bearer token.
func assertAuthHeader(t *testing.T, r *http.Request, expectedToken string) {
	t.Helper()
	auth := r.Header.Get("Authorization")
	expected := "Bearer " + expectedToken
	if auth != expected {
		t.Errorf("Authorization header: got %q, want %q", auth, expected)
	}
}

// --- Tests ---

func TestCreateComment_AuthorizationHeader(t *testing.T) {
	const token = "ghp_test_token_abc123"
	var capturedAuth string

	fs, srv := newFakeServer()
	_ = fs
	// Wrap with an auth-capturing handler
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		srv.Config.Handler.ServeHTTP(w, r)
	}))
	defer authSrv.Close()
	defer srv.Close()

	client := newGithubClient(authSrv.URL, token)
	err := client.CreateComment("org/repo", 1, "hello world")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if capturedAuth != "Bearer "+token {
		t.Errorf("Authorization header: got %q, want %q", capturedAuth, "Bearer "+token)
	}
}

func TestListComments_SinceParameter(t *testing.T) {
	// Verify that the since parameter is sent as RFC3339 and that the server-side
	// filtering honours it. We use a custom handler that captures the since query param.
	var capturedSince string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSince = r.URL.Query().Get("since")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"etag-1"`)
		w.WriteHeader(http.StatusOK)
		// Return one comment whose created_at is clearly after the since time
		w.Write([]byte(`[{"id":1,"body":"after","created_at":"2030-01-01T12:00:02Z"}]`))
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := newGithubClient(srv.URL, "token")

	since := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	comments, _, err := client.ListComments("org/repo", 1, since, "")
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if capturedSince == "" {
		t.Error("since parameter was not sent in request")
	}
	// Verify it's a valid RFC3339 timestamp
	parsed, err := time.Parse(time.RFC3339, capturedSince)
	if err != nil {
		t.Errorf("since parameter %q is not valid RFC3339: %v", capturedSince, err)
	}
	if !parsed.Equal(since) {
		t.Errorf("since parameter: got %v, want %v", parsed, since)
	}
	if len(comments) != 1 || comments[0].Body != "after" {
		t.Errorf("expected 1 comment 'after', got %v", comments)
	}
}

func TestListComments_ETagHandling(t *testing.T) {
	fs, srv := newFakeServer()
	_ = fs
	defer srv.Close()

	client := newGithubClient(srv.URL, "token")

	// First GET: no ETag — should return 200 with ETag
	comments, etag, err := client.ListComments("org/repo", 1, time.Time{}, "")
	if err != nil {
		t.Fatalf("first ListComments: %v", err)
	}
	if etag == "" {
		t.Error("expected ETag from first response, got empty string")
	}
	_ = comments

	// Second GET: use returned ETag — server has same state, expect 304 → empty list, same etag
	comments2, etag2, err := client.ListComments("org/repo", 1, time.Time{}, etag)
	if err != nil {
		t.Fatalf("conditional ListComments: %v", err)
	}
	if len(comments2) != 0 {
		t.Errorf("expected empty list on 304, got %d comments", len(comments2))
	}
	// ETag should be unchanged (or still valid)
	if etag2 == "" {
		t.Error("expected non-empty etag after 304")
	}
}

func TestListComments_304_ReturnsEmptyList(t *testing.T) {
	// Dedicated test: 304 must return empty list (not nil, not error)
	_, srv := newFakeServer()
	defer srv.Close()

	client := newGithubClient(srv.URL, "token")

	// Get initial ETag
	_, etag, err := client.ListComments("org/repo", 1, time.Time{}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Now send same ETag back — should get 304
	comments, returnedEtag, err := client.ListComments("org/repo", 1, time.Time{}, etag)
	if err != nil {
		t.Fatalf("expected no error on 304, got: %v", err)
	}
	if comments == nil {
		// nil slice is acceptable as "empty", but len must be 0
	}
	if len(comments) != 0 {
		t.Errorf("304 response: expected 0 comments, got %d", len(comments))
	}
	if returnedEtag == "" {
		t.Error("304 response: expected etag to be returned")
	}
}

func TestWriteThrottle(t *testing.T) {
	_, srv := newFakeServer()
	defer srv.Close()

	client := newGithubClient(srv.URL, "token")

	start := time.Now()

	// Two rapid CreateComment calls to the same issue
	if err := client.CreateComment("org/repo", 42, "msg1"); err != nil {
		t.Fatal(err)
	}
	if err := client.CreateComment("org/repo", 42, "msg2"); err != nil {
		t.Fatal(err)
	}

	elapsed := time.Since(start)

	// Second call must have been delayed >= 750ms from first
	if elapsed < 750*time.Millisecond {
		t.Errorf("write throttle: two CreateComment calls completed in %v, expected >= 750ms", elapsed)
	}
}

func TestWriteThrottle_DifferentIssues_NotThrottled(t *testing.T) {
	_, srv := newFakeServer()
	defer srv.Close()

	client := newGithubClient(srv.URL, "token")

	start := time.Now()

	// Two calls to DIFFERENT issues — no throttling expected
	if err := client.CreateComment("org/repo", 1, "msg1"); err != nil {
		t.Fatal(err)
	}
	if err := client.CreateComment("org/repo", 2, "msg2"); err != nil {
		t.Fatal(err)
	}

	elapsed := time.Since(start)

	// Should complete well under 750ms (no throttle between different issues)
	if elapsed >= 700*time.Millisecond {
		t.Errorf("no throttle expected for different issues, but elapsed %v", elapsed)
	}
}

func TestCreateIssue(t *testing.T) {
	fs, srv := newFakeServer()
	defer srv.Close()

	client := newGithubClient(srv.URL, "token")

	issueNumber, err := client.CreateIssue("org/repo", "campfire:abc123", "beacon body")
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issueNumber <= 0 {
		t.Errorf("expected positive issue number, got %d", issueNumber)
	}

	fs.mu.Lock()
	if len(fs.issues) != 1 {
		t.Errorf("expected 1 issue, got %d", len(fs.issues))
	}
	if fs.issues[0].Title != "campfire:abc123" {
		t.Errorf("issue title: got %q", fs.issues[0].Title)
	}
	fs.mu.Unlock()
}

func TestGetFile_NotFound(t *testing.T) {
	_, srv := newFakeServer()
	defer srv.Close()

	client := newGithubClient(srv.URL, "token")

	_, err := client.GetFile("org/repo", ".campfire/beacons/missing.json")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestPutFile_ThenGetFile(t *testing.T) {
	_, srv := newFakeServer()
	defer srv.Close()

	client := newGithubClient(srv.URL, "token")

	content := []byte(`{"campfire_id":"abc"}`)
	err := client.PutFile("org/repo", ".campfire/beacons/abc.json", "add beacon", content)
	if err != nil {
		t.Fatalf("PutFile: %v", err)
	}

	got, err := client.GetFile("org/repo", ".campfire/beacons/abc.json")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("GetFile: got %q, want %q", got, content)
	}
}

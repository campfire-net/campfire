// cmd/cf-ui/e2e_test.go — End-to-end integration tests for the cf-ui operator flow.
//
// Tests the full operator journey using Go's net/http client with a cookie jar
// against a real httptest.Server. No mocks, no browser automation.
//
// Flow under test:
//   authenticate → GET / (campfire list) → GET /c/{id} (detail + messages) →
//   POST /c/{id}/send (send message) → GET /events (SSE connection)
//
// All store interfaces are satisfied by in-memory test implementations that
// provide realistic data without hitting SQLite.
package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// --- In-memory test store implementations ---

// e2eCampfireLister implements CampfireLister with static test data.
type e2eCampfireLister struct {
	campfires []CampfireEntry
}

func (l *e2eCampfireLister) ListCampfires() ([]CampfireEntry, error) {
	return l.campfires, nil
}

// e2eMessageStore implements MessageStore with static test data.
type e2eMessageStore struct {
	mu       sync.Mutex
	messages map[string][]store.MessageRecord // campfireID → records
}

func newE2EMessageStore() *e2eMessageStore {
	return &e2eMessageStore{messages: make(map[string][]store.MessageRecord)}
}

func (s *e2eMessageStore) add(campfireID string, records ...store.MessageRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages[campfireID] = append(s.messages[campfireID], records...)
}

func (s *e2eMessageStore) ListMessages(campfireID string, afterTimestamp int64, filters ...store.MessageFilter) ([]store.MessageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.messages[campfireID]

	var f store.MessageFilter
	if len(filters) > 0 {
		f = filters[0]
	}

	var out []store.MessageRecord
	for _, r := range all {
		if afterTimestamp > 0 && r.Timestamp <= afterTimestamp {
			continue
		}
		// Tag filter: if tags specified, record must have at least one matching tag.
		if len(f.Tags) > 0 && !hasAnyTag(r.Tags, f.Tags) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func hasAnyTag(haystack, needles []string) bool {
	for _, n := range needles {
		for _, h := range haystack {
			if h == n {
				return true
			}
		}
	}
	return false
}

// e2eMessageSender implements MessageSender and records sent messages.
type e2eMessageSender struct {
	mu   sync.Mutex
	sent []fakeMsg
}

func (s *e2eMessageSender) Send(campfireID, senderEmail, text string, tags []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, fakeMsg{
		campfireID: campfireID,
		sender:     senderEmail,
		text:       text,
		tags:       tags,
	})
	return "e2e-msg-" + campfireID + "-" + text[:min(len(text), 8)], nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- Test server builder for E2E tests ---

// e2eBundle holds all components of a wired E2E test server.
type e2eBundle struct {
	srv      *httptest.Server
	bundle   muxBundle
	lister   *e2eCampfireLister
	msgStore *e2eMessageStore
	sender   *e2eMessageSender
}

// newE2EServer builds a fully wired cf-ui httptest.Server with test data injected
// at every interface: CampfireLister, MessageStore, and MessageSender.
// The server uses the same middleware stack as production (session, CSRF, SSE, latency).
func newE2EServer(t *testing.T) *e2eBundle {
	t.Helper()

	logger := newDiscardLogger()
	sessions := NewMemSessionStore()
	authCfg := newAuthConfig(logger, func(string) string { return "" }, "http://localhost", sessions, noopAuthProvider{})

	csrf, err := newCSRFStore()
	if err != nil {
		t.Fatalf("newCSRFStore: %v", err)
	}

	reg := NewMetricsRegistry()
	hub := NewSSEHub(sessions, logger).WithMetrics(reg)

	// Wire the test store implementations.
	lister := &e2eCampfireLister{}
	msgStore := newE2EMessageStore()
	sender := &e2eMessageSender{}

	detail := NewCampfireDetailHandlers(logger, msgStore).WithCSRF(csrf)

	sessionMW := SessionMiddleware(sessions)
	csrfMW := CSRFMiddleware(csrf)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))
	mux.HandleFunc("GET /metrics", handleMetrics(reg))
	registerAuthRoutes(mux, authCfg, csrfMW)

	mux.Handle("GET /", sessionMW(handleIndexWithStore(logger, lister)))
	mux.Handle("GET /c/{id}", sessionMW(csrfMW(http.HandlerFunc(detail.HandleDetail))))
	mux.Handle("GET /c/{id}/messages", sessionMW(http.HandlerFunc(detail.HandleMessages)))
	mux.Handle("POST /c/{id}/send", sessionMW(csrfMW(handleSend(logger, sender, hub))))
	mux.Handle("GET /events", sessionMW(handleEventsHandler(hub)))

	handler := LatencyMiddleware(reg)(mux)

	b := muxBundle{
		handler:   handler,
		authCfg:   authCfg,
		csrfStore: csrf,
		hub:       hub,
		metrics:   reg,
		sender:    sender,
	}

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return &e2eBundle{
		srv:      srv,
		bundle:   b,
		lister:   lister,
		msgStore: msgStore,
		sender:   sender,
	}
}

// authenticatedE2EClient returns an http.Client with a session cookie pre-seeded.
func authenticatedE2EClient(t *testing.T, eb *e2eBundle, email, displayName string) *http.Client {
	t.Helper()
	sessionToken := "e2e-session-" + t.Name()
	eb.bundle.authCfg.Sessions.Store(sessionToken, Identity{
		Email:       email,
		DisplayName: displayName,
		Provider:    "magic",
	}, time.Hour)
	jar := newCookieJar()
	u, _ := url.Parse(eb.srv.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: sessionCookieName, Value: sessionToken}})
	return &http.Client{Jar: jar}
}

// csrfTokenFor generates a CSRF token for the given session cookie value.
func csrfTokenFor(eb *e2eBundle, sessionToken string) string {
	return eb.bundle.csrfStore.tokenFor(sessionToken)
}

// sessionTokenForClient extracts the session token value stored by the client jar.
func sessionTokenForClient(t *testing.T, eb *e2eBundle, _ *http.Client, email string) string {
	t.Helper()
	// The session token is "e2e-session-" + t.Name() (see authenticatedE2EClient).
	return "e2e-session-" + t.Name()
}

// readBody reads the full response body and closes it.
func readBody(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// --- E2E Test Scenarios ---

// TestE2EAuthRedirectsUnauthenticated verifies that unauthenticated requests
// to protected routes receive 401 (not a redirect, per SessionMiddleware behaviour).
func TestE2EAuthRedirectsUnauthenticated(t *testing.T) {
	eb := newE2EServer(t)

	// Use a client that does NOT follow redirects and has no session cookie.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	protectedRoutes := []string{"/", "/c/someid", "/c/someid/messages", "/events"}
	for _, route := range protectedRoutes {
		t.Run(route, func(t *testing.T) {
			resp, err := client.Get(eb.srv.URL + route)
			if err != nil {
				t.Fatalf("GET %s: %v", route, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("expected 401 for unauthenticated %s, got %d", route, resp.StatusCode)
			}
		})
	}
}

// TestE2ECampfireListShowsCampfires verifies the operator sees their campfire list
// after authentication.
func TestE2ECampfireListShowsCampfires(t *testing.T) {
	eb := newE2EServer(t)

	// Seed campfires in the in-memory lister.
	eb.lister.campfires = []CampfireEntry{
		{
			ID:                "aabbcc001122",
			DisplayName:       "Engineering Campfire",
			MemberCount:       3,
			LastActivityAt:    time.Now().Add(-5 * time.Minute).UnixNano(),
			UnreadCount:       2,
			HasRecentActivity: true,
		},
		{
			ID:                "ddeeff334455",
			DisplayName:       "Ops Campfire",
			MemberCount:       2,
			LastActivityAt:    time.Now().Add(-2 * time.Hour).UnixNano(),
			UnreadCount:       0,
			HasRecentActivity: true,
		},
	}

	client := authenticatedE2EClient(t, eb, "operator@example.com", "Operator")

	resp, err := client.Get(eb.srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body := readBody(t, resp.Body)

	if !strings.Contains(body, "Engineering Campfire") {
		t.Error("campfire list should contain 'Engineering Campfire'")
	}
	if !strings.Contains(body, "Ops Campfire") {
		t.Error("campfire list should contain 'Ops Campfire'")
	}
	// The page should link to each campfire's detail page.
	if !strings.Contains(body, "/c/aabbcc001122") {
		t.Error("campfire list should link to /c/aabbcc001122")
	}
	if !strings.Contains(body, "/c/ddeeff334455") {
		t.Error("campfire list should link to /c/ddeeff334455")
	}
}

// TestE2ECampfireDetailShowsMessages verifies the operator can open a campfire
// and see its messages.
func TestE2ECampfireDetailShowsMessages(t *testing.T) {
	eb := newE2EServer(t)

	campfireID := "testfire001"
	now := time.Now()

	eb.msgStore.add(campfireID,
		store.MessageRecord{
			ID:        "msg-001",
			Sender:    "abc123sender",
			Payload:   []byte("Hello from the team!"),
			Tags:      []string{"status"},
			Timestamp: now.Add(-10 * time.Minute).UnixNano(),
			Instance:  "orchestrator",
		},
		store.MessageRecord{
			ID:        "msg-002",
			Sender:    "def456sender",
			Payload:   []byte("Build is green."),
			Tags:      []string{"finding"},
			Timestamp: now.Add(-5 * time.Minute).UnixNano(),
		},
	)

	client := authenticatedE2EClient(t, eb, "operator@example.com", "Operator")

	resp, err := client.Get(eb.srv.URL + "/c/" + campfireID)
	if err != nil {
		t.Fatalf("GET /c/%s: %v", campfireID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body := readBody(t, resp.Body)

	if !strings.Contains(body, "Hello from the team!") {
		t.Error("detail page should contain first message body")
	}
	if !strings.Contains(body, "Build is green.") {
		t.Error("detail page should contain second message body")
	}
	// Tags should be visible in the rendered HTML.
	if !strings.Contains(body, "status") {
		t.Error("detail page should render 'status' tag")
	}
	if !strings.Contains(body, "finding") {
		t.Error("detail page should render 'finding' tag")
	}
	// Compose box should be present for sending messages.
	if !strings.Contains(body, `name="message"`) {
		t.Error("detail page should include message compose textarea")
	}
	if !strings.Contains(body, `name="_csrf"`) {
		t.Error("detail page should include hidden CSRF field")
	}
}

// TestE2ESendMessage verifies the full send flow:
// authenticated POST with valid CSRF → 200 + HTML fragment containing the message.
func TestE2ESendMessage(t *testing.T) {
	eb := newE2EServer(t)
	campfireID := "sendfire001"

	client := authenticatedE2EClient(t, eb, "sender@example.com", "Sender")
	sessionToken := sessionTokenForClient(t, eb, client, "sender@example.com")
	csrf := csrfTokenFor(eb, sessionToken)

	form := url.Values{}
	form.Set("message", "This is a test message from E2E")
	form.Set("tag", "status")
	form.Set("_csrf", csrf)

	req, err := http.NewRequest(http.MethodPost,
		eb.srv.URL+"/c/"+campfireID+"/send",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Cookie jar carries the session cookie.
	u, _ := url.Parse(eb.srv.URL)
	for _, c := range client.Jar.Cookies(u) {
		req.AddCookie(c)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /c/%s/send: %v", campfireID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := readBody(t, resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	body := readBody(t, resp.Body)

	// The response should be an HTML fragment with the sent message.
	if !strings.Contains(body, "message-card") {
		t.Errorf("response should contain message-card HTML class, got: %s", body)
	}
	if !strings.Contains(body, "This is a test message from E2E") {
		t.Errorf("response should echo the sent message text, got: %s", body)
	}

	// Verify the sender was called with the right arguments.
	eb.sender.mu.Lock()
	defer eb.sender.mu.Unlock()
	if len(eb.sender.sent) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(eb.sender.sent))
	}
	sent := eb.sender.sent[0]
	if sent.campfireID != campfireID {
		t.Errorf("campfireID: got %q, want %q", sent.campfireID, campfireID)
	}
	if sent.text != "This is a test message from E2E" {
		t.Errorf("text: got %q, want %q", sent.text, "This is a test message from E2E")
	}
	if len(sent.tags) != 1 || sent.tags[0] != "status" {
		t.Errorf("tags: got %v, want [status]", sent.tags)
	}
}

// TestE2ECSRFRejected verifies that POST without a valid CSRF token is rejected with 403.
func TestE2ECSRFRejected(t *testing.T) {
	eb := newE2EServer(t)
	campfireID := "csrffire001"

	client := authenticatedE2EClient(t, eb, "user@example.com", "User")

	// Scenarios: missing token, invalid token.
	cases := []struct {
		name  string
		csrf  string
		wantStatus int
	}{
		{"missing_csrf", "", http.StatusForbidden},
		{"invalid_csrf", "not-a-valid-token", http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{}
			form.Set("message", "should not be sent")
			if tc.csrf != "" {
				form.Set("_csrf", tc.csrf)
			}

			req, err := http.NewRequest(http.MethodPost,
				eb.srv.URL+"/c/"+campfireID+"/send",
				strings.NewReader(form.Encode()))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			u, _ := url.Parse(eb.srv.URL)
			for _, c := range client.Jar.Cookies(u) {
				req.AddCookie(c)
			}

			// Don't follow redirects.
			noRedirClient := &http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
				Jar: client.Jar,
			}
			resp, err := noRedirClient.Do(req)
			if err != nil {
				t.Fatalf("POST /c/%s/send: %v", campfireID, err)
			}
			resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("expected %d for %s, got %d", tc.wantStatus, tc.name, resp.StatusCode)
			}
		})
	}

	// Verify no messages were sent to the store.
	eb.sender.mu.Lock()
	defer eb.sender.mu.Unlock()
	if len(eb.sender.sent) != 0 {
		t.Errorf("expected no send calls when CSRF fails, got %d", len(eb.sender.sent))
	}
}

// TestE2ESSEConnectionEstablished verifies that an authenticated client can open
// the SSE endpoint and receive the initial system event.
func TestE2ESSEConnectionEstablished(t *testing.T) {
	eb := newE2EServer(t)

	// Use short keepalive for the test so we don't wait 30s.
	eb.bundle.hub.keepaliveInterval = 500 * time.Millisecond
	eb.bundle.hub.sessionRecheckInterval = 10 * time.Second

	sessionToken := "e2e-sse-session-" + t.Name()
	eb.bundle.authCfg.Sessions.Store(sessionToken, Identity{
		Email:       "sse-user@example.com",
		DisplayName: "SSE User",
		Provider:    "magic",
	}, time.Hour)

	// Open SSE connection with a context we control.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, eb.srv.URL+"/events", nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// If context timed out after receiving data, that's fine.
		if ctx.Err() != nil {
			return
		}
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for SSE connection, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("expected text/event-stream content-type, got %q", ct)
	}

	// Read the first SSE event — should be the system "connected" event.
	scanner := bufio.NewScanner(resp.Body)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		// An empty line signals end of one SSE event.
		if line == "" && len(lines) >= 2 {
			break
		}
	}

	// We should have received at least an event type and data line.
	rawEvent := strings.Join(lines, "\n")
	if !strings.Contains(rawEvent, "event: system") {
		t.Errorf("expected system event as first SSE message, got:\n%s", rawEvent)
	}
	if !strings.Contains(rawEvent, "connected") {
		t.Errorf("expected 'connected' status in first SSE event, got:\n%s", rawEvent)
	}
}

// TestE2EFullOperatorFlow exercises the complete happy path:
// login → list campfires → open campfire → read messages → send message.
// This is the canonical integration scenario the done-condition requires.
func TestE2EFullOperatorFlow(t *testing.T) {
	eb := newE2EServer(t)

	const (
		operatorEmail = "alice@example.com"
		campfireID    = "fullflow001"
	)

	// Seed data: one campfire in the list, two messages in the campfire.
	eb.lister.campfires = []CampfireEntry{
		{
			ID:                campfireID,
			DisplayName:       "Full Flow Campfire",
			MemberCount:       1,
			LastActivityAt:    time.Now().Add(-1 * time.Minute).UnixNano(),
			HasRecentActivity: true,
		},
	}
	eb.msgStore.add(campfireID,
		store.MessageRecord{
			ID:        "ff-msg-001",
			Sender:    "aabbccddeeff",
			Payload:   []byte("First message in the campfire"),
			Tags:      []string{"finding"},
			Timestamp: time.Now().Add(-3 * time.Minute).UnixNano(),
		},
		store.MessageRecord{
			ID:        "ff-msg-002",
			Sender:    "112233445566",
			Payload:   []byte("Second message — a decision"),
			Tags:      []string{"decision"},
			Timestamp: time.Now().Add(-1 * time.Minute).UnixNano(),
		},
	)

	// Step 1: Authenticate (simulate magic-link login by seeding session directly).
	client := authenticatedE2EClient(t, eb, operatorEmail, "Alice")
	sessionToken := sessionTokenForClient(t, eb, client, operatorEmail)

	// Step 2: GET / — campfire list page.
	listResp, err := client.Get(eb.srv.URL + "/")
	if err != nil {
		t.Fatalf("step 2 GET /: %v", err)
	}
	listBody := readBody(t, listResp.Body)
	listResp.Body.Close()

	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("step 2: expected 200, got %d", listResp.StatusCode)
	}
	if !strings.Contains(listBody, "Full Flow Campfire") {
		t.Error("step 2: campfire list should show 'Full Flow Campfire'")
	}

	// Step 3: GET /c/{id} — campfire detail page with messages.
	detailResp, err := client.Get(eb.srv.URL + "/c/" + campfireID)
	if err != nil {
		t.Fatalf("step 3 GET /c/%s: %v", campfireID, err)
	}
	detailBody := readBody(t, detailResp.Body)
	detailResp.Body.Close()

	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("step 3: expected 200, got %d", detailResp.StatusCode)
	}
	if !strings.Contains(detailBody, "First message in the campfire") {
		t.Error("step 3: detail page should show first message")
	}
	if !strings.Contains(detailBody, "Second message — a decision") {
		t.Error("step 3: detail page should show second message")
	}

	// Step 4: POST /c/{id}/send — send a new message.
	csrf := csrfTokenFor(eb, sessionToken)

	form := url.Values{}
	form.Set("message", "Reply from the operator")
	form.Set("tag", "decision")
	form.Set("_csrf", csrf)

	req, err := http.NewRequest(http.MethodPost,
		eb.srv.URL+"/c/"+campfireID+"/send",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("step 4 build POST: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	u, _ := url.Parse(eb.srv.URL)
	for _, c := range client.Jar.Cookies(u) {
		req.AddCookie(c)
	}

	sendResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("step 4 POST /c/%s/send: %v", campfireID, err)
	}
	sendBody := readBody(t, sendResp.Body)
	sendResp.Body.Close()

	if sendResp.StatusCode != http.StatusOK {
		t.Fatalf("step 4: expected 200, got %d: %s", sendResp.StatusCode, sendBody)
	}

	// The response should be an HTML fragment with the sent message.
	if !strings.Contains(sendBody, "Reply from the operator") {
		t.Errorf("step 4: send response should echo the sent text, got: %s", sendBody)
	}
	if !strings.Contains(sendBody, "message-card") {
		t.Errorf("step 4: send response should contain message-card element, got: %s", sendBody)
	}

	// Verify the sender recorded the message correctly.
	eb.sender.mu.Lock()
	sentCount := len(eb.sender.sent)
	var lastSent fakeMsg
	if sentCount > 0 {
		lastSent = eb.sender.sent[sentCount-1]
	}
	eb.sender.mu.Unlock()

	if sentCount != 1 {
		t.Fatalf("step 4: expected 1 send call, got %d", sentCount)
	}
	if lastSent.sender != operatorEmail {
		t.Errorf("step 4: sender email: got %q, want %q", lastSent.sender, operatorEmail)
	}
	if lastSent.text != "Reply from the operator" {
		t.Errorf("step 4: message text: got %q, want %q", lastSent.text, "Reply from the operator")
	}
}

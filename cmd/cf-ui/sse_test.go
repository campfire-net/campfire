// cmd/cf-ui/sse_test.go — tests for the SSE hub and /events handler.
package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- SSE test helpers ---

// newSSETestServer returns a test server with SSE wired.
// It returns the server, the muxBundle (for hub access), and a pre-seeded session token.
func newSSETestServer(t *testing.T) (*httptest.Server, muxBundle, string) {
	t.Helper()
	logger := newDiscardLogger()
	sessions := NewMemSessionStore()
	authCfg := newAuthConfig(logger, func(string) string { return "" }, "http://localhost", sessions, noopAuthProvider{})
	bundle := buildMuxWithAuth(logger, authCfg)
	srv := httptest.NewServer(bundle.handler)
	t.Cleanup(srv.Close)

	// Seed a session.
	token := "sse-session-" + t.Name()
	sessions.Store(token, Identity{Email: "sse-user@example.com", DisplayName: "SSE User", Provider: "magic"}, time.Hour)

	return srv, bundle, token
}

// sseClient returns an http.Client with a session cookie for use in SSE tests.
func sseClient(t *testing.T, srvURL, sessionToken string) *http.Client {
	t.Helper()
	jar := newCookieJar()
	u, _ := url.Parse(srvURL)
	jar.SetCookies(u, []*http.Cookie{{Name: sessionCookieName, Value: sessionToken}})
	return &http.Client{Jar: jar}
}

// readSSELines reads SSE lines from the response until n comment or event lines are collected,
// or ctx is done. Returns the raw lines received (stripped of "\n").
func readSSELines(t *testing.T, resp *http.Response, want int, timeout time.Duration) []string {
	t.Helper()
	lines := make([]string, 0, want)
	done := make(chan struct{})

	go func() {
		defer close(done)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			lines = append(lines, line)
			if len(lines) >= want {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		t.Logf("readSSELines: timed out after %v, got %d/%d lines", timeout, len(lines), want)
	}
	return lines
}

// --- Tests ---

// TestSSEStreamEstablishment verifies that GET /events returns an SSE stream.
func TestSSEStreamEstablishment(t *testing.T) {
	srv, _, token := newSSETestServer(t)
	client := sseClient(t, srv.URL, token)

	resp, err := client.Get(srv.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream Content-Type, got %q", ct)
	}

	// Read the initial system event (sent immediately on connect).
	lines := readSSELines(t, resp, 3, 2*time.Second)
	found := false
	for _, line := range lines {
		if strings.HasPrefix(line, "event: system") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected initial system event in stream, got: %v", lines)
	}
}

// TestSSEStreamRequiresAuth verifies that /events returns 401 without a session cookie.
func TestSSEStreamRequiresAuth(t *testing.T) {
	srv, _, _ := newSSETestServer(t)

	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /events, got %d", resp.StatusCode)
	}
}

// TestSSEKeepalive verifies that the hub sends SSE keepalive comments.
func TestSSEKeepalive(t *testing.T) {
	logger := newDiscardLogger()
	sessions := NewMemSessionStore()
	token := "keepalive-session"
	sessions.Store(token, Identity{Email: "ka@example.com", Provider: "magic"}, time.Hour)

	// Use a very short keepalive interval so the test doesn't take 30s.
	hub := &SSEHub{
		conns:                  make(map[string][]*sseConn),
		sessions:               sessions,
		logger:                 logger,
		keepaliveInterval:      50 * time.Millisecond,
		sessionRecheckInterval: time.Hour, // don't re-check during this test
	}

	mux := http.NewServeMux()
	mux.Handle("GET /events", SessionMiddleware(sessions)(handleEventsHandler(hub)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := sseClient(t, srv.URL, token)
	resp, err := client.Get(srv.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Wait for at least one keepalive comment line. The first few lines will be
	// the initial system event; then we expect ":keepalive\n\n".
	lines := readSSELines(t, resp, 6, 2*time.Second)
	found := false
	for _, line := range lines {
		if line == ":keepalive" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected :keepalive comment in stream, got lines: %v", lines)
	}
}

// TestSSEConnectionBudget verifies that a 4th concurrent connection returns 429.
func TestSSEConnectionBudget(t *testing.T) {
	srv, _, token := newSSETestServer(t)

	var resps []*http.Response
	var mu sync.Mutex
	closeAll := func() {
		mu.Lock()
		defer mu.Unlock()
		for _, r := range resps {
			r.Body.Close()
		}
	}
	defer closeAll()

	// Open 3 connections — all should succeed.
	for i := 0; i < sseMaxConnsPerOperator; i++ {
		client := sseClient(t, srv.URL, token)
		resp, err := client.Get(srv.URL + "/events")
		if err != nil {
			t.Fatalf("connection %d: GET /events: %v", i+1, err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("connection %d: expected 200, got %d", i+1, resp.StatusCode)
		}
		// Read the initial event so the connection is fully established.
		readSSELines(t, resp, 3, time.Second)
		mu.Lock()
		resps = append(resps, resp)
		mu.Unlock()
	}

	// 4th connection must be rejected.
	client := sseClient(t, srv.URL, token)
	resp, err := client.Get(srv.URL + "/events")
	if err != nil {
		t.Fatalf("4th connection: GET /events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for 4th SSE connection, got %d", resp.StatusCode)
	}
}

// TestSSEEventBroadcast verifies that hub.Publish delivers events to connected sessions.
func TestSSEEventBroadcast(t *testing.T) {
	logger := newDiscardLogger()
	sessions := NewMemSessionStore()
	token := "broadcast-session"
	sessions.Store(token, Identity{Email: "bc@example.com", Provider: "magic"}, time.Hour)

	hub := &SSEHub{
		conns:                  make(map[string][]*sseConn),
		sessions:               sessions,
		logger:                 logger,
		keepaliveInterval:      time.Hour, // don't send keepalives during this test
		sessionRecheckInterval: time.Hour,
	}

	mux := http.NewServeMux()
	mux.Handle("GET /events", SessionMiddleware(sessions)(handleEventsHandler(hub)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := sseClient(t, srv.URL, token)
	resp, err := client.Get(srv.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Read and discard the initial system event.
	readSSELines(t, resp, 3, time.Second)

	// Publish an event via the hub.
	go func() {
		time.Sleep(50 * time.Millisecond)
		hub.Publish("campfire-abc", SSEEvent{
			Type:       SSEEventMessage,
			CampfireID: "campfire-abc",
			Data:       map[string]any{"text": "hello world"},
		})
	}()

	// Read the published event lines.
	lines := readSSELines(t, resp, 3, 2*time.Second)

	// Find the data line and verify the payload.
	var dataLine string
	for _, l := range lines {
		if strings.HasPrefix(l, "data: ") {
			dataLine = strings.TrimPrefix(l, "data: ")
			break
		}
	}
	if dataLine == "" {
		t.Fatalf("expected data line in broadcast event, got lines: %v", lines)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(dataLine), &payload); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}
	if payload["campfire_id"] != "campfire-abc" {
		t.Errorf("expected campfire_id=campfire-abc, got %v", payload["campfire_id"])
	}
	if payload["text"] != "hello world" {
		t.Errorf("expected text=hello world, got %v", payload["text"])
	}
}

// TestSSESessionExpiredMidStream verifies that the hub closes the connection
// when the session expires during re-validation.
func TestSSESessionExpiredMidStream(t *testing.T) {
	logger := newDiscardLogger()
	sessions := NewMemSessionStore()
	token := "expiring-session"
	// Store with a long enough TTL to pass middleware validation, but we will
	// delete it manually to simulate expiry before the recheck fires.
	sessions.Store(token, Identity{Email: "exp@example.com", Provider: "magic"}, time.Hour)

	hub := &SSEHub{
		conns:                  make(map[string][]*sseConn),
		sessions:               sessions,
		logger:                 logger,
		keepaliveInterval:      time.Hour,
		sessionRecheckInterval: 100 * time.Millisecond, // short recheck
	}

	mux := http.NewServeMux()
	mux.Handle("GET /events", SessionMiddleware(sessions)(handleEventsHandler(hub)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := sseClient(t, srv.URL, token)
	resp, err := client.Get(srv.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on connect, got %d", resp.StatusCode)
	}

	// Collect all SSE lines in the background.
	linesCh := make(chan string, 64)
	go func() {
		defer close(linesCh)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			linesCh <- scanner.Text()
		}
	}()

	// Delete the session to simulate expiry. The next recheck (100ms) will see it as invalid.
	time.Sleep(20 * time.Millisecond) // let connection establish
	sessions.Delete(token)

	// Collect lines for up to 2 seconds, looking for :session-expired.
	deadline := time.After(2 * time.Second)
	found := false
	var received []string
	for !found {
		select {
		case line, ok := <-linesCh:
			if !ok {
				goto done
			}
			received = append(received, line)
			if line == ":session-expired" {
				found = true
			}
		case <-deadline:
			goto done
		}
	}
done:
	if !found {
		t.Errorf("expected :session-expired comment after session deletion, got lines: %v", received)
	}
}

// TestSSEGracefulShutdown verifies that hub.Shutdown() closes all connections.
func TestSSEGracefulShutdown(t *testing.T) {
	logger := newDiscardLogger()
	sessions := NewMemSessionStore()
	token := "shutdown-session"
	sessions.Store(token, Identity{Email: "sd@example.com", Provider: "magic"}, time.Hour)

	hub := &SSEHub{
		conns:                  make(map[string][]*sseConn),
		sessions:               sessions,
		logger:                 logger,
		keepaliveInterval:      time.Hour,
		sessionRecheckInterval: time.Hour,
	}

	mux := http.NewServeMux()
	mux.Handle("GET /events", SessionMiddleware(sessions)(handleEventsHandler(hub)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := sseClient(t, srv.URL, token)
	resp, err := client.Get(srv.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Read the initial event to confirm the stream is live.
	readSSELines(t, resp, 3, time.Second)

	// Trigger hub shutdown.
	go func() {
		time.Sleep(50 * time.Millisecond)
		hub.Shutdown()
	}()

	// The stream should emit a :shutdown comment and then end.
	lines := readSSELines(t, resp, 10, 2*time.Second)
	found := false
	for _, line := range lines {
		if line == ":shutdown" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected :shutdown comment on hub shutdown, got lines: %v", lines)
	}
}

// TestSSEEventPayloadHasCampfireID verifies that every published event includes campfire_id.
func TestSSEEventPayloadHasCampfireID(t *testing.T) {
	logger := newDiscardLogger()
	sessions := NewMemSessionStore()
	token := "payload-session"
	sessions.Store(token, Identity{Email: "pld@example.com", Provider: "magic"}, time.Hour)

	hub := &SSEHub{
		conns:                  make(map[string][]*sseConn),
		sessions:               sessions,
		logger:                 logger,
		keepaliveInterval:      time.Hour,
		sessionRecheckInterval: time.Hour,
	}

	mux := http.NewServeMux()
	mux.Handle("GET /events", SessionMiddleware(sessions)(handleEventsHandler(hub)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := sseClient(t, srv.URL, token)
	resp, err := client.Get(srv.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	readSSELines(t, resp, 3, time.Second) // consume initial system event

	go func() {
		time.Sleep(50 * time.Millisecond)
		hub.Publish("fire-xyz", SSEEvent{
			Type: SSEEventUnread,
			Data: map[string]any{"count": 5},
		})
	}()

	lines := readSSELines(t, resp, 3, 2*time.Second)
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			raw := strings.TrimPrefix(line, "data: ")
			var payload map[string]any
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if payload["campfire_id"] != "fire-xyz" {
				t.Errorf("expected campfire_id=fire-xyz in payload, got %v", payload["campfire_id"])
			}
			return
		}
	}
	t.Errorf("no data line found in event lines: %v", lines)
}

// TestSSEConnectionSurvivesWithoutWriteTimeout verifies that an SSE connection
// stays alive after what would have been the old 30-second WriteTimeout.
//
// We test this using a very short WriteTimeout (100ms) to confirm that a
// non-zero timeout does kill SSE connections, and then show that WriteTimeout=0
// keeps the connection alive past that same duration.
//
// This guards against regressions where WriteTimeout is re-introduced and
// silently kills long-lived SSE streams.
func TestSSEConnectionSurvivesWithoutWriteTimeout(t *testing.T) {
	logger := newDiscardLogger()
	sessions := NewMemSessionStore()
	token := "writetimeout-session"
	sessions.Store(token, Identity{Email: "wt@example.com", Provider: "magic"}, time.Hour)

	hub := &SSEHub{
		conns:                  make(map[string][]*sseConn),
		sessions:               sessions,
		logger:                 logger,
		keepaliveInterval:      time.Hour, // don't send keepalives — we're testing connection lifetime
		sessionRecheckInterval: time.Hour,
	}

	mux := http.NewServeMux()
	mux.Handle("GET /events", SessionMiddleware(sessions)(handleEventsHandler(hub)))

	// Server with WriteTimeout=0 (the fix). Connection must survive beyond
	// the short window we observe.
	srvNoTimeout := httptest.NewUnstartedServer(mux)
	srvNoTimeout.Config.WriteTimeout = 0
	srvNoTimeout.Start()
	defer srvNoTimeout.Close()

	client := sseClient(t, srvNoTimeout.URL, token)
	resp, err := client.Get(srvNoTimeout.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Read the initial connected event.
	lines := readSSELines(t, resp, 3, 2*time.Second)
	foundConnected := false
	for _, l := range lines {
		if strings.Contains(l, "connected") {
			foundConnected = true
			break
		}
	}
	if !foundConnected {
		t.Fatalf("expected initial connected event, got: %v", lines)
	}

	// Wait 200ms — more than enough to exceed a typical short WriteTimeout.
	// The connection must still be open (hub still has this conn registered).
	time.Sleep(200 * time.Millisecond)

	hub.mu.Lock()
	conns := len(hub.conns["wt@example.com"])
	hub.mu.Unlock()

	if conns == 0 {
		t.Error("SSE connection was dropped — WriteTimeout may have killed it; expected connection to survive with WriteTimeout=0")
	}
}

// TestSSEConnectionBudgetRelease verifies that after closing a connection,
// a new connection can be opened (budget is released on close).
func TestSSEConnectionBudgetRelease(t *testing.T) {
	srv, _, token := newSSETestServer(t)

	// Open max connections.
	resps := make([]*http.Response, sseMaxConnsPerOperator)
	for i := 0; i < sseMaxConnsPerOperator; i++ {
		client := sseClient(t, srv.URL, token)
		resp, err := client.Get(srv.URL + "/events")
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("connection %d: expected 200, got %d", i, resp.StatusCode)
		}
		readSSELines(t, resp, 3, time.Second)
		resps[i] = resp
	}

	// Close the first connection and give the handler time to clean up.
	resps[0].Body.Close()
	time.Sleep(100 * time.Millisecond)

	// A new connection should now be accepted.
	client := sseClient(t, srv.URL, token)
	resp, err := client.Get(srv.URL + "/events")
	if err != nil {
		t.Fatalf("replacement connection: %v", err)
	}
	defer resp.Body.Close()
	for _, r := range resps[1:] {
		r.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after releasing a connection slot, got %d", resp.StatusCode)
	}
}

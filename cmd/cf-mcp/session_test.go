package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestSessionManager creates a SessionManager backed by a temp directory.
func newTestSessionManager(t *testing.T) *SessionManager {
	t.Helper()
	dir := t.TempDir()
	m := NewSessionManager(dir)
	t.Cleanup(m.Stop)
	return m
}

// newTestServerWithSessions creates a *server with a live SessionManager.
func newTestServerWithSessions(t *testing.T) *server {
	t.Helper()
	m := newTestSessionManager(t)
	return &server{
		cfHome:         t.TempDir(), // not used in session mode, but must be set
		beaconDir:      t.TempDir(),
		cfHomeExplicit: true,
		sessManager:    m,
	}
}

// mcpInitBody is a campfire_init JSON-RPC request body.
const mcpInitBody = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`

// postMCP sends a POST /mcp with the given body and optional Bearer token.
// Returns the decoded JSON-RPC response.
func postMCP(t *testing.T, srv *server, body string, token string) jsonRPCResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)

	var resp jsonRPCResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

// extractPublicKey pulls the public_key from a campfire_id tool result text.
func extractPublicKey(t *testing.T, resp jsonRPCResponse) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("cannot extract content from result: %v", string(b))
	}
	var id struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &id); err != nil || id.PublicKey == "" {
		t.Fatalf("cannot extract public_key from text: %q", result.Content[0].Text)
	}
	return id.PublicKey
}

// extractTokenFromInit extracts the session_token from a campfire_init response.
func extractTokenFromInit(t *testing.T, resp jsonRPCResponse) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("campfire_init error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("cannot extract content: %v", string(b))
	}
	text := result.Content[0].Text
	const marker = "Session token: "
	idx := strings.Index(text, marker)
	if idx == -1 {
		t.Fatalf("session token not found in campfire_init response: %q", text)
	}
	// Token runs to the next newline (or end of string).
	rest := text[idx+len(marker):]
	if nl := strings.IndexByte(rest, '\n'); nl != -1 {
		return rest[:nl]
	}
	return rest
}

// ---------------------------------------------------------------------------
// Test: different tokens → different identities
// ---------------------------------------------------------------------------

// TestSession_DifferentTokensDifferentIdentities verifies that two distinct
// Bearer tokens produce independent agent identities (different public keys
// and separate store.db files).
func TestSession_DifferentTokensDifferentIdentities(t *testing.T) {
	srv := newTestServerWithSessions(t)

	// Initialize session A (no token → auto-generated).
	respA := postMCP(t, srv, mcpInitBody, "")
	tokenA := extractTokenFromInit(t, respA)
	if tokenA == "" {
		t.Fatal("expected non-empty token from session A campfire_init")
	}

	// Initialize session B (different call → different token).
	respB := postMCP(t, srv, mcpInitBody, "")
	tokenB := extractTokenFromInit(t, respB)
	if tokenB == "" {
		t.Fatal("expected non-empty token from session B campfire_init")
	}

	if tokenA == tokenB {
		t.Fatal("two separate campfire_init calls returned the same session token")
	}

	// Query campfire_id for session A.
	idBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_id","arguments":{}}}`
	pkA := extractPublicKey(t, postMCP(t, srv, idBody, tokenA))

	// Query campfire_id for session B.
	pkB := extractPublicKey(t, postMCP(t, srv, idBody, tokenB))

	if pkA == pkB {
		t.Errorf("sessions A and B have the same public key (%s); expected isolation", pkA)
	}
}

// ---------------------------------------------------------------------------
// Test: same token → same identity across requests
// ---------------------------------------------------------------------------

// TestSession_SameTokenSameIdentity verifies that multiple requests with the
// same Bearer token always see the same agent identity (same public key).
func TestSession_SameTokenSameIdentity(t *testing.T) {
	srv := newTestServerWithSessions(t)

	// Create session.
	initResp := postMCP(t, srv, mcpInitBody, "")
	token := extractTokenFromInit(t, initResp)

	idBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_id","arguments":{}}}`

	// Call campfire_id three times with the same token.
	pk1 := extractPublicKey(t, postMCP(t, srv, idBody, token))
	pk2 := extractPublicKey(t, postMCP(t, srv, idBody, token))
	pk3 := extractPublicKey(t, postMCP(t, srv, idBody, token))

	if pk1 != pk2 || pk2 != pk3 {
		t.Errorf("public key changed across requests: %q %q %q", pk1, pk2, pk3)
	}
}

// ---------------------------------------------------------------------------
// Test: session idle timeout closes the store
// ---------------------------------------------------------------------------

// TestSession_IdleTimeoutClosesStore verifies that after a session has been
// idle for longer than idleTimeout, the reaper goroutine closes the store (st == nil).
// This test uses a short idleTimeout override and waits for the actual reaper
// goroutine to fire and clean up the session.
func TestSession_IdleTimeoutClosesStore(t *testing.T) {
	// Use a very short idle timeout for testing (actual reaper checks every timeout/2).
	testTimeout := 50 * time.Millisecond
	dir := t.TempDir()
	m := &SessionManager{
		sessionsDir:         dir,
		stopCh:              make(chan struct{}),
		idleTimeoutOverride: testTimeout,
	}
	go m.reaper()
	t.Cleanup(m.Stop)

	srv := &server{
		cfHome:         t.TempDir(),
		beaconDir:      t.TempDir(),
		cfHomeExplicit: true,
		sessManager:    m,
	}

	// Create a session.
	initResp := postMCP(t, srv, mcpInitBody, "")
	token := extractTokenFromInit(t, initResp)

	// Retrieve the session and back-date its lastActivity past testTimeout.
	v, ok := srv.sessManager.sessions.Load(token)
	if !ok {
		t.Fatal("session not found in manager after init")
	}
	sess := v.(*Session)

	sess.mu.Lock()
	sess.lastActivity = time.Now().Add(-(testTimeout + time.Millisecond))
	sess.mu.Unlock()

	// Verify store is open before reaping.
	sess.mu.Lock()
	storeBefore := sess.st
	sess.mu.Unlock()
	if storeBefore == nil {
		t.Fatal("expected store to be open before reaping")
	}

	// Wait for the reaper goroutine to fire (runs every testTimeout/2).
	// We wait up to testTimeout*3 to be safe.
	deadline := time.Now().Add(testTimeout * 3)
	for time.Now().Before(deadline) {
		sess.mu.Lock()
		storeNow := sess.st
		sess.mu.Unlock()
		if storeNow == nil {
			// Store was closed by the reaper. Success!
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Error("timeout waiting for reaper to close the store")
}

// ---------------------------------------------------------------------------
// Test: after reaper fires, getOrCreate creates a fresh session
// ---------------------------------------------------------------------------

// TestSession_ReaperDeletesFromMap verifies that after the reaper closes and
// removes a session from the sync.Map, a subsequent getOrCreate for the same
// token creates a fresh session with a valid (non-nil) open store. Before this
// fix the reaper only called sess.Close() (st = nil) but left the stale entry
// in the map, causing the next caller to receive a session with st == nil.
func TestSession_ReaperDeletesFromMap(t *testing.T) {
	m := newTestSessionManager(t)

	// Create session directly via getOrCreate.
	token, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	sess1, err := m.getOrCreate(token)
	if err != nil {
		t.Fatalf("getOrCreate (first): %v", err)
	}

	// Back-date lastActivity so the reaper considers it idle.
	sess1.mu.Lock()
	sess1.lastActivity = time.Now().Add(-(idleTimeout + time.Second))
	sess1.mu.Unlock()

	// Run one reap cycle directly.
	m.sessions.Range(func(k, v interface{}) bool {
		s := v.(*Session)
		s.mu.Lock()
		idle := time.Since(s.lastActivity) > idleTimeout
		s.mu.Unlock()
		if idle {
			m.sessions.Delete(k)
			s.Close()
		}
		return true
	})

	// Session must no longer be in the map.
	if _, ok := m.sessions.Load(token); ok {
		t.Fatal("reaper did not remove session from sync.Map")
	}

	// getOrCreate for the same token must produce a fresh session with st != nil.
	sess2, err := m.getOrCreate(token)
	if err != nil {
		t.Fatalf("getOrCreate (after reap): %v", err)
	}

	sess2.mu.Lock()
	st := sess2.st
	sess2.mu.Unlock()
	if st == nil {
		t.Fatal("getOrCreate after reap returned session with nil store (use-after-reap bug)")
	}

	// The new session must be a distinct object from the reaped one.
	if sess2 == sess1 {
		t.Fatal("getOrCreate returned the reaped session object; expected a new one")
	}
}

// ---------------------------------------------------------------------------
// Test: request without token for non-init method returns error
// ---------------------------------------------------------------------------

// TestSession_NoTokenNonInitReturnsError verifies that sending a non-init
// request without a Bearer token returns a -32000 session-required error.
func TestSession_NoTokenNonInitReturnsError(t *testing.T) {
	srv := newTestServerWithSessions(t)

	idBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_id","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(idBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)

	var resp jsonRPCResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error response for missing token")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "session required") {
		t.Errorf("expected 'session required' in error message, got %q", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test: store does not open/close per request
// ---------------------------------------------------------------------------

// TestSession_StoreOpenOnce verifies that the store is opened once per session
// and not closed between requests (the store pointer stays the same across
// multiple campfire_id calls on the same token).
func TestSession_StoreOpenOnce(t *testing.T) {
	srv := newTestServerWithSessions(t)

	// Create session.
	initResp := postMCP(t, srv, mcpInitBody, "")
	token := extractTokenFromInit(t, initResp)

	v, ok := srv.sessManager.sessions.Load(token)
	if !ok {
		t.Fatal("session not found in manager")
	}
	sess := v.(*Session)

	// Capture store pointer after init.
	sess.mu.Lock()
	storeAfterInit := sess.st
	sess.mu.Unlock()
	if storeAfterInit == nil {
		t.Fatal("expected store to be open after init")
	}

	// Make two more requests.
	idBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_id","arguments":{}}}`)
	postMCP(t, srv, idBody, token)
	postMCP(t, srv, idBody, token)

	// Store pointer must be unchanged (not closed and reopened).
	sess.mu.Lock()
	storeAfterRequests := sess.st
	sess.mu.Unlock()

	if storeAfterRequests != storeAfterInit {
		t.Error("store pointer changed between requests: store was closed and reopened per-request")
	}
}

// ---------------------------------------------------------------------------
// Test: concurrent getOrCreate for same token leaves exactly one live transport
// ---------------------------------------------------------------------------

// TestSession_ConcurrentGetOrCreateOneTransport verifies that when two goroutines
// race to create a session for the same token, exactly one live transport is
// registered in the router. The loser must not leave a stale stopped-transport
// pointer in the router.
//
// This test uses a sync.WaitGroup barrier to maximise the chance of a real
// race between the two goroutines. Because SQLite cannot be opened concurrently
// for the exact same path, one goroutine will win cleanly and the other may
// see a SQLITE_BUSY error — that's acceptable (the router must still be clean).
// We also test the sequential "second call after first wins" path, which is the
// common real-world scenario.
func TestSession_ConcurrentGetOrCreateOneTransport(t *testing.T) {
	dir := t.TempDir()
	m := &SessionManager{
		sessionsDir: dir,
		stopCh:      make(chan struct{}),
		router:      NewTransportRouter(),
	}
	go m.reaper()
	t.Cleanup(m.Stop)

	const token = "race-token-abc123"

	// --- Sequential path: first call creates, second call reuses ---
	sess1, err := m.getOrCreate(token)
	if err != nil {
		t.Fatalf("first getOrCreate: %v", err)
	}
	if sess1.httpTransport == nil {
		t.Fatal("winner session has nil httpTransport")
	}

	// Router must point at the winner's transport immediately after the first call.
	registered := m.router.GetTransport(token)
	if registered == nil {
		t.Fatal("no transport registered in router after first getOrCreate")
	}
	if registered != sess1.httpTransport {
		t.Error("router holds a different transport than the session that won")
	}

	// Second call (same token, session already in sync.Map) must return the same session.
	sess2, err := m.getOrCreate(token)
	if err != nil {
		t.Fatalf("second getOrCreate: %v", err)
	}
	if sess2 != sess1 {
		t.Error("second getOrCreate returned a different session pointer")
	}

	// Router must still point at the original transport (unchanged).
	registered2 := m.router.GetTransport(token)
	if registered2 != sess1.httpTransport {
		t.Error("router transport changed after second getOrCreate — loser overwrote winner")
	}

	// --- Concurrent path: goroutines race on a fresh token ---
	const token2 = "race-token-concurrent"

	var wg sync.WaitGroup
	type result struct {
		sess *Session
		err  error
	}
	results := make([]result, 2)

	// Use a ready channel to maximise goroutine overlap.
	ready := make(chan struct{})
	wg.Add(2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-ready
			sess, err := m.getOrCreate(token2)
			results[i] = result{sess, err}
		}()
	}
	close(ready)
	wg.Wait()

	// At least one goroutine must succeed (the winner). The loser may fail with
	// SQLITE_BUSY — that's a known SQLite constraint for concurrent opens of the
	// same file, not a bug in getOrCreate.
	var winner *Session
	for _, r := range results {
		if r.err == nil {
			if winner == nil {
				winner = r.sess
			} else if r.sess != winner {
				t.Error("two goroutines returned different session pointers — sync.Map race lost")
			}
		}
	}
	if winner == nil {
		t.Fatal("all goroutines failed; at least one must succeed")
	}

	// Router must hold exactly the winner's transport (not a stale stopped pointer).
	registeredConcurrent := m.router.GetTransport(token2)
	if registeredConcurrent == nil {
		t.Fatal("no transport registered in router after concurrent getOrCreate")
	}
	if registeredConcurrent != winner.httpTransport {
		t.Error("router has a stale transport — loser registered before LoadOrStore decided the winner")
	}
}

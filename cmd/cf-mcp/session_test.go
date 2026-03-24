package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		maxSessions:         defaultMaxSessions,
	}
	m.registry = newTokenRegistry()
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
	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found in manager after init")
	}

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

	// Create session via issueToken + getOrCreate (production path).
	token, err := m.issueToken()
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	sess1, err := m.getOrCreate(token)
	if err != nil {
		t.Fatalf("getOrCreate (first): %v", err)
	}

	// Capture internalID for later map check.
	internalID := sess1.internalID

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

	// Session must no longer be in the map (keyed by internalID).
	if _, ok := m.sessions.Load(internalID); ok {
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

	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found in manager")
	}

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
// Test: reaped session's campfire routes return 404
// ---------------------------------------------------------------------------

// TestSession_ReapedSessionCampfire404 verifies the done condition from workspace-zg09:
// after a session is reaped, /campfire/{id}/deliver for one of its campfires
// returns 404 (not an error from a stopped transport), and the router no longer
// holds the session transport or any of its campfire routes.
func TestSession_ReapedSessionCampfire404(t *testing.T) {
	dir := t.TempDir()
	router := NewTransportRouter()
	sm := &SessionManager{
		sessionsDir:  dir,
		stopCh:       make(chan struct{}),
		router:       router,
		externalAddr: "http://test-server",
		maxSessions:  defaultMaxSessions,
	}
	sm.registry = newTokenRegistry()
	go sm.reaper()
	t.Cleanup(sm.Stop)

	token, err := sm.issueToken()
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}

	sess, err := sm.getOrCreate(token)
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}

	// Register a campfire for this session.
	const campfireID = "deadbeef1234"
	router.RegisterForSession(campfireID, token, sess.httpTransport)

	// Campfire must be registered before reap.
	if tr := router.GetCampfireTransport(campfireID); tr == nil {
		t.Fatal("campfire not registered before reap")
	}

	// Simulate reap: remove from sessions map and close.
	sm.sessions.Delete(token)
	sess.Close()

	// After reap: campfire route must be gone.
	if tr := router.GetCampfireTransport(campfireID); tr != nil {
		t.Error("campfire route still present after session reap")
	}

	// After reap: session transport must be gone.
	if tr := router.GetTransport(token); tr != nil {
		t.Error("session transport still present after session reap")
	}

	// ServeHTTP must return 404 for the reaped campfire, not a transport error.
	req := httptest.NewRequest(http.MethodPost, "/campfire/"+campfireID+"/deliver", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 after reap, got %d", w.Code)
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
		maxSessions: defaultMaxSessions,
	}
	m.registry = newTokenRegistry()
	go m.reaper()
	t.Cleanup(m.Stop)

	token, err := m.issueToken()
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}

	// --- Sequential path: first call creates, second call reuses ---
	sess1, err := m.getOrCreate(token)
	if err != nil {
		t.Fatalf("first getOrCreate: %v", err)
	}
	if sess1.httpTransport == nil {
		t.Fatal("winner session has nil httpTransport")
	}

	// Router must point at the winner's transport immediately after the first call.
	// The router is keyed by token, not internalID.
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
	token2, err := m.issueToken()
	if err != nil {
		t.Fatalf("issueToken for concurrent test: %v", err)
	}

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

// ---------------------------------------------------------------------------
// Test: max-sessions limit blocks new session creation
// ---------------------------------------------------------------------------

// TestSession_MaxSessionsLimit verifies that getOrCreate returns a
// sessionLimitError when the number of active sessions reaches maxSessions,
// and that no directory is created for the rejected session.
func TestSession_MaxSessionsLimit(t *testing.T) {
	dir := t.TempDir()
	const limit = 3
	m := &SessionManager{
		sessionsDir: dir,
		stopCh:      make(chan struct{}),
		maxSessions: limit,
	}
	m.registry = newTokenRegistry()
	go m.reaper()
	t.Cleanup(m.Stop)

	// Create exactly limit sessions — all must succeed.
	var firstTok string
	for i := 0; i < limit; i++ {
		tok, err := m.issueToken()
		if err != nil {
			t.Fatalf("issueToken: %v", err)
		}
		if _, err := m.getOrCreate(tok); err != nil {
			t.Fatalf("getOrCreate session %d of %d failed: %v", i+1, limit, err)
		}
		if i == 0 {
			firstTok = tok
		}
	}

	// The next (limit+1) session must be rejected with sessionLimitError.
	extraTok, err := m.issueToken()
	if err != nil {
		t.Fatalf("issueToken for extra session: %v", err)
	}
	_, gotErr := m.getOrCreate(extraTok)
	if gotErr == nil {
		t.Fatal("expected error when exceeding max sessions, got nil")
	}
	var limitErr *sessionLimitError
	if !errors.As(gotErr, &limitErr) {
		t.Fatalf("expected *sessionLimitError, got %T: %v", gotErr, gotErr)
	}

	// No directory must have been created for the rejected token.
	// The directory is named by internalID (not token), but if no internalID
	// was allocated, no directory exists anywhere in the sessions dir for this token.
	internalID, _ := m.registry.lookup(extraTok, 0)
	if internalID != "" {
		expectedDir := filepath.Join(dir, internalID)
		if _, statErr := os.Stat(expectedDir); statErr == nil {
			t.Errorf("directory was created for rejected session: %s", expectedDir)
		}
	}

	// Fast path (existing session reuse) must still work for a session that
	// is already in the map — the limit only blocks new allocations.
	if _, err := m.getOrCreate(firstTok); err != nil {
		t.Errorf("reuse of existing session should not be blocked by limit: %v", err)
	}
}

// TestSession_MaxSessionsHTTP503 verifies that handleMCPSessioned returns HTTP
// 503 when the session limit is reached.
func TestSession_MaxSessionsHTTP503(t *testing.T) {
	dir := t.TempDir()
	const limit = 2
	m := &SessionManager{
		sessionsDir: dir,
		stopCh:      make(chan struct{}),
		maxSessions: limit,
	}
	m.registry = newTokenRegistry()
	go m.reaper()
	t.Cleanup(m.Stop)

	srv := &server{
		cfHome:         t.TempDir(),
		beaconDir:      t.TempDir(),
		cfHomeExplicit: true,
		sessManager:    m,
	}

	// Fill up to the limit using campfire_init (each call generates a new token).
	for i := 0; i < limit; i++ {
		resp := postMCP(t, srv, mcpInitBody, "")
		if resp.Error != nil {
			t.Fatalf("campfire_init %d of %d failed: code=%d msg=%s", i+1, limit, resp.Error.Code, resp.Error.Message)
		}
	}

	// The next campfire_init must return HTTP 503 with a -32000 JSON-RPC error.
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(mcpInitBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected HTTP 503, got %d", w.Code)
	}
	var resp jsonRPCResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error in 503 response")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "session limit reached") {
		t.Errorf("expected 'session limit reached' in error message, got %q", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test: token-path separation — session directory uses internal UUID, not token
// ---------------------------------------------------------------------------

// TestSession_TokenPathSeparation verifies that the session directory is named
// using the internal UUID (internalID), not the bearer token. The token must
// NOT appear as a path component in the filesystem.
func TestSession_TokenPathSeparation(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir)
	t.Cleanup(m.Stop)

	tok, err := m.registry.issue()
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	sess, err := m.getOrCreate(tok)
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}

	// The cfHome must NOT contain the token string.
	if strings.Contains(sess.cfHome, tok) {
		t.Errorf("session cfHome contains token: cfHome=%q token=%q", sess.cfHome, tok)
	}

	// The cfHome must exist on disk.
	if _, err := os.Stat(sess.cfHome); err != nil {
		t.Errorf("session directory does not exist: %v", err)
	}

	// The directory name must be the internalID.
	dirName := filepath.Base(sess.cfHome)
	if dirName == tok {
		t.Errorf("session directory is named by token; expected internalID")
	}
	if dirName != sess.internalID {
		t.Errorf("session directory name %q != internalID %q", dirName, sess.internalID)
	}
}

// ---------------------------------------------------------------------------
// Test: arbitrary token string rejected with error
// ---------------------------------------------------------------------------

// TestSession_ArbitraryTokenRejected verifies that getOrCreate rejects tokens
// that were not issued by the registry (arbitrary / not registered tokens).
func TestSession_ArbitraryTokenRejected(t *testing.T) {
	m := newTestSessionManager(t)

	// An externally-generated token that was never registered.
	arbitraryToken := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err := m.getOrCreate(arbitraryToken)
	if err == nil {
		t.Fatal("expected error for unregistered token, got nil")
	}
	// Error message must not contain the token itself.
	if strings.Contains(err.Error(), arbitraryToken) {
		t.Errorf("error message leaks token: %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Test: campfire_revoke_session → token rejected on next request
// ---------------------------------------------------------------------------

// TestSession_RevokeSession verifies that after calling campfire_revoke_session,
// the session token is invalidated and subsequent requests with that token are
// rejected with HTTP 401.
func TestSession_RevokeSession(t *testing.T) {
	srv := newTestServerWithSessions(t)

	// Init → get token.
	initResp := postMCP(t, srv, mcpInitBody, "")
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected token from campfire_init")
	}

	// Verify session is usable.
	idBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_id","arguments":{}}}`
	resp := postMCP(t, srv, idBody, token)
	if resp.Error != nil {
		t.Fatalf("pre-revoke campfire_id failed: %v", resp.Error.Message)
	}

	// Revoke the session.
	revokeBody := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"campfire_revoke_session","arguments":{}}}`
	revokeResp := postMCP(t, srv, revokeBody, token)
	if revokeResp.Error != nil {
		t.Fatalf("campfire_revoke_session failed: %v", revokeResp.Error.Message)
	}

	// After revoke: same token must be rejected.
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(idBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 after revoke, got %d", w.Code)
	}
	var postRevokeResp jsonRPCResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&postRevokeResp); err != nil {
		t.Fatalf("decode post-revoke response: %v", err)
	}
	if postRevokeResp.Error == nil {
		t.Fatal("expected error after revoke, got nil")
	}
	// Token must not appear in the error message.
	if strings.Contains(postRevokeResp.Error.Message, token) {
		t.Errorf("error message leaks token: %q", postRevokeResp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test: campfire_rotate_token → new token works, old token fails after grace
// ---------------------------------------------------------------------------

// TestSession_RotateToken verifies that after campfire_rotate_token:
// 1. The new token is returned and works immediately.
// 2. The old token fails after the grace period expires.
// 3. Session state (identity) is preserved — same public key on new token.
func TestSession_RotateToken(t *testing.T) {
	dir := t.TempDir()
	m := &SessionManager{
		sessionsDir:        dir,
		stopCh:             make(chan struct{}),
		maxSessions:        defaultMaxSessions,
		rotationGracePeriod: 50 * time.Millisecond, // very short for testing
	}
	m.registry = newTokenRegistry()
	go m.reaper()
	t.Cleanup(m.Stop)

	srv := &server{
		cfHome:         t.TempDir(),
		beaconDir:      t.TempDir(),
		cfHomeExplicit: true,
		sessManager:    m,
	}

	// Init → get token.
	initResp := postMCP(t, srv, mcpInitBody, "")
	oldToken := extractTokenFromInit(t, initResp)

	// Get identity with old token.
	idBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_id","arguments":{}}}`
	pkBefore := extractPublicKey(t, postMCP(t, srv, idBody, oldToken))

	// Rotate token.
	rotateBody := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"campfire_rotate_token","arguments":{}}}`
	rotateResp := postMCP(t, srv, rotateBody, oldToken)
	if rotateResp.Error != nil {
		t.Fatalf("campfire_rotate_token failed: %v", rotateResp.Error.Message)
	}

	// Extract new token from rotate response.
	newToken := extractNewTokenFromRotate(t, rotateResp)
	if newToken == "" {
		t.Fatal("expected new token from campfire_rotate_token")
	}
	if newToken == oldToken {
		t.Fatal("rotate returned same token as before")
	}

	// New token works immediately.
	pkAfter := extractPublicKey(t, postMCP(t, srv, idBody, newToken))
	if pkBefore != pkAfter {
		t.Errorf("public key changed after rotation: before=%q after=%q", pkBefore, pkAfter)
	}

	// Wait for grace period to expire.
	time.Sleep(100 * time.Millisecond)

	// Old token must now fail.
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(idBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+oldToken)
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 for old token after grace period, got %d", w.Code)
	}
}

// extractNewTokenFromRotate pulls the new_token from a campfire_rotate_token response.
func extractNewTokenFromRotate(t *testing.T, resp jsonRPCResponse) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("rotate error: %v", resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("cannot extract content: %s", string(b))
	}
	text := result.Content[0].Text
	const marker = "New session token: "
	idx := strings.Index(text, marker)
	if idx == -1 {
		t.Fatalf("new token not found in rotate response: %q", text)
	}
	rest := text[idx+len(marker):]
	if nl := strings.IndexByte(rest, '\n'); nl != -1 {
		return rest[:nl]
	}
	return rest
}

// ---------------------------------------------------------------------------
// Test: token older than 1 hour rejected
// ---------------------------------------------------------------------------

// TestSession_TokenExpiry verifies that tokens older than the TTL are rejected
// with HTTP 401 and a structured error indicating expiry.
// Strategy: issue a token with a normal TTL, then backdate its issuedAt via
// the registry internals so it appears expired on the next request.
func TestSession_TokenExpiry(t *testing.T) {
	srv := newTestServerWithSessions(t)

	// Init → get token.
	initResp := postMCP(t, srv, mcpInitBody, "")
	token := extractTokenFromInit(t, initResp)

	// Token works immediately.
	idBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_id","arguments":{}}}`
	resp := postMCP(t, srv, idBody, token)
	if resp.Error != nil {
		t.Fatalf("pre-expiry request failed: %v", resp.Error.Message)
	}

	// Backdate the token's issuedAt so it appears expired.
	srv.sessManager.registry.mu.Lock()
	if entry, ok := srv.sessManager.registry.tokens[token]; ok {
		entry.issuedAt = time.Now().Add(-(defaultTokenTTL + time.Second))
	}
	srv.sessManager.registry.mu.Unlock()

	// Token must now be rejected with expiry error.
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(idBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 for expired token, got %d", w.Code)
	}
	var expiredResp jsonRPCResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&expiredResp); err != nil {
		t.Fatalf("decode expired response: %v", err)
	}
	if expiredResp.Error == nil {
		t.Fatal("expected error for expired token")
	}
	// Must indicate expiry (not just generic error).
	if !strings.Contains(expiredResp.Error.Message, "expired") {
		t.Errorf("expected 'expired' in error message, got %q", expiredResp.Error.Message)
	}
	// Token must not appear in error message.
	if strings.Contains(expiredResp.Error.Message, token) {
		t.Errorf("error message leaks token: %q", expiredResp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test: token never appears in error messages
// ---------------------------------------------------------------------------

// TestSession_TokenNotInErrors verifies that error responses never contain
// the bearer token string.
func TestSession_TokenNotInErrors(t *testing.T) {
	srv := newTestServerWithSessions(t)

	// Init → get token.
	initResp := postMCP(t, srv, mcpInitBody, "")
	token := extractTokenFromInit(t, initResp)

	// Send a request with an invalid tool name to trigger an error response.
	badBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nonexistent_tool","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(badBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)

	body := w.Body.String()
	if strings.Contains(body, token) {
		t.Errorf("response body contains session token: %q", body)
	}
}

// TestTokenRegistry_LookupRace exercises the data race that existed when
// lookup() read tokenEntry fields after releasing the RWMutex. The -race
// detector should report no issues with the fixed implementation.
//
// The race: concurrent goroutines call lookup() while another goroutine
// calls revoke() and revokeWithGrace() on the same token, which mutates
// the entry's fields (revoked, gracePeriodUntil, internalID).
func TestTokenRegistry_LookupRace(t *testing.T) {
	r := newTokenRegistry()

	// Issue a token that will be continuously mutated by writers.
	tok, err := r.issue()
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	const goroutines = 20
	const iterations = 500

	var wg sync.WaitGroup

	// Readers: concurrent lookup() calls — this is where the race manifests.
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Result doesn't matter; we're exercising the race detector.
				_, _ = r.lookup(tok, 0)
			}
		}()
	}

	// Writers: concurrent revoke / revokeWithGrace / re-issue to mutate the
	// entry fields that lookup() reads after releasing the lock in the old code.
	for i := 0; i < goroutines/4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				r.revokeWithGrace(tok, time.Now().Add(100*time.Millisecond))
			}
		}()
	}
	for i := 0; i < goroutines/4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				r.revoke(tok)
			}
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Tests: TokenRegistry disk persistence
// ---------------------------------------------------------------------------

// TestTokenRegistry_PersistSurvivesRestart verifies that tokens written to
// disk by one TokenRegistry instance are loaded correctly by a new instance
// pointed at the same file. This is the core restart-survival scenario from
// design §5.b.
func TestTokenRegistry_PersistSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")

	// Instance 1: issue two tokens and revoke one.
	r1, err := newTokenRegistryFromFile(path)
	if err != nil {
		t.Fatalf("newTokenRegistryFromFile (r1): %v", err)
	}

	tok1, err := r1.issue()
	if err != nil {
		t.Fatalf("r1.issue tok1: %v", err)
	}
	tok2, err := r1.issue()
	if err != nil {
		t.Fatalf("r1.issue tok2: %v", err)
	}
	r1.revoke(tok2)

	// Verify file was written.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("registry file not created after issue: %v", err)
	}

	// Capture internalID of tok1 from r1.
	internalID1, err := r1.lookup(tok1, 0)
	if err != nil {
		t.Fatalf("r1.lookup tok1: %v", err)
	}

	// Instance 2: load from same file — simulates server restart.
	r2, err := newTokenRegistryFromFile(path)
	if err != nil {
		t.Fatalf("newTokenRegistryFromFile (r2): %v", err)
	}

	// tok1 must survive and map to the same internalID.
	id2, err := r2.lookup(tok1, 0)
	if err != nil {
		t.Fatalf("r2.lookup tok1 after restart: %v", err)
	}
	if id2 != internalID1 {
		t.Errorf("internalID changed after restart: before=%q after=%q", internalID1, id2)
	}

	// tok2 was revoked — must still be revoked after restart.
	_, err = r2.lookup(tok2, 0)
	if err == nil {
		t.Error("revoked tok2 should be rejected after restart, but lookup succeeded")
	}
	var revokedErr *tokenRevokedError
	if !errors.As(err, &revokedErr) {
		t.Errorf("expected tokenRevokedError for tok2, got: %v", err)
	}
}

// TestTokenRegistry_PersistRotation verifies that token rotation state
// (revokeWithGrace) is preserved across a simulated restart.
func TestTokenRegistry_PersistRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")

	r1, err := newTokenRegistryFromFile(path)
	if err != nil {
		t.Fatalf("newTokenRegistryFromFile: %v", err)
	}

	oldTok, err := r1.issue()
	if err != nil {
		t.Fatalf("issue old token: %v", err)
	}
	oldInternalID, _ := r1.lookup(oldTok, 0)

	newTok, err := r1.issueFor(oldInternalID)
	if err != nil {
		t.Fatalf("issueFor new token: %v", err)
	}

	// Revoke old token with a far-future grace period (so it's still in grace on reload).
	gracePeriod := time.Now().Add(10 * time.Minute)
	r1.revokeWithGrace(oldTok, gracePeriod)

	// Reload.
	r2, err := newTokenRegistryFromFile(path)
	if err != nil {
		t.Fatalf("newTokenRegistryFromFile (r2): %v", err)
	}

	// New token must work and map to same internalID.
	id, err := r2.lookup(newTok, 0)
	if err != nil {
		t.Fatalf("lookup new token after restart: %v", err)
	}
	if id != oldInternalID {
		t.Errorf("new token internalID changed: want %q got %q", oldInternalID, id)
	}

	// Old token is in grace period — must still be valid (grace not yet expired).
	_, err = r2.lookup(oldTok, 0)
	if err != nil {
		t.Errorf("old token in grace period should still be valid after restart: %v", err)
	}
}

// TestTokenRegistry_PersistDelete verifies that delete() is persisted so that
// a deleted token does not reappear after restart.
func TestTokenRegistry_PersistDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")

	r1, err := newTokenRegistryFromFile(path)
	if err != nil {
		t.Fatalf("newTokenRegistryFromFile: %v", err)
	}

	tok, err := r1.issue()
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	r1.delete(tok)

	// Reload.
	r2, err := newTokenRegistryFromFile(path)
	if err != nil {
		t.Fatalf("newTokenRegistryFromFile (r2): %v", err)
	}

	_, err = r2.lookup(tok, 0)
	if err == nil {
		t.Error("deleted token should not be present after restart")
	}
	var unknownErr *tokenUnknownError
	if !errors.As(err, &unknownErr) {
		t.Errorf("expected tokenUnknownError for deleted token, got: %v", err)
	}
}

// TestSessionManager_RegistryPersistOnNewSessionManager verifies the
// end-to-end wiring: NewSessionManager loads an existing registry file from
// sessionsDir, so tokens issued before a restart are still valid.
func TestSessionManager_RegistryPersistOnNewSessionManager(t *testing.T) {
	dir := t.TempDir()

	// First SessionManager: issue a token, create a session.
	m1 := NewSessionManager(dir)
	tok, err := m1.issueToken()
	if err != nil {
		t.Fatalf("m1.issueToken: %v", err)
	}
	id1, err := m1.validateToken(tok)
	if err != nil {
		t.Fatalf("m1.validateToken: %v", err)
	}
	m1.Stop()

	// Second SessionManager pointing at the same dir (simulates restart).
	m2 := NewSessionManager(dir)
	defer m2.Stop()

	// Token must still be valid and map to the same internalID.
	id2, err := m2.validateToken(tok)
	if err != nil {
		t.Fatalf("m2.validateToken after restart: %v", err)
	}
	if id1 != id2 {
		t.Errorf("internalID changed after restart: before=%q after=%q", id1, id2)
	}
}

// TestTokenRegistry_FreshDirCreatesEmptyRegistry verifies that pointing a
// registry at a non-existent file starts with an empty registry (no error).
func TestTokenRegistry_FreshDirCreatesEmptyRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subdir", "registry.json")

	// Create parent dir (in production this is done by NewSessionManager via
	// os.MkdirAll before the registry is loaded).
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	r, err := newTokenRegistryFromFile(path)
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}

	// Must be empty — issuing a token works.
	tok, err := r.issue()
	if err != nil {
		t.Fatalf("issue on fresh registry: %v", err)
	}
	if tok == "" {
		t.Error("expected non-empty token")
	}
}

// ---------------------------------------------------------------------------
// Test: per-IP rate limiting on campfire_init (design doc §5.b / S9)
// ---------------------------------------------------------------------------

// postMCPFromIP is like postMCP but sets the request's RemoteAddr so that
// the rate limiter sees the given IP.
func postMCPFromIP(t *testing.T, srv *server, body, token, ip string) (jsonRPCResponse, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":12345"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)

	result := w.Result()
	var resp jsonRPCResponse
	if err := json.NewDecoder(result.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp, result.StatusCode
}

// TestInitRateLimit_SameIPBlocked verifies that rapid campfire_init calls from
// the same IP are rate-limited after the per-IP limit is exhausted, returning
// HTTP 429 with a -32000 JSON-RPC error. (Design doc §5.b / adversary finding S9)
func TestInitRateLimit_SameIPBlocked(t *testing.T) {
	dir := t.TempDir()
	const rateLimit = 3
	m := &SessionManager{
		sessionsDir: dir,
		stopCh:      make(chan struct{}),
		maxSessions: defaultMaxSessions,
		initLimiter: newInitRateLimiter(rateLimit, initRateWindow),
	}
	m.registry = newTokenRegistry()
	go m.reaper()
	t.Cleanup(m.Stop)

	srv := &server{
		cfHome:         t.TempDir(),
		beaconDir:      t.TempDir(),
		cfHomeExplicit: true,
		sessManager:    m,
	}

	ip := "10.0.0.1"

	// First rateLimit calls must succeed.
	for i := 0; i < rateLimit; i++ {
		resp, code := postMCPFromIP(t, srv, mcpInitBody, "", ip)
		if code != http.StatusOK {
			t.Fatalf("init %d of %d: expected HTTP 200, got %d", i+1, rateLimit, code)
		}
		if resp.Error != nil {
			t.Fatalf("init %d of %d: unexpected error: code=%d msg=%s", i+1, rateLimit, resp.Error.Code, resp.Error.Message)
		}
	}

	// The (rateLimit+1)th call must be rejected with HTTP 429.
	resp, code := postMCPFromIP(t, srv, mcpInitBody, "", ip)
	if code != http.StatusTooManyRequests {
		t.Errorf("expected HTTP 429 after rate limit exhausted, got %d", code)
	}
	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error in 429 response")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "rate limit exceeded") {
		t.Errorf("expected 'rate limit exceeded' in error message, got %q", resp.Error.Message)
	}
}

// TestInitRateLimit_DifferentIPsIndependent verifies that rate limiting is
// per-IP: one IP hitting the limit does not block a different IP.
func TestInitRateLimit_DifferentIPsIndependent(t *testing.T) {
	dir := t.TempDir()
	const rateLimit = 2
	m := &SessionManager{
		sessionsDir: dir,
		stopCh:      make(chan struct{}),
		maxSessions: defaultMaxSessions,
		initLimiter: newInitRateLimiter(rateLimit, initRateWindow),
	}
	m.registry = newTokenRegistry()
	go m.reaper()
	t.Cleanup(m.Stop)

	srv := &server{
		cfHome:         t.TempDir(),
		beaconDir:      t.TempDir(),
		cfHomeExplicit: true,
		sessManager:    m,
	}

	ipA := "10.0.0.1"
	ipB := "10.0.0.2"

	// Exhaust the limit for ipA.
	for i := 0; i < rateLimit; i++ {
		_, code := postMCPFromIP(t, srv, mcpInitBody, "", ipA)
		if code != http.StatusOK {
			t.Fatalf("ipA init %d: expected 200, got %d", i+1, code)
		}
	}
	_, code := postMCPFromIP(t, srv, mcpInitBody, "", ipA)
	if code != http.StatusTooManyRequests {
		t.Errorf("ipA: expected 429 after limit, got %d", code)
	}

	// ipB must still be allowed — its window is independent.
	for i := 0; i < rateLimit; i++ {
		resp, code := postMCPFromIP(t, srv, mcpInitBody, "", ipB)
		if code != http.StatusOK {
			t.Errorf("ipB init %d: expected 200, got %d (ipA exhaustion must not block ipB)", i+1, code)
		}
		if resp.Error != nil {
			t.Errorf("ipB init %d: unexpected error: %s", i+1, resp.Error.Message)
		}
	}
}

// TestInitRateLimit_SlidingWindowExpiry verifies that the sliding window
// expires old timestamps so that an IP can create sessions again after the
// window elapses.
func TestInitRateLimit_SlidingWindowExpiry(t *testing.T) {
	const limit = 2
	window := 50 * time.Millisecond
	l := newInitRateLimiter(limit, window)

	ip := "10.0.0.1"

	// Consume the limit.
	for i := 0; i < limit; i++ {
		if !l.allow(ip) {
			t.Fatalf("allow %d of %d: expected true", i+1, limit)
		}
	}
	// Immediately blocked.
	if l.allow(ip) {
		t.Error("expected false after limit reached, got true")
	}

	// Wait for window to expire.
	time.Sleep(window + 10*time.Millisecond)

	// Must be allowed again.
	if !l.allow(ip) {
		t.Error("expected allow after window expired, got false")
	}
}

// TestInitRateLimit_XForwardedFor verifies that X-Forwarded-For is used as
// the client IP when present, so requests proxied through Fly.io's edge are
// rate-limited by the actual client IP, not the proxy IP.
func TestInitRateLimit_XForwardedFor(t *testing.T) {
	dir := t.TempDir()
	const rateLimit = 2
	m := &SessionManager{
		sessionsDir: dir,
		stopCh:      make(chan struct{}),
		maxSessions: defaultMaxSessions,
		initLimiter: newInitRateLimiter(rateLimit, initRateWindow),
	}
	m.registry = newTokenRegistry()
	go m.reaper()
	t.Cleanup(m.Stop)

	srv := &server{
		cfHome:         t.TempDir(),
		beaconDir:      t.TempDir(),
		cfHomeExplicit: true,
		sessManager:    m,
	}

	realIP := "203.0.113.5"
	proxyIP := "10.0.0.99" // proxy's RemoteAddr

	makeInitReqWithXFF := func() (jsonRPCResponse, int) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(mcpInitBody))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = proxyIP + ":8080"
		req.Header.Set("X-Forwarded-For", realIP)
		w := httptest.NewRecorder()
		srv.handleMCPSessioned(w, req)
		var resp jsonRPCResponse
		if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp, w.Code
	}

	// Exhaust the limit using the real IP via X-Forwarded-For.
	for i := 0; i < rateLimit; i++ {
		_, code := makeInitReqWithXFF()
		if code != http.StatusOK {
			t.Fatalf("init %d: expected 200, got %d", i+1, code)
		}
	}

	// Next request must be rate-limited.
	_, code := makeInitReqWithXFF()
	if code != http.StatusTooManyRequests {
		t.Errorf("expected 429 for real IP via X-Forwarded-For, got %d", code)
	}

	// A request from a different real IP (same proxy) must still be allowed.
	otherRealIP := "203.0.113.6"
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(mcpInitBody))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = proxyIP + ":8080"
	req.Header.Set("X-Forwarded-For", otherRealIP)
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("different real IP via XFF should be allowed, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Test: reaper close-before-delete ordering (campfire-agent-3bn)
// ---------------------------------------------------------------------------

// TestSession_ReaperCloseBeforeDelete verifies that the reaper calls Close()
// before Delete() so that a concurrent getOrCreate cannot observe a session
// that has been removed from the map but is still being closed.
//
// The bug: Delete(k) then Close() — a concurrent getOrCreate that runs between
// Delete and Close would create a new *Session for the same internalID, storing
// it in the map, and then Close() on the old session would tear down the store
// that the new session shares (they use the same cfHome on disk).
//
// The fix: Close() then Delete(k) — the session is fully torn down before it
// disappears from the map.  getOrCreate will not find it in the map, create a
// fresh session, and store that.
//
// This test runs with -race to catch the data race between the simulated reaper
// goroutine and the concurrent getOrCreate goroutines.
func TestSession_ReaperCloseBeforeDelete(t *testing.T) {
	dir := t.TempDir()
	m := &SessionManager{
		sessionsDir:         dir,
		stopCh:              make(chan struct{}),
		maxSessions:         defaultMaxSessions,
		idleTimeoutOverride: 50 * time.Millisecond,
	}
	m.registry = newTokenRegistry()
	go m.reaper()
	t.Cleanup(m.Stop)

	// Create a session.
	token, err := m.issueToken()
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	origSess, err := m.getOrCreate(token)
	if err != nil {
		t.Fatalf("getOrCreate (initial): %v", err)
	}

	// Back-date to trigger idle reap.
	origSess.mu.Lock()
	origSess.lastActivity = time.Now().Add(-(idleTimeout + time.Second))
	origSess.mu.Unlock()

	// Launch many concurrent getOrCreate goroutines that race with the reaper.
	// After the reaper fires, every successful getOrCreate must return a session
	// with a non-nil store (not a half-closed session).
	const goroutines = 20
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	sessions := make([]*Session, goroutines)

	// Give the reaper time to fire (it runs every idleTimeoutOverride/2 = 25ms).
	time.Sleep(75 * time.Millisecond)

	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, e := m.getOrCreate(token)
			sessions[i] = s
			errs[i] = e
		}()
	}
	wg.Wait()

	// At least one goroutine must succeed.
	var anySuccess bool
	for i := 0; i < goroutines; i++ {
		if errs[i] == nil {
			anySuccess = true
			sess := sessions[i]
			// The returned session must have a live (non-nil) store.
			sess.mu.Lock()
			st := sess.st
			sess.mu.Unlock()
			if st == nil {
				t.Errorf("goroutine %d: getOrCreate returned a session with nil store (close-before-delete bug)", i)
			}
			// Must not be the original (reaped) session.
			if sess == origSess {
				t.Errorf("goroutine %d: getOrCreate returned the reaped session object", i)
			}
		}
	}
	if !anySuccess {
		t.Fatal("all goroutines failed to getOrCreate after reap — expected at least one success")
	}
}

// ---------------------------------------------------------------------------
// Audit: campfire_revoke_session writes an audit entry before closing session
// ---------------------------------------------------------------------------

// TestAudit_RevokeSessionWritesEntry verifies that campfire_revoke_session
// logs a "revoke_session" audit entry before the session is torn down (§5.e).
func TestAudit_RevokeSessionWritesEntry(t *testing.T) {
	srv := newTestServerWithSessions(t)

	// Init to create identity and establish the audit campfire.
	initResp := postMCP(t, srv, mcpInitBody, "")
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected token from campfire_init")
	}

	// Grab the session and its auditWriter before revoking.
	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("expected session to exist after campfire_init")
	}
	sess.mu.Lock()
	aw := sess.auditWriter
	sess.mu.Unlock()
	if aw == nil {
		t.Skip("auditWriter is nil (audit campfire setup failed in test env); skipping")
	}

	// Capture written count before revoke.
	beforeRevoke := aw.Written()

	// Revoke the session — this should log the entry then close the session.
	revokeBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_revoke_session","arguments":{}}}`
	revokeResp := postMCP(t, srv, revokeBody, token)
	if revokeResp.Error != nil {
		t.Fatalf("campfire_revoke_session failed: %v", revokeResp.Error.Message)
	}

	// After revoke, aw.Close() was called (drains channel). Written count must have
	// increased by at least 1 (the revoke_session entry).
	afterRevoke := aw.Written()
	if afterRevoke <= beforeRevoke {
		t.Errorf("expected audit entry for revoke_session: written count before=%d after=%d", beforeRevoke, afterRevoke)
	}
}

// ---------------------------------------------------------------------------
// Audit: campfire_rotate_token writes an audit entry
// ---------------------------------------------------------------------------

// TestAudit_RotateTokenWritesEntry verifies that campfire_rotate_token logs
// a "rotate_token" audit entry after successful rotation (§5.e).
func TestAudit_RotateTokenWritesEntry(t *testing.T) {
	dir := t.TempDir()
	m := &SessionManager{
		sessionsDir:         dir,
		stopCh:              make(chan struct{}),
		maxSessions:         defaultMaxSessions,
		rotationGracePeriod: 50 * time.Millisecond,
	}
	m.registry = newTokenRegistry()
	m.initLimiter = newInitRateLimiter(defaultInitRateLimit, initRateWindow)
	go m.reaper()
	t.Cleanup(m.Stop)

	srv := &server{
		cfHome:         t.TempDir(),
		beaconDir:      t.TempDir(),
		cfHomeExplicit: true,
		sessManager:    m,
	}

	// Init to create identity and establish the audit campfire.
	initResp := postMCP(t, srv, mcpInitBody, "")
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected token from campfire_init")
	}

	// Grab the auditWriter before rotation.
	sess := m.getSession(token)
	if sess == nil {
		t.Fatal("expected session after campfire_init")
	}
	sess.mu.Lock()
	aw := sess.auditWriter
	sess.mu.Unlock()
	if aw == nil {
		t.Skip("auditWriter is nil (audit campfire setup failed in test env); skipping")
	}

	beforeRotate := aw.Written()

	// Rotate the token.
	rotateBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_rotate_token","arguments":{}}}`
	rotateResp := postMCP(t, srv, rotateBody, token)
	if rotateResp.Error != nil {
		t.Fatalf("campfire_rotate_token failed: %v", rotateResp.Error.Message)
	}

	// Flush so async writes complete.
	aw.Flush()

	afterRotate := aw.Written()
	if afterRotate <= beforeRotate {
		t.Errorf("expected audit entry for rotate_token: written count before=%d after=%d", beforeRotate, afterRotate)
	}
}

// ---------------------------------------------------------------------------
// Session tuning: tokenTTL / maxSessions / rotationGracePeriod wiring
// ---------------------------------------------------------------------------

// TestSessionManagerTuning verifies that the three operator-tunable session
// configuration fields (tokenTTL, maxSessions, rotationGracePeriod) are
// respected when set on the SessionManager.
func TestSessionManagerTuning(t *testing.T) {
	t.Run("tokenTTL enforced", func(t *testing.T) {
		dir := t.TempDir()
		sm := NewSessionManager(dir)
		defer sm.Stop()
		sm.tokenTTL = 1 * time.Millisecond // very short TTL

		// Issue a token and wait for it to expire.
		tok, err := sm.issueToken()
		if err != nil {
			t.Fatalf("issueToken: %v", err)
		}
		time.Sleep(5 * time.Millisecond)

		_, err = sm.validateToken(tok)
		if err == nil {
			t.Fatal("expected expired error, got nil")
		}
		var expErr *tokenExpiredError
		if !errors.As(err, &expErr) {
			t.Fatalf("expected tokenExpiredError, got %T: %v", err, err)
		}
	})

	t.Run("maxSessions enforced", func(t *testing.T) {
		dir := t.TempDir()
		sm := NewSessionManager(dir)
		defer sm.Stop()
		sm.maxSessions = 1

		// First session must succeed.
		tok1, err := sm.issueToken()
		if err != nil {
			t.Fatalf("issueToken 1: %v", err)
		}
		if _, err := sm.getOrCreate(tok1); err != nil {
			t.Fatalf("getOrCreate 1: %v", err)
		}

		// Second session must be rejected.
		tok2, err := sm.issueToken()
		if err != nil {
			t.Fatalf("issueToken 2: %v", err)
		}
		_, err = sm.getOrCreate(tok2)
		if err == nil {
			t.Fatal("expected session limit error, got nil")
		}
		var limitErr *sessionLimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("expected sessionLimitError, got %T: %v", err, err)
		}
		if limitErr.limit != 1 {
			t.Errorf("expected limit=1, got %d", limitErr.limit)
		}
	})

	t.Run("rotationGracePeriod enforced", func(t *testing.T) {
		dir := t.TempDir()
		sm := NewSessionManager(dir)
		defer sm.Stop()
		sm.rotationGracePeriod = 50 * time.Millisecond

		tok, err := sm.issueToken()
		if err != nil {
			t.Fatalf("issueToken: %v", err)
		}

		newTok, err := sm.rotateToken(tok)
		if err != nil {
			t.Fatalf("rotateToken: %v", err)
		}

		// Old token should still be valid during grace period.
		if _, err := sm.validateToken(tok); err != nil {
			t.Errorf("old token should be valid during grace period, got: %v", err)
		}

		// New token must be valid.
		if _, err := sm.validateToken(newTok); err != nil {
			t.Errorf("new token should be valid, got: %v", err)
		}

		// Wait for grace period to expire.
		time.Sleep(100 * time.Millisecond)

		// Old token should now be gone from the registry (deleted by background goroutine).
		_, err = sm.validateToken(tok)
		if err == nil {
			t.Error("old token should be invalid after grace period")
		}
	})
}

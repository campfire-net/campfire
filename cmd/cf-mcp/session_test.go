package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
// idle for longer than idleTimeout, the reaper closes the store (st == nil).
// This test injects a short timeout by directly manipulating lastActivity.
func TestSession_IdleTimeoutClosesStore(t *testing.T) {
	srv := newTestServerWithSessions(t)

	// Create a session.
	initResp := postMCP(t, srv, mcpInitBody, "")
	token := extractTokenFromInit(t, initResp)

	// Retrieve the session and back-date its lastActivity past idleTimeout.
	v, ok := srv.sessManager.sessions.Load(token)
	if !ok {
		t.Fatal("session not found in manager after init")
	}
	sess := v.(*Session)

	sess.mu.Lock()
	sess.lastActivity = time.Now().Add(-(idleTimeout + time.Second))
	sess.mu.Unlock()

	// Verify store is open before reaping.
	sess.mu.Lock()
	storeBefore := sess.st
	sess.mu.Unlock()
	if storeBefore == nil {
		t.Fatal("expected store to be open before reaping")
	}

	// Run one reap cycle directly (avoids sleeping for idleTimeout/2).
	srv.sessManager.sessions.Range(func(k, v interface{}) bool {
		s := v.(*Session)
		s.mu.Lock()
		idle := time.Since(s.lastActivity) > idleTimeout
		s.mu.Unlock()
		if idle {
			s.Close()
		}
		return true
	})

	// Store must be nil after the reap.
	sess.mu.Lock()
	storeAfter := sess.st
	sess.mu.Unlock()
	if storeAfter != nil {
		t.Error("expected store to be closed (nil) after idle timeout reap")
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

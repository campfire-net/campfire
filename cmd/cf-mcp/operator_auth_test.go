package main

// operator_auth_test.go — Tests for forge-tk- bearer auth dispatch.
//
// Tests verify:
//   - TestForgeTokenAuth_ValidToken: forge-tk- token resolved via Forge mock → session token returned
//   - TestForgeTokenAuth_InvalidToken: Forge returns 401 → 401 or -32000 error
//   - TestForgeTokenAuth_PrefixMismatch: sess- token falls through to normal validation (no Forge call)
//   - TestForgeTokenAuth_SkipsEnsureAccount: EnsureOperatorAccount is NOT called for forge-tk- auth
//   - TestForgeTokenAuth_SessionRegistered: operatorSessionIndex.AccountForToken returns correct account ID

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
)

// newForgeResolveServer returns a test HTTP server that responds to
// GET /v1/keys with a mock KeyRecord containing accountID.
// It also records Authorization headers from incoming requests.
func newForgeResolveServer(t *testing.T, accountID string, statusCode int) (*httptest.Server, *[]string) {
	t.Helper()
	var capturedAuthHeaders []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/keys" {
			t.Errorf("unexpected forge path: %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		capturedAuthHeaders = append(capturedAuthHeaders, r.Header.Get("Authorization"))

		if statusCode != http.StatusOK {
			w.WriteHeader(statusCode)
			return
		}

		// Return a minimal keyListResponse with one KeyRecord.
		resp := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{
					"token_hash_prefix": "forge-tk-test",
					"account_id":        accountID,
					"role":              "agent",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	return srv, &capturedAuthHeaders
}

// newSessionedServerWithForge creates a SessionManager-backed server with
// a mock Forge client configured for forge-tk- auth.
func newSessionedServerWithForge(t *testing.T, forgeClient *forge.Client) *server {
	t.Helper()
	dir := t.TempDir()
	sm := NewSessionManager(dir)
	sm.forgeAccounts = &forgeAccountManager{
		forge:           forgeClient,
		parentAccountID: "test-parent",
	}
	t.Cleanup(sm.Stop)

	srv := &server{
		cfHome:             dir,
		beaconDir:          dir,
		sessManager:        sm,
		cfHomeExplicit:     true,
		operatorSessionIdx: newOperatorSessionIndex(),
	}
	return srv
}

// postMCPRequest sends a JSON-RPC request to srv.handleMCPSessioned with the
// given Authorization header and returns the recorder.
func postMCPRequest(t *testing.T, srv *server, authHeader, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)
	return w
}

// decodeRPCResponse decodes the JSON-RPC response from the recorder.
func decodeRPCResponse(t *testing.T, w *httptest.ResponseRecorder) jsonRPCResponse {
	t.Helper()
	var resp jsonRPCResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding RPC response: %v", err)
	}
	return resp
}

// extractSessionToken extracts the "Session token:" from a campfire_init-style response.
func extractSessionTokenFromResponse(t *testing.T, resp jsonRPCResponse) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("cannot extract content from response: %v", string(b))
	}
	text := result.Content[0].Text
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Session token: ") {
			return strings.TrimPrefix(line, "Session token: ")
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Test: valid forge-tk- token → session token returned
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_ValidToken verifies that a request with a valid forge-tk-
// bearer token causes cf-mcp to call Forge's GET /v1/keys, receive an account
// ID, and return a session token in the response.
func TestForgeTokenAuth_ValidToken(t *testing.T) {
	const accountID = "acct-abc123"
	forgeSrv, capturedHeaders := newForgeResolveServer(t, accountID, http.StatusOK)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "Bearer forge-tk-test", body)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d; body: %s", w.Code, w.Body.String())
	}

	resp := decodeRPCResponse(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	// The Forge server must have been called with the forge-tk- key as the bearer.
	if len(*capturedHeaders) == 0 {
		t.Fatal("expected Forge GET /v1/keys to be called, but no requests received")
	}
	if !strings.Contains((*capturedHeaders)[0], "forge-tk-test") {
		t.Errorf("expected forge-tk-test in Forge request Authorization header, got: %q", (*capturedHeaders)[0])
	}

	// A session token must be present in the response text.
	sessToken := extractSessionTokenFromResponse(t, resp)
	if sessToken == "" {
		t.Error("expected 'Session token:' in forge-tk- auth response")
	}
	if strings.HasPrefix(sessToken, "forge-tk-") {
		t.Errorf("session token must be a new cf-issued token, not the forge key; got %q", sessToken)
	}
}

// ---------------------------------------------------------------------------
// Test: invalid forge-tk- token → 401
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_InvalidToken verifies that when Forge returns 401 for the
// forge-tk- key, cf-mcp responds with HTTP 401 and a -32000 JSON-RPC error.
func TestForgeTokenAuth_InvalidToken(t *testing.T) {
	forgeSrv, _ := newForgeResolveServer(t, "", http.StatusUnauthorized)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "Bearer forge-tk-bad-token", body)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 for invalid forge-tk- token, got %d", w.Code)
	}

	var resp jsonRPCResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding RPC response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error for invalid forge-tk- token")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "forge-tk-") {
		t.Errorf("expected 'forge-tk-' in error message, got: %q", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test: non-forge bearer token falls through to normal validation
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_PrefixMismatch verifies that a "Bearer sess-..." token
// (no forge-tk- prefix) is validated against the token registry as normal,
// without making any Forge API call.
func TestForgeTokenAuth_PrefixMismatch(t *testing.T) {
	forgeCallCount := 0
	forgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forgeCallCount++
		t.Errorf("Forge API must NOT be called for non-forge-tk- tokens; got request to %s", r.URL.Path)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	t.Cleanup(forgeSrv.Close)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_read","arguments":{"campfire_id":"abc"}}}`
	// Use a plausible but invalid session token (not forge-tk- prefix).
	w := postMCPRequest(t, srv, "Bearer sess-not-a-real-token", body)

	// Should return 401 (token not in registry) — NOT a 500 from Forge.
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 for unknown session token, got %d; body: %s", w.Code, w.Body.String())
	}

	if forgeCallCount > 0 {
		t.Errorf("Forge was called %d times for a non-forge-tk- token; must not be called", forgeCallCount)
	}
}

// ---------------------------------------------------------------------------
// Test: EnsureOperatorAccount is NOT called for forge-tk- auth
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_SkipsEnsureAccount verifies that the forge-tk- auth
// path does NOT call EnsureOperatorAccount (which would auto-provision a
// sub-account). The account already exists — forge-tk- auth skips provisioning.
func TestForgeTokenAuth_SkipsEnsureAccount(t *testing.T) {
	const accountID = "acct-existing-operator"

	ensureAccountCalled := false
	forgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts" && r.Method == http.MethodPost {
			// This is the EnsureOperatorAccount call — it must NOT be made.
			ensureAccountCalled = true
			t.Error("EnsureOperatorAccount must not be called for forge-tk- auth")
			http.Error(w, "not expected", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/v1/keys" {
			// Return valid key record.
			resp := map[string]interface{}{
				"object": "list",
				"data": []map[string]interface{}{
					{"token_hash_prefix": "forge-tk-test", "account_id": accountID, "role": "agent"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp) //nolint:errcheck
			return
		}
		http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
	}))
	t.Cleanup(forgeSrv.Close)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "Bearer forge-tk-operator-key", body)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if ensureAccountCalled {
		t.Error("EnsureOperatorAccount was called for forge-tk- auth; it must be skipped")
	}
}

// ---------------------------------------------------------------------------
// Test: revoked forge-tk- key → 401, no session issued
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_RevokedKey verifies that when Forge returns a KeyRecord
// with Revoked=true, cf-mcp responds with HTTP 401 and does NOT issue a session
// token or register the account in operatorSessionIndex.
func TestForgeTokenAuth_RevokedKey(t *testing.T) {
	const accountID = "acct-123"

	forgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/keys" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		resp := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{
					"token_hash_prefix": "forge-tk-revoked",
					"account_id":        accountID,
					"role":              "agent",
					"revoked":           true,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	t.Cleanup(forgeSrv.Close)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "Bearer forge-tk-revoked-key", body)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 for revoked forge-tk- key, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp jsonRPCResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding RPC response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error for revoked forge-tk- key")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "revoked") {
		t.Errorf("expected 'revoked' in error message, got: %q", resp.Error.Message)
	}

	// operatorSessionIndex must NOT have been modified.
	_, ok := srv.operatorSessionIdx.AccountForToken(accountID)
	if ok {
		t.Error("operatorSessionIndex must not contain account ID for revoked key")
	}
}

// ---------------------------------------------------------------------------
// Test: empty account ID from Forge → 500, no session issued
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_EmptyAccountID verifies that when Forge returns a KeyRecord
// with AccountID="" (and Revoked=false), cf-mcp responds with HTTP 500 and does
// NOT issue a session token or register anything in operatorSessionIndex.
func TestForgeTokenAuth_EmptyAccountID(t *testing.T) {
	forgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/keys" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		resp := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{
					"token_hash_prefix": "forge-tk-empty",
					"account_id":        "",
					"role":              "agent",
					"revoked":           false,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	t.Cleanup(forgeSrv.Close)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "Bearer forge-tk-empty-acct", body)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected HTTP 500 for empty account ID, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp jsonRPCResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding RPC response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error for empty account ID")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "empty account ID") {
		t.Errorf("expected 'empty account ID' in error message, got: %q", resp.Error.Message)
	}

	// operatorSessionIndex must NOT have been modified.
	_, ok := srv.operatorSessionIdx.AccountForToken("")
	if ok {
		t.Error("operatorSessionIndex must not contain empty account ID")
	}
}

// ---------------------------------------------------------------------------
// Test: operatorSessionIndex maps session token → account ID
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_SessionRegistered verifies that after a successful
// forge-tk- auth, operatorSessionIndex.AccountForToken(sessionToken) returns
// the account ID resolved from Forge.
func TestForgeTokenAuth_SessionRegistered(t *testing.T) {
	const accountID = "acct-registered-test"
	forgeSrv, _ := newForgeResolveServer(t, accountID, http.StatusOK)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "Bearer forge-tk-test", body)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d; body: %s", w.Code, w.Body.String())
	}

	resp := decodeRPCResponse(t, w)
	sessToken := extractSessionTokenFromResponse(t, resp)
	if sessToken == "" {
		t.Fatal("expected session token in response, got empty string")
	}

	// The session index must map the session token → accountID.
	gotAccountID, ok := srv.operatorSessionIdx.AccountForToken(sessToken)
	if !ok {
		t.Fatalf("operatorSessionIdx.AccountForToken(%q) returned ok=false; session not registered", sessToken)
	}
	if gotAccountID != accountID {
		t.Errorf("operatorSessionIdx.AccountForToken: got accountID=%q, want %q", gotAccountID, accountID)
	}
}

// ---------------------------------------------------------------------------
// Test: operator session cannot rotate token (campfire-agent-3af)
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_RotateTokenBlocked verifies that campfire_rotate_token
// returns HTTP 400 for operator sessions (forge-tk- auth), not a successful rotation.
// Rotation would break TTL=0 semantics since the new token wouldn't be in operatorSessionIdx.
func TestForgeTokenAuth_RotateTokenBlocked(t *testing.T) {
	const accountID = "acct-rotate-test"
	forgeSrv, _ := newForgeResolveServer(t, accountID, http.StatusOK)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	// Step 1: authenticate with forge-tk- to get a session token.
	initBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "Bearer forge-tk-test", initBody)
	if w.Code != http.StatusOK {
		t.Fatalf("init failed: HTTP %d; body: %s", w.Code, w.Body.String())
	}
	resp := decodeRPCResponse(t, w)
	sessToken := extractSessionTokenFromResponse(t, resp)
	if sessToken == "" {
		t.Fatal("expected session token from forge-tk- init")
	}

	// Step 2: try to rotate the operator session token — must be rejected.
	rotateBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_rotate_token","arguments":{}}}`
	w2 := postMCPRequest(t, srv, "Bearer "+sessToken, rotateBody)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("expected HTTP 400 for rotate_token on operator session, got %d; body: %s", w2.Code, w2.Body.String())
	}

	var rotResp jsonRPCResponse
	if err := json.NewDecoder(w2.Body).Decode(&rotResp); err != nil {
		t.Fatalf("decoding rotate response: %v", err)
	}
	if rotResp.Error == nil {
		t.Fatal("expected JSON-RPC error for rotate on operator session")
	}
	if rotResp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", rotResp.Error.Code)
	}
	// Verify the operator session token is still valid (not consumed by rotation).
	if _, ok := srv.operatorSessionIdx.AccountForToken(sessToken); !ok {
		t.Error("operator session token should still be in index after blocked rotation")
	}
}

// ---------------------------------------------------------------------------
// Test: forge-tk- re-auth reuses existing session token (campfire-agent-0t0)
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_ReuseExistingToken verifies that when a forge-tk- API key
// is presented for an account that already has a live session token, the same
// session token is returned rather than issuing a new one. This prevents
// unbounded token accumulation in the registry.
func TestForgeTokenAuth_ReuseExistingToken(t *testing.T) {
	const accountID = "acct-reuse-test"
	forgeSrv, _ := newForgeResolveServer(t, accountID, http.StatusOK)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`

	// First auth: get initial session token.
	w1 := postMCPRequest(t, srv, "Bearer forge-tk-test", body)
	if w1.Code != http.StatusOK {
		t.Fatalf("first init failed: HTTP %d; body: %s", w1.Code, w1.Body.String())
	}
	resp1 := decodeRPCResponse(t, w1)
	token1 := extractSessionTokenFromResponse(t, resp1)
	if token1 == "" {
		t.Fatal("expected session token from first forge-tk- init")
	}

	// Second auth: same forge-tk- key should return the same session token.
	w2 := postMCPRequest(t, srv, "Bearer forge-tk-test", body)
	if w2.Code != http.StatusOK {
		t.Fatalf("second init failed: HTTP %d; body: %s", w2.Code, w2.Body.String())
	}
	resp2 := decodeRPCResponse(t, w2)
	token2 := extractSessionTokenFromResponse(t, resp2)
	if token2 == "" {
		t.Fatal("expected session token from second forge-tk- init")
	}

	// The same token must be returned — no new token issued.
	if token1 != token2 {
		t.Errorf("expected same session token on re-auth: first=%q, second=%q", token1, token2)
	}

	// Only one token should be in the index for this account.
	tokens := srv.operatorSessionIdx.TokensForAccount(accountID)
	if len(tokens) != 1 {
		t.Errorf("expected 1 token in index for account, got %d: %v", len(tokens), tokens)
	}
}

// ---------------------------------------------------------------------------
// Test: campfire_revoke_session removes token from operatorSessionIndex (campfire-agent-kjv)
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_RevokeSessionCleansIndex verifies that revoking an operator
// session token removes it from operatorSessionIdx, preventing a desync where a
// revoked token stays live in the index with TTL=0 semantics.
func TestForgeTokenAuth_RevokeSessionCleansIndex(t *testing.T) {
	const accountID = "acct-revoke-clean"
	forgeSrv, _ := newForgeResolveServer(t, accountID, http.StatusOK)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	// Authenticate to get a session token.
	initBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "Bearer forge-tk-test", initBody)
	if w.Code != http.StatusOK {
		t.Fatalf("init failed: HTTP %d", w.Code)
	}
	resp := decodeRPCResponse(t, w)
	sessToken := extractSessionTokenFromResponse(t, resp)
	if sessToken == "" {
		t.Fatal("expected session token from init")
	}

	// Confirm it's in the index.
	if _, ok := srv.operatorSessionIdx.AccountForToken(sessToken); !ok {
		t.Fatal("token should be in index before revoke")
	}

	// Revoke the session.
	revokeBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_revoke_session","arguments":{}}}`
	w2 := postMCPRequest(t, srv, "Bearer "+sessToken, revokeBody)
	if w2.Code != http.StatusOK {
		t.Fatalf("revoke failed: HTTP %d; body: %s", w2.Code, w2.Body.String())
	}

	// Token must be removed from the index after revocation.
	if _, ok := srv.operatorSessionIdx.AccountForToken(sessToken); ok {
		t.Error("token must be removed from operatorSessionIdx after campfire_revoke_session")
	}
}

// ---------------------------------------------------------------------------
// Test: revoked forge-tk- key kills existing operator sessions (campfire-agent-j2r)
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_RevokedKeyRejectsOldSessionToken verifies that after a
// forge-tk- key is revoked, a subsequent Bearer request using the OLD session
// token (not the forge-tk- key) is rejected with HTTP 401. Without this,
// a TTL=0 session token issued before revocation would remain valid indefinitely.
// (campfire-agent-bdy)
func TestForgeTokenAuth_RevokedKeyRejectsOldSessionToken(t *testing.T) {
	const accountID = "acct-bdy-reject"

	revoked := false
	forgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/keys" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		resp := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{
					"token_hash_prefix": "forge-tk-bdy",
					"account_id":        accountID,
					"role":              "agent",
					"revoked":           revoked,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	t.Cleanup(forgeSrv.Close)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	// Step 1: authenticate while key is valid → receive session token.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w1 := postMCPRequest(t, srv, "Bearer forge-tk-bdy", body)
	if w1.Code != http.StatusOK {
		t.Fatalf("initial auth failed: HTTP %d; body: %s", w1.Code, w1.Body.String())
	}
	resp1 := decodeRPCResponse(t, w1)
	sessToken := extractSessionTokenFromResponse(t, resp1)
	if sessToken == "" {
		t.Fatal("expected session token from initial forge-tk- auth")
	}

	// Confirm session token is in the index.
	if _, ok := srv.operatorSessionIdx.AccountForToken(sessToken); !ok {
		t.Fatal("session token should be in index after initial auth")
	}

	// Step 2: revoke the forge-tk- key and present it again so the server
	// purges all operator sessions for this account.
	revoked = true
	w2 := postMCPRequest(t, srv, "Bearer forge-tk-bdy", body)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 for revoked forge-tk- key, got %d", w2.Code)
	}

	// Step 3: make a second request using the OLD session token (not forge-tk-).
	// The session was purged when the key was revoked — this must return 401.
	w3 := postMCPRequest(t, srv, "Bearer "+sessToken, body)
	if w3.Code != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 for old session token after key revocation, got %d; body: %s", w3.Code, w3.Body.String())
	}

	var resp3 jsonRPCResponse
	if err := json.NewDecoder(w3.Body).Decode(&resp3); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp3.Error == nil {
		t.Fatal("expected JSON-RPC error for old session token after key revocation")
	}
	if resp3.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp3.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// Test: forge-tk- re-auth with multiple tokens picks one, does not accumulate
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_MultiTokenReuseNoAccumulation verifies that when multiple
// session tokens are already registered for an account, a new forge-tk- auth
// request picks existing[0] and does not add a third token. The index must
// remain at 2 entries, not grow to 3.
// (campfire-agent-lpv)
func TestForgeTokenAuth_MultiTokenReuseNoAccumulation(t *testing.T) {
	const accountID = "acct-lpv-multi"
	forgeSrv, _ := newForgeResolveServer(t, accountID, http.StatusOK)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	// Manually register two tokens for the same account in the index by
	// issuing them through the session manager (so they are valid in the registry).
	tok1, err := srv.sessManager.issueForID(accountID)
	if err != nil {
		t.Fatalf("issuing tok1: %v", err)
	}
	tok2, err := srv.sessManager.issueForID(accountID)
	if err != nil {
		t.Fatalf("issuing tok2: %v", err)
	}
	srv.operatorSessionIdx.Register(accountID, tok1)
	srv.operatorSessionIdx.Register(accountID, tok2)

	// Confirm two tokens are present before the forge-tk- request.
	if got := len(srv.operatorSessionIdx.TokensForAccount(accountID)); got != 2 {
		t.Fatalf("expected 2 tokens before forge-tk- auth, got %d", got)
	}

	// Make a forge-tk- auth request: the server must pick one of the existing
	// tokens (existing[0]) and return it — not issue a new one.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "Bearer forge-tk-test", body)
	if w.Code != http.StatusOK {
		t.Fatalf("forge-tk- auth failed: HTTP %d; body: %s", w.Code, w.Body.String())
	}
	resp := decodeRPCResponse(t, w)
	returnedToken := extractSessionTokenFromResponse(t, resp)
	if returnedToken == "" {
		t.Fatal("expected session token from forge-tk- auth")
	}

	// The returned token must be one of the two pre-existing tokens.
	if returnedToken != tok1 && returnedToken != tok2 {
		t.Errorf("expected returned token to be tok1 or tok2 (reuse), got %q", returnedToken)
	}

	// The index must still have exactly 2 tokens — no new token accumulated.
	tokens := srv.operatorSessionIdx.TokensForAccount(accountID)
	if len(tokens) != 2 {
		t.Errorf("expected index to have 2 tokens after forge-tk- re-auth, got %d: %v", len(tokens), tokens)
	}
}

// ---------------------------------------------------------------------------
// Test: getOrCreateOperator failure does not remove pre-existing reused token
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_GetOrCreateFailurePreservesReusedToken verifies that when
// getOrCreateOperator fails AFTER the token-reuse path, the pre-existing tokens
// are NOT removed from operatorSessionIdx. The cleanup at main.go:4349-4357
// only removes a token when it was freshly issued (len==1 and matches), not
// when multiple tokens were already registered (reuse scenario).
// (campfire-agent-4nf)
func TestForgeTokenAuth_GetOrCreateFailurePreservesReusedToken(t *testing.T) {
	const accountID = "acct-4nf-preserve"
	forgeSrv, _ := newForgeResolveServer(t, accountID, http.StatusOK)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	// Register TWO tokens for the account directly in the index WITHOUT issuing
	// them through the session registry. This simulates the "multiple pre-existing
	// tokens" scenario (lpv). When getOrCreateOperator is called with existing[0],
	// registry.lookup will fail (token not in registry), triggering the cleanup path.
	// With 2 tokens, the cleanup condition (len==1) is false → neither is removed.
	const fakeToken1 = "sess-4nf-fake-token-alpha"
	const fakeToken2 = "sess-4nf-fake-token-beta"
	srv.operatorSessionIdx.Register(accountID, fakeToken1)
	srv.operatorSessionIdx.Register(accountID, fakeToken2)

	// Confirm two tokens are present.
	if got := len(srv.operatorSessionIdx.TokensForAccount(accountID)); got != 2 {
		t.Fatalf("expected 2 tokens in index before request, got %d", got)
	}

	// Make a forge-tk- auth request. The forge server resolves the key successfully,
	// the code finds 2 existing tokens and reuses existing[0]. Then getOrCreateOperator
	// fails because that token is not in the session registry. The cleanup at
	// main.go:4352 checks len(existing)==1 which is false (len==2), so it skips
	// removal. Both tokens must remain in the index.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "Bearer forge-tk-test", body)

	// The response is HTTP 200 with a JSON-RPC error body (non-limit errors don't
	// set a non-200 HTTP status — the error is encoded in the JSON-RPC envelope).
	var errResp jsonRPCResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if errResp.Error == nil {
		t.Fatalf("expected JSON-RPC error from getOrCreateOperator failure, got success; body: %s", w.Body.String())
	}

	// Both pre-existing tokens must still be in the index.
	tokensAfter := srv.operatorSessionIdx.TokensForAccount(accountID)
	if len(tokensAfter) != 2 {
		t.Errorf("expected 2 tokens in index after getOrCreateOperator failure, got %d: %v", len(tokensAfter), tokensAfter)
	}

	// Verify both specific tokens are still present.
	found1, found2 := false, false
	for _, tok := range tokensAfter {
		if tok == fakeToken1 {
			found1 = true
		}
		if tok == fakeToken2 {
			found2 = true
		}
	}
	if !found1 {
		t.Errorf("fakeToken1 was removed from index; it should have been preserved (reuse path, len>1)")
	}
	if !found2 {
		t.Errorf("fakeToken2 was removed from index; it should have been preserved (reuse path, len>1)")
	}
}

// ---------------------------------------------------------------------------
// Test: revoked forge-tk- key kills existing operator sessions (campfire-agent-j2r)
// ---------------------------------------------------------------------------

// TestForgeTokenAuth_RevokedKeyKillsExistingSessions verifies that when a forge-tk-
// key is detected as revoked, all existing operator session tokens for that account
// are also invalidated — not just the forge-tk- request itself. Without this,
// a client holding a session token issued under the revoked key could continue
// making requests indefinitely (TTL=0 sessions never expire).
func TestForgeTokenAuth_RevokedKeyKillsExistingSessions(t *testing.T) {
	const accountID = "acct-revoke-sessions"

	// First, the key is valid.
	revoked := false
	forgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/keys" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		resp := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{
					"token_hash_prefix": "forge-tk-test",
					"account_id":        accountID,
					"role":              "agent",
					"revoked":           revoked, // toggled mid-test
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	t.Cleanup(forgeSrv.Close)

	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	// Step 1: authenticate while key is valid → get session token.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w1 := postMCPRequest(t, srv, "Bearer forge-tk-test", body)
	if w1.Code != http.StatusOK {
		t.Fatalf("initial auth failed: HTTP %d", w1.Code)
	}
	resp1 := decodeRPCResponse(t, w1)
	sessToken := extractSessionTokenFromResponse(t, resp1)
	if sessToken == "" {
		t.Fatal("expected session token from initial auth")
	}

	// Confirm session is in the index.
	if _, ok := srv.operatorSessionIdx.AccountForToken(sessToken); !ok {
		t.Fatal("session token should be in index after initial auth")
	}

	// Step 2: revoke the forge-tk- key.
	revoked = true

	// Step 3: present the revoked forge-tk- key again.
	w2 := postMCPRequest(t, srv, "Bearer forge-tk-test", body)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 for revoked key, got %d; body: %s", w2.Code, w2.Body.String())
	}

	// Step 4: the existing session token must now be gone from the index.
	if _, ok := srv.operatorSessionIdx.AccountForToken(sessToken); ok {
		t.Error("existing session token must be removed from index when forge key is revoked")
	}
}

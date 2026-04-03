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
// Test: operatorSessionIndex maps session token → account ID
// ---------------------------------------------------------------------------

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

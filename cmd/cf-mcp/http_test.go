package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// POST /mcp — JSON-RPC over HTTP
// ---------------------------------------------------------------------------

// TestHTTP_CampfireInit verifies that POST /mcp with a campfire_init tool call
// returns a valid JSON-RPC response containing a public key.
func TestHTTP_CampfireInit(t *testing.T) {
	srv := newTestServer(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	srv.handleMCP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if rpcResp.JSONRPC != "2.0" {
		t.Errorf("expected jsonrpc=2.0, got %q", rpcResp.JSONRPC)
	}
	if rpcResp.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	// Result should contain a public_key field.
	resultBytes, err := json.Marshal(rpcResp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(result.Content) == 0 {
		t.Fatal("expected content in result")
	}

	// The text content is a formatted guide string containing the public key.
	text := result.Content[0].Text
	if !strings.Contains(text, "Identity") {
		t.Error("expected identity info in init result text")
	}
	// Public keys are 64-char hex strings; verify one is present.
	if len(text) < 64 {
		t.Error("init result text too short to contain a public key")
	}
}

// TestHTTP_SessionedCampfireInit verifies that POST /mcp in sessioned mode
// with a campfire_init call injects the session token into the response text.
// This includes a JSON round-trip of resp.Result to exercise the fix for the
// fragile []map[string]interface{} assertion (JSON always produces []interface{}).
func TestHTTP_SessionedCampfireInit(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionManager(dir)
	defer sm.Stop()

	srv := &server{
		cfHome:         dir,
		beaconDir:      dir,
		sessManager:    sm,
		cfHomeExplicit: true,
	}

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	// Round-trip the result through JSON to confirm the token survived.
	// Before the fix, []map[string]interface{} assertion failed silently after
	// any json.Marshal/Unmarshal cycle; this exercises the fixed code path.
	resultBytes, err := json.Marshal(rpcResp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content in result")
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "Session token:") {
		t.Errorf("expected 'Session token:' in campfire_init response, got: %q", text)
	}
	if !strings.Contains(text, "Authorization: Bearer") {
		t.Errorf("expected 'Authorization: Bearer' hint in campfire_init response, got: %q", text)
	}
}

// TestHTTP_ParseError verifies that invalid JSON in the request body returns
// a JSON-RPC -32700 parse error.
func TestHTTP_ParseError(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("not valid json"))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	srv.handleMCP(w, req)

	resp := w.Result()
	// JSON-RPC errors are still returned with HTTP 200 per spec.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if rpcResp.Error == nil {
		t.Fatal("expected error response")
	}
	if rpcResp.Error.Code != -32700 {
		t.Errorf("expected error code -32700, got %d", rpcResp.Error.Code)
	}
}

// TestHTTP_MethodNotAllowed verifies that GET /mcp returns 405.
func TestHTTP_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := httptest.NewRecorder()
	srv.handleMCP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestHTTP_Notification verifies that a notification (no response needed)
// returns 204 No Content.
func TestHTTP_Notification(t *testing.T) {
	srv := newTestServer(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"notifications/initialized"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleMCP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /sse — Server-Sent Events
// ---------------------------------------------------------------------------

// TestSSE_EndpointEvent verifies that the SSE stream sends an initial
// "endpoint" event pointing to /mcp.
func TestSSE_EndpointEvent(t *testing.T) {
	srv := newTestServer(t)

	// Use a real test server so we get proper flushing behavior.
	ts := httptest.NewServer(http.HandlerFunc(srv.handleSSE))
	defer ts.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("GET /sse: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", resp.Header.Get("Content-Type"))
	}

	// Read enough to get the endpoint event. The client timeout will
	// terminate the read if the server hangs.
	buf := make([]byte, 512)
	n, err := resp.Body.Read(buf)
	if n == 0 && err != nil {
		t.Fatalf("read SSE: %v", err)
	}

	data := string(buf[:n])
	if !strings.Contains(data, "event: endpoint") {
		t.Errorf("expected 'event: endpoint' in SSE stream, got %q", data)
	}
	if !strings.Contains(data, "data: /mcp") {
		t.Errorf("expected 'data: /mcp' in SSE stream, got %q", data)
	}
}

// TestSSE_MethodNotAllowed verifies that POST /sse returns 405.
func TestSSE_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/sse", nil)
	w := httptest.NewRecorder()
	srv.handleSSE(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /health
// ---------------------------------------------------------------------------

// TestHealth_OK verifies that GET /health returns 200 with JSON status.
func TestHealth_OK(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}

	var body struct {
		Status   string `json:"status"`
		Sessions int    `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("expected status=ok, got %q", body.Status)
	}
}

// TestHealth_MethodNotAllowed verifies that POST /health returns 405.
func TestHealth_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestHealth_SessionCount verifies that /health reports active session count.
func TestHealth_SessionCount(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionManager(dir)
	defer sm.Stop()

	// Create two sessions.
	tokA, err := sm.issueToken()
	if err != nil {
		t.Fatalf("issue token a: %v", err)
	}
	_, err = sm.getOrCreate(tokA)
	if err != nil {
		t.Fatalf("create session a: %v", err)
	}
	tokB, err := sm.issueToken()
	if err != nil {
		t.Fatalf("issue token b: %v", err)
	}
	_, err = sm.getOrCreate(tokB)
	if err != nil {
		t.Fatalf("create session b: %v", err)
	}

	srv := &server{
		cfHome:      dir,
		beaconDir:   dir,
		sessManager: sm,
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	var body struct {
		Status   string `json:"status"`
		Sessions int    `json:"sessions"`
	}
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Sessions != 2 {
		t.Errorf("expected sessions=2, got %d", body.Sessions)
	}
}

// ---------------------------------------------------------------------------
// Integration: full HTTP mux
// ---------------------------------------------------------------------------

// TestHTTP_MuxRouting verifies that serveHTTP wires up both /mcp and /sse.
func TestHTTP_MuxRouting(t *testing.T) {
	srv := newTestServer(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", srv.handleMCP)
	mux.HandleFunc("/sse", srv.handleSSE)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	// POST /mcp should work.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rpcResp.Error != nil {
		t.Errorf("unexpected error: %+v", rpcResp.Error)
	}

	// GET /sse should return event-stream.
	client := &http.Client{Timeout: 2 * time.Second}
	sseResp, err := client.Get(ts.URL + "/sse")
	if err != nil {
		t.Fatalf("GET /sse: %v", err)
	}
	defer sseResp.Body.Close()

	if sseResp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %q", sseResp.Header.Get("Content-Type"))
	}
}

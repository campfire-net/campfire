package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---------------------------------------------------------------------------
// Unit tests: helpers
// ---------------------------------------------------------------------------

// TestShortID tests the shortID length-safe helper.
func TestShortID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		n        int
		expected string
	}{
		{"empty string", "", 12, ""},
		{"shorter than n", "abc", 12, "abc"},
		{"exactly n chars", "123456789012", 12, "123456789012"},
		{"longer than n", "1234567890123", 12, "123456789012"},
		{"long string", "abcdefghijklmnopqrst", 12, "abcdefghijkl"},
		{"zero length", "abcdefgh", 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shortID(tt.input, tt.n)
			if result != tt.expected {
				t.Errorf("shortID(%q, %d) = %q, want %q", tt.input, tt.n, result, tt.expected)
			}
		})
	}
}

// TestShortIDNoPanic verifies that shortID guards against panics when
// handlers receive malformed campfireIDs shorter than 12 characters.
// This integration test calls handleSend with a 5-character campfireID
// and verifies it returns an error response instead of panicking.
func TestShortIDNoPanic(t *testing.T) {
	srv := newTestServer(t)

	// Initialize the server to create a valid identity
	initResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if initResp.Error != nil {
		t.Fatalf("campfire_init failed: %+v", initResp.Error)
	}

	// Construct a send request with a short campfireID (not a member scenario).
	// The server will try to query membership, fail, and hit the error path
	// that uses shortID to truncate the ID in the error message.
	params := map[string]interface{}{
		"campfire_id": "short",  // 5 characters, less than the 12 we try to slice
		"message":     "test",
	}
	resp := srv.handleSend(float64(1), params)

	// Verify we got an error response (not a panic).
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	// The error message should contain "not a member" or similar, with the short ID intact.
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	// Verify the error message was constructed safely (should contain "short" or "not a member").
	if resp.Error.Message == "" {
		t.Errorf("expected non-empty error message")
	}
	// The error message should contain the short ID safely without panic.
	if !contains(resp.Error.Message, "short") && !contains(resp.Error.Message, "not a member") {
		t.Errorf("unexpected error message: %s", resp.Error.Message)
	}
}

// contains is a helper to check if a string contains a substring.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Helper: build a server with temp dirs for cfHome and beaconDir.
// ---------------------------------------------------------------------------

func newTestServer(t *testing.T) *server {
	t.Helper()
	cfHome := t.TempDir()
	beaconDir := filepath.Join(cfHome, "beacons")
	if err := os.MkdirAll(beaconDir, 0700); err != nil {
		t.Fatalf("creating beacon dir: %v", err)
	}
	return &server{
		cfHome:         cfHome,
		beaconDir:      beaconDir,
		cfHomeExplicit: true,
	}
}

// newTestServerWithPrimitives creates a test server with exposePrimitives enabled,
// simulating --expose-primitives flag behavior.
func newTestServerWithPrimitives(t *testing.T) *server {
	t.Helper()
	srv := newTestServer(t)
	srv.exposePrimitives = true
	return srv
}

// ---------------------------------------------------------------------------
// JSON-RPC helper types for constructing requests.
// ---------------------------------------------------------------------------

func makeReq(method string, paramsJSON string) jsonRPCRequest {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  method,
	}
	if paramsJSON != "" {
		req.Params = json.RawMessage(paramsJSON)
	}
	return req
}

// ---------------------------------------------------------------------------
// MCP protocol: initialize
// ---------------------------------------------------------------------------

// TestDispatch_Initialize verifies that the "initialize" method returns
// MCP protocol version and server info.
func TestDispatch_Initialize(t *testing.T) {
	srv := newTestServer(t)
	resp := srv.dispatch(makeReq("initialize", "{}"))

	if resp.JSONRPC != "2.0" {
		t.Errorf("expected jsonrpc=2.0, got %q", resp.JSONRPC)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("expected non-nil result")
	}

	b, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}

	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("expected protocolVersion=2024-11-05, got %v", result["protocolVersion"])
	}

	info, ok := result["serverInfo"].(map[string]interface{})
	if !ok {
		t.Fatalf("serverInfo missing or wrong type: %v", result["serverInfo"])
	}
	if info["name"] != "campfire" {
		t.Errorf("expected serverInfo.name=campfire, got %v", info["name"])
	}
}

// ---------------------------------------------------------------------------
// MCP protocol: tools/list
// ---------------------------------------------------------------------------

// TestDispatch_ToolsList verifies that the default tools/list only returns base
// (convention-level) tools — not primitives — and that every listed tool has
// the required name and inputSchema fields.
func TestDispatch_ToolsList(t *testing.T) {
	srv := newTestServer(t)
	resp := srv.dispatch(makeReq("tools/list", "{}"))

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	b, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}

	toolsRaw, ok := result["tools"].([]interface{})
	if !ok {
		t.Fatalf("tools missing or wrong type: %T", result["tools"])
	}
	if len(toolsRaw) == 0 {
		t.Fatal("expected at least one tool")
	}

	// Verify every tool has name and inputSchema.
	for i, raw := range toolsRaw {
		tool, ok := raw.(map[string]interface{})
		if !ok {
			t.Errorf("tool[%d] is not an object", i)
			continue
		}
		if tool["name"] == "" || tool["name"] == nil {
			t.Errorf("tool[%d] missing name", i)
		}
		if tool["inputSchema"] == nil {
			t.Errorf("tool[%d] (%v) missing inputSchema", i, tool["name"])
		}
	}

	names := make(map[string]bool)
	for _, raw := range toolsRaw {
		tool := raw.(map[string]interface{})
		names[tool["name"].(string)] = true
	}

	// Base (non-primitive) tools must always appear.
	for _, expected := range []string{
		"campfire_init", "campfire_id",
		"campfire_join", "campfire_discover", "campfire_ls",
	} {
		if !names[expected] {
			t.Errorf("expected base tool %q in default tools/list, not found", expected)
		}
	}

	// Primitive tools must NOT appear in default mode.
	for _, primitive := range []string{
		"campfire_create", "campfire_send", "campfire_read",
		"campfire_commitment", "campfire_inspect", "campfire_dm",
		"campfire_await", "campfire_export",
	} {
		if names[primitive] {
			t.Errorf("primitive tool %q should not appear in default tools/list (need --expose-primitives)", primitive)
		}
	}
}

// TestDispatch_ToolsList_ExposePrimitives verifies that when exposePrimitives is set,
// both base and primitive tools appear in tools/list.
func TestDispatch_ToolsList_ExposePrimitives(t *testing.T) {
	srv := newTestServerWithPrimitives(t)
	resp := srv.dispatch(makeReq("tools/list", "{}"))

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	b, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}

	toolsRaw, ok := result["tools"].([]interface{})
	if !ok {
		t.Fatalf("tools missing or wrong type: %T", result["tools"])
	}

	names := make(map[string]bool)
	for _, raw := range toolsRaw {
		tool := raw.(map[string]interface{})
		names[tool["name"].(string)] = true
	}

	// All tools (base + primitive) must appear when --expose-primitives is set.
	for _, expected := range []string{
		"campfire_init", "campfire_id", "campfire_create",
		"campfire_join", "campfire_send", "campfire_read",
		"campfire_discover", "campfire_ls",
	} {
		if !names[expected] {
			t.Errorf("expected tool %q in tools/list with --expose-primitives, not found", expected)
		}
	}
}

// ---------------------------------------------------------------------------
// MCP protocol: notification (no response expected)
// ---------------------------------------------------------------------------

// TestDispatch_Notification verifies that notifications return an empty
// jsonRPCResponse (JSONRPC="") so the main loop skips encoding it.
func TestDispatch_Notification(t *testing.T) {
	srv := newTestServer(t)
	resp := srv.dispatch(makeReq("notifications/initialized", ""))
	if resp.JSONRPC != "" {
		t.Errorf("expected empty JSONRPC for notification, got %q", resp.JSONRPC)
	}
}

// ---------------------------------------------------------------------------
// MCP protocol: unknown method
// ---------------------------------------------------------------------------

// TestDispatch_UnknownMethod verifies that an unknown method returns a -32601
// method-not-found error.
func TestDispatch_UnknownMethod(t *testing.T) {
	srv := newTestServer(t)
	resp := srv.dispatch(makeReq("bogus/method", "{}"))
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("expected error code -32601, got %d", resp.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// tools/call: campfire_init (basic tool call flow)
// ---------------------------------------------------------------------------

// TestToolCall_Init verifies the end-to-end path of tools/call → campfire_init.
// Confirms that calling the tool creates an identity file and returns a valid
// MCP tool-result structure.
func TestToolCall_Init(t *testing.T) {
	srv := newTestServer(t)
	params := `{"name":"campfire_init","arguments":{}}`
	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	_ = params // suppress unused

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	// Result should be a map with "content" array.
	b, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}

	content, ok := result["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatalf("expected non-empty content array, got: %v", result)
	}

	item, ok := content[0].(map[string]interface{})
	if !ok {
		t.Fatal("content[0] is not an object")
	}
	if item["type"] != "text" {
		t.Errorf("expected content[0].type=text, got %v", item["type"])
	}
	text, _ := item["text"].(string)
	if text == "" {
		t.Error("expected non-empty text in tool result")
	}

	// Identity file must exist after init.
	idPath := filepath.Join(srv.cfHome, "identity.json")
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("identity.json not found after campfire_init: %v", err)
	}
}

// ---------------------------------------------------------------------------
// tools/call: unknown tool name
// ---------------------------------------------------------------------------

// TestToolCall_UnknownTool verifies that calling an unrecognized tool name
// returns a -32601 error.
func TestToolCall_UnknownTool(t *testing.T) {
	srv := newTestServer(t)
	resp := srv.dispatch(makeReq("tools/call", `{"name":"no_such_tool","arguments":{}}`))
	if resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("expected -32601, got %d", resp.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// tools/call: campfire_id without init (error path)
// ---------------------------------------------------------------------------

// TestToolCall_IDWithoutInit verifies that calling campfire_id before init
// returns a -32000 error (no identity exists).
func TestToolCall_IDWithoutInit(t *testing.T) {
	srv := newTestServer(t)
	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_id","arguments":{}}`))
	if resp.Error == nil {
		t.Fatal("expected error when calling campfire_id without init")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected -32000, got %d", resp.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// tools/call: campfire_init then campfire_id (happy path)
// ---------------------------------------------------------------------------

// TestToolCall_InitThenID verifies the common init → id sequence returns a
// public key.
func TestToolCall_InitThenID(t *testing.T) {
	srv := newTestServer(t)

	// Init first.
	r1 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if r1.Error != nil {
		t.Fatalf("init failed: %+v", r1.Error)
	}

	// Now campfire_id should succeed.
	r2 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_id","arguments":{}}`))
	if r2.Error != nil {
		t.Fatalf("id failed: %+v", r2.Error)
	}

	b, _ := json.Marshal(r2.Result)
	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshaling id result: %v", err)
	}

	content, ok := result["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatalf("expected content array, got %v", result)
	}
	item := content[0].(map[string]interface{})
	text, _ := item["text"].(string)
	if text == "" {
		t.Error("expected non-empty text from campfire_id")
	}
	// The text is JSON containing a public_key field.
	var idResult map[string]string
	if err := json.Unmarshal([]byte(text), &idResult); err != nil {
		t.Fatalf("parsing campfire_id text as JSON: %v", err)
	}
	if len(idResult["public_key"]) != 64 {
		t.Errorf("expected 64-char hex public_key, got %q (len %d)", idResult["public_key"], len(idResult["public_key"]))
	}
}

// ---------------------------------------------------------------------------
// Helper: param extraction
// ---------------------------------------------------------------------------

// TestGetStr verifies the getStr helper handles present, missing, and
// wrong-typed values.
func TestGetStr(t *testing.T) {
	params := map[string]interface{}{
		"key":   "value",
		"other": 42,
	}
	if got := getStr(params, "key"); got != "value" {
		t.Errorf("expected 'value', got %q", got)
	}
	if got := getStr(params, "missing"); got != "" {
		t.Errorf("expected empty string for missing key, got %q", got)
	}
	if got := getStr(params, "other"); got != "" {
		t.Errorf("expected empty string for non-string value, got %q", got)
	}
}

// TestGetBool verifies getBool handles present, missing, and non-bool values.
func TestGetBool(t *testing.T) {
	params := map[string]interface{}{
		"yes": true,
		"no":  false,
		"str": "true",
	}
	if !getBool(params, "yes") {
		t.Error("expected true")
	}
	if getBool(params, "no") {
		t.Error("expected false")
	}
	if getBool(params, "missing") {
		t.Error("expected false for missing key")
	}
	if getBool(params, "str") {
		t.Error("expected false for string value")
	}
}

// TestGetStringSlice verifies getStringSlice handles []interface{} and
// []string inputs, and missing keys.
func TestGetStringSlice(t *testing.T) {
	params := map[string]interface{}{
		"tags":  []interface{}{"a", "b", "c"},
		"typed": []string{"x", "y"},
	}
	got := getStringSlice(params, "tags")
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("unexpected result: %v", got)
	}
	got2 := getStringSlice(params, "typed")
	if len(got2) != 2 || got2[0] != "x" {
		t.Errorf("unexpected result for typed: %v", got2)
	}
	if getStringSlice(params, "missing") != nil {
		t.Error("expected nil for missing key")
	}
}

// ---------------------------------------------------------------------------
// Flock tests (Unix-only via build constraint on flock_unix.go)
// ---------------------------------------------------------------------------

// TestFlock_AcquireAndRelease verifies that we can acquire a file lock
// and then release it by closing the file.
func TestFlock_AcquireAndRelease(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "lock-*")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	defer f.Close()

	if err := tryFlock(f.Fd()); err != nil {
		t.Fatalf("tryFlock acquire failed: %v", err)
	}

	// Unlock by removing the exclusive lock.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("releasing flock: %v", err)
	}
}

// TestFlock_DoubleAcquireBlocks verifies that a second non-blocking flock
// attempt on the same file from the same process does NOT block (Linux
// allows same-PID re-locking), but that across two independent fds the
// second call fails with EWOULDBLOCK.
func TestFlock_DoubleAcquireBlocks(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")

	// First fd: acquire lock.
	f1, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("opening f1: %v", err)
	}
	defer f1.Close()

	if err := tryFlock(f1.Fd()); err != nil {
		t.Fatalf("first tryFlock failed: %v", err)
	}

	// Second fd on same file: should fail with EWOULDBLOCK.
	f2, err := os.OpenFile(lockPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("opening f2: %v", err)
	}
	defer f2.Close()

	err = tryFlock(f2.Fd())
	if err == nil {
		t.Error("expected tryFlock to fail when lock is held by another fd, but it succeeded")
	}
}

// TestFlock_ReleaseAndReacquire verifies that after releasing a lock
// another fd can acquire it.
func TestFlock_ReleaseAndReacquire(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")

	f1, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("opening f1: %v", err)
	}

	if err := tryFlock(f1.Fd()); err != nil {
		t.Fatalf("first tryFlock failed: %v", err)
	}

	// Release by closing f1.
	f1.Close()

	// f2 should now be able to acquire.
	f2, err := os.OpenFile(lockPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("opening f2: %v", err)
	}
	defer f2.Close()

	if err := tryFlock(f2.Fd()); err != nil {
		t.Errorf("expected tryFlock to succeed after release, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// validateAgentName: path traversal rejection
// ---------------------------------------------------------------------------

// TestValidateAgentName_Traversal verifies that names designed to escape the
// agents/ directory are rejected before any filesystem operation.
func TestValidateAgentName_Traversal(t *testing.T) {
	malicious := []string{
		"../escape",
		"../../tmp/evil",
		"foo/../bar",
		"/absolute",
		"sub/dir",
		`sub\dir`,
		"..",
		".",
	}
	for _, name := range malicious {
		if err := validateAgentName(name); err == nil {
			t.Errorf("validateAgentName(%q) expected error, got nil", name)
		}
	}
}

// TestValidateAgentName_Valid verifies that legitimate single-component names
// are accepted.
func TestValidateAgentName_Valid(t *testing.T) {
	valid := []string{
		"myagent",
		"valid-agent",
		"agent_123",
		"Agent.Name",
		"a",
	}
	for _, name := range valid {
		if err := validateAgentName(name); err != nil {
			t.Errorf("validateAgentName(%q) expected nil, got: %v", name, err)
		}
	}
}

// TestHandleInit_TraversalRejected verifies that campfire_init with a path
// traversal name returns a -32602 error and does not create any directory
// outside the expected base.
func TestHandleInit_TraversalRejected(t *testing.T) {
	srv := newTestServer(t)
	// Snapshot home dir state before call.
	home, _ := os.UserHomeDir()
	escapedPath := filepath.Join(home, ".campfire", "agents", "..", "escape")

	resp := srv.handleInit(float64(1), map[string]interface{}{
		"name": "../escape",
	})

	if resp.Error == nil {
		t.Fatal("expected error for traversal name, got nil")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected error code -32602, got %d", resp.Error.Code)
	}

	// Directory must not have been created.
	if _, err := os.Stat(escapedPath); err == nil {
		t.Errorf("traversal directory was created at %s — this is a security failure", escapedPath)
	}
}

// TestHandleInit_ValidNameSucceeds verifies that campfire_init with a valid
// single-component name proceeds normally (creates identity, no error).
func TestHandleInit_ValidNameSucceeds(t *testing.T) {
	// Use a temp dir as the named home so we don't touch ~/.campfire/agents.
	namedHome := t.TempDir()
	srv := &server{
		cfHome:         namedHome,
		beaconDir:      filepath.Join(namedHome, "beacons"),
		cfHomeExplicit: true,
	}
	if err := os.MkdirAll(srv.beaconDir, 0700); err != nil {
		t.Fatalf("creating beaconDir: %v", err)
	}

	// Calling with no name (session-scoped) uses the pre-configured cfHome.
	resp := srv.handleInit(float64(1), map[string]interface{}{})
	if resp.Error != nil {
		t.Fatalf("handleInit with no name failed: %+v", resp.Error)
	}

	// Identity file should exist in cfHome.
	idPath := filepath.Join(srv.cfHome, "identity.json")
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("identity.json not found after valid init: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FED-1: HTTP path campfire:* tag role enforcement
// ---------------------------------------------------------------------------

// setupHTTPSendEnv creates a server wired with an HTTP transport and a campfire
// whose single member has the given role. It returns the server and the campfire
// ID so callers can drive handleSend directly.
func setupHTTPSendEnv(t *testing.T, role string) (*server, string) {
	t.Helper()

	cfHome := t.TempDir()

	// Generate and persist an identity (the "agent" that will be sending).
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	idPath := filepath.Join(cfHome, "identity.json")
	if err := agentID.Save(idPath); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Create a campfire.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}
	campfireID := cf.PublicKeyHex()

	// Write campfire state to cfHome/<campfireID>/campfire.cbor.
	campfireDir := filepath.Join(cfHome, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(campfireDir, sub), 0755); err != nil {
			t.Fatalf("creating campfire subdir: %v", err)
		}
	}
	state := cf.State()
	stateBytes, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshaling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "campfire.cbor"), stateBytes, 0644); err != nil {
		t.Fatalf("writing campfire.cbor: %v", err)
	}

	// Write agent as a member with the given role.
	member := campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  1,
		Role:      role,
	}
	memberBytes, err := cfencoding.Marshal(member)
	if err != nil {
		t.Fatalf("marshaling member: %v", err)
	}
	memberPath := filepath.Join(campfireDir, "members", fmt.Sprintf("%x.cbor", agentID.PublicKey))
	if err := os.WriteFile(memberPath, memberBytes, 0644); err != nil {
		t.Fatalf("writing member file: %v", err)
	}

	// Set up a real store and record membership.
	st, err := store.Open(store.StorePath(cfHome))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if err := st.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: campfireDir,
		JoinProtocol: "open",
		Role:         role,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Wire up cfhttp.Transport (non-nil is what triggers the HTTP path).
	httpT := cfhttp.New("", st)

	srv := &server{
		cfHome:         cfHome,
		cfHomeExplicit: true,
		st:             st,
		httpTransport:  httpT,
	}

	return srv, campfireID
}

// TestHandleSend_FED1_WriterBlockedOnCampfireTag verifies that a writer role
// cannot send a message tagged with a campfire:* system tag.
func TestHandleSend_FED1_WriterBlockedOnCampfireTag(t *testing.T) {
	srv, campfireID := setupHTTPSendEnv(t, campfire.RoleWriter)

	resp := srv.handleSend(float64(1), map[string]interface{}{
		"campfire_id": campfireID,
		"message":     "hello",
		"tags":        []interface{}{"campfire:test"},
	})

	if resp.Error == nil {
		t.Fatal("expected error response for writer + campfire:* tag, got success")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "writers cannot send campfire system messages") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

// TestHandleSend_FED1_FullMemberAllowedCampfireTag verifies that a full member
// can send a message tagged with campfire:* without being blocked.
func TestHandleSend_FED1_FullMemberAllowedCampfireTag(t *testing.T) {
	srv, campfireID := setupHTTPSendEnv(t, campfire.RoleFull)

	resp := srv.handleSend(float64(1), map[string]interface{}{
		"campfire_id": campfireID,
		"message":     "hello",
		"tags":        []interface{}{"campfire:test"},
	})

	// The message should be stored successfully (no role-related error).
	if resp.Error != nil {
		t.Fatalf("expected success for full member + campfire:* tag, got error: %s", resp.Error.Message)
	}
}

// TestHandleSend_FED1_ObserverBlockedOnAnyMessage verifies that an observer
// cannot send any message, regardless of tags.
func TestHandleSend_FED1_ObserverBlockedOnAnyMessage(t *testing.T) {
	srv, campfireID := setupHTTPSendEnv(t, campfire.RoleObserver)

	resp := srv.handleSend(float64(1), map[string]interface{}{
		"campfire_id": campfireID,
		"message":     "hello",
		"tags":        []interface{}{"regular-tag"},
	})

	if resp.Error == nil {
		t.Fatal("expected error response for observer send, got success")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "observers cannot send messages") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// FED-2: routing:beacon payload validation in handleSend
// ---------------------------------------------------------------------------

// newInitializedTestServer creates a test server with a valid identity
// (campfire_init already called). The store is opened in session mode so
// handleSend can proceed past the identity-load step.
func newInitializedTestServer(t *testing.T) *server {
	t.Helper()
	srv := newTestServer(t)
	r := srv.handleInit(float64(1), map[string]interface{}{})
	if r.Error != nil {
		t.Fatalf("campfire_init failed: %+v", r.Error)
	}
	return srv
}

// validBeaconPayload returns a JSON-encoded, signed BeaconDeclaration for
// use in tests that require an accepted routing:beacon message.
func validBeaconPayload(t *testing.T) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ed25519 key: %v", err)
	}
	decl, err := beacon.SignDeclaration(pub, priv, "http://example.com:8080", "p2p-http", "test beacon", "open")
	if err != nil {
		t.Fatalf("signing beacon declaration: %v", err)
	}
	b, err := json.Marshal(decl)
	if err != nil {
		t.Fatalf("marshaling beacon declaration: %v", err)
	}
	return string(b)
}

// TestHandleSend_BeaconInvalidJSON verifies that a routing:beacon message
// with a payload that is not valid JSON is rejected with a -32000 error
// before anything is stored (FED-2 — beacon poisoning prevention).
func TestHandleSend_BeaconInvalidJSON(t *testing.T) {
	srv := newInitializedTestServer(t)

	resp := srv.handleSend(float64(1), map[string]interface{}{
		"campfire_id": "deadbeef", // does not need to be a real campfire; validation runs first
		"message":     "not-json-at-all",
		"tags":        []interface{}{"routing:beacon"},
	})

	if resp.Error == nil {
		t.Fatal("expected error response for invalid JSON beacon payload, got nil")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if !contains(resp.Error.Message, "not valid JSON") {
		t.Errorf("expected error message to mention 'not valid JSON', got: %s", resp.Error.Message)
	}
}

// TestHandleSend_BeaconBadSignature verifies that a routing:beacon message
// whose payload is valid JSON but fails inner_signature verification is
// rejected with a -32000 error before anything is stored (FED-2).
func TestHandleSend_BeaconBadSignature(t *testing.T) {
	srv := newInitializedTestServer(t)

	// Craft a syntactically valid BeaconDeclaration but with a tampered signature.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	decl, err := beacon.SignDeclaration(pub, priv, "http://example.com", "p2p-http", "test", "open")
	if err != nil {
		t.Fatalf("signing declaration: %v", err)
	}
	// Tamper: change the endpoint so the signature no longer matches.
	decl.Endpoint = "http://attacker.example.com/evil"
	b, _ := json.Marshal(decl)

	resp := srv.handleSend(float64(1), map[string]interface{}{
		"campfire_id": "deadbeef",
		"message":     string(b),
		"tags":        []interface{}{"routing:beacon"},
	})

	if resp.Error == nil {
		t.Fatal("expected error response for beacon with bad signature, got nil")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if !contains(resp.Error.Message, "signature verification") {
		t.Errorf("expected error message to mention 'signature verification', got: %s", resp.Error.Message)
	}
}

// TestHandleSend_BeaconValidPayload verifies that a routing:beacon message
// with a correctly signed payload passes the FED-2 validation layer and
// proceeds to the membership/send layer (which will return "not a member"
// rather than a validation error). This proves valid beacons are not spuriously
// rejected by the new validation gate.
func TestHandleSend_BeaconValidPayload(t *testing.T) {
	srv := newInitializedTestServer(t)

	payload := validBeaconPayload(t)

	resp := srv.handleSend(float64(1), map[string]interface{}{
		"campfire_id": fmt.Sprintf("%064x", make([]byte, 32)), // syntactically valid campfire ID
		"message":     payload,
		"tags":        []interface{}{"routing:beacon"},
	})

	// The beacon validation passes. The call will fail later because we are
	// not a member of this campfire — that is the expected behaviour.
	// It must NOT fail with a JSON or signature error.
	if resp.Error != nil {
		if contains(resp.Error.Message, "not valid JSON") || contains(resp.Error.Message, "signature verification") {
			t.Fatalf("valid beacon payload was incorrectly rejected by FED-2 validation: %s", resp.Error.Message)
		}
		// Any other error (membership, store, etc.) is acceptable — the beacon
		// validation passed.
	}
}

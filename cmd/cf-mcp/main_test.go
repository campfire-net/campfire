package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

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

// TestDispatch_ToolsList verifies that tools/list returns all known campfire
// tools with name and inputSchema fields.
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

	// Spot-check that core tools are present.
	names := make(map[string]bool)
	for _, raw := range toolsRaw {
		tool := raw.(map[string]interface{})
		names[tool["name"].(string)] = true
	}
	for _, expected := range []string{
		"campfire_init", "campfire_id", "campfire_create",
		"campfire_join", "campfire_send", "campfire_read",
		"campfire_discover", "campfire_ls",
	} {
		if !names[expected] {
			t.Errorf("expected tool %q in tools/list, not found", expected)
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

package main

// Tests for audit log feature (security model §5.e, bead campfire-agent-zwf).
//
// TDD sequence:
//   1. campfire_send creates an audit entry in the agent's audit campfire
//   2. campfire_audit returns correct action counts after several operations
//   3. Merkle root is computed correctly for known entries
//   4. audit entries include request_hash field

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test 1: campfire_send creates an audit entry in the agent's audit campfire
// ---------------------------------------------------------------------------

func TestAudit_SendCreatesAuditEntry(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	// Create an audit writer for this server.
	aw, err := NewAuditWriter(srv)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	srv.auditWriter = aw

	// Create a campfire to send to.
	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatalf("missing campfire_id")
	}

	// Send a message.
	sendArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"message":     "hello audit",
	})
	sendResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_send","arguments":`+string(sendArgs)+`}`))
	if sendResp.Error != nil {
		t.Fatalf("campfire_send failed: code=%d msg=%s", sendResp.Error.Code, sendResp.Error.Message)
	}

	// Flush audit entries.
	aw.Flush()

	// Read the audit campfire messages.
	auditID := aw.CampfireID()
	if auditID == "" {
		t.Fatal("audit campfire ID is empty")
	}

	readArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": auditID,
		"all":         true,
	})
	readResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_read","arguments":`+string(readArgs)+`}`))
	if readResp.Error != nil {
		t.Fatalf("campfire_read on audit campfire failed: code=%d msg=%s",
			readResp.Error.Code, readResp.Error.Message)
	}

	readText := extractResultText(t, readResp)

	// The audit campfire messages have payloads containing serialized AuditEntry JSON.
	// The payload appears as an escaped string in the outer JSON, so search for the
	// escaped form or just for the action value within the payload substring.
	// campfire_read returns messages where payload contains the audit entry JSON.
	// We check for the escaped form as it appears inside the payload string field.
	if !strings.Contains(readText, `action`) || !strings.Contains(readText, `send`) {
		t.Errorf("expected audit entry with action=send in audit campfire, got: %s", readText)
	}
}

// ---------------------------------------------------------------------------
// Test 2: campfire_audit returns correct action counts after several operations
// ---------------------------------------------------------------------------

func TestAudit_ToolReturnsCounts(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	aw, err := NewAuditWriter(srv)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	srv.auditWriter = aw

	// Create a campfire.
	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)

	// Send two messages.
	for i := 0; i < 2; i++ {
		sendArgs, _ := json.Marshal(map[string]interface{}{
			"campfire_id": campfireID,
			"message":     fmt.Sprintf("msg %d", i),
		})
		resp := srv.dispatch(makeReq("tools/call",
			`{"name":"campfire_send","arguments":`+string(sendArgs)+`}`))
		if resp.Error != nil {
			t.Fatalf("campfire_send %d failed: %s", i, resp.Error.Message)
		}
	}

	// Flush to ensure all entries are written.
	aw.Flush()

	// Call campfire_audit tool.
	auditResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_audit","arguments":{}}`))
	if auditResp.Error != nil {
		t.Fatalf("campfire_audit failed: code=%d msg=%s", auditResp.Error.Code, auditResp.Error.Message)
	}

	auditText := extractResultText(t, auditResp)

	// Should report totals including 2 sends and 1 create.
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(auditText), &result); err != nil {
		t.Fatalf("parsing campfire_audit result: %v — raw: %s", err, auditText)
	}

	total, _ := result["total_actions"].(float64)
	if total < 3 { // at least create + 2 sends
		t.Errorf("expected total_actions >= 3, got %v", total)
	}

	byType, _ := result["actions_by_type"].(map[string]interface{})
	if byType == nil {
		t.Fatalf("expected actions_by_type in result, got: %v", result)
	}
	sends, _ := byType["send"].(float64)
	if sends < 2 {
		t.Errorf("expected at least 2 send actions, got %v", sends)
	}
	creates, _ := byType["create"].(float64)
	if creates < 1 {
		t.Errorf("expected at least 1 create action, got %v", creates)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Merkle root is computed correctly for known entries
// ---------------------------------------------------------------------------

func TestAudit_MerkleRootCorrect(t *testing.T) {
	// Test the merkle tree computation directly with known inputs.
	entries := []AuditEntry{
		{Sequence: 1, Timestamp: 1000, Action: "create", AgentKey: "aabbcc", CampfireID: "ff00"},
		{Sequence: 2, Timestamp: 2000, Action: "send", AgentKey: "aabbcc", CampfireID: "ff00", RequestHash: "deadbeef"},
		{Sequence: 3, Timestamp: 3000, Action: "join", AgentKey: "aabbcc", CampfireID: "ff00"},
		{Sequence: 4, Timestamp: 4000, Action: "send", AgentKey: "aabbcc", CampfireID: "ff00", RequestHash: "cafebabe"},
	}

	root := computeMerkleRoot(entries)
	if root == "" {
		t.Fatal("expected non-empty Merkle root")
	}

	// Root must be a 64-char hex string (SHA-256).
	if len(root) != 64 {
		t.Errorf("expected 64-char hex Merkle root, got %q (len %d)", root, len(root))
	}

	// Deterministic: same input → same root.
	root2 := computeMerkleRoot(entries)
	if root != root2 {
		t.Error("merkle root is not deterministic")
	}

	// Different entries → different root.
	entries[0].Action = "join"
	root3 := computeMerkleRoot(entries)
	if root == root3 {
		t.Error("expected different Merkle root for different entries")
	}

	// Single entry: root = hash of that entry.
	single := []AuditEntry{entries[0]}
	singleRoot := computeMerkleRoot(single)
	b, _ := json.Marshal(single[0])
	h := sha256.Sum256(b)
	expected := hex.EncodeToString(h[:])
	if singleRoot != expected {
		t.Errorf("single-entry root: expected %s, got %s", expected, singleRoot)
	}
}

// ---------------------------------------------------------------------------
// Test 4: audit entries include request_hash field
// ---------------------------------------------------------------------------

func TestAudit_EntryIncludesRequestHash(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	aw, err := NewAuditWriter(srv)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	srv.auditWriter = aw

	// Log an entry with a known payload to verify request_hash.
	payload := `{"campfire_id":"abc","message":"hello"}`
	h := sha256.Sum256([]byte(payload))
	expectedHash := hex.EncodeToString(h[:])

	entry := AuditEntry{
		Sequence:    1,
		Timestamp:   time.Now().UnixNano(),
		Action:      "send",
		AgentKey:    "aabbcc",
		CampfireID:  "abc",
		RequestHash: expectedHash,
	}
	aw.Log(entry)
	aw.Flush()

	// Read the audit campfire and verify the entry has request_hash.
	auditID := aw.CampfireID()
	readArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": auditID,
		"all":         true,
	})
	readResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_read","arguments":`+string(readArgs)+`}`))
	if readResp.Error != nil {
		t.Fatalf("campfire_read: %s", readResp.Error.Message)
	}

	readText := extractResultText(t, readResp)
	if !strings.Contains(readText, expectedHash) {
		t.Errorf("expected request_hash %s in audit entry, got: %s", expectedHash, readText)
	}
}

// ---------------------------------------------------------------------------
// Test 5: Close() drains channel — no entries lost on shutdown
// ---------------------------------------------------------------------------

// TestAudit_CloseFlushesChannel verifies that entries enqueued via Log() are
// written to the audit campfire even when the process is shutting down.
// Before this fix, Close() was never called so entries in the buffered channel
// were silently dropped.
func TestAudit_CloseFlushesChannel(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	aw, err := NewAuditWriter(srv)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	srv.auditWriter = aw

	// Enqueue several entries rapidly (they will sit in the buffered channel
	// until the background goroutine drains them).
	const n = 5
	for i := 0; i < n; i++ {
		aw.Log(AuditEntry{
			Timestamp: time.Now().UnixNano(),
			Action:    "send",
			AgentKey:  fmt.Sprintf("key%d", i),
		})
	}

	// Close() must drain the channel before returning — no entries lost.
	aw.Close()

	// Read all messages from the audit campfire and count audit entries.
	auditID := aw.CampfireID()
	readArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": auditID,
		"all":         true,
	})
	readResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_read","arguments":`+string(readArgs)+`}`))
	if readResp.Error != nil {
		t.Fatalf("campfire_read: %s", readResp.Error.Message)
	}

	readText := extractResultText(t, readResp)

	// The payload is stored as an escaped JSON string inside the outer JSON, so
	// "action" appears as the escaped form \"action\". Count those — each audit
	// entry contributes one.
	count := strings.Count(readText, `\"action\"`)
	if count < n {
		t.Errorf("expected at least %d audit entries after Close(), found %d in: %s", n, count, readText)
	}
}

// ---------------------------------------------------------------------------
// Test 6: repeated campfire_init on the same session does not leak goroutines
// ---------------------------------------------------------------------------

// TestAudit_NoGoroutineLeakOnRepeatedInit verifies that calling campfire_init
// multiple times on the same session reuses the existing AuditWriter rather
// than creating a new one (and leaking its background goroutine).
//
// Before the fix: each campfire_init call on a session created a fresh *server
// with nil auditWriter, so handleInit spawned a new AuditWriter goroutine
// every call while the previous one was abandoned without Close().
//
// After the fix: the AuditWriter is stored in the Session and propagated to
// each per-request server, so only one goroutine is ever running per session.
func TestAudit_NoGoroutineLeakOnRepeatedInit(t *testing.T) {
	srv := newTestServerWithSessions(t)

	// First init — issues a session token.
	initResp1 := postMCP(t, srv, mcpInitBody, "")
	token := extractTokenFromInit(t, initResp1)
	if token == "" {
		t.Fatal("expected non-empty session token")
	}

	// Let goroutines settle and record baseline.
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	// Call campfire_init N more times on the same session.
	const extraInits = 5
	for i := 0; i < extraInits; i++ {
		resp := postMCP(t, srv, mcpInitBody, token)
		if resp.Error != nil {
			t.Fatalf("campfire_init[%d] failed: code=%d msg=%s", i+1, resp.Error.Code, resp.Error.Message)
		}
	}

	// Allow any goroutines to start and settle.
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	after := runtime.NumGoroutine()

	// If the AuditWriter is being re-created on each init, goroutine count
	// would grow by at least extraInits. Allow a small slack for unrelated
	// background goroutines that may start/stop during the test.
	const maxGrowth = 2
	growth := after - baseline
	if growth > maxGrowth {
		t.Errorf("goroutine count grew by %d after %d repeated campfire_init calls (baseline=%d after=%d); "+
			"expected growth <= %d — possible AuditWriter goroutine leak",
			growth, extraInits, baseline, after, maxGrowth)
	}

	// Also verify that the session's AuditWriter is stable: the same audit
	// campfire ID should be returned on every init call.
	extractAuditID := func(resp jsonRPCResponse) string {
		b, _ := json.Marshal(resp.Result)
		var outer struct {
			Content []struct{ Text string `json:"text"` } `json:"content"`
		}
		if err := json.Unmarshal(b, &outer); err != nil || len(outer.Content) == 0 {
			return ""
		}
		var ir struct {
			AuditCampfireID string `json:"audit_campfire_id"`
		}
		// The session HTTP layer appends "\n\nSession token: ..." after the JSON
		// object; json.NewDecoder stops at the first valid JSON value.
		json.NewDecoder(strings.NewReader(outer.Content[0].Text)).Decode(&ir) //nolint:errcheck
		return ir.AuditCampfireID
	}

	firstID := extractAuditID(initResp1)
	if firstID == "" {
		// Audit is best-effort; if the first call didn't produce an audit ID,
		// skip the stability check (environment may lack required deps).
		t.Log("audit_campfire_id not present in first init response; skipping audit ID stability check")
		return
	}

	// All subsequent inits should return the same audit campfire ID.
	for i := 0; i < 3; i++ {
		resp := postMCP(t, srv, mcpInitBody, token)
		gotID := extractAuditID(resp)
		if gotID != "" && gotID != firstID {
			t.Errorf("init[%d]: audit_campfire_id changed from %s to %s; AuditWriter is being re-created",
				i+1, firstID, gotID)
		}
	}
}

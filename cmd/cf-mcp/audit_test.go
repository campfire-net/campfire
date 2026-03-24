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

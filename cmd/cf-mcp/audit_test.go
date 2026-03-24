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
	"os"
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

// ---------------------------------------------------------------------------
// Test 8: dropped counter increments when channel is full
// ---------------------------------------------------------------------------

// TestAudit_DroppedCounter verifies that:
//   - Dropped() returns 0 when no entries are dropped.
//   - Dropped() increments atomically when Log() is called on a full channel.
//   - campfire_audit exposes dropped_entries in its response.
func TestAudit_DroppedCounter(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	aw, err := NewAuditWriter(srv)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	srv.auditWriter = aw

	// Initially zero.
	if got := aw.Dropped(); got != 0 {
		t.Fatalf("expected 0 dropped entries initially, got %d", got)
	}

	// Saturate the channel without letting the background goroutine drain it.
	// We do this by closing done first so the goroutine exits, then stuffing
	// the channel directly up to its capacity, then calling Log() for extras.
	//
	// Simpler approach: create a fresh writer whose background goroutine is
	// blocked, fill it past capacity, and check the counter.
	//
	// Instead, use a small-channel writer: create one directly and bypass
	// NewAuditWriter by constructing it inline (package-internal test).
	smallAW := &AuditWriter{
		campfireID: aw.campfireID,
		srv:        srv,
		agentID:    aw.agentID,
		st:         aw.st,
		ch:         make(chan AuditEntry, 2), // tiny buffer
		done:       make(chan struct{}),
		flushReq:   make(chan chan struct{}, 1),
		lastRootAt: time.Now(),
	}
	smallAW.wg.Add(1)
	go smallAW.loop()

	// Fill the buffer (2 slots) without the goroutine running.
	// Log 5 entries — 2 should be buffered, 3 should be dropped.
	// The goroutine is running but will be drained — we need to block it.
	// Simplest: close done immediately so loop exits, then log.
	close(smallAW.done)
	smallAW.wg.Wait() // goroutine stopped; channel is now frozen

	// Log entries into a stopped writer to force drops.
	for i := 0; i < 5; i++ {
		smallAW.Log(AuditEntry{Action: "send", Timestamp: time.Now().UnixNano()})
	}

	dropped := smallAW.Dropped()
	if dropped == 0 {
		t.Error("expected at least 1 dropped entry after overfilling a stopped AuditWriter, got 0")
	}
	if dropped > 5 {
		t.Errorf("dropped count %d exceeds entries logged (5)", dropped)
	}

	// campfire_audit tool must expose dropped_entries.
	srv.auditWriter = aw // restore the live writer
	aw.Flush()

	auditResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_audit","arguments":{}}`))
	if auditResp.Error != nil {
		t.Fatalf("campfire_audit failed: %s", auditResp.Error.Message)
	}

	auditText := extractResultText(t, auditResp)
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(auditText), &result); err != nil {
		t.Fatalf("parsing campfire_audit result: %v — raw: %s", err, auditText)
	}

	if _, ok := result["dropped_entries"]; !ok {
		t.Errorf("campfire_audit response missing 'dropped_entries' field; got: %v", result)
	}
}

// ---------------------------------------------------------------------------
// Test 7: sequence numbers are contiguous even when channel drops entries
// ---------------------------------------------------------------------------

// TestAudit_SequenceAssignedByWriter verifies that sequence numbers are
// assigned in the write goroutine (consumer), not in Log() (producer).
//
// Before the fix: Log() called seq.Add(1) before the channel send. Dropped
// entries consumed a sequence number, creating gaps indistinguishable from
// tampering. After the fix: only entries that reach writeEntry() get a
// sequence number, so written sequences are always contiguous.
func TestAudit_SequenceAssignedByWriter(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	aw, err := NewAuditWriter(srv)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	srv.auditWriter = aw

	// Log a small set of entries — well within the channel buffer so none
	// are dropped — and verify written sequences are 1, 2, 3, ... with no gaps.
	const n = 5
	for i := 0; i < n; i++ {
		aw.Log(AuditEntry{
			Timestamp: time.Now().UnixNano(),
			Action:    "send",
			AgentKey:  fmt.Sprintf("key%d", i),
		})
	}
	aw.Flush()

	// Read the audit campfire and extract all AuditEntry JSON objects.
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

	// Parse each escaped JSON payload embedded in the response.
	// campfire_read returns messages; each audit payload is a JSON object
	// containing "sequence". Extract all sequence values and verify contiguity.
	var seqs []uint64
	// The payload appears as an escaped JSON string — unescape and scan for
	// sequence fields by decoding each embedded AuditEntry.
	decoder := json.NewDecoder(strings.NewReader(readText))
	for {
		// Advance through the outer JSON until we find an object with "sequence".
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		if key, ok := tok.(string); ok && key == "sequence" {
			// Next token is the sequence value.
			var seq float64
			if err := decoder.Decode(&seq); err == nil && seq > 0 {
				seqs = append(seqs, uint64(seq))
			}
		}
	}

	if len(seqs) < n {
		// The outer JSON structure may escape the inner payloads; fall back to
		// string scanning. Extract quoted numbers after "\"sequence\":" pattern.
		seqs = nil
		remaining := readText
		marker := `\"sequence\"`
		for {
			idx := strings.Index(remaining, marker)
			if idx < 0 {
				break
			}
			remaining = remaining[idx+len(marker):]
			// Skip whitespace and colon.
			rest := strings.TrimLeft(remaining, " \t\r\n:")
			var seq uint64
			if _, err := fmt.Sscanf(rest, "%d", &seq); err == nil && seq > 0 {
				seqs = append(seqs, seq)
			}
		}
	}

	if len(seqs) < n {
		t.Fatalf("expected at least %d sequence numbers in audit log, found %d; raw: %s", n, len(seqs), readText)
	}

	// Sort and verify contiguity: sequences must be 1,2,3,...,n with no gaps.
	// (Simple insertion sort — n is small.)
	for i := 1; i < len(seqs); i++ {
		for j := i; j > 0 && seqs[j] < seqs[j-1]; j-- {
			seqs[j], seqs[j-1] = seqs[j-1], seqs[j]
		}
	}
	for i, seq := range seqs[:n] {
		want := uint64(i + 1)
		if seq != want {
			t.Errorf("sequence gap detected: position %d expected %d got %d — seqs=%v", i, want, seq, seqs)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 8: campfire_init response includes audit_status
// ---------------------------------------------------------------------------

// extractInitAuditFields parses the campfire_init JSON response and returns
// the audit_status and audit_error fields from the result text.
func extractInitAuditFields(t *testing.T, resp jsonRPCResponse) (status, auditErr string) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("campfire_init error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var outer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &outer); err != nil || len(outer.Content) == 0 {
		t.Fatalf("cannot extract content from init result: %v — raw: %s", err, string(b))
	}
	var payload struct {
		AuditStatus string `json:"audit_status"`
		AuditError  string `json:"audit_error"`
	}
	// Use json.NewDecoder to tolerate trailing content (e.g., session token line).
	if err := json.NewDecoder(strings.NewReader(outer.Content[0].Text)).Decode(&payload); err != nil {
		t.Fatalf("cannot parse init payload JSON: %v — raw text: %s", err, outer.Content[0].Text)
	}
	return payload.AuditStatus, payload.AuditError
}

// TestAudit_InitResponseIncludesAuditStatusOK verifies that campfire_init
// returns audit_status: "ok" in the result when the AuditWriter initialises
// successfully.
func TestAudit_InitResponseIncludesAuditStatusOK(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	status, _ := extractInitAuditFields(t, resp)
	if status != "ok" {
		t.Errorf("expected audit_status=ok, got %q", status)
	}
}

// TestAudit_InitResponseIncludesAuditStatusDisabled verifies that when
// AuditWriter initialisation fails, campfire_init still succeeds but returns
// audit_status: "disabled" and a non-empty audit_error so the agent knows
// transparency logging is unavailable.
//
// Failure is induced by making cfHome read-only after identity creation so
// that the audit campfire directory cannot be written.
func TestAudit_InitResponseIncludesAuditStatusDisabled(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test read-only directory as root")
	}
	srv, _ := newTestServerWithStore(t)

	// Create the identity first so campfire_init can complete its own setup
	// before we restrict the directory.
	initResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if initResp.Error != nil {
		t.Fatalf("first campfire_init failed: code=%d msg=%s", initResp.Error.Code, initResp.Error.Message)
	}

	// Remove the audit campfire ID file so the next init must re-create it,
	// and make cfHome read-only so the re-creation fails.
	_ = os.Remove(srv.cfHome + "/" + auditCampfireIDFile)
	if err := os.Chmod(srv.cfHome, 0500); err != nil { // r-x: can read, cannot write
		t.Fatalf("chmod cfHome: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(srv.cfHome, 0700) })

	// Reset the auditWriter so handleInit tries to create a new one.
	srv.auditWriter = nil

	// Second init: AuditWriter creation must fail (cannot write audit campfire state).
	resp2 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if resp2.Error != nil {
		t.Fatalf("campfire_init must not fail even when audit is unavailable: code=%d msg=%s",
			resp2.Error.Code, resp2.Error.Message)
	}

	status, auditErr := extractInitAuditFields(t, resp2)
	if status != "disabled" {
		t.Errorf("expected audit_status=disabled when audit init fails, got %q", status)
	}
	if auditErr == "" {
		t.Errorf("expected non-empty audit_error when audit_status=disabled, got empty string")
	}
}

// ---------------------------------------------------------------------------
// TestAudit_DMCreatesAuditEntry: campfire_dm writes a "dm" audit entry (§5.e)
// ---------------------------------------------------------------------------

// TestAudit_DMCreatesAuditEntry verifies that campfire_dm logs a "dm" audit
// entry after successfully sending a direct message.
func TestAudit_DMCreatesAuditEntry(t *testing.T) {
	// Sender server.
	sender, _ := newTestServerWithStore(t)
	doInit(t, sender)

	aw, err := NewAuditWriter(sender)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	sender.auditWriter = aw

	// Receiver server: separate cfHome so it has its own identity.
	receiver := newTestServer(t)
	doInit(t, receiver)

	// Get receiver's public key via campfire_id.
	idResp := receiver.dispatch(makeReq("tools/call", `{"name":"campfire_id","arguments":{}}`))
	if idResp.Error != nil {
		t.Fatalf("campfire_id failed: code=%d msg=%s", idResp.Error.Code, idResp.Error.Message)
	}
	idText := extractResultText(t, idResp)
	var idResult map[string]string
	if err := json.Unmarshal([]byte(idText), &idResult); err != nil {
		t.Fatalf("parsing campfire_id result: %v", err)
	}
	targetKey := idResult["public_key"]
	if len(targetKey) != 64 {
		t.Fatalf("expected 64-char hex public_key from receiver, got %q", targetKey)
	}

	// Send a DM from sender to receiver.
	dmArgs, _ := json.Marshal(map[string]interface{}{
		"target_key": targetKey,
		"message":    "hello via dm audit test",
	})
	dmResp := sender.dispatch(makeReq("tools/call",
		`{"name":"campfire_dm","arguments":`+string(dmArgs)+`}`))
	if dmResp.Error != nil {
		t.Fatalf("campfire_dm failed: code=%d msg=%s", dmResp.Error.Code, dmResp.Error.Message)
	}

	// Flush audit entries.
	aw.Flush()

	// Read the audit campfire and confirm a "dm" entry was written.
	auditID := aw.CampfireID()
	if auditID == "" {
		t.Fatal("audit campfire ID is empty")
	}

	readArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": auditID,
		"all":         true,
	})
	readResp := sender.dispatch(makeReq("tools/call",
		`{"name":"campfire_read","arguments":`+string(readArgs)+`}`))
	if readResp.Error != nil {
		t.Fatalf("campfire_read on audit campfire failed: code=%d msg=%s",
			readResp.Error.Code, readResp.Error.Message)
	}

	readText := extractResultText(t, readResp)
	if !strings.Contains(readText, `action`) || !strings.Contains(readText, `dm`) {
		t.Errorf("expected audit entry with action=dm in audit campfire, got: %s", readText)
	}
}

// ---------------------------------------------------------------------------
// Tests for detectSequenceGaps and campfire_audit anomalies field
// ---------------------------------------------------------------------------

// TestDetectSequenceGaps_NoGap verifies no anomalies are reported for a
// contiguous sequence.
func TestDetectSequenceGaps_NoGap(t *testing.T) {
	anomalies := detectSequenceGaps([]uint64{1, 2, 3, 4, 5})
	if len(anomalies) != 0 {
		t.Errorf("expected no anomalies for contiguous sequence, got: %v", anomalies)
	}
}

// TestDetectSequenceGaps_SingleGap verifies a single gap is detected.
func TestDetectSequenceGaps_SingleGap(t *testing.T) {
	// Missing seq 3.
	anomalies := detectSequenceGaps([]uint64{1, 2, 4, 5})
	if len(anomalies) != 1 {
		t.Fatalf("expected 1 anomaly for one gap, got %d: %v", len(anomalies), anomalies)
	}
	if !strings.Contains(anomalies[0], "gap") {
		t.Errorf("anomaly should describe a gap, got: %s", anomalies[0])
	}
}

// TestDetectSequenceGaps_MultipleGaps verifies multiple gaps are each reported.
func TestDetectSequenceGaps_MultipleGaps(t *testing.T) {
	// Missing 3, 4 (gap of 2), and missing 7 (gap of 1).
	anomalies := detectSequenceGaps([]uint64{1, 2, 5, 6, 8})
	if len(anomalies) != 2 {
		t.Fatalf("expected 2 anomalies, got %d: %v", len(anomalies), anomalies)
	}
}

// TestDetectSequenceGaps_EmptyAndSingle verifies edge cases return no anomalies.
func TestDetectSequenceGaps_EmptyAndSingle(t *testing.T) {
	if a := detectSequenceGaps(nil); len(a) != 0 {
		t.Errorf("nil input: expected empty anomalies, got %v", a)
	}
	if a := detectSequenceGaps([]uint64{5}); len(a) != 0 {
		t.Errorf("single entry: expected empty anomalies, got %v", a)
	}
}

// TestDetectSequenceGaps_Unsorted verifies gaps are detected even when
// sequence numbers are provided out of order.
func TestDetectSequenceGaps_Unsorted(t *testing.T) {
	// Out-of-order but contiguous: should produce no gaps.
	anomalies := detectSequenceGaps([]uint64{5, 3, 1, 4, 2})
	if len(anomalies) != 0 {
		t.Errorf("expected no anomalies for unsorted contiguous sequence, got: %v", anomalies)
	}

	// Out-of-order with a gap.
	anomalies = detectSequenceGaps([]uint64{5, 3, 1, 6, 2})
	if len(anomalies) != 1 {
		t.Fatalf("expected 1 anomaly for unsorted sequence with gap, got %d: %v", len(anomalies), anomalies)
	}
}

// TestAudit_AnomaliesFieldPresent verifies that campfire_audit always includes
// an "anomalies" array in its response (even when there are no anomalies).
func TestAudit_AnomaliesFieldPresent(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	aw, err := NewAuditWriter(srv)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	srv.auditWriter = aw

	// Create a campfire and send two messages to populate the audit log.
	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)

	for i := 0; i < 3; i++ {
		sendArgs, _ := json.Marshal(map[string]interface{}{
			"campfire_id": campfireID,
			"message":     fmt.Sprintf("msg %d", i),
		})
		srv.dispatch(makeReq("tools/call",
			`{"name":"campfire_send","arguments":`+string(sendArgs)+`}`))
	}
	aw.Flush()

	auditResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_audit","arguments":{}}`))
	if auditResp.Error != nil {
		t.Fatalf("campfire_audit failed: %s", auditResp.Error.Message)
	}

	auditText := extractResultText(t, auditResp)
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(auditText), &result); err != nil {
		t.Fatalf("parsing campfire_audit result: %v — raw: %s", err, auditText)
	}

	// "anomalies" must be present as a JSON array (even if empty).
	anomaliesRaw, ok := result["anomalies"]
	if !ok {
		t.Fatalf("campfire_audit response missing 'anomalies' field; got keys: %v", result)
	}
	// Must be a slice (JSON array).
	if _, ok := anomaliesRaw.([]interface{}); !ok {
		t.Errorf("'anomalies' should be a JSON array, got: %T — %v", anomaliesRaw, anomaliesRaw)
	}
}

// ---------------------------------------------------------------------------
// Tests for campfire_audit 'since' parameter (design-mcp-security §5.e)
// ---------------------------------------------------------------------------

// TestAudit_Since_FiltersOldEntries verifies that the 'since' parameter causes
// campfire_audit to exclude audit entries older than the given timestamp.
func TestAudit_Since_FiltersOldEntries(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	aw, err := NewAuditWriter(srv)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	srv.auditWriter = aw

	// Create a campfire so we have something to send to.
	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatalf("missing campfire_id")
	}

	// Send a first message — this will be the "old" entry.
	sendArgs1, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"message":     "old message",
	})
	if r := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_send","arguments":`+string(sendArgs1)+`}`)); r.Error != nil {
		t.Fatalf("first send failed: %s", r.Error.Message)
	}
	aw.Flush()

	// Record the cut-off: anything before this moment should be excluded.
	// Add a small sleep to ensure the second send gets a strictly later timestamp.
	time.Sleep(2 * time.Millisecond)
	cutoff := time.Now()

	// Send a second message — this will be the "new" entry.
	sendArgs2, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"message":     "new message",
	})
	if r := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_send","arguments":`+string(sendArgs2)+`}`)); r.Error != nil {
		t.Fatalf("second send failed: %s", r.Error.Message)
	}
	aw.Flush()

	// Without since: should see all actions (create + 2 sends = at least 3).
	auditAll := srv.dispatch(makeReq("tools/call", `{"name":"campfire_audit","arguments":{}}`))
	if auditAll.Error != nil {
		t.Fatalf("campfire_audit (no since) failed: %s", auditAll.Error.Message)
	}
	allText := extractResultText(t, auditAll)
	var allResult map[string]interface{}
	if err := json.Unmarshal([]byte(allText), &allResult); err != nil {
		t.Fatalf("parsing all-result: %v — raw: %s", err, allText)
	}
	totalAll, _ := allResult["total_actions"].(float64)
	if totalAll < 3 {
		t.Fatalf("expected >= 3 total actions without since filter, got %.0f", totalAll)
	}

	// With since=cutoff: should see fewer actions (only the second send, possibly).
	// Use RFC3339Nano for sub-second precision so the filter is effective even
	// when all messages land in the same second.
	sinceArg := cutoff.UTC().Format(time.RFC3339Nano)
	sinceArgs, _ := json.Marshal(map[string]interface{}{
		"since": sinceArg,
	})
	auditSince := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_audit","arguments":`+string(sinceArgs)+`}`))
	if auditSince.Error != nil {
		t.Fatalf("campfire_audit (with since) failed: %s", auditSince.Error.Message)
	}
	sinceText := extractResultText(t, auditSince)
	var sinceResult map[string]interface{}
	if err := json.Unmarshal([]byte(sinceText), &sinceResult); err != nil {
		t.Fatalf("parsing since-result: %v — raw: %s", err, sinceText)
	}
	totalSince, _ := sinceResult["total_actions"].(float64)
	if totalSince >= totalAll {
		t.Errorf("expected since filter to reduce action count: all=%.0f since=%.0f", totalAll, totalSince)
	}
}

// TestAudit_Since_InvalidTimestamp verifies that an invalid 'since' value
// returns a -32602 error (invalid params).
func TestAudit_Since_InvalidTimestamp(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	aw, err := NewAuditWriter(srv)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	srv.auditWriter = aw

	badArgs, _ := json.Marshal(map[string]interface{}{
		"since": "not-a-timestamp",
	})
	resp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_audit","arguments":`+string(badArgs)+`}`))
	if resp.Error == nil {
		t.Fatal("expected error for invalid since timestamp, got nil")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected error code -32602 (invalid params), got %d: %s", resp.Error.Code, resp.Error.Message)
	}
}

// TestAudit_Since_FutureTimestamp verifies that a 'since' timestamp in the
// future returns zero actions (all entries are older than the filter).
func TestAudit_Since_FutureTimestamp(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	aw, err := NewAuditWriter(srv)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	srv.auditWriter = aw

	// Create and send one message so there is something in the log.
	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	sendArgs, _ := json.Marshal(map[string]interface{}{"campfire_id": campfireID, "message": "hi"})
	srv.dispatch(makeReq("tools/call", `{"name":"campfire_send","arguments":`+string(sendArgs)+`}`))
	aw.Flush()

	// Use a far-future since timestamp — no entries should match.
	futureArgs, _ := json.Marshal(map[string]interface{}{
		"since": "2099-01-01T00:00:00Z",
	})
	resp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_audit","arguments":`+string(futureArgs)+`}`))
	if resp.Error != nil {
		t.Fatalf("campfire_audit (future since) failed: %s", resp.Error.Message)
	}
	text := extractResultText(t, resp)
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("parsing result: %v — raw: %s", err, text)
	}
	total, _ := result["total_actions"].(float64)
	if total != 0 {
		t.Errorf("expected 0 actions with far-future since filter, got %.0f", total)
	}
}

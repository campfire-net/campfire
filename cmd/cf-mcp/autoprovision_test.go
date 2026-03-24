package main

// Tests for campfire_init auto-provisioning:
//   1. New campfire_id → campfire created with default settings
//   2. Idempotent re-init → same campfire returned, no error
//   3. Agent registered as first member with role="full"
//   4. Free-tier rate limiting active (1000 msg/month via ratelimit.Wrapper)

import (
	"encoding/json"
	"testing"

	"github.com/campfire-net/campfire/pkg/ratelimit"
	"github.com/campfire-net/campfire/pkg/store"
)

// newTestServerWithStore creates a test server with a pre-opened SQLite store
// wrapped with the free-tier rate limiter. This mirrors the session creation
// path in session.go (getOrCreate) so that idempotency checks see the same
// store on repeated calls.
func newTestServerWithStore(t *testing.T) (*server, store.Store) {
	t.Helper()
	srv := newTestServer(t)

	rawStore, err := store.Open(store.StorePath(srv.cfHome))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { rawStore.Close() })

	rl := ratelimit.New(rawStore, ratelimit.Config{})
	srv.st = rl
	return srv, rl
}

// extractInitResult parses the campfire_init JSON response from a
// campfire_id auto-provision call and returns the fields as a map.
func extractInitResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("campfire_init error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshaling result: %v", err)
	}
	var outer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &outer); err != nil || len(outer.Content) == 0 {
		t.Fatalf("cannot extract content from init result: %v", string(b))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(outer.Content[0].Text), &payload); err != nil {
		t.Fatalf("parsing init result JSON: %v — raw text: %s", err, outer.Content[0].Text)
	}
	return payload
}

// ---------------------------------------------------------------------------
// Test: new campfire_id → campfire created
// ---------------------------------------------------------------------------

// TestAutoProvision_NewCampfire verifies that calling campfire_init with a
// campfire_id that doesn't exist in the store creates a new campfire with
// threshold=1 and returns campfire_status="created".
func TestAutoProvision_NewCampfire(t *testing.T) {
	srv, _ := newTestServerWithStore(t)

	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{"campfire_id":"test-new-campfire"}}`))

	payload := extractInitResult(t, resp)

	// Must report created status.
	if payload["campfire_status"] != "created" {
		t.Errorf("expected campfire_status=created, got %v", payload["campfire_status"])
	}

	// Must return a real campfire_id (64-char hex).
	cfID, _ := payload["campfire_id"].(string)
	if len(cfID) != 64 {
		t.Errorf("expected 64-char hex campfire_id, got %q (len=%d)", cfID, len(cfID))
	}

	// Threshold must be 1.
	threshold, ok := payload["threshold"].(float64)
	if !ok || threshold != 1 {
		t.Errorf("expected threshold=1, got %v", payload["threshold"])
	}

	// Role must be "full".
	if payload["role"] != "full" {
		t.Errorf("expected role=full, got %v", payload["role"])
	}

	// Free tier must be indicated.
	if payload["free_tier"] != true {
		t.Errorf("expected free_tier=true, got %v", payload["free_tier"])
	}

	// Monthly cap must be the default.
	monthlyCap, ok := payload["monthly_cap"].(float64)
	if !ok || int(monthlyCap) != ratelimit.DefaultMonthlyMessageCap {
		t.Errorf("expected monthly_cap=%d, got %v", ratelimit.DefaultMonthlyMessageCap, payload["monthly_cap"])
	}
}

// ---------------------------------------------------------------------------
// Test: idempotent re-init returns same campfire
// ---------------------------------------------------------------------------

// TestAutoProvision_Idempotent verifies that calling campfire_init twice with
// the same campfire_id returns the same campfire_id both times and reports
// campfire_status="exists" on the second call.
func TestAutoProvision_Idempotent(t *testing.T) {
	srv, _ := newTestServerWithStore(t)

	args := `{"name":"campfire_init","arguments":{"campfire_id":"idempotent-test"}}`

	// First call: create.
	resp1 := srv.dispatch(makeReq("tools/call", args))
	p1 := extractInitResult(t, resp1)
	if p1["campfire_status"] != "created" {
		t.Errorf("first call: expected campfire_status=created, got %v", p1["campfire_status"])
	}
	cfID1, _ := p1["campfire_id"].(string)
	if len(cfID1) != 64 {
		t.Fatalf("first call: expected 64-char hex campfire_id, got %q", cfID1)
	}

	// Second call with the returned campfire_id: idempotent.
	args2, _ := json.Marshal(map[string]interface{}{
		"name":      "campfire_init",
		"arguments": map[string]interface{}{"campfire_id": cfID1},
	})
	resp2 := srv.dispatch(makeReq("tools/call", string(args2)))
	p2 := extractInitResult(t, resp2)

	if p2["campfire_status"] != "exists" {
		t.Errorf("second call: expected campfire_status=exists, got %v", p2["campfire_status"])
	}
	cfID2, _ := p2["campfire_id"].(string)
	if cfID2 != cfID1 {
		t.Errorf("second call: campfire_id mismatch: first=%q, second=%q", cfID1, cfID2)
	}
}

// ---------------------------------------------------------------------------
// Test: agent registered as member with role="full"
// ---------------------------------------------------------------------------

// TestAutoProvision_MemberRegistered verifies that after campfire_init with a
// campfire_id, the store contains a membership record with role="full" for
// the created campfire.
func TestAutoProvision_MemberRegistered(t *testing.T) {
	srv, st := newTestServerWithStore(t)

	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{"campfire_id":"member-test"}}`))
	payload := extractInitResult(t, resp)

	cfID, _ := payload["campfire_id"].(string)
	if len(cfID) != 64 {
		t.Fatalf("expected 64-char hex campfire_id, got %q", cfID)
	}

	// Verify membership is recorded in the store.
	mem, err := st.GetMembership(cfID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if mem == nil {
		t.Fatal("no membership record found in store after auto-provision")
	}
	if mem.Role != "full" {
		t.Errorf("expected role=full, got %q", mem.Role)
	}
	if mem.Threshold != 1 {
		t.Errorf("expected threshold=1, got %d", mem.Threshold)
	}
}

// ---------------------------------------------------------------------------
// Test: free-tier rate limiting enforced
// ---------------------------------------------------------------------------

// TestAutoProvision_FreeTierRateLimit verifies that the production session
// creation path in session.go (SessionManager.getOrCreate) wraps the store
// with a *ratelimit.Wrapper, and that the wrapper enforces the 1000 msg/month
// default cap.
//
// This test exercises the REAL production path — it does NOT pre-inject a
// wrapper. It creates a session via SessionManager.getOrCreate (the same
// code path that runs in production for every MCP request), then asserts
// that the resulting session store is a *ratelimit.Wrapper, and that
// AddMessage on the session store is rejected with ErrMonthlyCapExceeded
// once the monthly cap is reached.
func TestAutoProvision_FreeTierRateLimit(t *testing.T) {
	// Use the production session manager (same code path as hosted cf-mcp).
	m := newTestSessionManager(t)

	// Create a session via the real production path.
	token, err := m.issueToken()
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	sess, err := m.getOrCreate(token)
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}

	// The session store MUST be a *ratelimit.Wrapper — this is what
	// session.go is supposed to create. If it isn't, the production path
	// does not apply rate limiting.
	rl, ok := sess.st.(*ratelimit.Wrapper)
	if !ok {
		t.Fatalf("session.go getOrCreate did not wrap store with *ratelimit.Wrapper; "+
			"got %T — free-tier rate limiting is NOT applied on the production path", sess.st)
	}

	// Use a synthetic campfire ID for message operations (the rate limiter
	// tracks per-campfire counts in memory; no real campfire needs to exist
	// in the store for AddMessage to exercise the limit enforcement path).
	const campfireID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// Monthly count must start at zero for a fresh session.
	if count := rl.MonthlyCount(campfireID); count != 0 {
		t.Errorf("expected monthly count=0 for fresh session, got %d", count)
	}

	// Seed the monthly counter to one below the cap so we can test the
	// boundary with two AddMessage calls instead of 1000.
	// This mirrors how an external metering system would restore state on
	// process restart (via SetMonthlyCount).
	rl.SetMonthlyCount(campfireID, ratelimit.DefaultMonthlyMessageCap-1)

	msg := store.MessageRecord{
		CampfireID: campfireID,
		Sender:     "test",
		Payload:    []byte("hello"),
		Timestamp:  1,
		ReceivedAt: 1,
	}

	// Message #1000 (count was 999) — must succeed.
	if _, err := rl.AddMessage(msg); err != nil {
		t.Fatalf("message at count=%d should succeed, got: %v",
			ratelimit.DefaultMonthlyMessageCap-1, err)
	}
	if count := rl.MonthlyCount(campfireID); count != ratelimit.DefaultMonthlyMessageCap {
		t.Errorf("expected monthly count=%d after message 1000, got %d",
			ratelimit.DefaultMonthlyMessageCap, count)
	}

	// Message #1001 — must be rejected with ErrMonthlyCapExceeded.
	_, err = rl.AddMessage(msg)
	if !ratelimit.IsMonthlyCapExceeded(err) {
		t.Errorf("message #1001 should be rejected with ErrMonthlyCapExceeded, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: campfire_id parameter appears in tools/list schema
// ---------------------------------------------------------------------------

// TestAutoProvision_InToolsListSchema verifies that the campfire_init tool
// schema advertises the campfire_id parameter.
func TestAutoProvision_InToolsListSchema(t *testing.T) {
	srv := newTestServer(t)
	resp := srv.dispatch(makeReq("tools/list", "{}"))
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}

	b, _ := json.Marshal(resp.Result)
	var result struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshaling tools/list: %v", err)
	}

	for _, tool := range result.Tools {
		if tool.Name != "campfire_init" {
			continue
		}
		var schema struct {
			Properties map[string]interface{} `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("parsing campfire_init schema: %v", err)
		}
		if _, ok := schema.Properties["campfire_id"]; !ok {
			t.Error("campfire_init schema does not advertise campfire_id parameter")
		}
		return
	}
	t.Error("campfire_init not found in tools/list")
}

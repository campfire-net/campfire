package main

// Tests for invite code feature (security model §5.a, bead campfire-agent-uei).
//
// TDD sequence:
//   1. campfire_create response includes invite_code field
//   2. campfire_join with valid invite_code succeeds
//   3. campfire_join without invite_code on campfire WITH codes fails
//   4. campfire_join with revoked code fails
//   5. campfire_join with exhausted code (max_uses reached) fails
//   6. grace period: HasAnyInvites=false allows join without code
//   7. campfire_invite tool creates additional codes
//   8. campfire_revoke_invite tool revokes a code

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// extractCreateResult parses a campfire_create JSON response and returns
// the top-level fields as a map.
func extractCreateResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("campfire_create error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
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
		t.Fatalf("unexpected result shape: %s", string(b))
	}
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(outer.Content[0].Text), &fields); err != nil {
		t.Fatalf("parsing create result JSON: %v", err)
	}
	return fields
}

// doInit initialises an identity on the server.
func doInit(t *testing.T, srv *server) {
	t.Helper()
	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if resp.Error != nil {
		t.Fatalf("campfire_init failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test 0: campfire_join schema declares invite_code parameter
// ---------------------------------------------------------------------------

func TestInvite_JoinSchemaHasInviteCode(t *testing.T) {
	srv := newTestServer(t)
	resp := srv.dispatch(makeReq("tools/list", "{}"))
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}

	b, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshaling tools/list result: %v", err)
	}

	toolsRaw, _ := result["tools"].([]interface{})
	var joinSchema map[string]interface{}
	for _, raw := range toolsRaw {
		tool, _ := raw.(map[string]interface{})
		if tool["name"] == "campfire_join" {
			schemaBytes, _ := json.Marshal(tool["inputSchema"])
			if err := json.Unmarshal(schemaBytes, &joinSchema); err != nil {
				t.Fatalf("unmarshaling campfire_join inputSchema: %v", err)
			}
			break
		}
	}
	if joinSchema == nil {
		t.Fatal("campfire_join tool not found in tools/list")
	}

	props, _ := joinSchema["properties"].(map[string]interface{})
	if props == nil {
		t.Fatal("campfire_join inputSchema has no properties")
	}
	if _, ok := props["invite_code"]; !ok {
		t.Error("campfire_join schema missing invite_code parameter — MCP clients cannot send it")
	}
	if _, ok := props["campfire_id"]; !ok {
		t.Error("campfire_join schema missing campfire_id parameter")
	}

	// invite_code must NOT be in required (it's optional — only needed when campfire has codes).
	required, _ := joinSchema["required"].([]interface{})
	for _, r := range required {
		if r == "invite_code" {
			t.Error("invite_code must not be in required — it is only needed when campfire enforces codes")
		}
	}
}

// ---------------------------------------------------------------------------
// Test 1: campfire_create response includes invite_code field
// ---------------------------------------------------------------------------

func TestInvite_CreateReturnsInviteCode(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, resp)

	code, ok := fields["invite_code"].(string)
	if !ok || code == "" {
		t.Fatalf("expected non-empty invite_code in campfire_create response, got: %v", fields["invite_code"])
	}
	// UUID format: 36 chars with dashes
	if len(code) != 36 {
		t.Errorf("expected UUID-format invite_code (len 36), got %q (len %d)", code, len(code))
	}
}

// ---------------------------------------------------------------------------
// Test 2: campfire_join with valid invite_code succeeds
// ---------------------------------------------------------------------------

func TestInvite_JoinWithValidCode(t *testing.T) {
	// Server A creates the campfire and has invite codes in its store.
	srvA, stA := newTestServerWithStore(t)
	doInit(t, srvA)

	createResp := srvA.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	inviteCode, _ := fields["invite_code"].(string)
	if campfireID == "" || inviteCode == "" {
		t.Fatalf("missing campfire_id or invite_code in create response: %v", fields)
	}

	// Server B: separate cfHome (own identity + own store), but reads campfire
	// state from A's transport directory (shared fs).
	srvB, stB := newTestServerWithStore(t)
	// Init B to get a different identity in srvB.cfHome.
	respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if respB.Error != nil {
		t.Fatalf("init on srvB: code=%d msg=%s", respB.Error.Code, respB.Error.Message)
	}

	// Copy the invite record from A's store into B's store so B can validate it.
	inv, err := stA.LookupInvite(inviteCode)
	if err != nil || inv == nil {
		t.Fatalf("looking up invite from A's store: %v, inv=%v", err, inv)
	}
	if err := stB.CreateInvite(*inv); err != nil {
		t.Fatalf("copying invite to B's store: %v", err)
	}

	// B reads campfire fs state from A's base dir.
	srvBIdentityHome := srvB.cfHome
	srvB.cfHome = srvA.cfHome
	// But we keep srvB's store as stB (opened from srvBIdentityHome).
	// Restore the identity path by keeping the identity in srvB's original cfHome.
	// handleJoin opens a new store from s.storePath() if s.st == nil.
	// We need s.st set to stB so the store check uses stB (which has the invite).
	// But we also need the identity path to resolve to srvB's original location.
	// Solution: keep cfHome pointing to srvA (for campfire state), but override st.
	_ = srvBIdentityHome
	// The identity was written to srvB.cfHome (srvBIdentityHome) during init.
	// After setting srvB.cfHome = srvA.cfHome, identity.Load will look in srvA.cfHome.
	// srvA already has an identity there, so this works — but it's srvA's identity, not B's.
	//
	// Better approach: srvB needs its own identity dir AND its own store, but read
	// campfire state from srvA's fs transport. In the test server, cfHome controls both.
	// The simplest fix: use the dispatch path directly without shared cfHome.
	// Instead, set srvB.cfHome back to its original, and copy the campfire state.
	srvB.cfHome = srvBIdentityHome

	// Copy campfire state from A's fs transport to B's fs transport.
	// The simplest thing that works: write the campfire state into B's expected path.
	// The fs transport reads from the default base dir (CF_HOME/../campfire or temp).
	// In test mode with cfHomeExplicit=true, fsTransport() returns fs.New(cfHome) only
	// when httpTransport != nil. Otherwise it uses fs.DefaultBaseDir().
	// DefaultBaseDir() is fixed (~/.local/share/campfire or similar), which means
	// two servers in the same test process share the same fs base dir.
	// That means srvB can read campfire state that srvA wrote. Good.
	//
	// The store is the key issue: srvB needs stB (not stA) so "already a member" check
	// uses stB's membership table. Set srvB.st = stB.
	srvB.st = stB

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"invite_code": inviteCode,
	})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if joinResp.Error != nil {
		t.Fatalf("campfire_join with valid invite code failed: code=%d msg=%s",
			joinResp.Error.Code, joinResp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test 3: campfire_join without invite_code on campfire WITH codes fails
// ---------------------------------------------------------------------------

func TestInvite_JoinWithoutCodeFails(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)

	// A second agent with a fresh identity tries to join without providing an invite code.
	srvB := newTestServer(t)
	srvB.beaconDir = srv.beaconDir
	respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if respB.Error != nil {
		t.Fatalf("init srvB: %v", respB.Error)
	}
	// Share the transport dir so srvB can read campfire state.
	srvB.cfHome = srv.cfHome

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		// deliberately no invite_code
	})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if joinResp.Error == nil {
		t.Fatal("expected error when joining campfire with codes but no invite_code provided")
	}
	if joinResp.Error.Code != -32000 {
		t.Errorf("expected -32000, got %d: %s", joinResp.Error.Code, joinResp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test 4: campfire_join with revoked code fails
// ---------------------------------------------------------------------------

func TestInvite_JoinWithRevokedCodeFails(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	doInit(t, srv)

	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	inviteCode, _ := fields["invite_code"].(string)

	// Revoke the invite code via the store directly.
	if err := st.RevokeInvite(campfireID, inviteCode); err != nil {
		t.Fatalf("revoking invite: %v", err)
	}

	// Attempt join with the revoked code.
	srvB := newTestServer(t)
	srvB.beaconDir = srv.beaconDir
	respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if respB.Error != nil {
		t.Fatalf("init srvB: %v", respB.Error)
	}
	srvB.cfHome = srv.cfHome

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"invite_code": inviteCode,
	})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if joinResp.Error == nil {
		t.Fatal("expected error when joining with revoked code")
	}
}

// ---------------------------------------------------------------------------
// Test 5: campfire_join with exhausted code (max_uses reached) fails
// ---------------------------------------------------------------------------

func TestInvite_JoinWithExhaustedCodeFails(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	doInit(t, srv)

	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	// The auto-generated code has max_uses=0 (unlimited). Create an exhausted code.
	exhaustedCode := "exxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
	err := st.CreateInvite(store.InviteRecord{
		CampfireID: campfireID,
		InviteCode: exhaustedCode,
		CreatedBy:  "test",
		CreatedAt:  time.Now().UnixNano(),
		Revoked:    false,
		MaxUses:    1,
		UseCount:   1, // already at max
		Label:      "exhausted",
	})
	if err != nil {
		t.Fatalf("creating exhausted invite: %v", err)
	}

	srvB := newTestServer(t)
	srvB.beaconDir = srv.beaconDir
	respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if respB.Error != nil {
		t.Fatalf("init srvB: %v", respB.Error)
	}
	srvB.cfHome = srv.cfHome

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"invite_code": exhaustedCode,
	})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if joinResp.Error == nil {
		t.Fatal("expected error when joining with exhausted code")
	}
}

// ---------------------------------------------------------------------------
// Test 6: grace period — handleJoin allows join without invite_code when the
// joining agent's store has no invite records for the campfire.
// ---------------------------------------------------------------------------
//
// This exercises the full handler path:
//   campfire_join → HasAnyInvites=false (srvB's store has no codes) → allow join
//
// srvA creates the campfire (writes fs state + stores invite in srvA's store).
// srvB has a completely independent store with no invite records for that campfire.
// srvB shares srvA's cfHome so it can read the campfire fs state.
// srvB dispatches campfire_join without an invite_code — must succeed.

func TestInvite_GracePeriodAllowsJoinWithoutCode(t *testing.T) {
	// srvA: creates the campfire. Its store holds the auto-generated invite code.
	srvA, _ := newTestServerWithStore(t)
	doInit(t, srvA)

	createResp := srvA.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatalf("missing campfire_id in create response: %v", fields)
	}

	// srvB: fresh store with its own identity. No invite records for campfireID.
	srvB, stB := newTestServerWithStore(t)
	respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if respB.Error != nil {
		t.Fatalf("init srvB: code=%d msg=%s", respB.Error.Code, respB.Error.Message)
	}

	// Verify stB has no invites for this campfire — this is the grace-period condition.
	hasAny, err := stB.HasAnyInvites(campfireID)
	if err != nil {
		t.Fatalf("HasAnyInvites on srvB store: %v", err)
	}
	if hasAny {
		t.Fatal("test precondition failed: srvB store already has invites for campfireID")
	}

	// Point srvB at srvA's campfire fs state so ReadState / ListMembers succeeds.
	// srvB keeps its own identity (already written to srvBIdentityHome during init).
	srvBIdentityHome := srvB.cfHome
	srvB.cfHome = srvA.cfHome // read campfire state from srvA's fs transport
	srvB.st = stB             // but use srvB's store (no invites)
	_ = srvBIdentityHome      // identity was written there; srvA's cfHome has srvA's identity,
	// which is fine — handleJoin only needs to load *an* identity to get a public key.

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		// deliberately no invite_code — grace period must allow this
	})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if joinResp.Error != nil {
		t.Fatalf("campfire_join without invite_code on no-invite campfire failed: code=%d msg=%s",
			joinResp.Error.Code, joinResp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test 7: campfire_invite tool creates additional codes
// ---------------------------------------------------------------------------

func TestInvite_ToolCreatesCode(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)

	// Create an additional invite code with max_uses and label.
	inviteArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"max_uses":    float64(5),
		"label":       "team-access",
	})
	resp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_invite","arguments":`+string(inviteArgs)+`}`))
	if resp.Error != nil {
		t.Fatalf("campfire_invite failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	// Extract the new code from the response.
	b, _ := json.Marshal(resp.Result)
	var outer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &outer); err != nil || len(outer.Content) == 0 {
		t.Fatalf("bad campfire_invite result shape: %s", string(b))
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(outer.Content[0].Text), &result); err != nil {
		t.Fatalf("parsing campfire_invite result: %v", err)
	}
	code, _ := result["invite_code"].(string)
	if code == "" {
		t.Fatalf("expected invite_code in campfire_invite response, got: %v", result)
	}
	if len(code) != 36 {
		t.Errorf("expected UUID-format invite_code (len 36), got %q", code)
	}
}

// ---------------------------------------------------------------------------
// Test 8: campfire_revoke_invite tool revokes a code
// ---------------------------------------------------------------------------

func TestInvite_ToolRevokesCode(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	doInit(t, srv)

	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	inviteCode, _ := fields["invite_code"].(string)

	// Revoke via the tool.
	revokeArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"invite_code": inviteCode,
	})
	resp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_revoke_invite","arguments":`+string(revokeArgs)+`}`))
	if resp.Error != nil {
		t.Fatalf("campfire_revoke_invite failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	// Verify the code is now revoked in the store.
	inv, err := st.LookupInvite(inviteCode)
	if err != nil {
		t.Fatalf("LookupInvite after revoke: %v", err)
	}
	if inv == nil {
		t.Fatal("invite not found after revoke")
	}
	if !inv.Revoked {
		t.Error("expected invite to be revoked")
	}
}

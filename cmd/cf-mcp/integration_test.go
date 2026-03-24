package main

// integration_test.go — Full E2E security model chain.
//
// 8 scenarios exercising the full handler chain with real server instances
// (newTestServerWithStore / newTestServerWithSessions), no mocks.
//
// Scenarios:
//   1. Create campfire returns invite code
//   2. Join without invite code rejected
//   3. Join with valid invite code succeeds
//   4. Revoked invite code rejected
//   5. Exhausted invite code rejected (max_uses)
//   6. Token rotation preserves session
//   7. Revoked session rejected
//   8. Blind commit verification round-trip
//
// Design doc: docs/design-mcp-security.md
// Bead: campfire-agent-w8m

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// ---------------------------------------------------------------------------
// Scenario 1: campfire_create returns an invite code
// ---------------------------------------------------------------------------

// TestIntegration_CreateReturnsInviteCode verifies the full handler chain for
// campfire_create: init identity → create campfire → response includes a
// non-empty UUID-format invite_code.
func TestIntegration_CreateReturnsInviteCode(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, resp)

	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("expected non-empty campfire_id in campfire_create response")
	}

	code, ok := fields["invite_code"].(string)
	if !ok || code == "" {
		t.Fatalf("expected non-empty invite_code in campfire_create response, got: %v", fields["invite_code"])
	}
	// UUID format: 36 chars with dashes.
	if len(code) != 36 {
		t.Errorf("expected UUID-format invite_code (len 36), got %q (len %d)", code, len(code))
	}
}

// ---------------------------------------------------------------------------
// Scenario 2: Join without invite code rejected
// ---------------------------------------------------------------------------

// TestIntegration_JoinWithoutCodeRejected verifies that campfire_join is
// rejected with a -32000 error when the campfire has invite codes registered
// and no invite_code is provided. Full handler chain: create → join (no code) → error.
//
// Both servers use fs.DefaultBaseDir() for campfire state (shared automatically
// when httpTransport == nil). Server B has its own cfHome for identity + store.
// B's store gets A's invite records copied in so HasAnyInvites returns true.
func TestIntegration_JoinWithoutCodeRejected(t *testing.T) {
	// Server A creates the campfire — invite codes are registered in A's store.
	srvA, stA := newTestServerWithStore(t)
	doInit(t, srvA)

	createResp := srvA.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatalf("missing campfire_id in create response: %v", fields)
	}

	// Server B: own cfHome (fresh identity), own store.
	// campfire fs state is shared via fs.DefaultBaseDir() automatically.
	srvB, stB := newTestServerWithStore(t)
	respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if respB.Error != nil {
		t.Fatalf("init srvB: code=%d msg=%s", respB.Error.Code, respB.Error.Message)
	}

	// Copy A's invite record into B's store — this activates invite enforcement
	// for B (HasAnyInvites → true, so a code is required to join).
	invitesA, err := stA.ListInvites(campfireID)
	if err != nil || len(invitesA) == 0 {
		t.Fatalf("listing invites from A's store: err=%v count=%d", err, len(invitesA))
	}
	if err := stB.CreateInvite(invitesA[0]); err != nil {
		t.Fatalf("copying invite to B's store: %v", err)
	}
	// srvB.st is already stB (set by newTestServerWithStore); no cfHome override needed.

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		// deliberately no invite_code
	})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))

	if joinResp.Error == nil {
		t.Fatal("expected error when joining campfire with codes but no invite_code provided, got nil")
	}
	if joinResp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d: %s", joinResp.Error.Code, joinResp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Scenario 3: Join with valid invite code succeeds
// ---------------------------------------------------------------------------

// TestIntegration_JoinWithValidCodeSucceeds verifies the full happy-path E2E:
// create campfire (get invite code) → separate agent joins with that code → success.
//
// Both servers share campfire fs state via fs.DefaultBaseDir() automatically.
// B's store gets A's invite record so ValidateAndUseInvite succeeds.
func TestIntegration_JoinWithValidCodeSucceeds(t *testing.T) {
	srvA, stA := newTestServerWithStore(t)
	doInit(t, srvA)

	createResp := srvA.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	inviteCode, _ := fields["invite_code"].(string)
	if campfireID == "" || inviteCode == "" {
		t.Fatalf("missing campfire_id or invite_code in create response: %v", fields)
	}

	// Server B: own cfHome (fresh identity), own store.
	srvB, stB := newTestServerWithStore(t)
	respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if respB.Error != nil {
		t.Fatalf("init srvB: code=%d msg=%s", respB.Error.Code, respB.Error.Message)
	}

	// Copy the invite record from A to B so B can validate and use it.
	inv, err := stA.LookupInvite(inviteCode)
	if err != nil || inv == nil {
		t.Fatalf("looking up invite from A's store: err=%v inv=%v", err, inv)
	}
	if err := stB.CreateInvite(*inv); err != nil {
		t.Fatalf("copying invite to B's store: %v", err)
	}
	// srvB.st is already stB; campfire fs state shared via DefaultBaseDir().

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
// Scenario 4: Revoked invite code rejected
// ---------------------------------------------------------------------------

// TestIntegration_RevokedCodeRejected verifies the full chain:
// create campfire → revoke invite code → attempt join → error.
//
// Both servers share campfire fs state via fs.DefaultBaseDir(). B's store
// gets the (already-revoked) invite record so LookupInvite returns Revoked=true.
func TestIntegration_RevokedCodeRejected(t *testing.T) {
	srvA, stA := newTestServerWithStore(t)
	doInit(t, srvA)

	createResp := srvA.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	inviteCode, _ := fields["invite_code"].(string)
	if campfireID == "" || inviteCode == "" {
		t.Fatalf("missing campfire_id or invite_code: %v", fields)
	}

	// Revoke the code in A's store before copying it to B.
	if err := stA.RevokeInvite(campfireID, inviteCode); err != nil {
		t.Fatalf("revoking invite: %v", err)
	}

	// Server B: fresh identity, own store.
	srvB, stB := newTestServerWithStore(t)
	respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if respB.Error != nil {
		t.Fatalf("init srvB: code=%d msg=%s", respB.Error.Code, respB.Error.Message)
	}

	// Copy the revoked invite record to B — Revoked=true tells ValidateAndUseInvite to reject it.
	inv, err := stA.LookupInvite(inviteCode)
	if err != nil || inv == nil {
		t.Fatalf("looking up revoked invite from A's store: err=%v inv=%v", err, inv)
	}
	if err := stB.CreateInvite(*inv); err != nil {
		t.Fatalf("copying revoked invite to B's store: %v", err)
	}
	// srvB.st is already stB; campfire fs state shared via DefaultBaseDir().

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"invite_code": inviteCode,
	})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if joinResp.Error == nil {
		t.Fatal("expected error when joining with revoked code, got nil")
	}
}

// ---------------------------------------------------------------------------
// Scenario 5: Exhausted invite code rejected (max_uses)
// ---------------------------------------------------------------------------

// TestIntegration_ExhaustedCodeRejected verifies the full chain:
// create campfire → create an invite code with max_uses=1/use_count=1 →
// attempt join → error.
func TestIntegration_ExhaustedCodeRejected(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	doInit(t, srv)

	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatalf("missing campfire_id in create response: %v", fields)
	}

	// Create an already-exhausted invite code (use_count == max_uses).
	exhaustedCode := "e1111111-1111-1111-1111-111111111111"
	err := st.CreateInvite(store.InviteRecord{
		CampfireID: campfireID,
		InviteCode: exhaustedCode,
		CreatedBy:  "integration-test",
		CreatedAt:  time.Now().UnixNano(),
		Revoked:    false,
		MaxUses:    1,
		UseCount:   1, // already at limit
		Label:      "exhausted-for-test",
	})
	if err != nil {
		t.Fatalf("creating exhausted invite: %v", err)
	}

	// Server B: fresh identity, own store. Campfire state shared via DefaultBaseDir().
	srvB, stB := newTestServerWithStore(t)
	respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if respB.Error != nil {
		t.Fatalf("init srvB: code=%d msg=%s", respB.Error.Code, respB.Error.Message)
	}

	// Copy the exhausted invite code into B's store.
	inv, err := st.LookupInvite(exhaustedCode)
	if err != nil || inv == nil {
		t.Fatalf("looking up exhausted invite: err=%v inv=%v", err, inv)
	}
	if err := stB.CreateInvite(*inv); err != nil {
		t.Fatalf("copying exhausted invite to B's store: %v", err)
	}
	// srvB.st is already stB; campfire fs state shared via DefaultBaseDir().

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"invite_code": exhaustedCode,
	})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if joinResp.Error == nil {
		t.Fatal("expected error when joining with exhausted invite code, got nil")
	}
}

// ---------------------------------------------------------------------------
// Scenario 6: Token rotation preserves session
// ---------------------------------------------------------------------------

// TestIntegration_TokenRotationPreservesSession verifies the full E2E rotation
// chain via the HTTP session handler:
//   init → campfire_id (get public key) → campfire_rotate_token →
//   campfire_id with new token (same public key) → old token eventually rejected.
func TestIntegration_TokenRotationPreservesSession(t *testing.T) {
	dir := t.TempDir()
	m := &SessionManager{
		sessionsDir:         dir,
		stopCh:              make(chan struct{}),
		maxSessions:         defaultMaxSessions,
		rotationGracePeriod: 50 * time.Millisecond, // very short for testing
	}
	m.registry = newTokenRegistry()
	go m.reaper()
	t.Cleanup(m.Stop)

	srv := &server{
		cfHome:         t.TempDir(),
		beaconDir:      t.TempDir(),
		cfHomeExplicit: true,
		sessManager:    m,
	}

	// Step 1: init, get token.
	initResp := postMCP(t, srv, mcpInitBody, "")
	oldToken := extractTokenFromInit(t, initResp)
	if oldToken == "" {
		t.Fatal("expected token from campfire_init")
	}

	// Step 2: record identity (public key) before rotation.
	idBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_id","arguments":{}}}`
	pkBefore := extractPublicKey(t, postMCP(t, srv, idBody, oldToken))
	if pkBefore == "" {
		t.Fatal("expected non-empty public key before rotation")
	}

	// Step 3: rotate token.
	rotateBody := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"campfire_rotate_token","arguments":{}}}`
	rotateResp := postMCP(t, srv, rotateBody, oldToken)
	if rotateResp.Error != nil {
		t.Fatalf("campfire_rotate_token failed: %v", rotateResp.Error.Message)
	}

	newToken := extractNewTokenFromRotate(t, rotateResp)
	if newToken == "" {
		t.Fatal("expected new token from campfire_rotate_token")
	}
	if newToken == oldToken {
		t.Fatal("rotate returned the same token — no rotation occurred")
	}

	// Step 4: new token works and preserves the same identity.
	pkAfter := extractPublicKey(t, postMCP(t, srv, idBody, newToken))
	if pkBefore != pkAfter {
		t.Errorf("public key changed after rotation: before=%q after=%q", pkBefore, pkAfter)
	}

	// Step 5: old token is rejected after grace period.
	time.Sleep(100 * time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(idBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+oldToken)
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 for old token after grace period, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Scenario 7: Revoked session rejected
// ---------------------------------------------------------------------------

// TestIntegration_RevokedSessionRejected verifies the full E2E session
// revocation chain:
//   init → verify session works → campfire_revoke_session →
//   subsequent request with same token → HTTP 401.
func TestIntegration_RevokedSessionRejected(t *testing.T) {
	srv := newTestServerWithSessions(t)

	// Step 1: init, get token.
	initResp := postMCP(t, srv, mcpInitBody, "")
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected token from campfire_init")
	}

	// Step 2: verify session is functional.
	idBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"campfire_id","arguments":{}}}`
	preRevokeResp := postMCP(t, srv, idBody, token)
	if preRevokeResp.Error != nil {
		t.Fatalf("pre-revoke campfire_id failed: code=%d msg=%s",
			preRevokeResp.Error.Code, preRevokeResp.Error.Message)
	}

	// Step 3: revoke the session.
	revokeBody := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"campfire_revoke_session","arguments":{}}}`
	revokeResp := postMCP(t, srv, revokeBody, token)
	if revokeResp.Error != nil {
		t.Fatalf("campfire_revoke_session failed: code=%d msg=%s",
			revokeResp.Error.Code, revokeResp.Error.Message)
	}

	// Step 4: same token must now be rejected with HTTP 401.
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(idBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.handleMCPSessioned(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected HTTP 401 after revocation, got %d", w.Code)
	}

	var postRevokeResp jsonRPCResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&postRevokeResp); err != nil {
		t.Fatalf("decode post-revoke response: %v", err)
	}
	if postRevokeResp.Error == nil {
		t.Fatal("expected error in post-revoke response, got nil")
	}
	// Token must not be leaked in the error message.
	if strings.Contains(postRevokeResp.Error.Message, token) {
		t.Errorf("error message leaks token: %q", postRevokeResp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Scenario 8: Blind commit verification round-trip
// ---------------------------------------------------------------------------

// TestIntegration_BlindCommitRoundTrip verifies the full E2E blind commit chain:
//   campfire_commitment helper (returns commitment+nonce) →
//   campfire_send with commitment + nonce →
//   campfire_read returns commitment_verified: true.
//
// This exercises the full handler chain from commitment computation through
// storage and read-time verification without any mocks.
func TestIntegration_BlindCommitRoundTrip(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	// Step 1: create a campfire.
	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("missing campfire_id in create response")
	}

	// Step 2: use campfire_commitment helper to get a valid commitment+nonce pair.
	payload := "integration test blind commit payload"
	helperArgs, _ := json.Marshal(map[string]interface{}{
		"payload": payload,
	})
	helperResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_commitment","arguments":`+string(helperArgs)+`}`))
	if helperResp.Error != nil {
		t.Fatalf("campfire_commitment failed: code=%d msg=%s",
			helperResp.Error.Code, helperResp.Error.Message)
	}

	// Extract commitment and nonce from helper response.
	helperBytes, _ := json.Marshal(helperResp.Result)
	var helperOuter struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(helperBytes, &helperOuter); err != nil || len(helperOuter.Content) == 0 {
		t.Fatalf("unexpected campfire_commitment result shape: %s", string(helperBytes))
	}
	var helperResult struct {
		Commitment string `json:"commitment"`
		Nonce      string `json:"nonce"`
	}
	if err := json.Unmarshal([]byte(helperOuter.Content[0].Text), &helperResult); err != nil {
		t.Fatalf("parsing campfire_commitment result: %v", err)
	}
	if helperResult.Commitment == "" || helperResult.Nonce == "" {
		t.Fatalf("empty commitment or nonce from helper: %+v", helperResult)
	}
	// commitment must be a 64-char hex SHA256.
	if len(helperResult.Commitment) != 64 {
		t.Errorf("expected 64-char hex commitment, got len=%d: %q",
			len(helperResult.Commitment), helperResult.Commitment)
	}

	// Step 3: verify the helper's commitment matches our own local computation.
	localCommitment := computeCommitment(payload, helperResult.Nonce)
	if localCommitment != helperResult.Commitment {
		t.Errorf("helper commitment mismatch: local=%q helper=%q",
			localCommitment, helperResult.Commitment)
	}

	// Step 4: send the message with commitment + nonce through the full handler chain.
	sendArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id":      campfireID,
		"message":          payload,
		"commitment":       helperResult.Commitment,
		"commitment_nonce": helperResult.Nonce,
	})
	sendResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_send","arguments":`+string(sendArgs)+`}`))
	if sendResp.Error != nil {
		t.Fatalf("campfire_send with commitment failed: code=%d msg=%s",
			sendResp.Error.Code, sendResp.Error.Message)
	}

	// Step 5: read back and verify commitment_verified: true.
	readArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"all":         true,
	})
	readResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_read","arguments":`+string(readArgs)+`}`))
	msgs := extractReadMessages(t, readResp)

	if len(msgs) == 0 {
		t.Fatal("expected at least one message in campfire_read response after send")
	}

	verified, hasField := msgs[0]["commitment_verified"]
	if !hasField {
		t.Fatalf("expected commitment_verified field in message, got fields: %v", msgs[0])
	}
	if verified != true {
		t.Errorf("expected commitment_verified=true for correctly committed message, got: %v", verified)
	}
}

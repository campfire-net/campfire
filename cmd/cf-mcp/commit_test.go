package main

// Tests for blind commit feature (security model §5.d, bead campfire-agent-9l7).
//
// TDD sequence:
//   1. send with commitment + nonce → read back shows commitment_verified: true
//   2. tampered payload → commitment_verified: false
//   3. send without commitment works as before (no commitment_verified in response)
//   4. campfire_commitment helper returns valid {commitment, nonce} that round-trips

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractSendResult parses a campfire_send JSON response and returns the
// top-level fields as a map.
func extractSendResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("campfire_send error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshaling send result: %v", err)
	}
	var outer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &outer); err != nil || len(outer.Content) == 0 {
		t.Fatalf("unexpected send result shape: %s", string(b))
	}
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(outer.Content[0].Text), &fields); err != nil {
		t.Fatalf("parsing send result JSON: %v", err)
	}
	return fields
}

// extractReadMessages parses a campfire_read JSON response and returns the
// messages as a slice of maps.
func extractReadMessages(t *testing.T, resp jsonRPCResponse) []map[string]interface{} {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("campfire_read error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshaling read result: %v", err)
	}
	var outer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &outer); err != nil || len(outer.Content) == 0 {
		t.Fatalf("unexpected read result shape: %s", string(b))
	}
	var msgs []map[string]interface{}
	if err := json.Unmarshal([]byte(outer.Content[0].Text), &msgs); err != nil {
		t.Fatalf("parsing read result JSON: %v", err)
	}
	return msgs
}

// computeCommitment returns SHA256(payload + nonce) as a hex string.
func computeCommitment(payload, nonce string) string {
	h := sha256.New()
	h.Write([]byte(payload))
	h.Write([]byte(nonce))
	return hex.EncodeToString(h.Sum(nil))
}

// randomHex generates n random bytes as a hex string.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("rand.Read: %v", err))
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Test 1: send with commitment + nonce → read back shows commitment_verified: true
// ---------------------------------------------------------------------------

func TestCommitment_SendWithCommitmentVerifiesOnRead(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("missing campfire_id in create response")
	}

	payload := "hello world"
	nonce := randomHex(16)
	commitment := computeCommitment(payload, nonce)

	sendArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"message":     payload,
		"commitment":  commitment,
		"commitment_nonce": nonce,
	})
	sendResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_send","arguments":`+string(sendArgs)+`}`))
	if sendResp.Error != nil {
		t.Fatalf("campfire_send failed: code=%d msg=%s", sendResp.Error.Code, sendResp.Error.Message)
	}

	// Read back with all:true to get the message.
	readArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"all":         true,
	})
	readResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_read","arguments":`+string(readArgs)+`}`))
	msgs := extractReadMessages(t, readResp)

	if len(msgs) == 0 {
		t.Fatal("expected at least one message in read response")
	}

	// The message should have commitment_verified: true.
	verified, hasField := msgs[0]["commitment_verified"]
	if !hasField {
		t.Fatalf("expected commitment_verified field in message, got: %v", msgs[0])
	}
	if verified != true {
		t.Errorf("expected commitment_verified=true, got: %v", verified)
	}
}

// ---------------------------------------------------------------------------
// Test 2: tampered payload → commitment_verified: false
// ---------------------------------------------------------------------------

// TestCommitment_TamperedPayloadFails verifies that if the stored payload does
// not match the commitment (simulating server-side substitution), the read
// returns commitment_verified: false.
//
// We exercise this by sending with a commitment that was computed against a
// DIFFERENT payload than what was actually sent. This simulates the scenario
// where the server would substitute the payload after the client computed the
// commitment.
func TestCommitment_TamperedPayloadFails(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("missing campfire_id in create response")
	}

	// Compute commitment for "original payload" but send "tampered payload".
	originalPayload := "original payload"
	tamperedPayload := "tampered payload"
	nonce := randomHex(16)
	commitment := computeCommitment(originalPayload, nonce) // committed to original, not tampered

	sendArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id":      campfireID,
		"message":          tamperedPayload, // actual message differs from commitment
		"commitment":       commitment,
		"commitment_nonce": nonce,
	})
	sendResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_send","arguments":`+string(sendArgs)+`}`))
	if sendResp.Error != nil {
		t.Fatalf("campfire_send failed: code=%d msg=%s", sendResp.Error.Code, sendResp.Error.Message)
	}

	readArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"all":         true,
	})
	readResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_read","arguments":`+string(readArgs)+`}`))
	msgs := extractReadMessages(t, readResp)

	if len(msgs) == 0 {
		t.Fatal("expected at least one message in read response")
	}

	verified, hasField := msgs[0]["commitment_verified"]
	if !hasField {
		t.Fatalf("expected commitment_verified field in message, got: %v", msgs[0])
	}
	if verified != false {
		t.Errorf("expected commitment_verified=false for tampered payload, got: %v", verified)
	}
}

// ---------------------------------------------------------------------------
// Test 3: send without commitment works as before (no commitment_verified in response)
// ---------------------------------------------------------------------------

func TestCommitment_SendWithoutCommitmentNoVerifiedField(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("missing campfire_id in create response")
	}

	sendArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"message":     "plain message without commitment",
	})
	sendResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_send","arguments":`+string(sendArgs)+`}`))
	if sendResp.Error != nil {
		t.Fatalf("campfire_send failed: code=%d msg=%s", sendResp.Error.Code, sendResp.Error.Message)
	}

	readArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"all":         true,
	})
	readResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_read","arguments":`+string(readArgs)+`}`))
	msgs := extractReadMessages(t, readResp)

	if len(msgs) == 0 {
		t.Fatal("expected at least one message in read response")
	}

	// Messages without commitment must NOT have the commitment_verified field.
	if _, hasField := msgs[0]["commitment_verified"]; hasField {
		t.Errorf("expected no commitment_verified field for message sent without commitment, got: %v", msgs[0])
	}
}

// ---------------------------------------------------------------------------
// Test 4: campfire_commitment helper returns valid {commitment, nonce} that round-trips
// ---------------------------------------------------------------------------

func TestCommitment_HelperRoundTrips(t *testing.T) {
	srv := newTestServer(t)

	payload := "test payload for commitment helper"
	helperArgs, _ := json.Marshal(map[string]interface{}{
		"payload": payload,
	})
	resp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_commitment","arguments":`+string(helperArgs)+`}`))
	if resp.Error != nil {
		t.Fatalf("campfire_commitment failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	b, _ := json.Marshal(resp.Result)
	var outer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &outer); err != nil || len(outer.Content) == 0 {
		t.Fatalf("unexpected campfire_commitment result shape: %s", string(b))
	}
	var result struct {
		Commitment string `json:"commitment"`
		Nonce      string `json:"nonce"`
	}
	if err := json.Unmarshal([]byte(outer.Content[0].Text), &result); err != nil {
		t.Fatalf("parsing campfire_commitment result JSON: %v", err)
	}

	if result.Commitment == "" {
		t.Fatal("expected non-empty commitment")
	}
	if result.Nonce == "" {
		t.Fatal("expected non-empty nonce")
	}
	// Commitment should be a 64-char hex string (SHA256 = 32 bytes = 64 hex chars).
	if len(result.Commitment) != 64 {
		t.Errorf("expected 64-char commitment hex, got len=%d: %q", len(result.Commitment), result.Commitment)
	}

	// Verify the commitment round-trips: SHA256(payload + nonce) == commitment.
	expected := computeCommitment(payload, result.Nonce)
	if result.Commitment != expected {
		t.Errorf("commitment does not match: expected %q, got %q", expected, result.Commitment)
	}
}

package provenance

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- helpers ---

var (
	testInitiatorKey = "initiator-pubkey-aaa"
	testTargetKey    = "target-pubkey-bbb"
	testCallback     = "cf://callback-campfire-123"
)

func issueTestChallenge(t *testing.T, c *Challenger, id string, now time.Time) *Challenge {
	t.Helper()
	ch, err := c.IssueChallenge(id, testInitiatorKey, testTargetKey, testCallback, now)
	if err != nil {
		t.Fatalf("IssueChallenge failed: %v", err)
	}
	return ch
}

func validResponse(ch *Challenge) *ChallengeResponse {
	return &ChallengeResponse{
		AntecedentID:    ch.ID,
		ResponderKey:    ch.TargetKey,
		MessageSender:   ch.TargetKey, // cryptographic envelope sender — must match TargetKey
		TargetKey:       ch.TargetKey,
		Nonce:           ch.Nonce,
		ContactMethod:   "cf://my-campfire",
		ProofType:       ProofCaptcha,
		ProofToken:      "solved-captcha-token",
		ProofProvenance: "captcha-service-sig",
		RespondedAt:     ch.IssuedAt.Add(30 * time.Second),
	}
}

// --- GenerateNonce ---

// TestGenerateNonce verifies nonce is 64 hex chars (32 bytes).
func TestGenerateNonce(t *testing.T) {
	nonce, err := GenerateNonce()
	if err != nil {
		t.Fatalf("GenerateNonce error: %v", err)
	}
	if len(nonce) != 64 {
		t.Errorf("expected 64-char hex nonce, got %d chars: %q", len(nonce), nonce)
	}
	// All hex.
	for _, c := range nonce {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("nonce contains non-hex char %q", c)
		}
	}
}

// TestGenerateNonce_Uniqueness verifies two nonces are different.
func TestGenerateNonce_Uniqueness(t *testing.T) {
	a, _ := GenerateNonce()
	b, _ := GenerateNonce()
	if a == b {
		t.Errorf("two generated nonces are identical: %q", a)
	}
}

// --- IssueChallenge ---

// TestIssueChallenge_Basic verifies challenge fields are populated correctly.
func TestIssueChallenge_Basic(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch, err := c.IssueChallenge("msg-001", testInitiatorKey, testTargetKey, testCallback, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ch.ID != "msg-001" {
		t.Errorf("ID: want %q, got %q", "msg-001", ch.ID)
	}
	if ch.InitiatorKey != testInitiatorKey {
		t.Errorf("InitiatorKey: want %q, got %q", testInitiatorKey, ch.InitiatorKey)
	}
	if ch.TargetKey != testTargetKey {
		t.Errorf("TargetKey: want %q, got %q", testTargetKey, ch.TargetKey)
	}
	if ch.CallbackCampfire != testCallback {
		t.Errorf("CallbackCampfire: want %q, got %q", testCallback, ch.CallbackCampfire)
	}
	if len(ch.Nonce) != 64 {
		t.Errorf("Nonce length: want 64, got %d", len(ch.Nonce))
	}
	if !ch.IssuedAt.Equal(now) {
		t.Errorf("IssuedAt: want %v, got %v", now, ch.IssuedAt)
	}
}

// TestIssueChallenge_MissingFields verifies validation errors.
func TestIssueChallenge_MissingFields(t *testing.T) {
	c := NewChallenger()
	now := time.Now()

	cases := []struct {
		id, initiator, target, callback string
	}{
		{"", testInitiatorKey, testTargetKey, testCallback},          // empty id
		{"x", "", testTargetKey, testCallback},                       // empty initiator
		{"x", testInitiatorKey, "", testCallback},                    // empty target
		{"x", testInitiatorKey, testTargetKey, ""},                   // empty callback
	}
	for _, tc := range cases {
		_, err := c.IssueChallenge(tc.id, tc.initiator, tc.target, tc.callback, now)
		if err == nil {
			t.Errorf("expected error for id=%q initiator=%q target=%q callback=%q", tc.id, tc.initiator, tc.target, tc.callback)
		}
	}
}

// --- Target-side rate limiting (§12.1) ---

// TestRateLimit_AllowsUpToMax verifies 10 challenges succeed for the same target.
func TestRateLimit_AllowsUpToMax(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	for i := 0; i < challengeRateMax; i++ {
		id := "msg-rate-" + string(rune('a'+i))
		_, err := c.IssueChallenge(id, testInitiatorKey, testTargetKey, testCallback, now)
		if err != nil {
			t.Fatalf("challenge %d: unexpected error: %v", i, err)
		}
	}
}

// TestRateLimit_RejectsAboveMax verifies the 11th challenge to the same target is rejected.
func TestRateLimit_RejectsAboveMax(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	for i := 0; i < challengeRateMax; i++ {
		id := "msg-rl-" + string(rune('a'+i))
		_, err := c.IssueChallenge(id, testInitiatorKey, testTargetKey, testCallback, now)
		if err != nil {
			t.Fatalf("challenge %d: unexpected: %v", i, err)
		}
	}
	_, err := c.IssueChallenge("msg-rl-overflow", testInitiatorKey, testTargetKey, testCallback, now)
	if err != ErrRateLimitExceeded {
		t.Errorf("expected ErrRateLimitExceeded, got %v", err)
	}
}

// TestRateLimit_WindowExpiry verifies timestamps outside the rate window don't count.
func TestRateLimit_WindowExpiry(t *testing.T) {
	c := NewChallenger()
	past := time.Now().Add(-2 * challengeRateWindow) // 2 hours ago — outside window
	for i := 0; i < challengeRateMax; i++ {
		id := "msg-old-" + string(rune('a'+i))
		_, err := c.IssueChallenge(id, testInitiatorKey, testTargetKey, testCallback, past)
		if err != nil {
			t.Fatalf("past challenge %d: unexpected: %v", i, err)
		}
	}
	// A new challenge now should succeed because the old ones expired.
	now := time.Now()
	_, err := c.IssueChallenge("msg-new-001", testInitiatorKey, testTargetKey, testCallback, now)
	if err != nil {
		t.Errorf("expected no error after window expiry, got: %v", err)
	}
}

// TestRateLimit_PerTarget verifies rate limits are independent per target key.
func TestRateLimit_PerTarget(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	otherTarget := "other-target-key-ccc"
	// Fill up testTargetKey.
	for i := 0; i < challengeRateMax; i++ {
		id := "msg-t1-" + string(rune('a'+i))
		_, err := c.IssueChallenge(id, testInitiatorKey, testTargetKey, testCallback, now)
		if err != nil {
			t.Fatalf("target1 challenge %d: unexpected: %v", i, err)
		}
	}
	// otherTarget should still succeed.
	_, err := c.IssueChallenge("msg-t2-001", testInitiatorKey, otherTarget, testCallback, now)
	if err != nil {
		t.Errorf("other target should not be rate-limited, got: %v", err)
	}
}

// --- ValidateResponse ---

// TestValidateResponse_Valid verifies a correct response is accepted.
func TestValidateResponse_Valid(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-valid-001", now)
	resp := validResponse(ch)

	matched, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("ValidateResponse error: %v", err)
	}
	if matched.ID != ch.ID {
		t.Errorf("returned wrong challenge: want %q, got %q", ch.ID, matched.ID)
	}
}

// TestValidateResponse_MissingAntecedent verifies missing antecedent ID is rejected.
func TestValidateResponse_MissingAntecedent(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-ant-001", now)
	resp := validResponse(ch)
	resp.AntecedentID = "" // strip antecedent

	_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err != ErrMissingAntecedent {
		t.Errorf("expected ErrMissingAntecedent, got %v", err)
	}
}

// TestValidateResponse_UnknownChallenge verifies response to unknown challenge is rejected.
func TestValidateResponse_UnknownChallenge(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-unk-001", now)
	resp := validResponse(ch)
	resp.AntecedentID = "nonexistent-challenge-id"

	_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err != ErrChallengeNotFound {
		t.Errorf("expected ErrChallengeNotFound, got %v", err)
	}
}

// TestValidateResponse_Expired verifies expired challenge is rejected.
func TestValidateResponse_Expired(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-exp-001", now)
	resp := validResponse(ch)

	// Respond after TTL has elapsed.
	_, err := c.ValidateResponse(resp, now.Add(challengeTTL+time.Second))
	if err != ErrChallengeExpired {
		t.Errorf("expected ErrChallengeExpired, got %v", err)
	}
}

// TestValidateResponse_WrongNonce verifies mismatched nonce is rejected.
func TestValidateResponse_WrongNonce(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-nonce-001", now)
	resp := validResponse(ch)
	resp.Nonce = "deadbeef" + strings.Repeat("0", 56) // wrong nonce

	_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err == nil {
		t.Error("expected error for wrong nonce, got nil")
	}
}

// TestValidateResponse_WrongTargetKey verifies mismatched target_key is rejected.
func TestValidateResponse_WrongTargetKey(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-tkey-001", now)
	resp := validResponse(ch)
	resp.TargetKey = "wrong-target-key"

	_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err == nil {
		t.Error("expected error for wrong target_key, got nil")
	}
}

// TestValidateResponse_WrongResponder verifies non-target responder is rejected.
func TestValidateResponse_WrongResponder(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-resp-001", now)
	resp := validResponse(ch)
	resp.ResponderKey = "impostor-key-zzz"

	_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err == nil {
		t.Error("expected error for wrong responder, got nil")
	}
}

// TestValidateResponse_MissingMessageSender verifies that a response without a
// MessageSender is rejected. Callers MUST populate this from the transport envelope
// before calling ValidateResponse. (Regression: campfire-agent-4bn)
func TestValidateResponse_MissingMessageSender(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-sender-missing-001", now)
	resp := validResponse(ch)
	resp.MessageSender = "" // simulate caller that omits the envelope sender

	_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err == nil {
		t.Error("expected error for missing MessageSender, got nil")
	}
}

// TestValidateResponse_WrongMessageSender verifies that a response whose MessageSender
// does not match the challenge TargetKey is rejected. This prevents a campfire member
// from forging an operator-verify response on behalf of another operator.
// (Regression: campfire-agent-4bn)
func TestValidateResponse_WrongMessageSender(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-sender-wrong-001", now)
	resp := validResponse(ch)
	resp.MessageSender = "impostor-member-key-zzz" // different campfire member forging response

	_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err == nil {
		t.Error("expected error for wrong MessageSender (forged response), got nil")
	}
}

// TestValidateResponse_OneTimeUse verifies a challenge cannot be answered twice.
func TestValidateResponse_OneTimeUse(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-once-001", now)
	resp := validResponse(ch)

	// First use: should succeed.
	_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("first response: unexpected error: %v", err)
	}

	// Second use: should fail — challenge consumed.
	_, err = c.ValidateResponse(resp, now.Add(20*time.Second))
	if err != ErrChallengeNotFound {
		t.Errorf("second response: expected ErrChallengeNotFound, got %v", err)
	}
}

// --- CreateAttestation ---

// TestCreateAttestation_Valid verifies attestation is created and stored correctly.
func TestCreateAttestation_Valid(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-attest-001", now)
	resp := validResponse(ch)

	matched, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("ValidateResponse: %v", err)
	}

	cfg := DefaultConfig()
	cfg.TrustedVerifierKeys[testInitiatorKey] = 0
	store := NewStore(cfg)

	verifiedAt := now.Add(10 * time.Second)
	a, err := CreateAttestation(store, "attest-001", matched, resp, verifiedAt)
	if err != nil {
		t.Fatalf("CreateAttestation: %v", err)
	}

	if a.ID != "attest-001" {
		t.Errorf("attestation ID: want attest-001, got %q", a.ID)
	}
	if a.TargetKey != testTargetKey {
		t.Errorf("TargetKey: want %q, got %q", testTargetKey, a.TargetKey)
	}
	if a.VerifierKey != testInitiatorKey {
		t.Errorf("VerifierKey: want %q, got %q", testInitiatorKey, a.VerifierKey)
	}
	if a.Nonce != ch.Nonce {
		t.Errorf("Nonce mismatch")
	}
	if !a.CoSigned {
		t.Error("expected CoSigned=true for challenge/response attestation")
	}
	if a.ContactMethod != resp.ContactMethod {
		t.Errorf("ContactMethod: want %q, got %q", resp.ContactMethod, a.ContactMethod)
	}
	if a.ProofType != ProofCaptcha {
		t.Errorf("ProofType: want captcha, got %q", a.ProofType)
	}

	// Store should reflect level 2 or 3 for the target.
	level := store.Level(testTargetKey)
	if level < LevelContactable {
		t.Errorf("expected at least LevelContactable, got %v", level)
	}
}

// TestCreateAttestation_SelfAttestation verifies initiator==target is rejected.
func TestCreateAttestation_SelfAttestation(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	// Craft a challenge where initiator == target.
	ch := &Challenge{
		ID:               "msg-self-001",
		InitiatorKey:     testTargetKey, // same as target — self-attestation
		TargetKey:        testTargetKey,
		Nonce:            "aabbccdd" + strings.Repeat("0", 56),
		CallbackCampfire: testCallback,
		IssuedAt:         now,
	}
	// Manually register to bypass IssueChallenge validation (which doesn't block self-challenges).
	c.mu.Lock()
	c.active[ch.ID] = ch
	c.mu.Unlock()

	resp := &ChallengeResponse{
		AntecedentID:    ch.ID,
		ResponderKey:    ch.TargetKey,
		MessageSender:   ch.TargetKey, // cryptographic envelope sender
		TargetKey:       ch.TargetKey,
		Nonce:           ch.Nonce,
		ContactMethod:   "cf://self",
		ProofType:       ProofCaptcha,
		ProofToken:      "solved-captcha-token",
		ProofProvenance: "captcha-service-sig",
	}

	matched, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("ValidateResponse: %v", err)
	}

	store := NewStore(DefaultConfig()) // AllowSelfAttestation == false

	_, err = CreateAttestation(store, "attest-self-001", matched, resp, now.Add(10*time.Second))
	if err != ErrSelfAttestation {
		t.Errorf("expected ErrSelfAttestation, got %v", err)
	}
}

// TestCreateAttestation_NilInputs verifies nil checks.
func TestCreateAttestation_NilInputs(t *testing.T) {
	store := NewStore(DefaultConfig())
	ch := &Challenge{ID: "x", TargetKey: "t", InitiatorKey: "i", Nonce: "n"}
	// resp must have valid proof fields — the nil/empty-arg checks for store, ID,
	// challenge, and response are all checked before proof validation.
	resp := &ChallengeResponse{
		ProofType:  ProofCaptcha,
		ProofToken: "token",
	}

	if _, err := CreateAttestation(nil, "id", ch, resp, time.Now()); err == nil {
		t.Error("expected error for nil store")
	}
	if _, err := CreateAttestation(store, "", ch, resp, time.Now()); err == nil {
		t.Error("expected error for empty attestation ID")
	}
	if _, err := CreateAttestation(store, "id", nil, resp, time.Now()); err == nil {
		t.Error("expected error for nil challenge")
	}
	if _, err := CreateAttestation(store, "id", ch, nil, time.Now()); err == nil {
		t.Error("expected error for nil response")
	}
}

// TestFullFlow_ChallengeResponseAttestation exercises the complete happy path.
func TestFullFlow_ChallengeResponseAttestation(t *testing.T) {
	c := NewChallenger()
	now := time.Now()

	// Step 1: Issue challenge.
	ch, err := c.IssueChallenge("flow-ch-001", testInitiatorKey, testTargetKey, testCallback, now)
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}

	// Step 2: Operator responds.
	resp := &ChallengeResponse{
		AntecedentID:    ch.ID,
		ResponderKey:    testTargetKey,
		MessageSender:   testTargetKey, // cryptographic envelope sender
		TargetKey:       testTargetKey,
		Nonce:           ch.Nonce,
		ContactMethod:   "cf://contact-campfire",
		ProofType:       ProofTOTP,
		ProofToken:      "123456",
		ProofProvenance: "totp-issuer",
		RespondedAt:     now.Add(2 * time.Minute),
	}

	// Step 3: Validate response.
	matched, err := c.ValidateResponse(resp, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("ValidateResponse: %v", err)
	}

	// Step 4: Create attestation.
	cfg := DefaultConfig()
	cfg.TrustedVerifierKeys[testInitiatorKey] = 0
	store := NewStore(cfg)

	verifiedAt := now.Add(2 * time.Minute)
	_, err = CreateAttestation(store, "flow-attest-001", matched, resp, verifiedAt)
	if err != nil {
		t.Fatalf("CreateAttestation: %v", err)
	}

	// Step 5: Verify level.
	level := store.LevelAt(testTargetKey, verifiedAt)
	if level < LevelContactable {
		t.Errorf("expected LevelContactable (2) or better, got %v", level)
	}
}

// --- Regression: proof_type and proof_token validation (campfire-agent-feo) ---
//
// Before this fix, CreateAttestation and ValidateResponse accepted responses with
// empty proof_type or proof_token. An agent could submit empty proof fields and
// receive a valid attestation — bypassing human-presence verification entirely.

// TestValidateResponse_EmptyProofType verifies that a response with an empty proof_type
// is rejected. An attestation without a declared proof mechanism has no evidence of
// human presence and MUST NOT be accepted. (Regression: campfire-agent-feo)
func TestValidateResponse_EmptyProofType(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-proof-type-empty-001", now)
	resp := validResponse(ch)
	resp.ProofType = "" // strip proof_type

	_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err != ErrEmptyProofType {
		t.Errorf("expected ErrEmptyProofType for empty proof_type, got %v", err)
	}
}

// TestValidateResponse_UnknownProofType verifies that a response with an unrecognized
// proof_type is rejected. Accepting unknown proof types would let an attacker smuggle
// unverifiable "proofs" past validation. (Regression: campfire-agent-feo)
func TestValidateResponse_UnknownProofType(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-proof-type-unknown-001", now)
	resp := validResponse(ch)
	resp.ProofType = ProofType("brain-scan") // unrecognized proof type

	_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err != ErrUnknownProofType {
		t.Errorf("expected ErrUnknownProofType for unrecognized proof_type, got %v", err)
	}
}

// TestValidateResponse_EmptyProofToken verifies that a response with an empty proof_token
// is rejected. Without an actual token there is nothing to verify — the attestation has
// no human-presence evidence. (Regression: campfire-agent-feo)
func TestValidateResponse_EmptyProofToken(t *testing.T) {
	c := NewChallenger()
	now := time.Now()
	ch := issueTestChallenge(t, c, "msg-proof-token-empty-001", now)
	resp := validResponse(ch)
	resp.ProofToken = "" // strip proof_token

	_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
	if err != ErrEmptyProofToken {
		t.Errorf("expected ErrEmptyProofToken for empty proof_token, got %v", err)
	}
}

// TestCreateAttestation_EmptyProofType verifies that CreateAttestation rejects a
// response with an empty proof_type. This is a defense-in-depth check: CreateAttestation
// may be called with a manually constructed ChallengeResponse without going through
// ValidateResponse. (Regression: campfire-agent-feo)
func TestCreateAttestation_EmptyProofType(t *testing.T) {
	store := NewStore(DefaultConfig())
	ch := &Challenge{ID: "x", TargetKey: "target", InitiatorKey: "initiator", Nonce: "n"}
	resp := &ChallengeResponse{
		ProofType:  "", // empty — no proof mechanism declared
		ProofToken: "some-token",
	}

	_, err := CreateAttestation(store, "attest-id", ch, resp, time.Now())
	if err != ErrEmptyProofType {
		t.Errorf("expected ErrEmptyProofType, got %v", err)
	}
}

// TestCreateAttestation_UnknownProofType verifies that CreateAttestation rejects a
// response with an unrecognized proof_type. (Regression: campfire-agent-feo)
func TestCreateAttestation_UnknownProofType(t *testing.T) {
	store := NewStore(DefaultConfig())
	ch := &Challenge{ID: "x", TargetKey: "target", InitiatorKey: "initiator", Nonce: "n"}
	resp := &ChallengeResponse{
		ProofType:  ProofType("retinal-scan"), // not in the recognized set
		ProofToken: "some-token",
	}

	_, err := CreateAttestation(store, "attest-id", ch, resp, time.Now())
	if err != ErrUnknownProofType {
		t.Errorf("expected ErrUnknownProofType, got %v", err)
	}
}

// TestCreateAttestation_EmptyProofToken verifies that CreateAttestation rejects a
// response with an empty proof_token. (Regression: campfire-agent-feo)
func TestCreateAttestation_EmptyProofToken(t *testing.T) {
	store := NewStore(DefaultConfig())
	ch := &Challenge{ID: "x", TargetKey: "target", InitiatorKey: "initiator", Nonce: "n"}
	resp := &ChallengeResponse{
		ProofType:  ProofCaptcha,
		ProofToken: "", // empty — no actual proof provided
	}

	_, err := CreateAttestation(store, "attest-id", ch, resp, time.Now())
	if err != ErrEmptyProofToken {
		t.Errorf("expected ErrEmptyProofToken, got %v", err)
	}
}

// --- Regression: ProofToken must be cryptographically structured, not just non-empty (campfire-agent-nyt) ---
//
// Before this fix, any non-empty string was accepted as a proof_token. A random garbage
// string would pass validation even though it bears no resemblance to a valid proof.
//
// Fix: validateProofTokenFormat enforces structural rules per proof_type:
//   - TOTP: exactly 6 or 8 decimal digits (RFC 6238)
//   - SMS: 4-8 decimal digits
//   - captcha: >=16 printable non-whitespace characters
//   - hardware: >=32 printable non-whitespace characters
//   - email-link: >=32 printable non-whitespace characters

// TestValidateResponse_GarbageProofTokenRejected verifies that a random/garbage string
// is rejected as a proof_token even when it is non-empty. (Regression: campfire-agent-nyt)
func TestValidateResponse_GarbageProofTokenRejected(t *testing.T) {
	garbageCases := []struct {
		pt    ProofType
		token string
		desc  string
	}{
		{ProofTOTP, "abc123", "totp: non-digit chars"},
		{ProofTOTP, "12345", "totp: 5 digits (too short)"},
		{ProofTOTP, "1234567", "totp: 7 digits (wrong length)"},
		{ProofTOTP, "123456789", "totp: 9 digits (too long)"},
		{ProofTOTP, "abcdefgh", "totp: 8 non-digit chars"},
		{ProofSMS, "123", "sms: 3 digits (too short)"},
		{ProofSMS, "123456789", "sms: 9 digits (too long)"},
		{ProofSMS, "abc", "sms: non-digit chars"},
		{ProofSMS, "12 34", "sms: contains space"},
		{ProofCaptcha, "tooshort", "captcha: too short (8 chars)"},
		{ProofCaptcha, "x", "captcha: single char"},
		{ProofHardware, "tooshorthardware", "hardware: too short (16 chars)"},
		{ProofHardware, "x", "hardware: single char"},
		{ProofEmailLink, "tooshortemaillink", "email-link: too short (17 chars)"},
	}

	for _, tc := range garbageCases {
		t.Run(tc.desc, func(t *testing.T) {
			c := NewChallenger()
			now := time.Now()
			ch := issueTestChallenge(t, c, "msg-garbage-"+tc.desc, now)
			resp := validResponse(ch)
			resp.ProofType = tc.pt
			resp.ProofToken = tc.token

			_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
			if err == nil {
				t.Errorf("expected error for garbage proof_token %q (type %q), got nil", tc.token, tc.pt)
				return
			}
			if !errors.Is(err, ErrInvalidProofToken) {
				t.Errorf("expected ErrInvalidProofToken for %q (type %q), got: %v", tc.token, tc.pt, err)
			}
		})
	}
}

// TestCreateAttestation_GarbageProofTokenRejected verifies that CreateAttestation also
// rejects garbage proof_tokens (defense-in-depth). (Regression: campfire-agent-nyt)
func TestCreateAttestation_GarbageProofTokenRejected(t *testing.T) {
	store := NewStore(DefaultConfig())
	ch := &Challenge{ID: "y", TargetKey: "target", InitiatorKey: "initiator", Nonce: "n"}

	garbageCases := []struct {
		pt    ProofType
		token string
	}{
		{ProofTOTP, "notdigits"},
		{ProofSMS, "x"},
		{ProofCaptcha, "tooshort"},
		{ProofHardware, "tooshort"},
		{ProofEmailLink, "tooshort"},
	}

	for _, tc := range garbageCases {
		resp := &ChallengeResponse{
			ProofType:  tc.pt,
			ProofToken: tc.token,
		}
		_, err := CreateAttestation(store, "attest-garbage", ch, resp, time.Now())
		if err == nil {
			t.Errorf("expected error for garbage token %q (type %q), got nil", tc.token, tc.pt)
			continue
		}
		if !errors.Is(err, ErrInvalidProofToken) {
			t.Errorf("expected ErrInvalidProofToken for %q (type %q), got: %v", tc.token, tc.pt, err)
		}
	}
}

// --- Regression: unanswered challenges accumulate unbounded (campfire-agent-t0r) ---
//
// Before this fix, IssueChallenge added entries to the active map but nothing ever
// removed challenges that were never answered (target offline, ignored, etc.).
// Over time, the active map grew without bound. With FileChallenger, this leak
// also persisted across restarts.

// TestChallenger_ExpiredChallengeEvictedOnNextIssue verifies that an expired,
// unanswered challenge is removed from the active map when the next IssueChallenge
// call is made. (Regression: campfire-agent-t0r)
func TestChallenger_ExpiredChallengeEvictedOnNextIssue(t *testing.T) {
	c := NewChallenger()
	now := time.Now()

	// Issue a challenge that will expire.
	issueTestChallenge(t, c, "msg-expire-unanswered-001", now)

	// Verify it's in the active map.
	c.mu.Lock()
	initialSize := len(c.active)
	c.mu.Unlock()
	if initialSize != 1 {
		t.Fatalf("expected 1 active challenge, got %d", initialSize)
	}

	// Advance time past TTL and issue another challenge.
	future := now.Add(challengeTTL + time.Second)
	issueTestChallenge(t, c, "msg-new-after-expiry-001", future)

	// The expired challenge must have been evicted; only the new one remains.
	c.mu.Lock()
	afterSize := len(c.active)
	_, expiredStillPresent := c.active["msg-expire-unanswered-001"]
	c.mu.Unlock()

	if expiredStillPresent {
		t.Error("expired unanswered challenge was not evicted from the active map")
	}
	if afterSize != 1 {
		t.Errorf("expected 1 active challenge after eviction, got %d", afterSize)
	}
}

// TestChallenger_ExpiredChallengeCannotBeValidated verifies that an unanswered
// challenge cannot be validated after its TTL expires. (Regression: campfire-agent-t0r)
func TestChallenger_ExpiredChallengeCannotBeValidated(t *testing.T) {
	c := NewChallenger()
	now := time.Now()

	ch := issueTestChallenge(t, c, "msg-expire-validate-001", now)

	// Advance time past TTL and issue a new challenge to trigger eviction.
	future := now.Add(challengeTTL + time.Second)
	issueTestChallenge(t, c, "msg-new-trigger-001", future)

	// Attempt to validate the expired (now-evicted) challenge.
	resp := validResponse(ch)
	_, err := c.ValidateResponse(resp, future.Add(time.Second))
	if err != ErrChallengeNotFound {
		t.Errorf("expected ErrChallengeNotFound for evicted expired challenge, got %v", err)
	}
}

// TestChallenger_ActiveMapBoundedUnderLoad verifies that the active map does not grow
// without bound when many challenges are issued but none are answered.
// (Regression: campfire-agent-t0r)
func TestChallenger_ActiveMapBoundedUnderLoad(t *testing.T) {
	c := NewChallenger()
	now := time.Now()

	// Issue max challenges against distinct target keys (to avoid the rate limit).
	// These will all expire without being answered.
	for i := 0; i < 20; i++ {
		target := testTargetKey + "-bounded-" + string(rune('a'+i))
		id := "msg-bounded-" + string(rune('a'+i))
		_, err := c.IssueChallenge(id, testInitiatorKey, target, testCallback, now)
		if err != nil {
			t.Fatalf("IssueChallenge %d: unexpected error: %v", i, err)
		}
	}

	// Sanity: 20 unanswered challenges in map.
	c.mu.Lock()
	beforeSize := len(c.active)
	c.mu.Unlock()
	if beforeSize != 20 {
		t.Fatalf("expected 20 active challenges before eviction, got %d", beforeSize)
	}

	// Advance past TTL and issue one more challenge to trigger lazy eviction.
	future := now.Add(challengeTTL + time.Second)
	_, err := c.IssueChallenge("msg-bounded-trigger", testInitiatorKey, testTargetKey+"-new", testCallback, future)
	if err != nil {
		t.Fatalf("trigger IssueChallenge: unexpected error: %v", err)
	}

	// All 20 expired challenges must be gone; only the new one remains.
	c.mu.Lock()
	afterSize := len(c.active)
	c.mu.Unlock()
	if afterSize != 1 {
		t.Errorf("expected active map to contain only 1 challenge after TTL eviction, got %d", afterSize)
	}
}

// TestChallenger_PruneExpired verifies the explicit PruneExpired method evicts
// challenges past their TTL without requiring a new IssueChallenge call.
// (Regression: campfire-agent-t0r)
func TestChallenger_PruneExpired(t *testing.T) {
	c := NewChallenger()
	now := time.Now()

	issueTestChallenge(t, c, "msg-prune-001", now)
	issueTestChallenge(t, c, "msg-prune-002", now)

	// Prune while still fresh — nothing should be removed.
	c.PruneExpired(now.Add(time.Second))
	c.mu.Lock()
	sizeBeforeExpiry := len(c.active)
	c.mu.Unlock()
	if sizeBeforeExpiry != 2 {
		t.Errorf("expected 2 active challenges before TTL, got %d", sizeBeforeExpiry)
	}

	// Prune after TTL — both must be removed.
	c.PruneExpired(now.Add(challengeTTL + time.Second))
	c.mu.Lock()
	sizeAfterExpiry := len(c.active)
	c.mu.Unlock()
	if sizeAfterExpiry != 0 {
		t.Errorf("expected 0 active challenges after PruneExpired past TTL, got %d", sizeAfterExpiry)
	}
}

// --- Regression: challenge ID collision silently overwrites active challenge (campfire-agent-08m) ---
//
// Before this fix, IssueChallenge would silently overwrite an existing active challenge
// when the same ID was presented twice. An attacker or buggy caller could clobber a
// pending challenge, invalidating the original operator's nonce and hijacking the
// verification flow. The fix returns ErrChallengeIDCollision instead of overwriting.

// TestIssueChallenge_DuplicateIDRejected verifies that issuing a challenge with an ID
// that is already active returns ErrChallengeIDCollision rather than silently
// overwriting the existing challenge. (Regression: campfire-agent-08m)
func TestIssueChallenge_DuplicateIDRejected(t *testing.T) {
	c := NewChallenger()
	now := time.Now()

	// Issue the first challenge successfully.
	first, err := c.IssueChallenge("msg-dup-001", testInitiatorKey, testTargetKey, testCallback, now)
	if err != nil {
		t.Fatalf("first IssueChallenge: unexpected error: %v", err)
	}

	// A second call with the same ID must be rejected.
	_, err = c.IssueChallenge("msg-dup-001", testInitiatorKey, testTargetKey, testCallback, now.Add(time.Second))
	if err != ErrChallengeIDCollision {
		t.Errorf("expected ErrChallengeIDCollision for duplicate ID, got %v", err)
	}

	// The original challenge must still be intact — the nonce must not have been replaced.
	c.mu.Lock()
	stored, ok := c.active["msg-dup-001"]
	c.mu.Unlock()
	if !ok {
		t.Fatal("original challenge was removed from active map after collision attempt")
	}
	if stored.Nonce != first.Nonce {
		t.Errorf("original challenge nonce was overwritten: want %q, got %q", first.Nonce, stored.Nonce)
	}
	if stored.IssuedAt != first.IssuedAt {
		t.Errorf("original challenge IssuedAt was overwritten: want %v, got %v", first.IssuedAt, stored.IssuedAt)
	}
}

// TestIssueChallenge_DuplicateIDAfterExpiry verifies that once an expired challenge has
// been evicted, the same ID may be reused without triggering ErrChallengeIDCollision.
// (Regression: campfire-agent-08m)
func TestIssueChallenge_DuplicateIDAfterExpiry(t *testing.T) {
	c := NewChallenger()
	now := time.Now()

	// Issue a challenge that will expire.
	_, err := c.IssueChallenge("msg-reuse-001", testInitiatorKey, testTargetKey, testCallback, now)
	if err != nil {
		t.Fatalf("first IssueChallenge: unexpected error: %v", err)
	}

	// Advance past TTL and trigger a new IssueChallenge to evict the expired entry.
	future := now.Add(challengeTTL + time.Second)
	_, err = c.IssueChallenge("msg-reuse-001", testInitiatorKey, testTargetKey, testCallback, future)
	if err != nil {
		t.Errorf("expected no error when reusing an ID after expiry eviction, got %v", err)
	}
}

// TestValidateResponse_AllKnownProofTypesAccepted verifies that all five recognized
// proof types pass validation when a non-empty proof_token is provided.
// This ensures the valid set doesn't accidentally exclude any spec-defined type.
func TestValidateResponse_AllKnownProofTypesAccepted(t *testing.T) {
	// Each token must satisfy the structural format for its proof_type.
	validTokens := map[ProofType]string{
		ProofCaptcha:   "captcha-solution-token-abc123",       // 29 chars, opaque service token
		ProofTOTP:      "123456",                              // 6 decimal digits (RFC 6238)
		ProofHardware:  "hardware-attestation-blob-base64-ab", // 35 chars, attestation data
		ProofSMS:       "4567",                                // 4 decimal digits
		ProofEmailLink: "email-link-signed-redirect-token-xyz", // 36 chars, signed token
	}

	for _, pt := range []ProofType{ProofCaptcha, ProofTOTP, ProofHardware, ProofSMS, ProofEmailLink} {
		c := NewChallenger()
		now := time.Now()
		ch := issueTestChallenge(t, c, "msg-pt-"+string(pt)+"-001", now)
		resp := validResponse(ch)
		resp.ProofType = pt
		resp.ProofToken = validTokens[pt]

		_, err := c.ValidateResponse(resp, now.Add(10*time.Second))
		if err != nil {
			t.Errorf("proof_type %q with valid token should be accepted, got error: %v", pt, err)
		}
	}
}

// --- Regression: targetTimestamps map grows unboundedly for unique target keys (campfire-agent-33c) ---
//
// Before this fix, PruneExpired called pruneExpiredChallenges which removed expired
// entries from c.active but never swept c.targetTimestamps. An attacker (or long-running
// legitimate workload) generating challenges for many distinct target keys would cause the
// targetTimestamps map to grow without bound — a memory leak and DoS vector. The rate-window
// pruning only happened per-key during IssueChallenge, so keys with no further activity
// were never cleaned up.
//
// Fix: pruneExpiredChallenges now performs a global sweep of targetTimestamps after
// removing expired challenges, calling pruneTargetTimestamps for every key.

// TestChallenger_PruneExpired_CleansTargetTimestamps verifies that PruneExpired removes
// targetTimestamps entries for targets whose timestamps have all fallen outside the rate
// window. Without this fix the map grows unboundedly for unique target keys.
// (Regression: campfire-agent-33c)
func TestChallenger_PruneExpired_CleansTargetTimestamps(t *testing.T) {
	c := NewChallenger()
	now := time.Now()

	// Issue one challenge per unique target key — simulates the DoS pattern of
	// many unique targets, none of which ever re-challenge or respond.
	const numTargets = 30
	for i := 0; i < numTargets; i++ {
		target := fmt.Sprintf("target-unique-gc-%03d", i)
		id := fmt.Sprintf("msg-gc-%03d", i)
		_, err := c.IssueChallenge(id, testInitiatorKey, target, testCallback, now)
		if err != nil {
			t.Fatalf("IssueChallenge for target %q: %v", target, err)
		}
	}

	// Sanity: all target keys are present in targetTimestamps.
	c.mu.Lock()
	before := len(c.targetTimestamps)
	c.mu.Unlock()
	if before != numTargets {
		t.Fatalf("expected %d entries in targetTimestamps before prune, got %d", numTargets, before)
	}

	// Advance time past both the challenge TTL and the rate window, then prune.
	// All timestamps fall outside the rate window — every key should be swept.
	future := now.Add(challengeRateWindow + time.Second)
	c.PruneExpired(future)

	// targetTimestamps must be empty — no keys with in-window timestamps remain.
	c.mu.Lock()
	after := len(c.targetTimestamps)
	c.mu.Unlock()
	if after != 0 {
		t.Errorf("expected targetTimestamps to be empty after PruneExpired past rate window, got %d entries", after)
	}

	// active map must also be empty — all challenges expired.
	c.mu.Lock()
	activeAfter := len(c.active)
	c.mu.Unlock()
	if activeAfter != 0 {
		t.Errorf("expected active map to be empty after PruneExpired past TTL, got %d entries", activeAfter)
	}
}

// TestChallenger_PruneExpired_KeepsActiveTargetTimestamps verifies that PruneExpired does
// NOT remove targetTimestamps entries for targets that still have in-window timestamps.
// (Regression: campfire-agent-33c)
func TestChallenger_PruneExpired_KeepsActiveTargetTimestamps(t *testing.T) {
	c := NewChallenger()
	now := time.Now()

	// One target that will fall outside the window by prune time.
	_, err := c.IssueChallenge("msg-gc-old-001", testInitiatorKey, "target-old", testCallback, now)
	if err != nil {
		t.Fatalf("IssueChallenge old: %v", err)
	}

	// One target that stays within the window (issued 30 minutes after "now").
	recentTime := now.Add(30 * time.Minute)
	_, err = c.IssueChallenge("msg-gc-recent-001", testInitiatorKey, "target-recent", testCallback, recentTime)
	if err != nil {
		t.Fatalf("IssueChallenge recent: %v", err)
	}

	// Prune at a time where the old target's timestamp is outside the window
	// but the recent target's timestamp is still inside.
	// Rate window = 1 hour; old timestamp is at "now"; pruning at now+61min.
	pruneAt := now.Add(challengeRateWindow + time.Minute)
	c.PruneExpired(pruneAt)

	c.mu.Lock()
	_, oldPresent := c.targetTimestamps["target-old"]
	_, recentPresent := c.targetTimestamps["target-recent"]
	c.mu.Unlock()

	if oldPresent {
		t.Error("target-old should have been swept from targetTimestamps (all its timestamps expired)")
	}
	if !recentPresent {
		t.Error("target-recent should still be in targetTimestamps (its timestamp is within the rate window)")
	}
}

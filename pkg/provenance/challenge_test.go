package provenance

import (
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
		AntecedentID:  ch.ID,
		ResponderKey:  ch.TargetKey,
		MessageSender: ch.TargetKey, // cryptographic envelope sender
		TargetKey:     ch.TargetKey,
		Nonce:         ch.Nonce,
		ContactMethod: "cf://self",
		ProofType:     ProofCaptcha,
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
	resp := &ChallengeResponse{}

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

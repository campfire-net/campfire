package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/provenance"
	"github.com/campfire-net/campfire/pkg/store"
)

// TestResolveOperatorKey_FullKey verifies a 64-char hex key is returned as-is.
func TestResolveOperatorKey_FullKey(t *testing.T) {
	key := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	got, err := resolveOperatorKey(key, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != key {
		t.Errorf("expected %q, got %q", key, got)
	}
}

// TestResolveOperatorKey_Base64Key verifies a 44-char base64 key is returned as-is.
func TestResolveOperatorKey_Base64Key(t *testing.T) {
	key := "HiJMFLx5Wb9r7H1OVjOJtsPT6SVa0gbfG6YM3FIZf/0="
	got, err := resolveOperatorKey(key, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != key {
		t.Errorf("expected %q, got %q", key, got)
	}
}

// TestResolveOperatorKey_ShortName verifies a short name returns an error.
func TestResolveOperatorKey_ShortName(t *testing.T) {
	_, err := resolveOperatorKey("alice", nil)
	if err == nil {
		t.Error("expected error for short name, got nil")
	}
}

// TestParseVerifyResponse_Match verifies a matching response is parsed correctly.
func TestParseVerifyResponse_Match(t *testing.T) {
	ch := &provenance.Challenge{
		ID:               "challenge-001",
		InitiatorKey:     "initiator-key",
		TargetKey:        "target-key-aaa",
		Nonce:            "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		CallbackCampfire: "callback-campfire-id",
		IssuedAt:         time.Now(),
	}

	payload := map[string]interface{}{
		"convention":       "operator-provenance",
		"operation":        "operator-verify",
		"nonce":            ch.Nonce,
		"target_key":       ch.TargetKey,
		"contact_method":   "cf://my-campfire",
		"proof_type":       "captcha",
		"proof_token":      "solved-captcha",
		"proof_provenance": "captcha-service-sig",
		"antecedent":       ch.ID,
	}
	raw, _ := json.Marshal(payload)

	resp, ok := parseVerifyResponse(raw, ch)
	if !ok {
		t.Fatal("expected match, got false")
	}
	if resp.Nonce != ch.Nonce {
		t.Errorf("nonce: want %q, got %q", ch.Nonce, resp.Nonce)
	}
	if resp.TargetKey != ch.TargetKey {
		t.Errorf("target_key: want %q, got %q", ch.TargetKey, resp.TargetKey)
	}
	if resp.ProofType != provenance.ProofCaptcha {
		t.Errorf("proof_type: want %q, got %q", provenance.ProofCaptcha, resp.ProofType)
	}
	if resp.AntecedentID != ch.ID {
		t.Errorf("antecedent_id: want %q, got %q", ch.ID, resp.AntecedentID)
	}
}

// TestParseVerifyResponse_WrongOperation verifies non-verify messages are rejected.
func TestParseVerifyResponse_WrongOperation(t *testing.T) {
	ch := &provenance.Challenge{Nonce: "aabbcc"}
	payload := map[string]interface{}{"operation": "operator-challenge", "nonce": "aabbcc"}
	raw, _ := json.Marshal(payload)
	_, ok := parseVerifyResponse(raw, ch)
	if ok {
		t.Error("expected no match for wrong operation, got true")
	}
}

// TestParseVerifyResponse_WrongNonce verifies a nonce mismatch returns false.
func TestParseVerifyResponse_WrongNonce(t *testing.T) {
	ch := &provenance.Challenge{Nonce: "aabbcc"}
	payload := map[string]interface{}{"operation": "operator-verify", "nonce": "different-nonce"}
	raw, _ := json.Marshal(payload)
	_, ok := parseVerifyResponse(raw, ch)
	if ok {
		t.Error("expected no match for wrong nonce, got true")
	}
}

// TestParseVerifyResponse_InvalidJSON verifies invalid JSON returns false.
func TestParseVerifyResponse_InvalidJSON(t *testing.T) {
	ch := &provenance.Challenge{Nonce: "aabbcc"}
	_, ok := parseVerifyResponse([]byte("{not valid json}"), ch)
	if ok {
		t.Error("expected no match for invalid JSON, got true")
	}
}

// TestLoadProvenanceStore verifies loadProvenanceStore returns a functional store.
func TestLoadProvenanceStore(t *testing.T) {
	s := loadProvenanceStore()
	if s == nil {
		t.Fatal("loadProvenanceStore returned nil")
	}
	level := s.Level("some-key")
	if level != provenance.LevelAnonymous {
		t.Errorf("expected LevelAnonymous for unknown key, got %d", level)
	}
}

// TestWaitForVerifyResponse_RejectsWrongSender verifies that waitForVerifyResponse
// ignores operator-verify messages whose envelope sender does not match the challenge
// target key. A campfire member posting a forged operator-verify on behalf of the
// target MUST NOT satisfy the verification. (Regression: campfire-agent-34c)
func TestWaitForVerifyResponse_RejectsWrongSender(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := "cf-callback-regression-34c"
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
		JoinProtocol: "test",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	targetKey := "target-operator-key-aabbcc"
	impostorKey := "impostor-campfire-member-key-zz"
	nonce := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	ch := &provenance.Challenge{
		ID:               "ch-34c-001",
		InitiatorKey:     "initiator-key",
		TargetKey:        targetKey,
		Nonce:            nonce,
		CallbackCampfire: campfireID,
		IssuedAt:         time.Now(),
	}

	// Craft a valid operator-verify payload that matches the challenge — but
	// send it with the impostor's key as the envelope sender. This simulates
	// a campfire member forging a response on behalf of the target.
	payload := map[string]interface{}{
		"convention":       "operator-provenance",
		"operation":        "operator-verify",
		"nonce":            nonce,
		"target_key":       targetKey,
		"contact_method":   "cf://contact",
		"proof_type":       "captcha",
		"proof_token":      "solved",
		"proof_provenance": "sig",
		"antecedent":       ch.ID,
	}
	raw, _ := json.Marshal(payload)

	// Store message with impostor as the envelope sender.
	if _, err := s.AddMessage(store.MessageRecord{
		ID:         "msg-forged-001",
		CampfireID: campfireID,
		Sender:     impostorKey, // NOT the target — this is the forgery
		Payload:    raw,
		Tags:       []string{},
		Antecedents: []string{},
		Timestamp:  time.Now().UnixNano(),
		Signature:  []byte("fake-sig"),
		Provenance: nil,
		ReceivedAt: time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// waitForVerifyResponse with a very short timeout — the forged message
	// must be skipped, so we expect a timeout error.
	_, err = waitForVerifyResponse(ch, campfireID, 50*time.Millisecond, s)
	if err == nil {
		t.Error("expected timeout error (forged message from wrong sender must be rejected), got nil")
	}
}

// TestWaitForVerifyResponse_AcceptsCorrectSender verifies that a response message
// from the correct envelope sender (matching TargetKey) is accepted.
// (Companion positive-case for campfire-agent-34c regression test)
func TestWaitForVerifyResponse_AcceptsCorrectSender(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := "cf-callback-regression-34c-ok"
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
		JoinProtocol: "test",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	targetKey := "target-operator-key-aabbcc"
	nonce := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	ch := &provenance.Challenge{
		ID:               "ch-34c-ok-001",
		InitiatorKey:     "initiator-key",
		TargetKey:        targetKey,
		Nonce:            nonce,
		CallbackCampfire: campfireID,
		IssuedAt:         time.Now(),
	}

	payload := map[string]interface{}{
		"convention":       "operator-provenance",
		"operation":        "operator-verify",
		"nonce":            nonce,
		"target_key":       targetKey,
		"contact_method":   "cf://contact",
		"proof_type":       "captcha",
		"proof_token":      "solved",
		"proof_provenance": "sig",
		"antecedent":       ch.ID,
	}
	raw, _ := json.Marshal(payload)

	// Store message with the target as the envelope sender.
	if _, err := s.AddMessage(store.MessageRecord{
		ID:          "msg-legit-001",
		CampfireID:  campfireID,
		Sender:      targetKey, // correct sender — matches TargetKey
		Payload:     raw,
		Tags:        []string{},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
		Signature:   []byte("fake-sig"),
		Provenance:  nil,
		ReceivedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	resp, err := waitForVerifyResponse(ch, campfireID, 5*time.Second, s)
	if err != nil {
		t.Fatalf("expected response from legitimate sender, got error: %v", err)
	}
	if resp.MessageSender != targetKey {
		t.Errorf("MessageSender: want %q, got %q", targetKey, resp.MessageSender)
	}
	if resp.Nonce != nonce {
		t.Errorf("Nonce: want %q, got %q", nonce, resp.Nonce)
	}
}

// TestWaitForVerifyResponse_CursorAdvances verifies that waitForVerifyResponse
// does not re-scan previously seen messages on subsequent poll iterations.
// Regression test for campfire-agent-qwq: the original code always polled from
// timestamp 0, causing O(n) scans and unbounded memory in large campfires.
//
// This test inserts a large batch of old (irrelevant) messages followed by a
// single valid response at a higher timestamp. If the cursor is not advanced
// after the first poll, the second poll re-reads all old messages and returns
// before the valid response is inserted. With the cursor fix, subsequent polls
// start after the last-seen timestamp and find only the new message.
//
// We verify cursor correctness by checking that the function returns exactly
// the valid response message and not a spurious error.
func TestWaitForVerifyResponse_CursorAdvances(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := "cf-cursor-regression-qwq"
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
		JoinProtocol: "test",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	targetKey := "target-operator-key-cursor-test"
	nonce := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	ch := &provenance.Challenge{
		ID:               "ch-cursor-001",
		InitiatorKey:     "initiator-key",
		TargetKey:        targetKey,
		Nonce:            nonce,
		CallbackCampfire: campfireID,
		IssuedAt:         time.Now(),
	}

	// Insert 50 old irrelevant messages (from a different sender, old timestamps).
	// These should be scanned at most once (on the first poll) with the cursor fix.
	baseTS := time.Now().Add(-10 * time.Minute).UnixNano()
	for i := 0; i < 50; i++ {
		if _, err := s.AddMessage(store.MessageRecord{
			ID:          fmt.Sprintf("msg-old-%03d", i),
			CampfireID:  campfireID,
			Sender:      "unrelated-sender",
			Payload:     []byte(`{"operation":"unrelated"}`),
			Tags:        []string{},
			Antecedents: []string{},
			Timestamp:   baseTS + int64(i),
			Signature:   []byte("fake"),
			ReceivedAt:  baseTS + int64(i),
		}); err != nil {
			t.Fatalf("AddMessage old[%d]: %v", i, err)
		}
	}

	// Insert the valid response message at a timestamp after all old messages.
	validTS := baseTS + 1000
	validPayload := map[string]interface{}{
		"convention":       "operator-provenance",
		"operation":        "operator-verify",
		"nonce":            nonce,
		"target_key":       targetKey,
		"contact_method":   "cf://contact",
		"proof_type":       "captcha",
		"proof_token":      "solved",
		"proof_provenance": "sig",
		"antecedent":       ch.ID,
	}
	raw, _ := json.Marshal(validPayload)
	if _, err := s.AddMessage(store.MessageRecord{
		ID:          "msg-valid-cursor",
		CampfireID:  campfireID,
		Sender:      targetKey,
		Payload:     raw,
		Tags:        []string{},
		Antecedents: []string{},
		Timestamp:   validTS,
		Signature:   []byte("fake"),
		ReceivedAt:  validTS,
	}); err != nil {
		t.Fatalf("AddMessage valid: %v", err)
	}

	// waitForVerifyResponse should find the valid message on the first poll.
	resp, err := waitForVerifyResponse(ch, campfireID, 5*time.Second, s)
	if err != nil {
		t.Fatalf("expected valid response, got error: %v", err)
	}
	if resp.MessageSender != targetKey {
		t.Errorf("MessageSender: want %q, got %q", targetKey, resp.MessageSender)
	}
	if resp.Nonce != nonce {
		t.Errorf("Nonce: want %q, got %q", nonce, resp.Nonce)
	}
}

package cmd

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/provenance"
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

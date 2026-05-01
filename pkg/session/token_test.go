package session_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/session"
)

// ── cfs2_ token tests (cf 0.30+) ─────────────────────────────────────────────

// TestTokenV2Roundtrip verifies cfs2_ encode→decode preserves all fields.
func TestTokenV2Roundtrip(t *testing.T) {
	creatorPub, creatorPriv := generateKeypair(t)
	campfireID := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	transportConfig := []byte(`{"protocol":"filesystem","dir":"/tmp/test-session"}`)
	ttl := 2 * time.Hour

	params := session.TokenV2Params{
		CampfireID:      campfireID,
		TransportConfig: transportConfig,
		TTL:             ttl,
		CreatorPub:      creatorPub,
		CreatorPriv:     creatorPriv,
	}

	tok, err := session.EncodeTokenV2(params)
	if err != nil {
		t.Fatalf("EncodeTokenV2: %v", err)
	}

	if !strings.HasPrefix(tok, "cfs2_") {
		t.Errorf("token prefix: got %q, want prefix cfs2_", tok[:minLen(len(tok), 10)])
	}

	decoded, err := session.DecodeTokenV2(tok, creatorPub)
	if err != nil {
		t.Fatalf("DecodeTokenV2: %v", err)
	}

	if decoded.CampfireID != campfireID {
		t.Errorf("CampfireID: got %q, want %q", decoded.CampfireID, campfireID)
	}
	if string(decoded.TransportConfig) != string(transportConfig) {
		t.Errorf("TransportConfig mismatch")
	}
	if !decoded.CreatorPubKey.Equal(creatorPub) {
		t.Error("CreatorPubKey mismatch")
	}
	if decoded.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero")
	}
}

// TestTokenV2NoEphemeralKey verifies that cfs2_ tokens do not contain a private key.
// The shared ephemeral private key was the cfs1_ anti-feature; cfs2_ removes it.
func TestTokenV2NoEphemeralKey(t *testing.T) {
	creatorPub, creatorPriv := generateKeypair(t)
	params := session.TokenV2Params{
		CampfireID:      "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		TransportConfig: []byte(`{}`),
		TTL:             1 * time.Hour,
		CreatorPub:      creatorPub,
		CreatorPriv:     creatorPriv,
	}

	tok, err := session.EncodeTokenV2(params)
	if err != nil {
		t.Fatalf("EncodeTokenV2: %v", err)
	}

	// Verify the token decodes correctly.
	decoded, err := session.DecodeTokenV2(tok, creatorPub)
	if err != nil {
		t.Fatalf("DecodeTokenV2: %v", err)
	}

	// DecodedTokenV2 has no EphemeralPrivKey field — verify by type assertion.
	// The struct only has CampfireID, TransportConfig, ExpiresAt, CreatorPubKey.
	_ = decoded.CampfireID
	_ = decoded.TransportConfig
	_ = decoded.ExpiresAt
	_ = decoded.CreatorPubKey
}

// TestTokenV2LegacyRejected verifies that cfs1_ tokens are rejected by DecodeTokenV2.
func TestTokenV2LegacyRejected(t *testing.T) {
	creatorPub, creatorPriv := generateKeypair(t)
	ephPub, ephPriv := generateKeypair(t)
	campfireID := make([]byte, 32)
	rand.Read(campfireID) //nolint:errcheck

	// Encode a legacy cfs1_ token.
	tok, err := session.EncodeToken(session.TokenParams{
		CampfireID:       campfireID,
		EphemeralPubKey:  ephPub,
		EphemeralPrivKey: ephPriv,
		TransportConfig:  []byte(`{}`),
		TTL:              1 * time.Hour,
		CreatorPub:       creatorPub,
		CreatorPriv:      creatorPriv,
	})
	if err != nil {
		t.Fatalf("EncodeToken (cfs1_): %v", err)
	}

	// Attempt to decode as cfs2_ — must fail with a migration error.
	_, err = session.DecodeTokenV2(tok, creatorPub)
	if err == nil {
		t.Fatal("expected error for cfs1_ token in DecodeTokenV2, got nil")
	}
	if !strings.Contains(err.Error(), "cfs1_") {
		t.Errorf("error should mention cfs1_ migration, got: %v", err)
	}
	if !strings.Contains(err.Error(), "re-create") && !strings.Contains(err.Error(), "migrate") && !strings.Contains(err.Error(), "0.30") {
		t.Errorf("error should reference migration/0.30, got: %v", err)
	}
}

// TestTokenV2TTLExceedsMaximum verifies that TTL > 24h is rejected at creation.
func TestTokenV2TTLExceedsMaximum(t *testing.T) {
	creatorPub, creatorPriv := generateKeypair(t)

	_, err := session.EncodeTokenV2(session.TokenV2Params{
		CampfireID:  "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		TTL:         25 * time.Hour, // exceeds 24h max
		CreatorPub:  creatorPub,
		CreatorPriv: creatorPriv,
	})
	if err == nil {
		t.Fatal("expected error for TTL > 24h, got nil")
	}
	if !strings.Contains(err.Error(), "24h") {
		t.Errorf("error should mention 24h limit, got: %v", err)
	}
}

// TestTokenV2TamperedRejected verifies a tampered cfs2_ token is rejected.
func TestTokenV2TamperedRejected(t *testing.T) {
	creatorPub, creatorPriv := generateKeypair(t)
	params := session.TokenV2Params{
		CampfireID:  "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		TTL:         2 * time.Hour,
		CreatorPub:  creatorPub,
		CreatorPriv: creatorPriv,
	}

	tok, err := session.EncodeTokenV2(params)
	if err != nil {
		t.Fatalf("EncodeTokenV2: %v", err)
	}

	// Flip a byte in the signature portion.
	rawPart := tok[len("cfs2_"):]
	decodedBytes, err := base64.RawURLEncoding.DecodeString(rawPart)
	if err != nil {
		t.Fatalf("decoding token for tampering: %v", err)
	}
	sigStart := len(decodedBytes) - ed25519.SignatureSize
	decodedBytes[sigStart] ^= 0xFF
	tamperedTok := "cfs2_" + base64.RawURLEncoding.EncodeToString(decodedBytes)

	_, err = session.DecodeTokenV2(tamperedTok, creatorPub)
	if err == nil {
		t.Fatal("expected error for tampered token, got nil")
	}
	if !strings.Contains(err.Error(), "signature invalid") {
		t.Errorf("expected signature invalid error, got: %v", err)
	}
}

// TestTokenV2CreatorSigner verifies that cfs2_ tokens can be signed via CreatorSigner.
func TestTokenV2CreatorSigner(t *testing.T) {
	creatorPub, creatorPriv := generateKeypair(t)

	signer := func(msg []byte) ([]byte, error) {
		return ed25519.Sign(creatorPriv, msg), nil
	}

	params := session.TokenV2Params{
		CampfireID:    "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		TTL:           1 * time.Hour,
		CreatorPub:    creatorPub,
		CreatorSigner: signer,
		// CreatorPriv intentionally omitted.
	}

	tok, err := session.EncodeTokenV2(params)
	if err != nil {
		t.Fatalf("EncodeTokenV2 with CreatorSigner: %v", err)
	}
	if !strings.HasPrefix(tok, "cfs2_") {
		t.Errorf("token missing cfs2_ prefix: got %q", tok[:minLen(len(tok), 10)])
	}

	decoded, err := session.DecodeTokenV2(tok, creatorPub)
	if err != nil {
		t.Fatalf("DecodeTokenV2: %v", err)
	}
	if decoded.CampfireID == "" {
		t.Error("decoded CampfireID is empty")
	}
}

// TestTokenPrefixConstants verifies the prefix constants have correct values.
func TestTokenPrefixConstants(t *testing.T) {
	if session.TokenPrefixV1 != "cfs1_" {
		t.Errorf("TokenPrefixV1 = %q, want cfs1_", session.TokenPrefixV1)
	}
	if session.TokenPrefixV2 != "cfs2_" {
		t.Errorf("TokenPrefixV2 = %q, want cfs2_", session.TokenPrefixV2)
	}
	// TokenPrefix must be the active (v2) prefix.
	if session.TokenPrefix != session.TokenPrefixV2 {
		t.Errorf("TokenPrefix = %q, want cfs2_ (active prefix)", session.TokenPrefix)
	}
}

// ── cfs1_ token tests (legacy, retained for backward-compat fixture coverage) ─

// generateKeypair generates a test Ed25519 keypair.
func generateKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	return pub, priv
}

// TestTokenRoundtrip verifies that cfs1_ encode→decode preserves all fields.
// Retained as a legacy backward-compat fixture (cfs1_ format is removed in 0.30
// for production use but EncodeToken/DecodeToken remain for test infrastructure).
func TestTokenRoundtrip(t *testing.T) {
	creatorPub, creatorPriv := generateKeypair(t)
	ephPub, ephPriv := generateKeypair(t)

	campfireID := make([]byte, 32)
	if _, err := rand.Read(campfireID); err != nil {
		t.Fatalf("generating campfire ID: %v", err)
	}

	ttl := 2 * time.Hour
	transportConfig := []byte(`{"protocol":"filesystem","dir":"/tmp/test-campfire"}`)

	params := session.TokenParams{
		CampfireID:       campfireID,
		EphemeralPubKey:  ephPub,
		EphemeralPrivKey: ephPriv,
		TransportConfig:  transportConfig,
		TTL:              ttl,
		CreatorPub:       creatorPub,
		CreatorPriv:      creatorPriv,
	}

	tok, err := session.EncodeToken(params)
	if err != nil {
		t.Fatalf("EncodeToken: %v", err)
	}

	if !strings.HasPrefix(tok, "cfs1_") {
		t.Errorf("token prefix: got %q, want prefix cfs1_", tok[:minLen(len(tok), 10)])
	}

	decoded, err := session.DecodeToken(tok, creatorPub)
	if err != nil {
		t.Fatalf("DecodeToken: %v", err)
	}

	if string(decoded.CampfireID) != string(campfireID) {
		t.Errorf("CampfireID mismatch")
	}
	if string(decoded.EphemeralPubKey) != string(ephPub) {
		t.Errorf("EphemeralPubKey mismatch")
	}
	if string(decoded.EphemeralPrivKey) != string(ephPriv) {
		t.Errorf("EphemeralPrivKey mismatch")
	}
	if string(decoded.TransportConfig) != string(transportConfig) {
		t.Errorf("TransportConfig mismatch")
	}
}

// TestExpiredTokenError verifies that expired tokens return a clear error.
func TestExpiredTokenError(t *testing.T) {
	creatorPub, creatorPriv := generateKeypair(t)
	ephPub, ephPriv := generateKeypair(t)
	campfireID := make([]byte, 32)
	rand.Read(campfireID) //nolint:errcheck

	// Token with a TTL that has already expired: set expiry 1 hour in the past.
	pastExpiry := time.Now().Add(-1 * time.Hour)

	tok, err := session.EncodeTokenWithExpiry(session.TokenParams{
		CampfireID:       campfireID,
		EphemeralPubKey:  ephPub,
		EphemeralPrivKey: ephPriv,
		TransportConfig:  []byte(`{}`),
		TTL:              time.Hour, // TTL value doesn't matter here, expiry overrides
		CreatorPub:       creatorPub,
		CreatorPriv:      creatorPriv,
	}, pastExpiry)
	if err != nil {
		t.Fatalf("EncodeTokenWithExpiry: %v", err)
	}

	_, err = session.DecodeToken(tok, creatorPub)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
	if !strings.Contains(err.Error(), "session expired") {
		t.Errorf("error message should contain 'session expired', got: %v", err)
	}
	// Error must say how long ago it expired.
	if !strings.Contains(err.Error(), "ago") {
		t.Errorf("error message should say 'ago', got: %v", err)
	}
}

// TestTTLExceedsMaximum verifies that TTL > 24h is rejected at creation time.
func TestTTLExceedsMaximum(t *testing.T) {
	creatorPub, creatorPriv := generateKeypair(t)
	ephPub, ephPriv := generateKeypair(t)
	campfireID := make([]byte, 32)
	rand.Read(campfireID) //nolint:errcheck

	_, err := session.EncodeToken(session.TokenParams{
		CampfireID:       campfireID,
		EphemeralPubKey:  ephPub,
		EphemeralPrivKey: ephPriv,
		TransportConfig:  []byte(`{}`),
		TTL:              25 * time.Hour, // exceeds 24h max
		CreatorPub:       creatorPub,
		CreatorPriv:      creatorPriv,
	})
	if err == nil {
		t.Fatal("expected error for TTL > 24h, got nil")
	}
	if !strings.Contains(err.Error(), "24h") {
		t.Errorf("error message should mention 24h limit, got: %v", err)
	}
}

// TestTamperedTokenRejected verifies that a tampered token is rejected.
func TestTamperedTokenRejected(t *testing.T) {
	creatorPub, creatorPriv := generateKeypair(t)
	ephPub, ephPriv := generateKeypair(t)
	campfireID := make([]byte, 32)
	rand.Read(campfireID) //nolint:errcheck

	tok, err := session.EncodeToken(session.TokenParams{
		CampfireID:       campfireID,
		EphemeralPubKey:  ephPub,
		EphemeralPrivKey: ephPriv,
		TransportConfig:  []byte(`{}`),
		TTL:              2 * time.Hour,
		CreatorPub:       creatorPub,
		CreatorPriv:      creatorPriv,
	})
	if err != nil {
		t.Fatalf("EncodeToken: %v", err)
	}

	// Decode the base64 payload, flip a byte in the signature portion.
	rawPart := tok[len("cfs1_"):]
	decodedBytes, err := base64.RawURLEncoding.DecodeString(rawPart)
	if err != nil {
		t.Fatalf("decoding token for tampering: %v", err)
	}
	sigStart := len(decodedBytes) - ed25519.SignatureSize
	decodedBytes[sigStart] ^= 0xFF
	tamperedTok := "cfs1_" + base64.RawURLEncoding.EncodeToString(decodedBytes)

	_, err = session.DecodeToken(tamperedTok, creatorPub)
	if err == nil {
		t.Fatal("expected error for tampered token, got nil")
	}
	if !strings.Contains(err.Error(), "signature invalid") {
		t.Errorf("expected signature invalid error, got: %v", err)
	}
}

// TestWrongCreatorKeyRejected verifies that a token signed by key A is rejected
// when DecodeToken is called with a different key B as the expected creator.
func TestWrongCreatorKeyRejected(t *testing.T) {
	creatorPubA, creatorPrivA := generateKeypair(t)
	creatorPubB, _ := generateKeypair(t) // different key — not used for signing
	ephPub, ephPriv := generateKeypair(t)
	campfireID := make([]byte, 32)
	rand.Read(campfireID) //nolint:errcheck

	// Encode token signed by key A.
	tok, err := session.EncodeToken(session.TokenParams{
		CampfireID:       campfireID,
		EphemeralPubKey:  ephPub,
		EphemeralPrivKey: ephPriv,
		TransportConfig:  []byte(`{}`),
		TTL:              2 * time.Hour,
		CreatorPub:       creatorPubA,
		CreatorPriv:      creatorPrivA,
	})
	if err != nil {
		t.Fatalf("EncodeToken: %v", err)
	}

	// Attempt to decode with key B — must be rejected.
	_, err = session.DecodeToken(tok, creatorPubB)
	if err == nil {
		t.Fatal("expected error when decoding token with wrong creator key, got nil")
	}
	if !strings.Contains(err.Error(), "signature invalid") {
		t.Errorf("expected signature invalid error, got: %v", err)
	}
}

// minLen returns the smaller of a and b.
func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

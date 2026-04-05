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

// generateKeypair generates a test Ed25519 keypair.
func generateKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	return pub, priv
}

// TestTokenRoundtrip verifies that encoding then decoding preserves all fields.
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
		CampfireID:      campfireID,
		EphemeralPubKey: ephPub,
		EphemeralPrivKey: ephPriv,
		TransportConfig: transportConfig,
		TTL:             ttl,
		CreatorPub:      creatorPub,
		CreatorPriv:     creatorPriv,
	}

	tok, err := session.EncodeToken(params)
	if err != nil {
		t.Fatalf("EncodeToken: %v", err)
	}

	if !strings.HasPrefix(tok, "cfs1_") {
		t.Errorf("token prefix: got %q, want prefix cfs1_", tok[:min(len(tok), 10)])
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

	// Decode the base64 payload, flip a byte in the signature portion
	// (last 64 bytes = ed25519.SignatureSize), re-encode as valid base64.
	// This exercises ed25519.Verify() rejection, not base64 decode failure.
	rawPart := tok[len("cfs1_"):]
	decoded, err := base64.RawURLEncoding.DecodeString(rawPart)
	if err != nil {
		t.Fatalf("decoding token for tampering: %v", err)
	}
	// Flip a byte in the signature (last 64 bytes).
	sigStart := len(decoded) - ed25519.SignatureSize
	decoded[sigStart] ^= 0xFF
	tamperedTok := "cfs1_" + base64.RawURLEncoding.EncodeToString(decoded)

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

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

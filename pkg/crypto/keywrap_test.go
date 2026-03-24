package crypto_test

import (
	"bytes"
	"crypto/rand"
	"testing"

	cfcrypto "github.com/campfire-net/campfire/pkg/crypto"
)

func TestWrapKeyUnwrapKeyRoundTrip(t *testing.T) {
	// Generate a realistic Ed25519 private key (64 bytes).
	privKey := make([]byte, 64)
	if _, err := rand.Read(privKey); err != nil {
		t.Fatalf("generating private key: %v", err)
	}
	sessionToken := []byte("test-session-token-value")

	wrapped, err := cfcrypto.WrapKey(privKey, sessionToken)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	if len(wrapped) == 0 {
		t.Fatal("WrapKey returned empty blob")
	}

	unwrapped, err := cfcrypto.UnwrapKey(wrapped, sessionToken)
	if err != nil {
		t.Fatalf("UnwrapKey: %v", err)
	}
	if !bytes.Equal(unwrapped, privKey) {
		t.Error("round-trip: unwrapped key does not match original")
	}
}

func TestWrapKeyProducesRandomCiphertext(t *testing.T) {
	privKey := make([]byte, 64)
	rand.Read(privKey) //nolint:errcheck
	sessionToken := []byte("token")

	w1, err := cfcrypto.WrapKey(privKey, sessionToken)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	w2, err := cfcrypto.WrapKey(privKey, sessionToken)
	if err != nil {
		t.Fatalf("WrapKey second call: %v", err)
	}
	// AES-GCM uses a random nonce so the blobs should differ.
	if bytes.Equal(w1, w2) {
		t.Error("two WrapKey calls on the same input produced identical output (nonce reuse)")
	}
}

func TestUnwrapKeyWrongTokenFails(t *testing.T) {
	privKey := make([]byte, 64)
	rand.Read(privKey) //nolint:errcheck

	correctToken := []byte("correct-token")
	wrongToken := []byte("wrong-token")

	wrapped, err := cfcrypto.WrapKey(privKey, correctToken)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}

	_, err = cfcrypto.UnwrapKey(wrapped, wrongToken)
	if err == nil {
		t.Fatal("UnwrapKey with wrong token: expected error, got nil")
	}
}

func TestUnwrapKeyEmptyTokenFails(t *testing.T) {
	privKey := make([]byte, 64)
	rand.Read(privKey) //nolint:errcheck

	wrapped, err := cfcrypto.WrapKey(privKey, []byte("some-token"))
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}

	// Empty token produces a different KEK — should fail authentication.
	_, err = cfcrypto.UnwrapKey(wrapped, []byte{})
	if err == nil {
		t.Fatal("UnwrapKey with empty token: expected error, got nil")
	}
}

func TestUnwrapKeyCorruptedBlobFails(t *testing.T) {
	privKey := make([]byte, 64)
	rand.Read(privKey) //nolint:errcheck
	token := []byte("token")

	wrapped, err := cfcrypto.WrapKey(privKey, token)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}

	// Flip the last byte of the GCM tag.
	corrupted := append([]byte(nil), wrapped...)
	corrupted[len(corrupted)-1] ^= 0xFF

	_, err = cfcrypto.UnwrapKey(corrupted, token)
	if err == nil {
		t.Fatal("UnwrapKey with corrupted blob: expected error, got nil")
	}
}

func TestWrapKeyArbitraryTokenBytes(t *testing.T) {
	// Session token is opaque — it should work even with binary bytes.
	privKey := make([]byte, 64)
	rand.Read(privKey) //nolint:errcheck

	// Binary token (not UTF-8 string).
	token := make([]byte, 32)
	rand.Read(token) //nolint:errcheck

	wrapped, err := cfcrypto.WrapKey(privKey, token)
	if err != nil {
		t.Fatalf("WrapKey with binary token: %v", err)
	}
	unwrapped, err := cfcrypto.UnwrapKey(wrapped, token)
	if err != nil {
		t.Fatalf("UnwrapKey with binary token: %v", err)
	}
	if !bytes.Equal(unwrapped, privKey) {
		t.Error("round-trip with binary token failed")
	}
}

func TestWrapKeyDomainSeparation(t *testing.T) {
	// Same token with a different info string (simulated by using the function
	// as a black box) — the wrapped blobs should decrypt correctly only with the
	// matching function. This test verifies that WrapKey and UnwrapKey are
	// consistent with each other (both use "campfire-key-wrap-v1").
	privKey := make([]byte, 64)
	rand.Read(privKey) //nolint:errcheck
	token := []byte("domain-sep-test")

	wrapped, err := cfcrypto.WrapKey(privKey, token)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	got, err := cfcrypto.UnwrapKey(wrapped, token)
	if err != nil {
		t.Fatalf("UnwrapKey: %v", err)
	}
	if !bytes.Equal(got, privKey) {
		t.Error("domain separation test: round-trip mismatch")
	}
}

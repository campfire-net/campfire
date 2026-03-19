package http

// Internal tests for verifyRequestSignature edge cases.
// Package http (not http_test) to access the unexported function.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func makeTestSignerKeyPair(t *testing.T) (privKey ed25519.PrivateKey, pubKeyHex string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ed25519 key: %v", err)
	}
	return priv, hex.EncodeToString(pub)
}

func signBody(priv ed25519.PrivateKey, body []byte) string {
	sig := ed25519.Sign(priv, body)
	return base64.StdEncoding.EncodeToString(sig)
}

// TestVerifyRequestSignatureValid confirms a correctly signed request passes.
func TestVerifyRequestSignatureValid(t *testing.T) {
	priv, pubHex := makeTestSignerKeyPair(t)
	body := []byte(`{"msg":"hello"}`)
	sigB64 := signBody(priv, body)

	if err := verifyRequestSignature(pubHex, sigB64, body); err != nil {
		t.Fatalf("expected valid signature to pass: %v", err)
	}
}

// TestVerifyRequestSignatureEmptyBody verifies that signing an empty body works.
func TestVerifyRequestSignatureEmptyBody(t *testing.T) {
	priv, pubHex := makeTestSignerKeyPair(t)
	body := []byte{}
	sigB64 := signBody(priv, body)

	if err := verifyRequestSignature(pubHex, sigB64, body); err != nil {
		t.Fatalf("expected valid empty-body signature to pass: %v", err)
	}
}

// TestVerifyRequestSignatureEmptyBodyNonEmptySig checks that a valid sig over
// non-empty body is rejected when the body is actually empty.
func TestVerifyRequestSignatureEmptyBodyNonEmptySig(t *testing.T) {
	priv, pubHex := makeTestSignerKeyPair(t)
	nonEmptyBody := []byte(`{"msg":"not empty"}`)
	sigB64 := signBody(priv, nonEmptyBody) // signed over non-empty body

	// Present empty body — signature should not verify.
	if err := verifyRequestSignature(pubHex, sigB64, []byte{}); err == nil {
		t.Fatal("expected non-empty signature over empty body to fail, got nil")
	}
}

// TestVerifyRequestSignatureReplayedSignature verifies that a signature from a
// different (older) request body is rejected for a new body.
func TestVerifyRequestSignatureReplayedSignature(t *testing.T) {
	priv, pubHex := makeTestSignerKeyPair(t)

	originalBody := []byte(`{"action":"join","campfire":"old"}`)
	replayedSigB64 := signBody(priv, originalBody)

	newBody := []byte(`{"action":"join","campfire":"new"}`)

	// Replaying the original signature against the new body must fail.
	if err := verifyRequestSignature(pubHex, replayedSigB64, newBody); err == nil {
		t.Fatal("replayed signature should be rejected for a different body")
	}
}

// TestVerifyRequestSignatureWrongKey verifies that a valid signature from one key
// is rejected when presented as if from a different key.
func TestVerifyRequestSignatureWrongKey(t *testing.T) {
	priv1, _ := makeTestSignerKeyPair(t)
	_, pubHex2 := makeTestSignerKeyPair(t) // different keypair

	body := []byte(`{"msg":"signed by key1"}`)
	sigB64 := signBody(priv1, body) // signed with key1

	// Claimed to be from key2 — must fail.
	if err := verifyRequestSignature(pubHex2, sigB64, body); err == nil {
		t.Fatal("signature from wrong key should be rejected")
	}
}

// TestVerifyRequestSignatureWrongLengthKey verifies that a public key that is
// not exactly ed25519.PublicKeySize bytes is rejected.
func TestVerifyRequestSignatureWrongLengthKey(t *testing.T) {
	priv, _ := makeTestSignerKeyPair(t)
	body := []byte("body")
	sigB64 := signBody(priv, body)

	for _, keyHex := range []string{
		"",                                       // empty key
		hex.EncodeToString(make([]byte, 16)),     // 16 bytes (too short)
		hex.EncodeToString(make([]byte, 33)),     // 33 bytes (too long)
		hex.EncodeToString(make([]byte, 64)),     // 64 bytes (double-length)
	} {
		if err := verifyRequestSignature(keyHex, sigB64, body); err == nil {
			t.Errorf("expected error for wrong-length key %q, got nil", keyHex)
		}
	}
}

// TestVerifyRequestSignatureTamperedSig verifies that a tampered signature is rejected.
func TestVerifyRequestSignatureTamperedSig(t *testing.T) {
	priv, pubHex := makeTestSignerKeyPair(t)
	body := []byte("important request body")
	sig := ed25519.Sign(priv, body)

	// Flip the last byte of the signature.
	sig[len(sig)-1] ^= 0xFF
	tamperedSigB64 := base64.StdEncoding.EncodeToString(sig)

	if err := verifyRequestSignature(pubHex, tamperedSigB64, body); err == nil {
		t.Fatal("tampered signature should be rejected")
	}
}

// TestVerifyRequestSignatureInvalidBase64Sig verifies that a malformed base64
// signature is rejected before any verification attempt.
func TestVerifyRequestSignatureInvalidBase64Sig(t *testing.T) {
	_, pubHex := makeTestSignerKeyPair(t)
	body := []byte("some body")

	if err := verifyRequestSignature(pubHex, "not-valid-base64!!!", body); err == nil {
		t.Fatal("invalid base64 signature should be rejected")
	}
}

// TestVerifyRequestSignatureInvalidHexKey verifies that a malformed hex key is rejected.
func TestVerifyRequestSignatureInvalidHexKey(t *testing.T) {
	priv, _ := makeTestSignerKeyPair(t)
	body := []byte("body")
	sigB64 := signBody(priv, body)

	if err := verifyRequestSignature("not-hex!!", sigB64, body); err == nil {
		t.Fatal("invalid hex key should be rejected")
	}
}

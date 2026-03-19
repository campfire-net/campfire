package http

// Internal (white-box) tests for aesGCMEncrypt, aesGCMDecrypt, hkdfSHA256,
// generateX25519Key, and parseX25519PublicKey.
//
// These are "package http" tests (no _test suffix on the package) so they can
// access unexported symbols.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

// --- aesGCMEncrypt / aesGCMDecrypt ---

func TestAESGCMRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generating key: %v", err)
	}
	plaintext := []byte("hello, campfire!")

	ct, err := aesGCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("aesGCMEncrypt: %v", err)
	}
	if len(ct) == 0 {
		t.Fatal("aesGCMEncrypt: returned empty ciphertext")
	}

	pt, err := aesGCMDecrypt(key, ct)
	if err != nil {
		t.Fatalf("aesGCMDecrypt: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Errorf("decrypted %q, want %q", pt, plaintext)
	}
}

func TestAESGCMEncryptEmptyPlaintext(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key) //nolint:errcheck

	ct, err := aesGCMEncrypt(key, []byte{})
	if err != nil {
		t.Fatalf("aesGCMEncrypt: %v", err)
	}
	pt, err := aesGCMDecrypt(key, ct)
	if err != nil {
		t.Fatalf("aesGCMDecrypt: %v", err)
	}
	if len(pt) != 0 {
		t.Errorf("expected empty plaintext, got %v", pt)
	}
}

func TestAESGCMEncryptWrongKeyLength(t *testing.T) {
	for _, keyLen := range []int{0, 16, 31, 33, 64} {
		key := make([]byte, keyLen)
		if _, err := aesGCMEncrypt(key, []byte("test")); err == nil {
			t.Errorf("aesGCMEncrypt with %d-byte key: expected error, got nil", keyLen)
		}
	}
}

func TestAESGCMDecryptWrongKeyLength(t *testing.T) {
	for _, keyLen := range []int{0, 16, 31, 33, 64} {
		key := make([]byte, keyLen)
		if _, err := aesGCMDecrypt(key, []byte("nonce+ct")); err == nil {
			t.Errorf("aesGCMDecrypt with %d-byte key: expected error, got nil", keyLen)
		}
	}
}

func TestAESGCMDecryptTruncatedData(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key) //nolint:errcheck

	// Data shorter than the GCM nonce (12 bytes).
	for _, l := range []int{0, 1, 5, 11} {
		data := make([]byte, l)
		if _, err := aesGCMDecrypt(key, data); err == nil {
			t.Errorf("aesGCMDecrypt with %d-byte data: expected error, got nil", l)
		}
	}
}

func TestAESGCMDecryptTamperedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key) //nolint:errcheck

	ct, err := aesGCMEncrypt(key, []byte("secret"))
	if err != nil {
		t.Fatalf("aesGCMEncrypt: %v", err)
	}

	// Flip the last byte of the ciphertext (auth tag area).
	tampered := append([]byte(nil), ct...)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := aesGCMDecrypt(key, tampered); err == nil {
		t.Fatal("aesGCMDecrypt with tampered ciphertext: expected error, got nil")
	}
}

func TestAESGCMDecryptWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	rand.Read(key1) //nolint:errcheck
	rand.Read(key2) //nolint:errcheck

	ct, err := aesGCMEncrypt(key1, []byte("secret"))
	if err != nil {
		t.Fatalf("aesGCMEncrypt: %v", err)
	}
	if _, err := aesGCMDecrypt(key2, ct); err == nil {
		t.Fatal("aesGCMDecrypt with wrong key: expected error, got nil")
	}
}

func TestAESGCMNonceIsRandom(t *testing.T) {
	// Two encryptions of the same plaintext with the same key should produce
	// different ciphertexts (different nonces).
	key := make([]byte, 32)
	rand.Read(key) //nolint:errcheck
	plaintext := []byte("same message")

	ct1, err := aesGCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("first encrypt: %v", err)
	}
	ct2, err := aesGCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("second encrypt: %v", err)
	}
	if bytes.Equal(ct1, ct2) {
		t.Error("two encryptions of the same plaintext produced identical ciphertext (nonce reuse)")
	}
}

// --- hkdfSHA256 ---

func TestHKDFSHA256Length(t *testing.T) {
	ikm := make([]byte, 32)
	rand.Read(ikm) //nolint:errcheck

	okm, err := hkdfSHA256(ikm, "campfire-join-v1")
	if err != nil {
		t.Fatalf("hkdfSHA256: %v", err)
	}
	if len(okm) != 32 {
		t.Errorf("expected 32-byte OKM, got %d", len(okm))
	}
}

func TestHKDFSHA256Deterministic(t *testing.T) {
	ikm := make([]byte, 32)
	rand.Read(ikm) //nolint:errcheck

	okm1, _ := hkdfSHA256(ikm, "test-info")
	okm2, _ := hkdfSHA256(ikm, "test-info")
	if !bytes.Equal(okm1, okm2) {
		t.Error("hkdfSHA256 is not deterministic for same inputs")
	}
}

func TestHKDFSHA256DifferentInfo(t *testing.T) {
	ikm := make([]byte, 32)
	rand.Read(ikm) //nolint:errcheck

	okm1, _ := hkdfSHA256(ikm, "campfire-join-v1")
	okm2, _ := hkdfSHA256(ikm, "campfire-rekey-v1")
	if bytes.Equal(okm1, okm2) {
		t.Error("different info strings produced same OKM (domain separation failure)")
	}
}

func TestHKDFSHA256DifferentIKM(t *testing.T) {
	ikm1 := make([]byte, 32)
	ikm2 := make([]byte, 32)
	rand.Read(ikm1) //nolint:errcheck
	rand.Read(ikm2) //nolint:errcheck

	okm1, _ := hkdfSHA256(ikm1, "test")
	okm2, _ := hkdfSHA256(ikm2, "test")
	if bytes.Equal(okm1, okm2) {
		t.Error("different IKM produced same OKM")
	}
}

// TestHKDFSHA256NotRawSharedSecret verifies that the derived key differs from
// the raw X25519 shared secret (i.e., HKDF is actually applied).
func TestHKDFSHA256NotRawSharedSecret(t *testing.T) {
	ikm := make([]byte, 32)
	rand.Read(ikm) //nolint:errcheck

	okm, err := hkdfSHA256(ikm, "campfire-join-v1")
	if err != nil {
		t.Fatalf("hkdfSHA256: %v", err)
	}
	if bytes.Equal(okm, ikm) {
		t.Error("hkdfSHA256 returned the raw IKM unchanged — KDF is not applied")
	}
}

// --- generateX25519Key / parseX25519PublicKey ---

func TestGenerateX25519Key(t *testing.T) {
	priv, err := generateX25519Key()
	if err != nil {
		t.Fatalf("generateX25519Key: %v", err)
	}
	if priv == nil {
		t.Fatal("generateX25519Key: returned nil")
	}
	pub := priv.PublicKey()
	if len(pub.Bytes()) != 32 {
		t.Errorf("expected 32-byte X25519 public key, got %d", len(pub.Bytes()))
	}
}

func TestParseX25519PublicKey(t *testing.T) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	pubBytes := priv.PublicKey().Bytes()

	parsed, err := parseX25519PublicKey(pubBytes)
	if err != nil {
		t.Fatalf("parseX25519PublicKey: %v", err)
	}
	if !bytes.Equal(parsed.Bytes(), pubBytes) {
		t.Errorf("parsed public key bytes mismatch")
	}
}

func TestParseX25519PublicKeyInvalid(t *testing.T) {
	// Too short.
	if _, err := parseX25519PublicKey([]byte{0x01, 0x02}); err == nil {
		t.Error("parseX25519PublicKey with 2 bytes: expected error, got nil")
	}
	// Empty.
	if _, err := parseX25519PublicKey([]byte{}); err == nil {
		t.Error("parseX25519PublicKey with empty bytes: expected error, got nil")
	}
	// Wrong length (33 bytes).
	if _, err := parseX25519PublicKey(make([]byte, 33)); err == nil {
		t.Error("parseX25519PublicKey with 33 bytes: expected error, got nil")
	}
}

// TestX25519ECDHWithHKDF is an end-to-end test of the full key exchange flow:
// generate two keypairs, do ECDH on both sides, apply HKDF, encrypt, decrypt.
func TestX25519ECDHWithHKDF(t *testing.T) {
	privA, err := generateX25519Key()
	if err != nil {
		t.Fatalf("generating key A: %v", err)
	}
	privB, err := generateX25519Key()
	if err != nil {
		t.Fatalf("generating key B: %v", err)
	}

	rawAB, err := privA.ECDH(privB.PublicKey())
	if err != nil {
		t.Fatalf("ECDH A->B: %v", err)
	}
	rawBA, err := privB.ECDH(privA.PublicKey())
	if err != nil {
		t.Fatalf("ECDH B->A: %v", err)
	}

	keyA, err := hkdfSHA256(rawAB, "campfire-join-v1")
	if err != nil {
		t.Fatalf("hkdfSHA256 A side: %v", err)
	}
	keyB, err := hkdfSHA256(rawBA, "campfire-join-v1")
	if err != nil {
		t.Fatalf("hkdfSHA256 B side: %v", err)
	}

	if !bytes.Equal(keyA, keyB) {
		t.Fatal("ECDH + HKDF: A and B derived different keys")
	}

	plaintext := []byte("end-to-end campfire key exchange test")
	ct, err := aesGCMEncrypt(keyA, plaintext)
	if err != nil {
		t.Fatalf("aesGCMEncrypt: %v", err)
	}
	pt, err := aesGCMDecrypt(keyB, ct)
	if err != nil {
		t.Fatalf("aesGCMDecrypt: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Errorf("decrypted %q, want %q", pt, plaintext)
	}
}

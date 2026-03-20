package crypto_test

import (
	"bytes"
	"crypto/rand"
	"testing"

	cfcrypto "github.com/campfire-net/campfire/pkg/crypto"
)

// --- AESGCMEncrypt / AESGCMDecrypt ---

func TestAESGCMRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generating key: %v", err)
	}
	plaintext := []byte("hello, campfire!")

	ct, err := cfcrypto.AESGCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("AESGCMEncrypt: %v", err)
	}
	if len(ct) == 0 {
		t.Fatal("AESGCMEncrypt: returned empty ciphertext")
	}

	pt, err := cfcrypto.AESGCMDecrypt(key, ct)
	if err != nil {
		t.Fatalf("AESGCMDecrypt: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Errorf("decrypted %q, want %q", pt, plaintext)
	}
}

func TestAESGCMEncryptEmptyPlaintext(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key) //nolint:errcheck

	ct, err := cfcrypto.AESGCMEncrypt(key, []byte{})
	if err != nil {
		t.Fatalf("AESGCMEncrypt: %v", err)
	}
	pt, err := cfcrypto.AESGCMDecrypt(key, ct)
	if err != nil {
		t.Fatalf("AESGCMDecrypt: %v", err)
	}
	if len(pt) != 0 {
		t.Errorf("expected empty plaintext, got %v", pt)
	}
}

func TestAESGCMEncryptWrongKeyLength(t *testing.T) {
	for _, keyLen := range []int{0, 16, 31, 33, 64} {
		key := make([]byte, keyLen)
		if _, err := cfcrypto.AESGCMEncrypt(key, []byte("test")); err == nil {
			t.Errorf("AESGCMEncrypt with %d-byte key: expected error, got nil", keyLen)
		}
	}
}

func TestAESGCMDecryptWrongKeyLength(t *testing.T) {
	for _, keyLen := range []int{0, 16, 31, 33, 64} {
		key := make([]byte, keyLen)
		if _, err := cfcrypto.AESGCMDecrypt(key, []byte("nonce+ct")); err == nil {
			t.Errorf("AESGCMDecrypt with %d-byte key: expected error, got nil", keyLen)
		}
	}
}

func TestAESGCMDecryptTruncatedData(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key) //nolint:errcheck

	// Data shorter than the GCM nonce (12 bytes).
	for _, l := range []int{0, 1, 5, 11} {
		data := make([]byte, l)
		if _, err := cfcrypto.AESGCMDecrypt(key, data); err == nil {
			t.Errorf("AESGCMDecrypt with %d-byte data: expected error, got nil", l)
		}
	}
}

func TestAESGCMDecryptTamperedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key) //nolint:errcheck

	ct, err := cfcrypto.AESGCMEncrypt(key, []byte("secret"))
	if err != nil {
		t.Fatalf("AESGCMEncrypt: %v", err)
	}

	tampered := append([]byte(nil), ct...)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := cfcrypto.AESGCMDecrypt(key, tampered); err == nil {
		t.Fatal("AESGCMDecrypt with tampered ciphertext: expected error, got nil")
	}
}

func TestAESGCMDecryptWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	rand.Read(key1) //nolint:errcheck
	rand.Read(key2) //nolint:errcheck

	ct, err := cfcrypto.AESGCMEncrypt(key1, []byte("secret"))
	if err != nil {
		t.Fatalf("AESGCMEncrypt: %v", err)
	}
	if _, err := cfcrypto.AESGCMDecrypt(key2, ct); err == nil {
		t.Fatal("AESGCMDecrypt with wrong key: expected error, got nil")
	}
}

func TestAESGCMNonceIsRandom(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key) //nolint:errcheck
	plaintext := []byte("same message")

	ct1, err := cfcrypto.AESGCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("first encrypt: %v", err)
	}
	ct2, err := cfcrypto.AESGCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("second encrypt: %v", err)
	}
	if bytes.Equal(ct1, ct2) {
		t.Error("two encryptions of the same plaintext produced identical ciphertext (nonce reuse)")
	}
}

// --- AESGCMEncryptWithNonce / AESGCMDecryptWithNonce ---

func TestAESGCMWithNonceRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	nonce := make([]byte, 12)
	rand.Read(key)   //nolint:errcheck
	rand.Read(nonce) //nolint:errcheck
	plaintext := []byte("campfire key material")

	ct, err := cfcrypto.AESGCMEncryptWithNonce(key, nonce, plaintext)
	if err != nil {
		t.Fatalf("AESGCMEncryptWithNonce: %v", err)
	}

	pt, err := cfcrypto.AESGCMDecryptWithNonce(key, nonce, ct)
	if err != nil {
		t.Fatalf("AESGCMDecryptWithNonce: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Errorf("decrypted %q, want %q", pt, plaintext)
	}
}

func TestAESGCMWithNonceWrongKeyLength(t *testing.T) {
	nonce := make([]byte, 12)
	for _, keyLen := range []int{0, 16, 31, 33} {
		key := make([]byte, keyLen)
		if _, err := cfcrypto.AESGCMEncryptWithNonce(key, nonce, []byte("x")); err == nil {
			t.Errorf("AESGCMEncryptWithNonce with %d-byte key: expected error", keyLen)
		}
		if _, err := cfcrypto.AESGCMDecryptWithNonce(key, nonce, []byte("x")); err == nil {
			t.Errorf("AESGCMDecryptWithNonce with %d-byte key: expected error", keyLen)
		}
	}
}

func TestAESGCMWithNonceWrongNonceLength(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key) //nolint:errcheck
	for _, nonceLen := range []int{0, 8, 11, 13, 24} {
		nonce := make([]byte, nonceLen)
		if _, err := cfcrypto.AESGCMEncryptWithNonce(key, nonce, []byte("x")); err == nil {
			t.Errorf("AESGCMEncryptWithNonce with %d-byte nonce: expected error", nonceLen)
		}
		if _, err := cfcrypto.AESGCMDecryptWithNonce(key, nonce, []byte("x")); err == nil {
			t.Errorf("AESGCMDecryptWithNonce with %d-byte nonce: expected error", nonceLen)
		}
	}
}

func TestAESGCMWithNonceTampered(t *testing.T) {
	key := make([]byte, 32)
	nonce := make([]byte, 12)
	rand.Read(key)   //nolint:errcheck
	rand.Read(nonce) //nolint:errcheck

	ct, err := cfcrypto.AESGCMEncryptWithNonce(key, nonce, []byte("secret"))
	if err != nil {
		t.Fatalf("AESGCMEncryptWithNonce: %v", err)
	}

	tampered := append([]byte(nil), ct...)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := cfcrypto.AESGCMDecryptWithNonce(key, nonce, tampered); err == nil {
		t.Fatal("AESGCMDecryptWithNonce with tampered ciphertext: expected error")
	}
}

// --- HKDFSha256ZeroSalt ---

func TestHKDFZeroSaltLength(t *testing.T) {
	ikm := make([]byte, 32)
	rand.Read(ikm) //nolint:errcheck

	okm, err := cfcrypto.HKDFSha256ZeroSalt(ikm, "campfire-join-v1")
	if err != nil {
		t.Fatalf("HKDFSha256ZeroSalt: %v", err)
	}
	if len(okm) != 32 {
		t.Errorf("expected 32-byte OKM, got %d", len(okm))
	}
}

func TestHKDFZeroSaltDeterministic(t *testing.T) {
	ikm := make([]byte, 32)
	rand.Read(ikm) //nolint:errcheck

	okm1, _ := cfcrypto.HKDFSha256ZeroSalt(ikm, "test-info")
	okm2, _ := cfcrypto.HKDFSha256ZeroSalt(ikm, "test-info")
	if !bytes.Equal(okm1, okm2) {
		t.Error("HKDFSha256ZeroSalt is not deterministic")
	}
}

func TestHKDFZeroSaltDomainSeparation(t *testing.T) {
	ikm := make([]byte, 32)
	rand.Read(ikm) //nolint:errcheck

	okm1, _ := cfcrypto.HKDFSha256ZeroSalt(ikm, "campfire-join-v1")
	okm2, _ := cfcrypto.HKDFSha256ZeroSalt(ikm, "campfire-rekey-v1")
	if bytes.Equal(okm1, okm2) {
		t.Error("different info strings produced same OKM (domain separation failure)")
	}
}

func TestHKDFZeroSaltDifferentIKM(t *testing.T) {
	ikm1 := make([]byte, 32)
	ikm2 := make([]byte, 32)
	rand.Read(ikm1) //nolint:errcheck
	rand.Read(ikm2) //nolint:errcheck

	okm1, _ := cfcrypto.HKDFSha256ZeroSalt(ikm1, "test")
	okm2, _ := cfcrypto.HKDFSha256ZeroSalt(ikm2, "test")
	if bytes.Equal(okm1, okm2) {
		t.Error("different IKM produced same OKM")
	}
}

func TestHKDFZeroSaltNotRawIKM(t *testing.T) {
	ikm := make([]byte, 32)
	rand.Read(ikm) //nolint:errcheck

	okm, err := cfcrypto.HKDFSha256ZeroSalt(ikm, "campfire-join-v1")
	if err != nil {
		t.Fatalf("HKDFSha256ZeroSalt: %v", err)
	}
	if bytes.Equal(okm, ikm) {
		t.Error("HKDFSha256ZeroSalt returned raw IKM unchanged — KDF not applied")
	}
}

// --- HKDFSha256WithSalt ---

func TestHKDFWithSaltLength(t *testing.T) {
	ikm := make([]byte, 32)
	salt := make([]byte, 12) // nonce-sized salt
	rand.Read(ikm)           //nolint:errcheck
	rand.Read(salt)          //nolint:errcheck

	okm, err := cfcrypto.HKDFSha256WithSalt(ikm, salt, "campfire-key-delivery")
	if err != nil {
		t.Fatalf("HKDFSha256WithSalt: %v", err)
	}
	if len(okm) != 32 {
		t.Errorf("expected 32-byte OKM, got %d", len(okm))
	}
}

func TestHKDFWithSaltDeterministic(t *testing.T) {
	ikm := make([]byte, 32)
	salt := make([]byte, 12)
	rand.Read(ikm)  //nolint:errcheck
	rand.Read(salt) //nolint:errcheck

	okm1, _ := cfcrypto.HKDFSha256WithSalt(ikm, salt, "campfire-key-delivery")
	okm2, _ := cfcrypto.HKDFSha256WithSalt(ikm, salt, "campfire-key-delivery")
	if !bytes.Equal(okm1, okm2) {
		t.Error("HKDFSha256WithSalt is not deterministic")
	}
}

func TestHKDFWithSaltVsZeroSaltDiffer(t *testing.T) {
	// Verify that using a non-zero salt produces a different result than zero-salt,
	// confirming the two HKDF functions are not equivalent.
	ikm := make([]byte, 32)
	salt := make([]byte, 12)
	rand.Read(ikm)  //nolint:errcheck
	rand.Read(salt) //nolint:errcheck

	// Ensure salt is not all-zeros (which would make the two functions equivalent).
	salt[0] = 0xFF

	okm1, _ := cfcrypto.HKDFSha256WithSalt(ikm, salt, "campfire-key-delivery")
	okm2, _ := cfcrypto.HKDFSha256ZeroSalt(ikm, "campfire-key-delivery")
	if bytes.Equal(okm1, okm2) {
		t.Error("HKDFSha256WithSalt and HKDFSha256ZeroSalt produced same result with non-zero salt")
	}
}

func TestHKDFWithSaltDomainSeparation(t *testing.T) {
	ikm := make([]byte, 32)
	salt := make([]byte, 12)
	rand.Read(ikm)  //nolint:errcheck
	rand.Read(salt) //nolint:errcheck

	okm1, _ := cfcrypto.HKDFSha256WithSalt(ikm, salt, "campfire-key-delivery")
	okm2, _ := cfcrypto.HKDFSha256WithSalt(ikm, salt, "campfire-join-v1")
	if bytes.Equal(okm1, okm2) {
		t.Error("different info strings produced same OKM")
	}
}

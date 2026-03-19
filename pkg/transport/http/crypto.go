package http

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

// HkdfSHA256 implements HKDF (RFC 5869) with SHA-256.
// It derives a 32-byte key from an X25519 shared secret (IKM) and an
// application-specific info string. A zero-length salt causes HKDF-Extract
// to use a block of zeros as the HMAC key, which is the RFC-specified default.
//
// This function MUST be used instead of passing the raw X25519 shared secret
// directly to AES-GCM. Raw X25519 output is uniformly random but is not a
// proper key derivation step — HKDF provides domain separation and allows
// the same shared secret to produce independent keys for different purposes.
//
// Exported so callers outside the package (e.g., deliverRekey in cmd/cf) can
// use the same derivation without duplicating the logic.
func HkdfSHA256(sharedSecret []byte, info string) ([]byte, error) {
	h := sha256.New

	// Extract: PRK = HMAC-SHA256(salt=zeros, IKM=sharedSecret)
	salt := make([]byte, h().Size())
	extractor := hmac.New(h, salt)
	extractor.Write(sharedSecret)
	prk := extractor.Sum(nil)

	// Expand: OKM = T(1) = HMAC-SHA256(PRK, info || 0x01)
	expander := hmac.New(h, prk)
	io.WriteString(expander, info) //nolint:errcheck
	expander.Write([]byte{0x01})
	okm := expander.Sum(nil) // 32 bytes for SHA-256

	return okm[:32], nil
}

// generateX25519Key creates a new ephemeral X25519 private key.
func generateX25519Key() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// parseX25519PublicKey parses raw X25519 public key bytes.
func parseX25519PublicKey(raw []byte) (*ecdh.PublicKey, error) {
	return ecdh.X25519().NewPublicKey(raw)
}

// AESGCMEncrypt encrypts plaintext with AES-256-GCM using key (must be 32 bytes).
// Returns nonce || ciphertext.
//
// Exported so callers outside the package (e.g., cmd/cf/cmd/evict.go) can use
// the same encryption primitive without duplicating the logic.
func AESGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("AESGCMEncrypt: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AESGCMEncrypt: creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("AESGCMEncrypt: creating GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("AESGCMEncrypt: generating nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, plaintext, nil)
	return ct, nil
}

// aesGCMDecrypt decrypts nonce||ciphertext with AES-256-GCM using key (must be 32 bytes).
func aesGCMDecrypt(key, data []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("aesGCMDecrypt: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aesGCMDecrypt: creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aesGCMDecrypt: creating GCM: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("aesGCMDecrypt: data too short")
	}
	nonce, ct := data[:nonceSize], data[nonceSize:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("aesGCMDecrypt: decryption failed: %w", err)
	}
	return pt, nil
}

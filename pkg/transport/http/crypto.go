package http

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// HkdfSHA256 derives a 32-byte AES-256 key from ikm (raw ECDH shared secret)
// using HKDF-SHA256 (RFC 5869) with an empty salt and the given info string.
// This provides domain separation and proper key derivation from DH output.
// Exported so callers outside the package (e.g., deliverRekey in cmd/cf) can
// use the same derivation without duplicating the logic.
func HkdfSHA256(ikm []byte, info string) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, ikm, nil, info, 32)
	if err != nil {
		return nil, fmt.Errorf("HkdfSHA256: %w", err)
	}
	return key, nil
}

// hkdfSHA256 is a package-internal alias for HkdfSHA256.
func hkdfSHA256(ikm []byte, info string) ([]byte, error) {
	return HkdfSHA256(ikm, info)
}

// generateX25519Key creates a new ephemeral X25519 private key.
func generateX25519Key() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// parseX25519PublicKey parses raw X25519 public key bytes.
func parseX25519PublicKey(raw []byte) (*ecdh.PublicKey, error) {
	return ecdh.X25519().NewPublicKey(raw)
}

// aesGCMEncrypt encrypts plaintext with AES-256-GCM using key (must be 32 bytes).
// Returns nonce || ciphertext.
func aesGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("aesGCMEncrypt: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aesGCMEncrypt: creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aesGCMEncrypt: creating GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("aesGCMEncrypt: generating nonce: %w", err)
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

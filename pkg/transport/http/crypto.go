package http

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"fmt"
)

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

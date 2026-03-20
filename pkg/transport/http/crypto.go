package http

import (
	"crypto/ecdh"
	"crypto/rand"

	cfcrypto "github.com/campfire-net/campfire/pkg/crypto"
)

// HkdfSHA256 derives a 32-byte AES key from an X25519 shared secret using
// HKDF-SHA256 with a zero-length salt (RFC 5869 §2.2 default).
//
// info provides domain separation between protocols:
//   - "campfire-join-v1" for join key exchange
//   - "campfire-rekey-v1" for rekey key exchange
//
// Exported so callers outside the package (e.g., deliverRekey in cmd/cf) can
// use the same derivation without duplicating the logic.
//
// Delegates to pkg/crypto.HKDFSha256ZeroSalt. See that function's documentation
// for the full HKDF-SHA256 specification and audit notes.
func HkdfSHA256(sharedSecret []byte, info string) ([]byte, error) {
	return cfcrypto.HKDFSha256ZeroSalt(sharedSecret, info)
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
//
// Delegates to pkg/crypto.AESGCMEncrypt. See that function's documentation for
// wire format and nonce generation details.
func AESGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	return cfcrypto.AESGCMEncrypt(key, plaintext)
}

// aesGCMDecrypt decrypts nonce||ciphertext with AES-256-GCM using key (must be 32 bytes).
//
// Delegates to pkg/crypto.AESGCMDecrypt.
func aesGCMDecrypt(key, data []byte) ([]byte, error) {
	return cfcrypto.AESGCMDecrypt(key, data)
}

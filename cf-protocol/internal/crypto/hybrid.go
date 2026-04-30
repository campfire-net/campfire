// Package crypto provides shared AES-256-GCM and HKDF-SHA256 primitives used
// by campfire's key delivery and key exchange protocols.
//
// # Protocol overview
//
// Campfire uses hybrid encryption in two distinct protocols, both built from
// the same primitives defined here:
//
//  1. Ed25519 key delivery (pkg/identity): encrypts the campfire private key
//     to a recipient's Ed25519 public key (converted to X25519). Wire format:
//     ephemeral_x25519_pub (32) || nonce (12) || GCM_ciphertext.
//     HKDF info: "campfire-key-delivery". HKDF salt: nonce.
//
//  2. Ephemeral X25519 key exchange (pkg/transport/http): used for join and
//     rekey. Both parties generate ephemeral X25519 keypairs, ECDH, then
//     derive an AES key via HKDF. Wire format: nonce (12) || GCM_ciphertext.
//     HKDF info: "campfire-join-v1" or "campfire-rekey-v1". HKDF salt: empty
//     (zero-block default per RFC 5869 §2.2).
//
// The two protocols differ in HKDF salt (nonce vs. empty) and wire format.
// The difference is intentional and documented here. If both protocols were
// using the same salt policy, they would be equivalent. They are not unified
// into a single function to preserve the explicit domain separation provided
// by the distinct info strings and to avoid silently changing the on-wire
// format for either protocol.
//
// # Audit surface
//
// All AES-256-GCM encryption and decryption, and all HKDF-SHA256 key
// derivation for campfire key delivery, is implemented in this package.
// A security auditor reviewing key delivery needs to read only this file
// and the two call sites (pkg/identity and pkg/transport/http) to reason
// about the full cryptographic surface.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

// AESGCMEncrypt encrypts plaintext with AES-256-GCM using key (must be 32 bytes).
// Returns nonce (12 bytes) || ciphertext || GCM tag.
//
// The nonce is randomly generated on each call. Callers MUST NOT reuse the
// same (key, nonce) pair; since the nonce is generated here, reuse is prevented
// by the CSPRNG.
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

// AESGCMDecrypt decrypts nonce (12 bytes) || ciphertext || GCM tag produced
// by AESGCMEncrypt. key must be 32 bytes.
func AESGCMDecrypt(key, data []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("AESGCMDecrypt: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AESGCMDecrypt: creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("AESGCMDecrypt: creating GCM: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("AESGCMDecrypt: data too short")
	}
	nonce, ct := data[:nonceSize], data[nonceSize:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("AESGCMDecrypt: decryption failed: %w", err)
	}
	return pt, nil
}

// AESGCMEncryptWithNonce encrypts plaintext with AES-256-GCM using a caller-
// supplied nonce. key must be 32 bytes; nonce must be 12 bytes.
//
// This variant is used by Ed25519 key delivery (pkg/identity) where the nonce
// is also passed to HKDF as the salt, so the nonce must be generated before
// key derivation and passed to both functions. Callers are responsible for
// ensuring nonce uniqueness; the nonce should be generated from a CSPRNG.
//
// Returns the raw GCM ciphertext + tag (no nonce prepended). The caller is
// responsible for embedding the nonce in the wire format.
func AESGCMEncryptWithNonce(key, nonce, plaintext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("AESGCMEncryptWithNonce: key must be 32 bytes, got %d", len(key))
	}
	if len(nonce) != 12 {
		return nil, fmt.Errorf("AESGCMEncryptWithNonce: nonce must be 12 bytes, got %d", len(nonce))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AESGCMEncryptWithNonce: creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("AESGCMEncryptWithNonce: creating GCM: %w", err)
	}
	return gcm.Seal(nil, nonce, plaintext, nil), nil
}

// AESGCMDecryptWithNonce decrypts raw GCM ciphertext + tag using a caller-
// supplied nonce. key must be 32 bytes; nonce must be 12 bytes.
//
// This variant mirrors AESGCMEncryptWithNonce. The caller extracts the nonce
// from the wire format before calling this function.
func AESGCMDecryptWithNonce(key, nonce, ciphertext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("AESGCMDecryptWithNonce: key must be 32 bytes, got %d", len(key))
	}
	if len(nonce) != 12 {
		return nil, fmt.Errorf("AESGCMDecryptWithNonce: nonce must be 12 bytes, got %d", len(nonce))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AESGCMDecryptWithNonce: creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("AESGCMDecryptWithNonce: creating GCM: %w", err)
	}
	pt, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("AESGCMDecryptWithNonce: decryption failed: %w", err)
	}
	return pt, nil
}

// HKDFSha256ZeroSalt derives a 32-byte key from sharedSecret and info using
// HKDF-SHA256 with an all-zero salt (RFC 5869 §2.2 default).
//
// Used by the ephemeral X25519 key exchange protocols (join and rekey), where
// no salt is available at derivation time. Per RFC 5869, a zero-length salt
// causes HKDF-Extract to use a block of HMAC-sized zeros, which is cryptographically
// sound when the IKM (shared secret) has sufficient entropy.
//
// info provides domain separation between protocols (e.g., "campfire-join-v1",
// "campfire-rekey-v1"). Different info strings produce independent keys from
// the same shared secret.
func HKDFSha256ZeroSalt(sharedSecret []byte, info string) ([]byte, error) {
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

// HKDFSha256WithSalt derives a 32-byte key from sharedSecret using HKDF-SHA256
// with an explicit salt and info string.
//
// Used by Ed25519 key delivery (pkg/identity), where the AES-GCM nonce is used
// as the HKDF salt. Using the nonce as salt binds the derived key to the
// specific nonce, which provides an additional layer of key commitment. This
// differs from standard practice (zero salt) and is documented here to make
// the difference auditable.
//
// info provides domain separation (e.g., "campfire-key-delivery").
func HKDFSha256WithSalt(sharedSecret, salt []byte, info string) ([]byte, error) {
	h := sha256.New

	// Extract: PRK = HMAC-SHA256(salt, IKM=sharedSecret)
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

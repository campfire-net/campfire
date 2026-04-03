package crypto

import (
	"bytes"
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// keywrapV2Magic is the three-byte prefix that identifies blobs produced by
// WrapKey using argon2id KEK derivation. Old blobs (HKDF, zero salt) have no
// such prefix; UnwrapKey uses this marker to select the correct derivation path.
var keywrapV2Magic = []byte{0x6B, 0x77, 0x02} // "kw" + version 2

// keywrapSaltLen is the length of the random argon2id salt prepended to the
// wrapped blob after the magic prefix.
const keywrapSaltLen = 32

// argon2idTime is the number of argon2id iterations (time cost).
// Chosen to provide meaningful brute-force resistance on modern hardware.
const argon2idTime = 3

// argon2idMemory is the argon2id memory cost in KiB (64 MiB).
const argon2idMemory = 64 * 1024

// argon2idThreads is the argon2id parallelism parameter.
const argon2idThreads = 4

// argon2idKeyLen is the output key length for argon2id (256-bit AES key).
const argon2idKeyLen = 32

// WrapKey encrypts privKey with a KEK derived from sessionToken using
// argon2id with a cryptographically random salt, then AES-256-GCM.
//
// Wire format: magic (3) || salt (32) || nonce (12) || ciphertext || GCM tag
//
// The random salt ensures that the same passphrase produces a different KEK on
// every call, preventing brute-force pre-computation and rainbow table attacks.
// argon2id provides key stretching (time=3, memory=64MiB, threads=4) making
// offline dictionary attacks computationally expensive.
//
// The session token is treated as opaque bytes; its format is not validated.
func WrapKey(privKey, sessionToken []byte) ([]byte, error) {
	// Generate a fresh random salt for argon2id.
	salt := make([]byte, keywrapSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("WrapKey: generating salt: %w", err)
	}

	// Derive KEK using argon2id.
	kek := argon2.IDKey(sessionToken, salt, argon2idTime, argon2idMemory, argon2idThreads, argon2idKeyLen)

	// Encrypt the private key.
	wrapped, err := AESGCMEncrypt(kek, privKey)
	if err != nil {
		return nil, fmt.Errorf("WrapKey: encrypting: %w", err)
	}

	// Assemble: magic || salt || nonce+ciphertext+tag
	blob := make([]byte, 0, len(keywrapV2Magic)+keywrapSaltLen+len(wrapped))
	blob = append(blob, keywrapV2Magic...)
	blob = append(blob, salt...)
	blob = append(blob, wrapped...)
	return blob, nil
}

// UnwrapKey decrypts a wrapped key blob produced by WrapKey using the same
// session token. Returns the original private key bytes.
//
// Handles both the current argon2id format (magic-prefixed) and the legacy
// HKDF-zero-salt format for backward compatibility with existing wrapped keys.
//
// Returns an error if the session token is wrong or the blob is corrupted.
func UnwrapKey(wrapped, sessionToken []byte) ([]byte, error) {
	if bytes.HasPrefix(wrapped, keywrapV2Magic) {
		return unwrapKeyV2(wrapped, sessionToken)
	}
	return unwrapKeyV1Legacy(wrapped, sessionToken)
}

// unwrapKeyV2 decrypts a blob produced by the current WrapKey (argon2id).
// Format: magic (3) || salt (32) || nonce (12) || ciphertext || tag
func unwrapKeyV2(blob, sessionToken []byte) ([]byte, error) {
	// magic (3) + salt (32) + nonce (12) + at least 1 byte of ciphertext
	const minLen = 3 + keywrapSaltLen + 12 + 1
	if len(blob) < minLen {
		return nil, fmt.Errorf("UnwrapKey: blob too short for v2 format")
	}
	offset := len(keywrapV2Magic)
	salt := blob[offset : offset+keywrapSaltLen]
	cipherBlob := blob[offset+keywrapSaltLen:]

	// Re-derive KEK using argon2id with the stored salt.
	kek := argon2.IDKey(sessionToken, salt, argon2idTime, argon2idMemory, argon2idThreads, argon2idKeyLen)

	privKey, err := AESGCMDecrypt(kek, cipherBlob)
	if err != nil {
		return nil, fmt.Errorf("UnwrapKey: decryption failed (wrong session token or corrupted blob): %w", err)
	}
	return privKey, nil
}

// WrapKeyLegacyHKDF is exported for testing only. It produces blobs in the old
// HKDF/zero-salt format so tests can verify that UnwrapKey's backward-compat
// path correctly handles pre-argon2id wrapped keys.
//
// Do NOT use in production code. New wraps must use WrapKey (argon2id).
func WrapKeyLegacyHKDF(privKey, sessionToken []byte) ([]byte, error) {
	kek, err := HKDFSha256ZeroSalt(sessionToken, "campfire-key-wrap-v1")
	if err != nil {
		return nil, fmt.Errorf("WrapKeyLegacyHKDF: deriving KEK: %w", err)
	}
	wrapped, err := AESGCMEncrypt(kek, privKey)
	if err != nil {
		return nil, fmt.Errorf("WrapKeyLegacyHKDF: encrypting: %w", err)
	}
	return wrapped, nil
}

// unwrapKeyV1Legacy decrypts a blob produced by the old WrapKey (HKDF, zero salt).
// Format: nonce (12) || ciphertext || GCM tag
//
// This path exists solely for backward compatibility with identity files written
// before the argon2id upgrade. New wrapped keys never use this path.
func unwrapKeyV1Legacy(wrapped, sessionToken []byte) ([]byte, error) {
	kek, err := HKDFSha256ZeroSalt(sessionToken, "campfire-key-wrap-v1")
	if err != nil {
		return nil, fmt.Errorf("UnwrapKey: deriving legacy KEK: %w", err)
	}
	privKey, err := AESGCMDecrypt(kek, wrapped)
	if err != nil {
		return nil, fmt.Errorf("UnwrapKey: decryption failed (wrong session token or corrupted blob): %w", err)
	}
	return privKey, nil
}

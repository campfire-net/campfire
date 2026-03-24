// Package crypto — encryption extension primitives.
//
// This file implements the message confidentiality extension per spec-encryption.md v0.2.
// It provides:
//   - CEK derivation (HKDF-SHA256 with epoch bytes as salt, protocol-fixed info string)
//   - Root secret epoch chaining (hash-chain for joins/scheduled, fresh random for evictions)
//   - EncryptedPayload type and CBOR wire format
//   - EncryptPayload / DecryptPayload with mandatory AAD (spec §4.2)
//
// CRITICAL: AAD is mandatory. Using nil AAD silently allows ciphertext transplant
// attacks (see spec §4.2, attack A11). All message encryption MUST use EncryptPayload.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"

	"github.com/campfire-net/campfire/pkg/encoding"
)

// newAESBlock creates a new AES cipher.Block from a 32-byte key.
func newAESBlock(key []byte) (cipher.Block, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("newAESBlock: %w", err)
	}
	return block, nil
}

// newGCM wraps a cipher.Block in AES-GCM mode.
func newGCM(block cipher.Block) (cipher.AEAD, error) {
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("newGCM: %w", err)
	}
	return gcm, nil
}

// cekInfo is the protocol-fixed HKDF info string for CEK derivation.
// MUST be hardcoded per spec §3.1. Do NOT read from campfire:encrypted-init.
const cekInfo = "campfire-message-key-v1"

// epochChainSalt is the HKDF salt for hash-chain root secret derivation (spec §3.2).
const epochChainSalt = "campfire-epoch-chain"

// EncryptedPayload is the CBOR-encoded envelope placed in Message.Payload
// when campfire.Encrypted == true. Wire format per spec §4.1.
type EncryptedPayload struct {
	Epoch      uint64 `cbor:"1,keyasint"`
	Nonce      []byte `cbor:"2,keyasint"`
	Ciphertext []byte `cbor:"3,keyasint"`
}

// PayloadAAD is the AAD struct bound to each ciphertext (spec §4.2).
// Prevents ciphertext transplant across messages, campfires, epochs.
// timestamp is TAINTED (sender-asserted) — prevents replay with altered
// timestamps but does not authenticate time.
type PayloadAAD struct {
	MessageID  []byte `cbor:"1,keyasint"` // message.id (hex bytes)
	Sender     string `cbor:"2,keyasint"` // message.sender (hex string)
	Campfire   []byte `cbor:"3,keyasint"` // campfire public key bytes
	Epoch      uint64 `cbor:"4,keyasint"` // encrypted_payload.epoch
	Timestamp  int64  `cbor:"5,keyasint"` // message.timestamp (TAINTED)
	Algorithm  string `cbor:"6,keyasint"` // "AES-256-GCM" — prevents future downgrade
}

// DeriveEpochCEK derives the Campfire Encryption Key for a given root secret and epoch.
//
// CEK = HKDF-SHA256(ikm=rootSecret, salt=epoch(8 bytes big-endian), info="campfire-message-key-v1")
//
// Per spec §3.1: info string is protocol-fixed and MUST NOT be read from the
// campfire:encrypted-init system message.
func DeriveEpochCEK(rootSecret []byte, epoch uint64, _ []byte) ([]byte, error) {
	if len(rootSecret) != 32 {
		return nil, fmt.Errorf("DeriveEpochCEK: rootSecret must be 32 bytes, got %d", len(rootSecret))
	}
	epochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBytes, epoch)
	return HKDFSha256WithSalt(rootSecret, epochBytes, cekInfo)
}

// NextRootSecret derives the next root secret via hash-chain (spec §3.2).
// Used for joins and scheduled rotations — O(0) key delivery cost.
// All members who hold currentRoot can derive the same nextRoot locally.
//
// root_secret_{n+1} = HKDF-SHA256(ikm=root_secret_n, salt="campfire-epoch-chain", info=epoch_{n+1}(8 bytes big-endian))
func NextRootSecret(currentRoot []byte, nextEpoch uint64) ([]byte, error) {
	if len(currentRoot) != 32 {
		return nil, fmt.Errorf("NextRootSecret: currentRoot must be 32 bytes, got %d", len(currentRoot))
	}
	epochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBytes, nextEpoch)
	// HKDFSha256WithSalt(ikm, salt, info): salt="campfire-epoch-chain", info=epoch bytes (as raw string)
	return HKDFSha256WithSalt(currentRoot, []byte(epochChainSalt), string(epochBytes))
}

// GenerateRootSecret generates a fresh random 32-byte root secret.
// Used for evictions and voluntary leaves (spec §3.2, fresh secret generation).
func GenerateRootSecret() ([]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("GenerateRootSecret: %w", err)
	}
	return secret, nil
}

// MarshalEncryptedPayload CBOR-encodes an EncryptedPayload for the message wire format.
func MarshalEncryptedPayload(ep EncryptedPayload) ([]byte, error) {
	return encoding.Marshal(ep)
}

// UnmarshalEncryptedPayload CBOR-decodes an EncryptedPayload from payload bytes.
func UnmarshalEncryptedPayload(data []byte) (EncryptedPayload, error) {
	var ep EncryptedPayload
	if err := encoding.Unmarshal(data, &ep); err != nil {
		return EncryptedPayload{}, fmt.Errorf("UnmarshalEncryptedPayload: %w", err)
	}
	return ep, nil
}

// BuildPayloadAAD constructs and CBOR-encodes the AAD for message encryption.
// Parameters map directly to spec §4.2 fields.
func BuildPayloadAAD(messageID []byte, sender string, campfirePubKey []byte, epoch uint64, timestamp int64) ([]byte, error) {
	aad := PayloadAAD{
		MessageID: messageID,
		Sender:    sender,
		Campfire:  campfirePubKey,
		Epoch:     epoch,
		Timestamp: timestamp,
		Algorithm: "AES-256-GCM",
	}
	return encoding.Marshal(aad)
}

// EncryptPayload encrypts plaintext under cek with AES-256-GCM.
// aad binds the ciphertext to the message context (mandatory per spec §4.2).
// Returns an EncryptedPayload ready for CBOR marshaling into Message.Payload.
//
// The nonce is randomly generated (12 bytes, spec §4.4).
func EncryptPayload(plaintext, cek []byte, epoch uint64, aad []byte) (EncryptedPayload, error) {
	if len(cek) != 32 {
		return EncryptedPayload{}, fmt.Errorf("EncryptPayload: cek must be 32 bytes, got %d", len(cek))
	}

	// Generate random 12-byte nonce (spec §4.4).
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return EncryptedPayload{}, fmt.Errorf("EncryptPayload: generating nonce: %w", err)
	}

	ciphertext, err := aesGCMEncryptWithAAD(cek, nonce, plaintext, aad)
	if err != nil {
		return EncryptedPayload{}, fmt.Errorf("EncryptPayload: %w", err)
	}

	return EncryptedPayload{
		Epoch:      epoch,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}, nil
}

// DecryptPayload decrypts an EncryptedPayload using cek.
// aad must match the AAD used during encryption; mismatches cause authentication failure.
func DecryptPayload(ep EncryptedPayload, cek []byte, aad []byte) ([]byte, error) {
	if len(cek) != 32 {
		return nil, fmt.Errorf("DecryptPayload: cek must be 32 bytes, got %d", len(cek))
	}
	plaintext, err := aesGCMDecryptWithAAD(cek, ep.Nonce, ep.Ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("DecryptPayload: %w", err)
	}
	return plaintext, nil
}

// aesGCMEncryptWithAAD encrypts plaintext with AES-256-GCM using caller-supplied nonce and AAD.
// This is the AAD-aware variant required by spec §4.2 (attack A11).
// Using nil AAD silently drops all authentication binding — DO NOT use nil AAD for message encryption.
func aesGCMEncryptWithAAD(key, nonce, plaintext, aad []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("aesGCMEncryptWithAAD: key must be 32 bytes, got %d", len(key))
	}
	if len(nonce) != 12 {
		return nil, fmt.Errorf("aesGCMEncryptWithAAD: nonce must be 12 bytes, got %d", len(nonce))
	}
	block, err := newAESBlock(key)
	if err != nil {
		return nil, fmt.Errorf("aesGCMEncryptWithAAD: %w", err)
	}
	gcm, err := newGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aesGCMEncryptWithAAD: %w", err)
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nil
}

// aesGCMDecryptWithAAD decrypts ciphertext with AES-256-GCM using caller-supplied nonce and AAD.
func aesGCMDecryptWithAAD(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("aesGCMDecryptWithAAD: key must be 32 bytes, got %d", len(key))
	}
	if len(nonce) != 12 {
		return nil, fmt.Errorf("aesGCMDecryptWithAAD: nonce must be 12 bytes, got %d", len(nonce))
	}
	block, err := newAESBlock(key)
	if err != nil {
		return nil, fmt.Errorf("aesGCMDecryptWithAAD: %w", err)
	}
	gcm, err := newGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aesGCMDecryptWithAAD: %w", err)
	}
	pt, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("aesGCMDecryptWithAAD: decryption failed: %w", err)
	}
	return pt, nil
}

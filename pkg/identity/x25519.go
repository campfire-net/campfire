package identity

// x25519.go — Ed25519→X25519 key conversion and encrypted key delivery.
//
// Conversion math: RFC 7748 §4.1 (Montgomery/Edwards birational equivalence).
// Encryption scheme: X25519 ECDH + HKDF-SHA256 + AES-256-GCM.
// Wire format: ephemeral_x25519_pub (32) || nonce (12) || GCM_ciphertext.
//
// This is used for campfire private key delivery on join: the admitting member
// encrypts the campfire private key bytes to the joiner's Ed25519 public key
// (converted to X25519), and the joiner decrypts using their Ed25519 private
// key (converted to X25519).

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha512"
	"errors"
	"fmt"
	"io"
	"math/big"

	cfcrypto "github.com/campfire-net/campfire/pkg/crypto"
)

// p25519 is the field prime 2^255 - 19.
var p25519 = new(big.Int).Sub(
	new(big.Int).Lsh(big.NewInt(1), 255),
	big.NewInt(19),
)

// Ed25519ToX25519Pub converts an Ed25519 public key to its corresponding
// X25519 public key (Montgomery u-coordinate).
//
// Formula (RFC 7748 §4.1):
//
//	u = (1 + y) / (1 - y) mod p
//
// where y is the little-endian Edwards y-coordinate (sign bit cleared).
func Ed25519ToX25519Pub(edPub []byte) ([]byte, error) {
	if len(edPub) != 32 {
		return nil, errors.New("ed25519 public key must be 32 bytes")
	}

	// The Ed25519 public key is the compressed Edwards y-coordinate.
	// The most significant bit of the last byte is the sign of x (unused for conversion).
	yBytes := make([]byte, 32)
	copy(yBytes, edPub)
	yBytes[31] &= 0x7F // clear sign bit

	// Interpret as little-endian big.Int.
	y := new(big.Int).SetBytes(reverse(yBytes))

	p := p25519
	one := big.NewInt(1)

	// num = 1 + y
	num := new(big.Int).Add(one, y)
	num.Mod(num, p)

	// den = 1 - y
	den := new(big.Int).Sub(one, y)
	den.Mod(den, p)

	// u = num * modInverse(den, p) mod p
	denInv := new(big.Int).ModInverse(den, p)
	if denInv == nil {
		return nil, errors.New("ed25519 public key: denominator has no inverse (point at infinity)")
	}
	u := new(big.Int).Mul(num, denInv)
	u.Mod(u, p)

	// Encode as 32-byte little-endian.
	uBytes := u.Bytes() // big-endian
	uLE := reverse(uBytes)
	// Pad to 32 bytes.
	result := make([]byte, 32)
	copy(result, uLE)
	return result, nil
}

// Ed25519ToX25519Priv converts an Ed25519 private key (64-byte Go representation:
// seed || public key) to the corresponding X25519 scalar.
//
// Per RFC 7748 §6.2 and draft-ietf-lwig-curve-representations:
//   - Extract the 32-byte seed (first 32 bytes of the Go Ed25519 private key).
//   - Compute h = SHA-512(seed).
//   - Clamp h[0:32]: clear bits 0,1,2 of h[0]; clear bit 7 of h[31]; set bit 6 of h[31].
//   - Return h[0:32] as the X25519 scalar.
func Ed25519ToX25519Priv(edPriv []byte) ([]byte, error) {
	if len(edPriv) != 64 {
		return nil, errors.New("ed25519 private key must be 64 bytes (seed || pubkey)")
	}

	seed := edPriv[:32]
	h := sha512.Sum512(seed)

	scalar := make([]byte, 32)
	copy(scalar, h[:32])

	// Clamp per RFC 7748 §5.
	scalar[0] &= 248  // clear bits 0, 1, 2
	scalar[31] &= 127 // clear bit 7
	scalar[31] |= 64  // set bit 6

	return scalar, nil
}

// x25519BasePoint computes X25519(scalar, basepoint) using crypto/ecdh.
// This is used internally to verify keypair consistency in tests.
func x25519BasePoint(scalar []byte) ([]byte, error) {
	curve := ecdh.X25519()
	priv, err := curve.NewPrivateKey(scalar)
	if err != nil {
		return nil, fmt.Errorf("creating X25519 private key: %w", err)
	}
	return priv.PublicKey().Bytes(), nil
}

// EncryptToEd25519Key encrypts plaintext so only the holder of the given
// Ed25519 private key can decrypt it.
//
// Wire format (all fields concatenated):
//
//	ephemeral_x25519_pub (32 bytes)
//	nonce               (12 bytes)
//	AES-256-GCM ciphertext + tag (len(plaintext) + 16 bytes)
//
// Algorithm:
//  1. Convert recipientEd25519Pub to X25519 public key.
//  2. Generate ephemeral X25519 keypair.
//  3. ECDH shared secret = X25519(ephemeral_priv, recipient_x25519_pub).
//  4. key_material = HKDF-SHA256(shared_secret, nonce as salt, "campfire-key-delivery" as info)[0:32].
//  5. Ciphertext = AES-256-GCM(key_material, nonce, plaintext).
func EncryptToEd25519Key(recipientEd25519Pub []byte, plaintext []byte) ([]byte, error) {
	// Convert recipient Ed25519 pub to X25519.
	recipientX25519Pub, err := Ed25519ToX25519Pub(recipientEd25519Pub)
	if err != nil {
		return nil, fmt.Errorf("converting recipient public key: %w", err)
	}

	curve := ecdh.X25519()

	// Load recipient X25519 public key.
	recipientPub, err := curve.NewPublicKey(recipientX25519Pub)
	if err != nil {
		return nil, fmt.Errorf("loading recipient X25519 public key: %w", err)
	}

	// Generate ephemeral X25519 keypair.
	ephemeralPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating ephemeral key: %w", err)
	}
	ephemeralPub := ephemeralPriv.PublicKey().Bytes() // 32 bytes

	// ECDH.
	sharedSecret, err := ephemeralPriv.ECDH(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}

	// Generate nonce. The nonce is also used as the HKDF salt (see HKDFSha256WithSalt).
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	// Derive AES-256 key via HKDF-SHA256.
	// salt = nonce, info = "campfire-key-delivery".
	// Using the nonce as salt binds the derived key to this specific nonce.
	// See pkg/crypto package doc for the full audit notes on this choice.
	keyMaterial, err := cfcrypto.HKDFSha256WithSalt(sharedSecret, nonce, "campfire-key-delivery")
	if err != nil {
		return nil, fmt.Errorf("HKDF: %w", err)
	}

	// AES-256-GCM encrypt using caller-supplied nonce (embedded in wire format below).
	gcmCiphertext, err := cfcrypto.AESGCMEncryptWithNonce(keyMaterial, nonce, plaintext)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM encrypt: %w", err)
	}

	// Wire format: ephemeral_pub || nonce || ciphertext+tag
	out := make([]byte, 32+12+len(gcmCiphertext))
	copy(out[0:32], ephemeralPub)
	copy(out[32:44], nonce)
	copy(out[44:], gcmCiphertext)
	return out, nil
}

// DecryptWithEd25519Key decrypts a ciphertext produced by EncryptToEd25519Key,
// using the recipient's Ed25519 private key.
func DecryptWithEd25519Key(recipientEd25519Priv []byte, ciphertext []byte) ([]byte, error) {
	// Minimum size: 32 (ephemeral pub) + 12 (nonce) + 16 (GCM tag).
	if len(ciphertext) < 32+12+16 {
		return nil, errors.New("ciphertext too short")
	}

	ephemeralPubBytes := ciphertext[0:32]
	nonce := ciphertext[32:44]
	gcmCiphertext := ciphertext[44:]

	// Convert recipient Ed25519 priv to X25519 scalar.
	recipientX25519Scalar, err := Ed25519ToX25519Priv(recipientEd25519Priv)
	if err != nil {
		return nil, fmt.Errorf("converting recipient private key: %w", err)
	}

	curve := ecdh.X25519()

	// Load recipient X25519 private key.
	recipientPriv, err := curve.NewPrivateKey(recipientX25519Scalar)
	if err != nil {
		return nil, fmt.Errorf("loading recipient X25519 private key: %w", err)
	}

	// Load ephemeral public key.
	ephemeralPub, err := curve.NewPublicKey(ephemeralPubBytes)
	if err != nil {
		return nil, fmt.Errorf("loading ephemeral public key: %w", err)
	}

	// ECDH: shared secret = X25519(recipient_priv, ephemeral_pub).
	sharedSecret, err := recipientPriv.ECDH(ephemeralPub)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}

	// Derive AES-256 key via HKDF-SHA256 (same parameters as encryption).
	keyMaterial, err := cfcrypto.HKDFSha256WithSalt(sharedSecret, nonce, "campfire-key-delivery")
	if err != nil {
		return nil, fmt.Errorf("HKDF: %w", err)
	}

	// AES-256-GCM decrypt using the extracted nonce.
	plaintext, err := cfcrypto.AESGCMDecryptWithNonce(keyMaterial, nonce, gcmCiphertext)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM decryption failed (wrong key or tampered): %w", err)
	}
	return plaintext, nil
}

// reverse returns a new slice with the bytes in reversed order.
func reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

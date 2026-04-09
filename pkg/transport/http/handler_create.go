package http

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
)

// VerifyRequestSignature is the exported form of verifyRequestSignature.
// It checks the Ed25519 request signature covering timestamp + nonce + body.
// Used by cmd/cf-mcp/transport_http.go to verify /campfire/create requests
// without depending on the unexported handler struct.
func VerifyRequestSignature(senderHex, sigB64, nonce, timestamp string, body []byte) error {
	return verifyRequestSignature(senderHex, sigB64, nonce, timestamp, body)
}

// DecryptCampfirePrivKey is the exported form of decryptCampfirePrivKey.
// See that function's documentation for the full ECDH protocol.
//
// Deprecated: This ephemeral-key variant requires a two-round protocol because
// the creator cannot know the relay's ephemeral pub key before sending the
// request. Use DecryptCampfirePrivKeyWithStaticKey instead, which allows the
// creator to look up the relay's static pub key via GET /campfire/relay-info
// and complete campfire creation in a single round.
//
// This export is retained for use in tests and potential future protocol
// variants. cmd/cf-mcp/transport_http.go no longer calls this function.
func DecryptCampfirePrivKey(creatorEphemPubHex string, encryptedPrivKey []byte) (privKeyBytes []byte, relayPubHex string, err error) {
	return decryptCampfirePrivKey(creatorEphemPubHex, encryptedPrivKey)
}

// DecryptCampfirePrivKeyWithStaticKey decrypts the campfire private key using
// the relay's STATIC X25519 private key instead of generating a fresh ephemeral.
//
// Protocol:
//  1. Creator generates ephemeral X25519 keypair (creatorEphPriv, creatorEphPub).
//  2. Creator encrypts campfire privkey with ECDH(creatorEphPriv, relayStaticPub).
//  3. Creator sends creatorEphPub + encrypted privkey in the request.
//  4. Relay decrypts with ECDH(relayStaticPriv, creatorEphPub).
//
// This enables a single-round protocol since the relay's static pub is known in
// advance (via GET /campfire/relay-info or out-of-band configuration).
//
// Returns (privKeyBytes, relayStaticPubHex, error).
func DecryptCampfirePrivKeyWithStaticKey(relayStaticPriv *ecdh.PrivateKey, creatorEphemPubHex string, encryptedPrivKey []byte) (privKeyBytes []byte, relayPubHex string, err error) {
	// Parse the creator's ephemeral X25519 public key.
	creatorPubBytes, err := hex.DecodeString(creatorEphemPubHex)
	if err != nil {
		return nil, "", fmt.Errorf("decoding creator ephemeral X25519 public key: %w", err)
	}
	creatorX25519, err := parseX25519PublicKey(creatorPubBytes)
	if err != nil {
		return nil, "", fmt.Errorf("parsing creator ephemeral X25519 public key: %w", err)
	}

	// ECDH: derive shared secret using relay's static key.
	shared, err := relayStaticPriv.ECDH(creatorX25519)
	if err != nil {
		return nil, "", fmt.Errorf("ECDH key exchange: %w", err)
	}

	// Derive AES key using HKDF-SHA256 (same info string as join).
	aesKey, err := HkdfSHA256(shared, "campfire-join-v1")
	if err != nil {
		return nil, "", fmt.Errorf("HKDF key derivation: %w", err)
	}

	// Decrypt the campfire private key.
	privKeyBytes, err = aesGCMDecrypt(aesKey, encryptedPrivKey)
	if err != nil {
		return nil, "", fmt.Errorf("decrypting campfire private key: %w", err)
	}

	relayPubHex = fmt.Sprintf("%x", relayStaticPriv.PublicKey().Bytes())
	return privKeyBytes, relayPubHex, nil
}

// ValidateCampfirePrivKey is the exported form of validateCampfirePrivKey.
// Verifies that the decrypted private key produces the claimed campfire_id.
func ValidateCampfirePrivKey(privKeyBytes []byte, claimedCampfireID string) error {
	return validateCampfirePrivKey(privKeyBytes, claimedCampfireID)
}

// CreateCampfireRequest is the body for POST /campfire/create.
// The creator sends the campfire's public key, its private key encrypted via
// ECDH key exchange, and metadata needed to register the campfire on the relay.
type CreateCampfireRequest struct {
	// CampfireID is the hex-encoded Ed25519 public key of the campfire.
	CampfireID string `json:"campfire_id"`
	// EncryptedPrivKey is the campfire private key, encrypted with AES-256-GCM
	// using a key derived from ECDH between the client's ephemeral X25519 key
	// and the relay's ephemeral X25519 key. The client sends this pre-encrypted.
	EncryptedPrivKey []byte `json:"encrypted_priv_key"`
	// EphemeralX25519Pub is the creator's ephemeral X25519 public key (hex).
	// The relay uses this with its own ephemeral private key to derive the same
	// AES key the creator used to encrypt the campfire private key.
	EphemeralX25519Pub string `json:"ephemeral_x25519_pub"`
	// JoinProtocol is "open" or "invite-only".
	JoinProtocol string `json:"join_protocol"`
	// ReceptionRequirements is the list of required tags for messages.
	ReceptionRequirements []string `json:"reception_requirements"`
	// Threshold is the signing threshold (1 for standard campfires).
	Threshold uint `json:"threshold"`
	// Description is a human-readable description of the campfire.
	Description string `json:"description"`
	// CreatorPubkey is the hex-encoded Ed25519 public key of the creator.
	// Must match the X-Campfire-Sender header.
	CreatorPubkey string `json:"creator_pubkey"`
}

// CreateCampfireResponse is the response body for a successful POST /campfire/create.
type CreateCampfireResponse struct {
	// CampfireID is the hex-encoded campfire public key (echoed back).
	CampfireID string `json:"campfire_id"`
	// RelayX25519Pub is the relay's ephemeral X25519 public key (hex).
	// The creator can use this to verify the ECDH exchange completed.
	RelayX25519Pub string `json:"relay_x25519_pub"`
	// Beacon is the portable beacon string for the campfire (beacon:BASE64).
	// Empty when beacon generation is unavailable (no private key).
	Beacon string `json:"beacon,omitempty"`
	// Endpoint is the relay's public endpoint URL.
	Endpoint string `json:"endpoint"`
}

// RelayInfoResponse is the body for GET /campfire/relay-info.
// Creators use this to discover the relay's static X25519 public key before
// calling POST /campfire/create.
type RelayInfoResponse struct {
	// RelayX25519Pub is the relay's static X25519 public key (hex).
	// Empty when the relay has no static X25519 key configured (relay-create unavailable).
	RelayX25519Pub string `json:"relay_x25519_pub"`
	// Endpoint is the relay's public endpoint URL.
	Endpoint string `json:"endpoint"`
}

// decryptCampfirePrivKey performs the ECDH key exchange from the relay's side
// to decrypt the campfire private key sent by the creator.
//
// The creator:
//  1. Generated ephemeral X25519 keypair.
//  2. Derived a shared secret using ECDH(creatorEphemPriv, relayEphemPub) — but for
//     this protocol, the creator doesn't have the relay's pub key yet.
//
// Revised (corrected) protocol (same as join but inverted sender/receiver):
//  1. Creator generates ephemeral X25519 keypair (ephPub sent in request).
//  2. Relay generates ephemeral X25519 keypair (ephPub returned in response).
//  3. Relay derives shared = ECDH(relayEphemPriv, creatorEphemPub).
//  4. Relay derives key = HkdfSHA256(shared, "campfire-join-v1").
//  5. Relay decrypts EncryptedPrivKey using AES-256-GCM with derived key.
//
// Returns (privKeyBytes, relayEphemPubHex, error).
func decryptCampfirePrivKey(creatorEphemPubHex string, encryptedPrivKey []byte) (privKeyBytes []byte, relayPubHex string, err error) {
	// Parse the creator's ephemeral X25519 public key.
	creatorPubBytes, err := hex.DecodeString(creatorEphemPubHex)
	if err != nil {
		return nil, "", fmt.Errorf("decoding creator ephemeral X25519 public key: %w", err)
	}
	creatorX25519, err := parseX25519PublicKey(creatorPubBytes)
	if err != nil {
		return nil, "", fmt.Errorf("parsing creator ephemeral X25519 public key: %w", err)
	}

	// Generate the relay's ephemeral X25519 keypair.
	relayEphemPriv, err := generateX25519Key()
	if err != nil {
		return nil, "", fmt.Errorf("generating relay ephemeral X25519 key: %w", err)
	}
	relayEphemPub := relayEphemPriv.PublicKey()

	// ECDH: derive shared secret.
	shared, err := relayEphemPriv.ECDH(creatorX25519)
	if err != nil {
		return nil, "", fmt.Errorf("ECDH key exchange: %w", err)
	}

	// Derive AES key using HKDF-SHA256 (same info string as join).
	aesKey, err := HkdfSHA256(shared, "campfire-join-v1")
	if err != nil {
		return nil, "", fmt.Errorf("HKDF key derivation: %w", err)
	}

	// Decrypt the campfire private key.
	privKeyBytes, err = aesGCMDecrypt(aesKey, encryptedPrivKey)
	if err != nil {
		return nil, "", fmt.Errorf("decrypting campfire private key: %w", err)
	}

	relayPubHex = fmt.Sprintf("%x", relayEphemPub.Bytes())
	return privKeyBytes, relayPubHex, nil
}

// validateCampfirePrivKey verifies that the decrypted private key produces the
// claimed campfire_id (Ed25519 public key). The campfire_id is the hex-encoded
// Ed25519 public key, which can be derived from the private key.
//
// In Go's crypto/ed25519, the private key seed is the first 32 bytes; calling
// ed25519.NewKeyFromSeed re-derives the full keypair.
func validateCampfirePrivKey(privKeyBytes []byte, claimedCampfireID string) error {
	if len(privKeyBytes) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid private key length: %d (expected %d)", len(privKeyBytes), ed25519.PrivateKeySize)
	}
	privKey := ed25519.PrivateKey(privKeyBytes)
	derivedPub := privKey.Public().(ed25519.PublicKey)
	derivedHex := fmt.Sprintf("%x", derivedPub)
	if derivedHex != claimedCampfireID {
		return fmt.Errorf("private key does not match claimed campfire_id")
	}
	return nil
}

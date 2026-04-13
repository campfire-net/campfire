// Package session implements zero-ceremony session tokens for ephemeral
// multi-agent coordination via campfire.
//
// # Security model
//
// A session token is a BEARER CREDENTIAL — anyone holding the token can send
// and read messages in the session campfire. Treat it like a password in
// transit: do not log tokens, do not embed them in URLs, transmit only over
// encrypted channels.
//
// The shared ephemeral key means there is NO per-message attribution inside a
// session. All participants share the same signing key. Do not use session
// tokens when individual sender attribution is required; use cf join instead.
//
// # Token format
//
// cfs1_<base64url(payload + creator_sig[64])>
//
// where payload is the CBOR encoding of TokenPayload. The creator_sig is an
// Ed25519 signature over the CBOR payload bytes, preventing token forgery.
//
// The "cfs1_" prefix enables version evolution: future formats use a different
// prefix (cfs2_, cfs3_, ...) and coexist with v1 tokens.
//
// # TTL enforcement
//
// TTL is enforced client-side at decode time. The maximum TTL is 24 hours.
// Tokens cannot be used past their expiry, even by 1 second.
package session

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
)

const (
	// TokenPrefix is the version prefix for v1 session tokens.
	TokenPrefix = "cfs1_"

	// MaxTTL is the maximum allowed session TTL.
	MaxTTL = 24 * time.Hour
)

// TokenPayload is the CBOR-encoded body of a session token.
// It is signed by the creator's identity key, binding the token to the creator.
type TokenPayload struct {
	// CampfireID is the 32-byte Ed25519 public key of the session campfire.
	CampfireID []byte `cbor:"1,keyasint"`

	// EphemeralPubKey is the 32-byte ephemeral Ed25519 public key.
	// Shared among all session participants (no per-sender attribution).
	EphemeralPubKey []byte `cbor:"2,keyasint"`

	// EphemeralPrivKey is the 64-byte ephemeral Ed25519 private key.
	// All participants sign messages with this key.
	EphemeralPrivKey []byte `cbor:"3,keyasint"`

	// TransportConfig is the CBOR- or JSON-encoded transport configuration
	// needed to send/read on the session campfire.
	TransportConfig []byte `cbor:"4,keyasint"`

	// ExpiresAt is the Unix timestamp (nanoseconds) after which the token is invalid.
	ExpiresAt int64 `cbor:"5,keyasint"`

	// CreatorPubKey is the 32-byte Ed25519 public key of the token creator.
	// Used by DecodeToken to verify the creator_sig.
	CreatorPubKey []byte `cbor:"6,keyasint"`
}

// TokenParams holds the inputs to EncodeToken.
type TokenParams struct {
	// CampfireID is the 32-byte Ed25519 public key of the session campfire.
	CampfireID []byte

	// EphemeralPubKey is the ephemeral Ed25519 public key shared among participants.
	EphemeralPubKey ed25519.PublicKey

	// EphemeralPrivKey is the ephemeral Ed25519 private key shared among participants.
	EphemeralPrivKey ed25519.PrivateKey

	// TransportConfig is the serialized transport configuration.
	TransportConfig []byte

	// TTL is the session lifetime. Maximum 24 hours. Enforced at creation time.
	TTL time.Duration

	// CreatorPub is the creator's Ed25519 public key. Embedded in the token
	// so verifiers can check the creator_sig without an out-of-band lookup.
	CreatorPub ed25519.PublicKey

	// CreatorPriv is the creator's Ed25519 private key used to sign the token.
	// Deprecated: prefer CreatorSigner to support backend-delegated signing (e.g.
	// ssh-agent). CreatorPriv is used as a fallback when CreatorSigner is nil.
	CreatorPriv ed25519.PrivateKey

	// CreatorSigner is an optional signing function that produces the creator
	// signature. When non-nil it is used instead of CreatorPriv, allowing the
	// token to be signed by a backend (e.g. SSHAgentBackend) without exposing
	// the private key. The function must return a 64-byte Ed25519 signature.
	CreatorSigner func([]byte) ([]byte, error)
}

// DecodedToken holds the verified content of a session token.
type DecodedToken struct {
	// CampfireID is the session campfire's 32-byte public key.
	CampfireID []byte

	// EphemeralPubKey is the shared ephemeral public key.
	EphemeralPubKey ed25519.PublicKey

	// EphemeralPrivKey is the shared ephemeral private key.
	EphemeralPrivKey ed25519.PrivateKey

	// TransportConfig is the serialized transport configuration.
	TransportConfig []byte

	// ExpiresAt is the expiry time of this token.
	ExpiresAt time.Time

	// CreatorPubKey is the creator's Ed25519 public key.
	CreatorPubKey ed25519.PublicKey
}

// EncodeToken creates a signed session token with the given parameters.
// Returns an error if TTL exceeds MaxTTL (24 hours).
func EncodeToken(p TokenParams) (string, error) {
	expiry := time.Now().Add(p.TTL)
	return EncodeTokenWithExpiry(p, expiry)
}

// EncodeTokenWithExpiry creates a signed session token with an explicit expiry time.
// This is exposed for testing; production callers should use EncodeToken.
// Returns an error if the TTL (time until expiry) exceeds MaxTTL.
func EncodeTokenWithExpiry(p TokenParams, expiry time.Time) (string, error) {
	ttl := time.Until(expiry)
	if ttl > MaxTTL {
		return "", fmt.Errorf("session TTL %v exceeds maximum allowed %v", p.TTL, MaxTTL)
	}

	payload := TokenPayload{
		CampfireID:       p.CampfireID,
		EphemeralPubKey:  []byte(p.EphemeralPubKey),
		EphemeralPrivKey: []byte(p.EphemeralPrivKey),
		TransportConfig:  p.TransportConfig,
		ExpiresAt:        expiry.UnixNano(),
		CreatorPubKey:    []byte(p.CreatorPub),
	}

	payloadBytes, err := cfencoding.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encoding token payload: %w", err)
	}

	var sig []byte
	if p.CreatorSigner != nil {
		var signErr error
		sig, signErr = p.CreatorSigner(payloadBytes)
		if signErr != nil {
			return "", fmt.Errorf("signing token payload: %w", signErr)
		}
	} else {
		sig = ed25519.Sign(p.CreatorPriv, payloadBytes)
	}
	if len(sig) != ed25519.SignatureSize {
		return "", fmt.Errorf("unexpected signature size: %d", len(sig))
	}

	raw := append(payloadBytes, sig...)
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return TokenPrefix + encoded, nil
}

// DecodeToken decodes and verifies a session token. The creatorPub parameter
// is the expected creator public key; if nil, the key embedded in the token is
// used (less strict — suitable when the creator identity is unknown in advance).
//
// Returns an error if:
//   - the token has an unknown prefix (not "cfs1_")
//   - base64 decoding fails
//   - CBOR decoding fails
//   - the creator signature does not verify
//   - the token has expired (even by 1 nanosecond)
func DecodeToken(tok string, creatorPub ed25519.PublicKey) (*DecodedToken, error) {
	if len(tok) < len(TokenPrefix) || tok[:len(TokenPrefix)] != TokenPrefix {
		return nil, fmt.Errorf("unknown session token format (expected %s prefix)", TokenPrefix)
	}

	raw, err := base64.RawURLEncoding.DecodeString(tok[len(TokenPrefix):])
	if err != nil {
		return nil, fmt.Errorf("decoding session token: %w", err)
	}

	if len(raw) < ed25519.SignatureSize {
		return nil, fmt.Errorf("session token too short")
	}

	payloadBytes := raw[:len(raw)-ed25519.SignatureSize]
	sig := raw[len(raw)-ed25519.SignatureSize:]

	var payload TokenPayload
	if err := cfencoding.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("decoding token payload: %w", err)
	}

	// Determine the verification key: prefer the caller-supplied key if non-nil;
	// fall back to the key embedded in the token.
	verifyKey := creatorPub
	if verifyKey == nil {
		if len(payload.CreatorPubKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("token has no valid creator public key")
		}
		verifyKey = ed25519.PublicKey(payload.CreatorPubKey)
	}

	if !ed25519.Verify(verifyKey, payloadBytes, sig) {
		return nil, fmt.Errorf("session token signature invalid")
	}

	// TTL enforcement: reject expired tokens, even by 1 nanosecond.
	now := time.Now()
	expiry := time.Unix(0, payload.ExpiresAt)
	if !now.Before(expiry) {
		ago := now.Sub(expiry).Truncate(time.Second)
		return nil, fmt.Errorf("session expired %v ago", ago)
	}

	return &DecodedToken{
		CampfireID:       payload.CampfireID,
		EphemeralPubKey:  ed25519.PublicKey(payload.EphemeralPubKey),
		EphemeralPrivKey: ed25519.PrivateKey(payload.EphemeralPrivKey),
		TransportConfig:  payload.TransportConfig,
		ExpiresAt:        expiry,
		CreatorPubKey:    ed25519.PublicKey(payload.CreatorPubKey),
	}, nil
}

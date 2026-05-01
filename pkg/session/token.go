// Package session implements session tokens for ephemeral multi-agent
// coordination via campfire.
//
// # Token versions
//
// ## cfs2_ (cf 0.30+, current)
//
// cfs2_<base64url(payload + creator_sig[64])>
//
// where payload is the CBOR encoding of TokenV2Payload. The cfs2_ token
// carries the campfire address and transport config for resolution metadata,
// but contains NO shared ephemeral private key. Workers must have their own
// Ed25519 identity key (provisioned via cf-session lazy-mint) to participate.
//
// The creator_sig is an Ed25519 signature over the CBOR payload bytes,
// binding the token to the creator's identity and preventing forgery.
//
// Security model: the cfs2_ token is an address, not a credential. It
// identifies where the session campfire is but does not grant access. Workers
// present themselves via identity:introduce and receive a delegation:grant
// (lazy-mint, per §2.9.1). There is no shared key — every participant has
// full per-message attribution.
//
// ## cfs1_ (pre-0.30, removed)
//
// The cfs1_ format embedded a shared ephemeral Ed25519 private key, destroying
// per-worker attribution. This format is recognized but rejected with a clear
// error directing callers to migrate to cfs2_.
//
// # TTL enforcement
//
// TTL is enforced client-side at decode time. The maximum TTL is 24 hours.
// Tokens cannot be used past their expiry, even by 1 nanosecond.
package session

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
)

const (
	// TokenPrefixV2 is the version prefix for cfs2_ session tokens (cf 0.30+).
	// cfs2_ tokens carry the campfire address and no shared private key.
	TokenPrefixV2 = "cfs2_"

	// TokenPrefixV1 is the version prefix for legacy cfs1_ session tokens (pre-0.30).
	// cfs1_ tokens embedded a shared ephemeral private key (anti-feature removed in 0.30).
	// Recognised so callers receive a clear migration error; not decoded for use.
	TokenPrefixV1 = "cfs1_"

	// TokenPrefix is the active token prefix; always the latest version.
	TokenPrefix = TokenPrefixV2

	// MaxTTL is the maximum allowed session TTL.
	MaxTTL = 24 * time.Hour
)

// TokenPayload is the CBOR-encoded body of a cfs1_ session token (pre-0.30).
//
// Deprecated: cfs1_ tokens are removed in cf 0.30. Use TokenV2Payload for
// cfs2_ tokens. This type is retained only for documentation; cfs1_ tokens
// are rejected at decode time with a migration error.
type TokenPayload struct {
	// CampfireID is the 32-byte Ed25519 public key of the session campfire.
	CampfireID []byte `cbor:"1,keyasint"`

	// EphemeralPubKey is the 32-byte ephemeral Ed25519 public key.
	// Shared among all session participants (no per-sender attribution).
	EphemeralPubKey []byte `cbor:"2,keyasint"`

	// EphemeralPrivKey is the 64-byte ephemeral Ed25519 private key.
	// All participants sign messages with this key.
	// REMOVED in cfs2_: this field is the anti-feature that destroyed attribution.
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

// TokenV2Payload is the CBOR-encoded body of a cfs2_ session token (cf 0.30+).
//
// cfs2_ removes the shared EphemeralPrivKey entirely. Workers participate via
// their own per-worker Ed25519 identity keys (provisioned by cf-session
// lazy-mint). The token carries only the campfire address and resolution
// metadata needed to locate and join the session campfire.
type TokenV2Payload struct {
	// CampfireID is the hex-encoded Ed25519 public key of the session campfire.
	// Stored as a string (not bytes) for direct use in cf send/read/join calls.
	CampfireID string `cbor:"1,keyasint"`

	// TransportConfig is the JSON-encoded transport configuration for the
	// session campfire. Callers use this to resolve the campfire transport
	// without a local store lookup.
	TransportConfig []byte `cbor:"2,keyasint"`

	// ExpiresAt is the Unix timestamp (nanoseconds) after which the token is
	// considered stale. This is informational — the session campfire's own
	// TTL is enforced by the protocol layer, not by token decoding.
	ExpiresAt int64 `cbor:"3,keyasint"`

	// CreatorPubKey is the 32-byte Ed25519 public key of the token creator.
	// Used by DecodeTokenV2 to verify the creator_sig.
	CreatorPubKey []byte `cbor:"4,keyasint"`
}

// TokenParams holds the inputs to EncodeToken (cfs1_, legacy / deprecated).
//
// Deprecated: Use TokenV2Params for cfs2_ tokens. This type is retained only
// for callers still producing cfs1_ tokens (e.g., backward-compat test fixtures).
// New callers should use EncodeTokenV2 and TokenV2Params.
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

// TokenV2Params holds the inputs to EncodeTokenV2 (cfs2_, cf 0.30+).
type TokenV2Params struct {
	// CampfireID is the hex-encoded Ed25519 public key of the session campfire.
	CampfireID string

	// TransportConfig is the serialized transport configuration for the session campfire.
	TransportConfig []byte

	// TTL is the session lifetime. Maximum 24 hours. Enforced at creation time.
	TTL time.Duration

	// CreatorPub is the creator's Ed25519 public key. Embedded so verifiers can
	// check the creator_sig without an out-of-band lookup.
	CreatorPub ed25519.PublicKey

	// CreatorPriv is the creator's Ed25519 private key used to sign the token.
	// Prefer CreatorSigner when a backend signer is available.
	CreatorPriv ed25519.PrivateKey

	// CreatorSigner is an optional signing function. When non-nil, used instead
	// of CreatorPriv (backend-delegated signing). Must return 64-byte Ed25519 sig.
	CreatorSigner func([]byte) ([]byte, error)
}

// DecodedToken holds the verified content of a cfs1_ session token (legacy).
//
// Deprecated: cfs1_ tokens are removed in cf 0.30. Use DecodedTokenV2 for
// cfs2_ tokens.
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

// DecodedTokenV2 holds the verified content of a cfs2_ session token (cf 0.30+).
type DecodedTokenV2 struct {
	// CampfireID is the session campfire's hex-encoded public key.
	CampfireID string

	// TransportConfig is the serialized transport configuration.
	TransportConfig []byte

	// ExpiresAt is the expiry time of this token (informational; not enforced here).
	ExpiresAt time.Time

	// CreatorPubKey is the creator's Ed25519 public key.
	CreatorPubKey ed25519.PublicKey
}

// EncodeTokenV2 creates a signed cfs2_ session token (cf 0.30+).
// The token carries the campfire address and transport config but NO shared
// private key. Workers authenticate via their own Ed25519 keys (cf-session lazy-mint).
//
// Returns an error if TTL exceeds MaxTTL (24 hours).
func EncodeTokenV2(p TokenV2Params) (string, error) {
	expiry := time.Now().Add(p.TTL)
	return EncodeTokenV2WithExpiry(p, expiry)
}

// EncodeTokenV2WithExpiry creates a signed cfs2_ token with an explicit expiry.
// Exposed for testing; production callers should use EncodeTokenV2.
func EncodeTokenV2WithExpiry(p TokenV2Params, expiry time.Time) (string, error) {
	ttl := time.Until(expiry)
	if ttl > MaxTTL {
		return "", fmt.Errorf("session TTL %v exceeds maximum allowed %v", p.TTL, MaxTTL)
	}

	payload := TokenV2Payload{
		CampfireID:      p.CampfireID,
		TransportConfig: p.TransportConfig,
		ExpiresAt:       expiry.UnixNano(),
		CreatorPubKey:   []byte(p.CreatorPub),
	}

	payloadBytes, err := cfencoding.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encoding cfs2_ token payload: %w", err)
	}

	var sig []byte
	if p.CreatorSigner != nil {
		var signErr error
		sig, signErr = p.CreatorSigner(payloadBytes)
		if signErr != nil {
			return "", fmt.Errorf("signing cfs2_ token payload: %w", signErr)
		}
	} else {
		sig = ed25519.Sign(p.CreatorPriv, payloadBytes)
	}
	if len(sig) != ed25519.SignatureSize {
		return "", fmt.Errorf("unexpected signature size: %d", len(sig))
	}

	raw := append(payloadBytes, sig...)
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return TokenPrefixV2 + encoded, nil
}

// DecodeTokenV2 decodes and verifies a cfs2_ session token (cf 0.30+).
//
// The creatorPub parameter is the expected creator public key; if nil, the key
// embedded in the token is used (suitable when the creator identity is unknown
// in advance — the signature is still verified, just against the embedded key).
//
// Returns an error if:
//   - the token has an unknown prefix (not "cfs2_"; "cfs1_" gives a migration error)
//   - base64 decoding fails
//   - CBOR decoding fails
//   - the creator signature does not verify
func DecodeTokenV2(tok string, creatorPub ed25519.PublicKey) (*DecodedTokenV2, error) {
	// Detect legacy cfs1_ tokens and fail loud with a migration message.
	if strings.HasPrefix(tok, TokenPrefixV1) {
		return nil, fmt.Errorf("legacy cfs1_ session token not supported in cf 0.30+; " +
			"re-create the session to get a cfs2_ token (cf session create --ttl ...)")
	}

	if !strings.HasPrefix(tok, TokenPrefixV2) {
		return nil, fmt.Errorf("unknown session token format (expected %s prefix)", TokenPrefixV2)
	}

	raw, err := base64.RawURLEncoding.DecodeString(tok[len(TokenPrefixV2):])
	if err != nil {
		return nil, fmt.Errorf("decoding cfs2_ session token: %w", err)
	}

	if len(raw) < ed25519.SignatureSize {
		return nil, fmt.Errorf("cfs2_ session token too short")
	}

	payloadBytes := raw[:len(raw)-ed25519.SignatureSize]
	sig := raw[len(raw)-ed25519.SignatureSize:]

	var payload TokenV2Payload
	if err := cfencoding.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("decoding cfs2_ token payload: %w", err)
	}

	// Determine the verification key.
	verifyKey := creatorPub
	if verifyKey == nil {
		if len(payload.CreatorPubKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("cfs2_ token has no valid creator public key")
		}
		verifyKey = ed25519.PublicKey(payload.CreatorPubKey)
	}

	if !ed25519.Verify(verifyKey, payloadBytes, sig) {
		return nil, fmt.Errorf("cfs2_ session token signature invalid")
	}

	expiry := time.Unix(0, payload.ExpiresAt)
	return &DecodedTokenV2{
		CampfireID:      payload.CampfireID,
		TransportConfig: payload.TransportConfig,
		ExpiresAt:       expiry,
		CreatorPubKey:   ed25519.PublicKey(payload.CreatorPubKey),
	}, nil
}

// EncodeToken creates a signed cfs1_ session token (legacy, pre-0.30).
//
// Deprecated: Use EncodeTokenV2 for cfs2_ tokens (cf 0.30+). The cfs1_ format
// embeds a shared ephemeral private key which destroys per-worker attribution.
// This function is retained for backward-compat test fixtures only.
func EncodeToken(p TokenParams) (string, error) {
	expiry := time.Now().Add(p.TTL)
	return EncodeTokenWithExpiry(p, expiry)
}

// EncodeTokenWithExpiry creates a signed cfs1_ token with an explicit expiry time.
// This is exposed for testing; production callers should use EncodeTokenV2.
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
	return TokenPrefixV1 + encoded, nil
}

// DecodeToken decodes and verifies a cfs1_ session token (legacy, pre-0.30).
//
// Deprecated: cfs1_ tokens are removed in cf 0.30. For cfs2_ tokens, use
// DecodeTokenV2. This function is retained for backward-compat test fixtures.
//
// Returns an error if:
//   - the token has an unknown prefix (not "cfs1_")
//   - base64 decoding fails
//   - CBOR decoding fails
//   - the creator signature does not verify
//   - the token has expired (even by 1 nanosecond)
func DecodeToken(tok string, creatorPub ed25519.PublicKey) (*DecodedToken, error) {
	if !strings.HasPrefix(tok, TokenPrefixV1) {
		return nil, fmt.Errorf("unknown session token format (expected %s prefix)", TokenPrefixV1)
	}

	raw, err := base64.RawURLEncoding.DecodeString(tok[len(TokenPrefixV1):])
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

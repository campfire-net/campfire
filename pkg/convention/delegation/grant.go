// Package delegation implements grant validation for the identity-delegation
// convention (identity-delegation-v0.1.md §4).
package delegation

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// MaxGrantTTL is the hard ceiling on grant lifetime per identity-delegation-v0.1.md §4 rule 4.
// A grant cannot live longer than 7 days; issuance rejects longer TTLs rather than silently clamping.
const MaxGrantTTL = 7 * 24 * time.Hour

// GrantedTag is the tag produced by an identity-delegation:grant operation.
const GrantedTag = "identity:granted"

// Typed sentinel errors returned by ValidateGrant and PostGrant.
var (
	// ErrSignatureInvalid is returned when the grant message signature does not
	// verify under msg.Sender (rule 1).
	ErrSignatureInvalid = errors.New("grant signature invalid")

	// ErrCampfireMismatch is returned when payload.campfire_id differs from the
	// campfire the message was read from (rule 2).
	ErrCampfireMismatch = errors.New("grant campfire_id mismatch")

	// ErrGrantExpired is returned when payload.expires_at <= now-60 (rule 3).
	ErrGrantExpired = errors.New("grant expired")

	// ErrGrantCeilingExceeded is returned when payload.expires_at exceeds
	// msg.Timestamp/1e9 + 7*86400 (rule 4), or when PostGrant is called with
	// ttl > MaxGrantTTL.
	ErrGrantCeilingExceeded = errors.New("grant expiry exceeds 7-day ceiling")

	// ErrGrantTTLInvalid is returned when PostGrant is called with a zero or
	// negative TTL. Issuance requires a positive lifetime.
	ErrGrantTTLInvalid = errors.New("grant TTL must be > 0")
)

// GrantPayload is the JSON payload of an identity:granted message.
type GrantPayload struct {
	ChildPubkey string `json:"child_pubkey"`
	CampfireID  string `json:"campfire_id"`
	ExpiresAt   int64  `json:"expires_at"`
}

// ParseGrantPayload decodes a raw JSON payload into a GrantPayload.
func ParseGrantPayload(payload []byte) (*GrantPayload, error) {
	var gp GrantPayload
	if err := json.Unmarshal(payload, &gp); err != nil {
		return nil, fmt.Errorf("parsing grant payload: %w", err)
	}
	return &gp, nil
}

// ValidateGrant checks a grant message against the five validation rules from
// identity-delegation-v0.1.md §4.
//
//   - Rule 1: message signature verifies under msg.Sender.
//   - Rule 2: payload.campfire_id == campfireIDHex.
//   - Rule 3: payload.expires_at > now.Unix() - 60 (clock-skew slack).
//   - Rule 4: payload.expires_at <= msg.Timestamp/1e9 + 7*86400 (hard ceiling).
//   - Rule 5: grant is not revoked (returns nil — enforcement wired in campfire-ab7).
//
// campfireIDHex is the hex-encoded ID of the campfire the message was read from.
// now is the caller-supplied current time (enables deterministic testing).
func ValidateGrant(grant *message.Message, campfireIDHex string, now time.Time) error {
	// Rule 1: signature must verify.
	if !grant.VerifySignature() {
		return ErrSignatureInvalid
	}

	// Parse payload for rules 2–4.
	gp, err := ParseGrantPayload(grant.Payload)
	if err != nil {
		return fmt.Errorf("grant payload: %w", err)
	}

	// Rule 2: campfire_id must match the campfire the message was read from.
	if gp.CampfireID != campfireIDHex {
		return ErrCampfireMismatch
	}

	// Rule 3: not expired (with 60-second clock-skew slack).
	if gp.ExpiresAt <= now.Unix()-60 {
		return ErrGrantExpired
	}

	// Rule 4: hard ceiling — at most 7 days from message creation.
	msgTimeSec := grant.Timestamp / 1e9
	ceiling := msgTimeSec + 7*86400
	if gp.ExpiresAt > ceiling {
		return ErrGrantCeilingExceeded
	}

	// Rule 5: revocation check — deferred to campfire-ab7.
	return nil
}

// PostGrant constructs and posts an identity:granted message from the client's
// identity (the parent/grant signer) to childPubkey, valid in the given campfire
// for the given TTL. Returns the posted *message.Message on success.
//
// TTL must be > 0 and <= MaxGrantTTL (7 days). Zero/negative returns
// ErrGrantTTLInvalid; longer than 7 days returns ErrGrantCeilingExceeded.
// The ceiling is not silently clamped — surfacing a clear error is more useful
// than hiding the surprise.
//
// The client's identity IS the grant parent; callers that want to issue from
// a different parent construct a different client. This matches how every
// other SDK write operation (Send, Admit, Evict, etc.) works.
//
// PostGrant does not mint the child keypair (caller's responsibility), does
// not auto-retry on send failure, and does not check revocation (that's the
// read path — see Resolve).
//
// See identity-delegation-v0.1.md §3 for the wire format.
func PostGrant(
	ctx context.Context,
	client *protocol.Client,
	campfireID []byte,
	childPubkey ed25519.PublicKey,
	ttl time.Duration,
) (*message.Message, error) {
	if ttl <= 0 {
		return nil, ErrGrantTTLInvalid
	}
	if ttl > MaxGrantTTL {
		return nil, ErrGrantCeilingExceeded
	}
	if client == nil {
		return nil, errors.New("PostGrant: client is nil")
	}
	if len(childPubkey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("PostGrant: child pubkey must be %d bytes, got %d", ed25519.PublicKeySize, len(childPubkey))
	}
	if len(campfireID) == 0 {
		return nil, errors.New("PostGrant: campfire ID is empty")
	}

	campfireHex := hex.EncodeToString(campfireID)
	// NOTE on the ceiling off-by-epsilon: expiresAt is computed against caller
	// wall time, but message.NewMessage (invoked inside client.Send) stamps
	// msg.Timestamp with a later real time. Rule 4 checks
	// expiresAt > msg.Timestamp/1e9 + 7*86400 — since msg.Timestamp > now,
	// the check is strict-less-than and passes for ttl == exactly 7 days.
	// No internal clamp needed.
	expiresAt := time.Now().Add(ttl).Unix()

	payload, err := json.Marshal(GrantPayload{
		ChildPubkey: hex.EncodeToString(childPubkey),
		CampfireID:  campfireHex,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		return nil, fmt.Errorf("PostGrant: marshal payload: %w", err)
	}

	// ctx is accepted for future use (cancellation of slow transports); the
	// current protocol.Client.Send signature does not take one. Kept in the
	// signature so callers don't have to restructure when it's plumbed through.
	_ = ctx

	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireHex,
		Payload:    payload,
		Tags:       []string{GrantedTag},
	})
	if err != nil {
		return nil, fmt.Errorf("PostGrant: send: %w", err)
	}
	return msg, nil
}

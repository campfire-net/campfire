// Package delegation implements grant validation for the identity-delegation
// convention (identity-delegation-v0.1.md §4).
package delegation

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
)

// GrantedTag is the tag produced by an identity-delegation:grant operation.
const GrantedTag = "identity:granted"

// Typed sentinel errors returned by ValidateGrant.
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
	// msg.Timestamp/1e9 + 7*86400 (rule 4).
	ErrGrantCeilingExceeded = errors.New("grant expiry exceeds 7-day ceiling")
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

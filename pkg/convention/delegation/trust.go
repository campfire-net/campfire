// Package delegation implements grant validation and trust resolution for the
// identity-delegation convention (identity-delegation-v0.1.md §4,
// identity-v0.2-trust-resolution.md §4-§5, identity-delegation-revocation.md §4).
package delegation

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// MaxChainDepth is the maximum number of grant hops the resolver will walk
// before returning DepthExceeded. A legitimate chain is never deeper than 5
// hops; 10 is a generous safety cap that terminates adversarial or cyclic input.
const MaxChainDepth = 10

// MaxGrantReadLimit caps the number of identity:granted or identity:revoked
// messages a single trust-walk read will pull into memory. Legitimate deployments
// have on the order of dozens of grants per campfire; this cap prevents a peer
// who floods a campfire with grants from causing unbounded memory allocation
// during findValidGrant (campfire-d38). If this limit is hit, a valid grant for
// the target child may still be present in the truncated tail — callers should
// treat "at-limit with no valid grant for child" as a possible false negative
// for adversarial traffic, not for legitimate chains.
const MaxGrantReadLimit = 10000

// RevokedTag is the tag produced by an identity-delegation:revoke operation.
//
// NOTE: this constant duplicates convention.IdentityRevokedTag (see
// pkg/convention/identity.go). The delegation package cannot import convention
// (convention imports delegation), and the reverse import would break the
// dependency graph. A test asserts both constants equal "identity:revoked" —
// if this string changes, update both sites and the test will tell you
// (campfire-3a8).
const RevokedTag = "identity:revoked"

// ErrStoreRead wraps any underlying store.Read / client.Read error produced
// during trust resolution. Resolve surfaces this class of failure via the
// ReadError Outcome variant so callers can distinguish "store unavailable"
// from "no grant exists" (both of which previously silently mapped to
// DeadEnd — campfire-188).
var ErrStoreRead = errors.New("store read failed")

// GrantInfo holds the parsed payload of a single identity:granted message
// together with the message metadata needed by callers inspecting the chain.
type GrantInfo struct {
	// MessageID is the campfire message ID of the grant message.
	MessageID string
	// ParentPubkey is the signer of the grant (the parent in the delegation tree).
	ParentPubkey ed25519.PublicKey
	// ChildPubkey is the delegated key (the child in the delegation tree).
	ChildPubkey ed25519.PublicKey
	// Payload holds the parsed grant payload (child_pubkey, campfire_id, expires_at).
	Payload GrantPayload
	// Timestamp is the grant message timestamp (nanoseconds).
	Timestamp int64
}

// Outcome is the sealed interface returned by Resolve.
// Callers switch on the concrete type: Resolved, DeadEnd, InvalidGrant, DepthExceeded.
type Outcome interface{ outcome() }

// Resolved means the sender traces back to a trust anchor via a valid,
// unrevoked grant chain. Chain may be empty when the sender is itself an anchor.
type Resolved struct {
	// Chain is the sequence of grants walked, in order from sender to anchor.
	// Empty when the sender is itself a trust anchor.
	Chain []GrantInfo
	// Anchor is the trust-anchor pubkey at which the walk terminated.
	Anchor ed25519.PublicKey
}

// DeadEnd means the walk stopped because no valid unrevoked grant was found for
// the last key in the chain, and that key is not a trust anchor.
type DeadEnd struct {
	// Chain is the sequence of grants walked so far (may be empty).
	Chain []GrantInfo
	// LastResolved is the pubkey for which no grant could be found.
	LastResolved ed25519.PublicKey
}

// InvalidGrant means a grant message was found but failed validation (signature,
// campfire_id mismatch, expired, ceiling violated, or malformed payload).
type InvalidGrant struct {
	// Chain is the sequence of valid grants walked before the bad grant.
	Chain []GrantInfo
	// BadGrant is the protocol.Message that failed validation.
	BadGrant *protocol.Message
	// Err is the validation error from ValidateGrant.
	Err error
}

// DepthExceeded means the walk hit MaxChainDepth without reaching a trust anchor.
// This indicates a pathological or cyclic delegation chain.
type DepthExceeded struct {
	// Chain is the partial chain up to the depth limit.
	Chain []GrantInfo
}

// ReadError means the trust walker hit a store I/O error before it could
// complete the walk. This is distinct from DeadEnd — DeadEnd is "the log
// says no valid grant exists"; ReadError is "we couldn't read the log."
// Callers must treat ReadError as non-authoritative: the sender's trust
// state is unknown, and the policy decision (fail-open, fail-closed, retry)
// is the caller's. Fixes campfire-188.
type ReadError struct {
	// Chain is the partial chain walked successfully before the read failed.
	Chain []GrantInfo
	// Target is the pubkey whose grant lookup (or revocation lookup) failed.
	Target ed25519.PublicKey
	// Err is the underlying store error, wrapped in ErrStoreRead for
	// errors.Is checks.
	Err error
}

func (Resolved) outcome()      {}
func (DeadEnd) outcome()       {}
func (InvalidGrant) outcome()  {}
func (DepthExceeded) outcome() {}
func (ReadError) outcome()     {}

// revokePayload is the JSON payload of an identity:revoked message.
type revokePayload struct {
	ChildPubkey string `json:"child_pubkey"`
}

// grantCandidate is an internal pair used during grant lookup.
type grantCandidate struct {
	msg protocol.Message
	gp  GrantPayload
}

// Resolve walks the local campfire log to determine whether sender is trusted by
// any of the given anchors, following the algorithm from identity-v0.2-trust-resolution.md §4
// extended with the revocation check from identity-delegation-revocation.md §4.
//
// ctx is passed through but currently unused (all reads are synchronous local queries).
// client must be a member of the campfire identified by campfireID.
// campfireID is the raw 32-byte Ed25519 public key of the campfire.
// sender is the ed25519 public key of the message sender to resolve.
// anchors is the list of trusted anchor pubkeys; if sender is in this list,
// Resolved is returned immediately with an empty chain.
//
// Returns one of: Resolved, DeadEnd, InvalidGrant, DepthExceeded.
func Resolve(
	_ context.Context,
	client *protocol.Client,
	campfireID []byte,
	sender ed25519.PublicKey,
	anchors []ed25519.PublicKey,
) Outcome {
	campfireHex := hex.EncodeToString(campfireID)
	now := time.Now()
	chain := make([]GrantInfo, 0)
	current := sender

	for depth := 0; depth < MaxChainDepth; depth++ {
		// If current is a trust anchor, resolution succeeds.
		for _, anchor := range anchors {
			if current.Equal(anchor) {
				return Resolved{Chain: chain, Anchor: current}
			}
		}

		// Find valid non-revoked grant for current.
		gi, badGrant, err := findValidGrant(client, campfireHex, current, now)
		if err != nil {
			// Store I/O error — surface as ReadError so the caller can
			// distinguish "log unreadable" from "no grant exists" (campfire-188).
			if errors.Is(err, ErrStoreRead) {
				return ReadError{Chain: chain, Target: current, Err: err}
			}
			// Otherwise a grant was found but failed validation — return InvalidGrant.
			return InvalidGrant{Chain: chain, BadGrant: badGrant, Err: err}
		}
		if gi == nil {
			// No valid unrevoked grant; walk ends here.
			return DeadEnd{Chain: chain, LastResolved: current}
		}

		chain = append(chain, *gi)
		current = gi.ParentPubkey
	}

	return DepthExceeded{Chain: chain}
}

// findValidGrant queries the local campfire log for a valid, unrevoked
// identity:granted message whose payload.child_pubkey matches childPubkey.
//
// It iterates grants in descending timestamp order. For each grant it:
//  1. Validates via ValidateGrant (signature, campfire_id, expiry, ceiling,
//     child_pubkey well-formedness).
//  2. Checks for a newer identity:revoked message from the same parent for the same child.
//
// Returns:
//   - (*GrantInfo, nil, nil)         — valid unrevoked grant found
//   - (nil, nil, nil)                — no grant exists at all (DeadEnd)
//   - (nil, *Message, validationErr) — a grant was found but failed validation (InvalidGrant)
//   - (nil, nil, ErrStoreRead…)      — store I/O error (surfaced as ReadError by Resolve)
func findValidGrant(
	client *protocol.Client,
	campfireHex string,
	childPubkey ed25519.PublicKey,
	now time.Time,
) (*GrantInfo, *protocol.Message, error) {
	childHex := hex.EncodeToString(childPubkey)

	// Read identity:granted messages from the campfire (local only), capped at
	// MaxGrantReadLimit to prevent a grant-flood from blowing memory (campfire-d38).
	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireHex,
		Tags:       []string{GrantedTag},
		SkipSync:   true,
		Limit:      MaxGrantReadLimit,
	})
	if err != nil {
		// Surface the store error instead of silently mapping to DeadEnd
		// (campfire-188). Resolve converts this into a ReadError Outcome.
		return nil, nil, fmt.Errorf("%w: reading grants: %v", ErrStoreRead, err)
	}

	// Collect candidates matching this child pubkey.
	var candidates []grantCandidate
	for _, msg := range result.Messages {
		var gp GrantPayload
		if err := json.Unmarshal(msg.Payload, &gp); err != nil {
			continue
		}
		if gp.ChildPubkey != childHex {
			continue
		}
		candidates = append(candidates, grantCandidate{msg: msg, gp: gp})
	}

	// Sort descending by Timestamp (most-recent-first).
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].msg.Timestamp > candidates[j].msg.Timestamp
	})

	for i := range candidates {
		c := &candidates[i]

		// Re-construct a message.Message for ValidateGrant (needs VerifySignature).
		rawMsg := protoMsgToRaw(c.msg)

		if err := ValidateGrant(rawMsg, campfireHex, now); err != nil {
			// Per spec §4: skip invalid grants and try the next candidate.
			continue
		}

		// Validation passed. Check for a revocation from the same parent.
		revoked, revokeErr := isRevoked(client, campfireHex, childHex, c.msg.Sender, c.msg.Timestamp)
		if revokeErr != nil {
			// Store error on revoke lookup — surface as ReadError rather than
			// silently treating the grant as revoked (campfire-188). A store
			// failure is not the same as "revoked"; conflating them is a
			// correctness bug for any caller that relies on the Outcome type.
			return nil, nil, revokeErr
		}
		if revoked {
			// Revoked; try the next (older) grant.
			continue
		}

		// Valid and not revoked — build GrantInfo. Both parentBytes (from signed
		// Sender field) and childBytes (from payload, now validated by
		// ValidateGrant) are guaranteed well-formed.
		parentBytes, err := hex.DecodeString(c.msg.Sender)
		if err != nil || len(parentBytes) != ed25519.PublicKeySize {
			pmsg := c.msg
			return nil, &pmsg, ErrSignatureInvalid
		}
		childBytes, err := hex.DecodeString(c.gp.ChildPubkey)
		if err != nil || len(childBytes) != ed25519.PublicKeySize {
			// Unreachable: ValidateGrant already enforced child_pubkey
			// well-formedness. If this fires, ValidateGrant has a bug.
			pmsg := c.msg
			return nil, &pmsg, ErrGrantChildKeyMalformed
		}

		gi := &GrantInfo{
			MessageID:    c.msg.ID,
			ParentPubkey: ed25519.PublicKey(parentBytes),
			ChildPubkey:  ed25519.PublicKey(childBytes),
			Payload:      c.gp,
			Timestamp:    c.msg.Timestamp,
		}
		return gi, nil, nil
	}

	// No valid unrevoked grant found.
	return nil, nil, nil
}

// isRevoked checks whether a newer identity:revoked message from parentHex for
// childHex exists in the campfire, posted strictly after grantTimestamp.
//
// Store errors are wrapped in ErrStoreRead and propagated up. findValidGrant's
// caller (Resolve) converts that to a ReadError Outcome so the caller can tell
// "revoke lookup failed" apart from "no revocation found" (campfire-188).
func isRevoked(
	client *protocol.Client,
	campfireHex string,
	childHex string,
	parentHex string,
	grantTimestamp int64,
) (bool, error) {
	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireHex,
		Tags:       []string{RevokedTag},
		Sender:     parentHex,
		SkipSync:   true,
		Limit:      MaxGrantReadLimit,
	})
	if err != nil {
		return false, fmt.Errorf("%w: reading revocations: %v", ErrStoreRead, err)
	}

	for _, msg := range result.Messages {
		if msg.Timestamp <= grantTimestamp {
			continue
		}
		var rp revokePayload
		if err := json.Unmarshal(msg.Payload, &rp); err != nil {
			continue
		}
		if rp.ChildPubkey == childHex {
			return true, nil
		}
	}
	return false, nil
}

// protoMsgToRaw converts a protocol.Message back to a message.Message so that
// ValidateGrant can call VerifySignature (which requires []byte Sender and Signature).
//
// Why this shim exists (campfire-1b1):
//   - delegation.findValidGrant reads messages via protocol.Client.Read, which returns
//     protocol.Message with Sender as a hex string (SDK-facing type).
//   - ValidateGrant requires *message.Message because its signature verification path
//     calls message.Message.VerifySignature(), which needs Sender as []byte.
//   - delegation cannot import protocol (protocol imports convention, convention imports
//     delegation — that cycle is already disallowed). Moving VerifySignature to accept
//     raw bytes would require touching message.Message.VerifySignature (a broad API
//     change) or splitting ValidateGrant into a "verify-then-validate" pair.
//   - We cannot touch ValidateGrant here (that is PR #1 territory, campfire-af4).
//
// The shim is therefore the correct short-term solution. A future PR (#5 or later)
// should add a ValidateGrantFromProto overload that accepts protocol.Message directly
// (hex-decode once, then validate), letting us delete protoMsgToRaw and the
// intermediate allocation. Track as campfire-1b1.
func protoMsgToRaw(pm protocol.Message) *message.Message {
	senderBytes, _ := hex.DecodeString(pm.Sender)
	return &message.Message{
		ID:          pm.ID,
		Sender:      senderBytes,
		Payload:     pm.Payload,
		Tags:        pm.Tags,
		Antecedents: pm.Antecedents,
		Timestamp:   pm.Timestamp,
		Signature:   pm.Signature,
	}
}

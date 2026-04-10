// Package delegation implements grant validation and trust resolution for the
// identity-delegation convention (identity-delegation-v0.1.md §4,
// identity-v0.2-trust-resolution.md §4-§5, identity-delegation-revocation.md §4).
package delegation

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// MAX_CHAIN_DEPTH is the maximum number of grant hops the resolver will walk
// before returning DepthExceeded. A legitimate chain is never deeper than 5
// hops; 10 is a generous safety cap that terminates adversarial or cyclic input.
const MAX_CHAIN_DEPTH = 10

// RevokedTag is the tag produced by an identity-delegation:revoke operation.
const RevokedTag = "identity:revoked"

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

// DepthExceeded means the walk hit MAX_CHAIN_DEPTH without reaching a trust anchor.
// This indicates a pathological or cyclic delegation chain.
type DepthExceeded struct {
	// Chain is the partial chain up to the depth limit.
	Chain []GrantInfo
}

func (Resolved) outcome()      {}
func (DeadEnd) outcome()       {}
func (InvalidGrant) outcome()  {}
func (DepthExceeded) outcome() {}

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

	for depth := 0; depth < MAX_CHAIN_DEPTH; depth++ {
		// If current is a trust anchor, resolution succeeds.
		for _, anchor := range anchors {
			if current.Equal(anchor) {
				return Resolved{Chain: chain, Anchor: current}
			}
		}

		// Find valid non-revoked grant for current.
		gi, badGrant, validationErr := findValidGrant(client, campfireHex, current, now)
		if validationErr != nil {
			// A grant was found but failed validation — return InvalidGrant.
			return InvalidGrant{Chain: chain, BadGrant: badGrant, Err: validationErr}
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
//  1. Validates via ValidateGrant (signature, campfire_id, expiry, ceiling).
//  2. Checks for a newer identity:revoked message from the same parent for the same child.
//
// Returns:
//   - (*GrantInfo, nil, nil)   — valid unrevoked grant found
//   - (nil, nil, nil)          — no grant exists at all (DeadEnd)
//   - (nil, *Message, error)   — a grant was found but failed validation (InvalidGrant)
func findValidGrant(
	client *protocol.Client,
	campfireHex string,
	childPubkey ed25519.PublicKey,
	now time.Time,
) (*GrantInfo, *protocol.Message, error) {
	childHex := hex.EncodeToString(childPubkey)

	// Read all identity:granted messages from the campfire (local only).
	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireHex,
		Tags:       []string{GrantedTag},
		SkipSync:   true,
	})
	if err != nil {
		// Treat read errors as no grant found (DeadEnd); caller decides policy.
		return nil, nil, nil
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
			// Read error on revoke query — treat conservatively: skip this grant.
			continue
		}
		if revoked {
			// Revoked; try the next (older) grant.
			continue
		}

		// Valid and not revoked — build GrantInfo.
		parentBytes, err := hex.DecodeString(c.msg.Sender)
		if err != nil || len(parentBytes) != ed25519.PublicKeySize {
			pmsg := c.msg
			return nil, &pmsg, ErrSignatureInvalid
		}
		childBytes, _ := hex.DecodeString(c.gp.ChildPubkey)

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
	})
	if err != nil {
		return false, err
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

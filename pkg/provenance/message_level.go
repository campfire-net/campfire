package provenance

// message_level.go — LevelFromMessage computes the operator provenance level
// for a message based on its transport properties and sender identity.
//
// Covered bead: campfire-agent-0ca (provenance bridge tiers).
//
// Level rules (highest wins):
//   - Level 3 (Present):     the sender's public key is in the provided root-key set
//                            (i.e. the message was resolved via a quorum/root-key call).
//   - Level 2 (Contactable): the message traversed at least one blind-relay provenance
//                            hop (i.e. it was forwarded by a bridge transport).
//   - Level 0 (Anonymous):   default when neither of the above applies.
//
// The distinction between Level 0 and Level 1 (self-claimed) requires attestation
// store context and cannot be computed from the message alone.

import "github.com/campfire-net/campfire/pkg/campfire"

// ProvenanceHop is the minimal interface over message.ProvenanceHop needed to
// compute message-level provenance without importing pkg/message directly.
// Callers pass hop slices from protocol.Message.Provenance or message.Message.Provenance.
type ProvenanceHop interface {
	// GetRole returns the campfire membership role for this hop.
	GetRole() string
}

// LevelFromMessage computes the operator provenance level for a message.
//
// hops is the provenance chain (from protocol.Message.Provenance or message.Message.Provenance).
// senderKey is the hex-encoded public key of the message sender.
// rootKeys is the set of known root/center campfire public keys (hex-encoded).
// A non-empty intersection of senderKey with rootKeys elevates to Level 3.
//
// Rules applied in priority order (highest wins):
//  1. Level 3 — senderKey is in rootKeys (quorum call / root-key signed message)
//  2. Level 2 — any hop carries campfire.RoleBlindRelay (message traversed a bridge)
//  3. Level 0 — default (no attestation context available from the message alone)
func LevelFromMessage(hops []MessageHop, senderKey string, rootKeys map[string]bool) Level {
	// Level 3: sender is a known root key.
	if rootKeys[senderKey] {
		return LevelPresent
	}

	// Level 2: at least one blind-relay hop in the provenance chain.
	for _, hop := range hops {
		if campfire.IsBlindRelay(hop.Role) {
			return LevelContactable
		}
	}

	return LevelAnonymous
}

// MessageHop is a minimal representation of a provenance hop used by LevelFromMessage.
// This type avoids a direct import of pkg/message from pkg/provenance.
// Callers convert message.ProvenanceHop to MessageHop using MessageHopFromSlice or directly.
type MessageHop struct {
	// Role is the campfire membership role of the relaying node (e.g. "full", "blind-relay").
	Role string
}

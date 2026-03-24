// Encryption extension types for the campfire protocol.
//
// Implements the system message payloads for spec-encryption.md v0.2:
//   - campfire:encrypted-init: creation-time commitment to encryption flag
//   - campfire:membership-commit: atomic membership change + epoch rotation
//
// These messages are signed by the campfire key. Neither payload is encrypted
// under the CEK (encrypted-init contains algorithm metadata, not secrets;
// membership-commit epoch rotation predates/supersedes the old CEK).
package campfire

// System message tag constants for the encryption extension (spec Appendix A).
const (
	// TagEncryptedInit is the tag for the campfire:encrypted-init system message.
	// Published at campfire creation when encrypted=true. Commits the encryption
	// flag under the campfire signature for downgrade prevention (spec §6.2).
	TagEncryptedInit = "campfire:encrypted-init"

	// TagMembershipCommit is the tag for the campfire:membership-commit system message.
	// Atomically combines a membership change with epoch rotation (spec §6.1).
	TagMembershipCommit = "campfire:membership-commit"
)

// MembershipCommitReason is the type of membership change triggering epoch rotation.
type MembershipCommitReason string

const (
	// MembershipCommitJoin: member joined, hash-chain epoch derivation (O(0) delivery).
	MembershipCommitJoin MembershipCommitReason = "join"
	// MembershipCommitEvict: member evicted, fresh random root secret with O(N) delivery.
	MembershipCommitEvict MembershipCommitReason = "evict"
	// MembershipCommitLeave: voluntary leave, fresh random root secret with O(N) delivery.
	MembershipCommitLeave MembershipCommitReason = "leave"
	// MembershipCommitScheduled: scheduled rotation, hash-chain (O(0) delivery).
	MembershipCommitScheduled MembershipCommitReason = "scheduled"
)

// MembershipCommitPayload is the payload of a campfire:membership-commit system message.
//
// This message atomically combines a membership change with key epoch rotation,
// eliminating the race condition between separate membership and rekey messages (attack A1).
//
// The payload is NOT encrypted under the CEK (the old CEK may be retired at commit time).
// When ChainDerived == false, the Deliveries map contains per-member hybrid-encrypted
// root secrets. Blind relays are NOT included in Deliveries.
//
// Wire format: CBOR per spec §6.1.
type MembershipCommitPayload struct {
	// Type is the kind of membership change that triggered this epoch rotation.
	Type MembershipCommitReason `cbor:"1,keyasint" json:"type"`
	// Member is the public key (hex) of the joining/leaving/evicted member.
	// Empty for scheduled rotations.
	Member string `cbor:"2,keyasint,omitempty" json:"member,omitempty"`
	// NewEpoch is the new key epoch number after this commit.
	NewEpoch uint64 `cbor:"3,keyasint" json:"new_epoch"`
	// NewMembershipHash is the SHA-256 hash of the new membership set.
	NewMembershipHash []byte `cbor:"4,keyasint" json:"new_membership_hash"`
	// ChainDerived indicates whether the new root secret is hash-chain-derived.
	// true = all members derive locally from their current root secret (O(0)).
	// false = a fresh random root secret was generated; check Deliveries map.
	ChainDerived bool `cbor:"5,keyasint" json:"chain_derived"`
	// Deliveries maps member public key hex → hybrid-encrypted new root secret.
	// Present only when ChainDerived == false (eviction/leave).
	// Keys are hex-encoded Ed25519 public keys; values are encrypted via
	// EncryptToEd25519Key() from pkg/identity (spec §3.3, key delivery).
	// Blind relay members are NOT included in this map.
	Deliveries map[string][]byte `cbor:"6,keyasint,omitempty" json:"deliveries,omitempty"`
}

// EncryptedInitPayload is the payload of a campfire:encrypted-init system message.
//
// Published at campfire creation when encrypted=true. This message commits the
// encryption flag under the campfire key signature. Members use the existence
// and signature of this message to verify the encryption flag, independent of
// relay-provided state (downgrade prevention per spec §2.1, §6.2).
//
// The payload is NOT encrypted (it contains algorithm metadata, not secret material).
//
// IMPORTANT: The info string in this message is for documentation purposes only.
// Implementations MUST NOT use it for HKDF derivation — the info string is
// protocol-fixed as "campfire-message-key-v1" (spec §3.1, attack A8).
type EncryptedInitPayload struct {
	// Epoch is always 0 (creation epoch).
	Epoch uint64 `cbor:"1,keyasint" json:"epoch"`
	// Algorithm is the encryption algorithm, "AES-256-GCM".
	Algorithm string `cbor:"2,keyasint" json:"algorithm"`
	// KDF is the key derivation function, "HKDF-SHA256".
	KDF string `cbor:"3,keyasint" json:"kdf"`
	// Info is the HKDF info string (documentation only — MUST be hardcoded, not read from here).
	Info string `cbor:"4,keyasint" json:"info"`
}

// NewEncryptedInitPayload returns the standard EncryptedInitPayload for epoch 0.
func NewEncryptedInitPayload() EncryptedInitPayload {
	return EncryptedInitPayload{
		Epoch:     0,
		Algorithm: "AES-256-GCM",
		KDF:       "HKDF-SHA256",
		Info:      "campfire-message-key-v1",
	}
}

// NewMembershipCommitPayload constructs a MembershipCommitPayload for an eviction
// or leave event (ChainDerived=false), automatically excluding blind-relay members
// from the Deliveries map (spec §6.1).
//
// members is the full membership set. deliveries maps full-member pubkey hex →
// hybrid-encrypted new root secret. Blind-relay members MUST NOT be included in
// deliveries; this constructor enforces that invariant by filtering out any
// blind-relay keys from the provided deliveries map.
func NewMembershipCommitPayload(
	reason MembershipCommitReason,
	member string,
	newEpoch uint64,
	newMembershipHash []byte,
	members []Member,
	deliveries map[string][]byte,
) MembershipCommitPayload {
	// Build a set of blind-relay pubkey hex strings.
	blindRelays := make(map[string]struct{})
	for _, m := range members {
		if IsBlindRelay(m.Role) {
			blindRelays[encodePubKey(m.PublicKey)] = struct{}{}
		}
	}

	// Filter deliveries: exclude blind-relay members (security invariant).
	filtered := make(map[string][]byte, len(deliveries))
	for pk, enc := range deliveries {
		if _, isBlind := blindRelays[pk]; !isBlind {
			filtered[pk] = enc
		}
	}

	return MembershipCommitPayload{
		Type:              reason,
		Member:            member,
		NewEpoch:          newEpoch,
		NewMembershipHash: newMembershipHash,
		ChainDerived:      false,
		Deliveries:        filtered,
	}
}

// encodePubKey hex-encodes a public key slice for use as a Deliveries map key.
func encodePubKey(key []byte) string {
	const hextable = "0123456789abcdef"
	buf := make([]byte, len(key)*2)
	for i, b := range key {
		buf[i*2] = hextable[b>>4]
		buf[i*2+1] = hextable[b&0xf]
	}
	return string(buf)
}

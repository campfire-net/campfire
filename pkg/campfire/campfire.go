package campfire

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"
)

// Membership role constants.
//
// These constants represent the campfire membership role namespace. They govern
// what actions a member may take within a campfire. This is distinct from the
// peer endpoint role namespace (store.PeerRoleCreator/PeerRoleMember), which
// records the peer's standing relative to a local store and is used for
// server-side delivery enforcement. The two namespaces share a Role field on
// different types (campfire.Member vs. store.PeerEndpoint) but have different
// semantics and should not be conflated.
const (
	// RoleObserver can read messages only. Cannot send. Client-side enforced.
	RoleObserver = "observer"
	// RoleWriter can read and send regular messages. Cannot send campfire:* system messages.
	RoleWriter = "writer"
	// RoleFull has full access: read, send, and sign system messages.
	// This is the default for backward compatibility (empty role = full).
	RoleFull = "full"
	// RoleBlindRelay is a relay member that stores/forwards encrypted messages but
	// does NOT hold the CEK and cannot decrypt payloads (spec-encryption.md v0.2 §2.5).
	// Blind relays:
	//   - Are included in the membership hash (for transparency)
	//   - Sign provenance hops with role: "blind-relay" (verified field)
	//   - Are SKIPPED in key delivery maps within campfire:membership-commit messages
	//   - Can evaluate filters on plaintext metadata (tags, sender, timestamp)
	//   - Cannot initiate epoch rotation (they do not hold key material)
	RoleBlindRelay = "blind-relay"
)

// EffectiveRole returns the canonical role string, defaulting empty/legacy
// role values to RoleFull for backward compatibility.
func EffectiveRole(role string) string {
	switch role {
	case RoleObserver, RoleWriter, RoleFull, RoleBlindRelay:
		return role
	default:
		// empty, "member", "creator", and any unknown legacy value → full
		return RoleFull
	}
}

// IsBlindRelay reports whether the role is RoleBlindRelay.
// Blind relays are excluded from key delivery maps in campfire:membership-commit.
func IsBlindRelay(role string) bool {
	return role == RoleBlindRelay
}

// DeliveryMode constants for campfire message delivery.
const (
	// DeliveryModePull means members poll the server for messages (default).
	DeliveryModePull = "pull"
	// DeliveryModePush means the server pushes messages to members.
	DeliveryModePush = "push"
)

// ValidDeliveryMode reports whether mode is a known delivery mode.
func ValidDeliveryMode(mode string) bool {
	return mode == DeliveryModePull || mode == DeliveryModePush
}

// EffectiveDeliveryModes returns the delivery modes for a campfire, defaulting
// to ["pull"] when modes is nil or empty (backward compat: pre-field-9 campfires).
func EffectiveDeliveryModes(modes []string) []string {
	if len(modes) == 0 {
		return []string{DeliveryModePull}
	}
	return modes
}

// Campfire represents a campfire's state.
// PublicKey and PrivateKey hold the campfire's Ed25519 keypair directly —
// pkg/campfire no longer depends on pkg/identity (infrastructure package).
type Campfire struct {
	PublicKey             ed25519.PublicKey  `cbor:"1,keyasint" json:"-"`
	PrivateKey            ed25519.PrivateKey `cbor:"-" json:"-"`
	JoinProtocol          string             `cbor:"2,keyasint" json:"join_protocol"`
	ReceptionRequirements []string           `cbor:"3,keyasint" json:"reception_requirements"`
	Members               []Member           `cbor:"4,keyasint" json:"members"`
	CreatedAt             int64              `cbor:"5,keyasint" json:"created_at"`
	Threshold             uint               `cbor:"6,keyasint" json:"threshold"`
	// Encrypted is true when this campfire uses E2E payload encryption (spec-encryption.md v0.2 §2.1).
	// CBOR field 7, omitempty — backward compatible: absent = false (unencrypted).
	Encrypted bool `cbor:"7,keyasint,omitempty" json:"encrypted,omitempty"`
	// KeyEpoch is the current symmetric key epoch (spec-encryption.md v0.2 §3.4).
	// CBOR field 8, omitempty — zero value = epoch 0.
	KeyEpoch uint64 `cbor:"8,keyasint,omitempty" json:"key_epoch,omitempty"`
	// DeliveryModes declares how this campfire delivers messages to members.
	// Valid values: "pull" (members poll) and "push" (server pushes).
	// Empty/nil defaults to ["pull"] via EffectiveDeliveryModes().
	DeliveryModes []string `json:"delivery_modes,omitempty"`
}

// Member represents a campfire member.
type Member struct {
	PublicKey []byte `cbor:"1,keyasint" json:"public_key"`
	JoinedAt  int64  `cbor:"2,keyasint" json:"joined_at"`
	Role      string `cbor:"3,keyasint,omitempty" json:"role,omitempty"`
}

// CampfireState is the on-disk representation in the transport directory.
// Includes the campfire's private key (filesystem transport trust model).
//
// NOTE: CampfireState intentionally does not carry Members. The filesystem
// transport stores each member as a separate MemberRecord file alongside the
// campfire state. Callers that need the full in-memory Campfire (including
// members) should call CampfireState.ToCampfire(members).
type CampfireState struct {
	PublicKey             []byte   `cbor:"1,keyasint" json:"public_key"`
	PrivateKey            []byte   `cbor:"2,keyasint" json:"private_key"`
	JoinProtocol          string   `cbor:"3,keyasint" json:"join_protocol"`
	ReceptionRequirements []string `cbor:"4,keyasint" json:"reception_requirements"`
	CreatedAt             int64    `cbor:"5,keyasint" json:"created_at"`
	Threshold             uint     `cbor:"6,keyasint" json:"threshold"`
	// Encrypted is true when this campfire uses E2E payload encryption (spec-encryption.md v0.2 §2.1).
	// CBOR field 7, omitempty — backward compatible: absent = false (unencrypted).
	Encrypted bool `cbor:"7,keyasint,omitempty" json:"encrypted,omitempty"`
	// KeyEpoch is the current symmetric key epoch.
	// CBOR field 8, omitempty — zero value = epoch 0.
	KeyEpoch uint64 `cbor:"8,keyasint,omitempty" json:"key_epoch,omitempty"`
	// DeliveryModes declares how this campfire delivers messages to members.
	// CBOR field 9, omitempty — backward compatible: absent = ["pull"].
	// Valid values: "pull" (members poll for messages), "push" (server pushes to members).
	// Empty/nil on read defaults to ["pull"] via EffectiveDeliveryModes().
	DeliveryModes []string `cbor:"9,keyasint,omitempty" json:"delivery_modes,omitempty"`
}

// ToCampfire reconstructs a live Campfire from this on-disk state and the
// provided member list. When the private key is absent (read-only / remote
// campfire states), Campfire.PrivateKey will be nil.
func (s *CampfireState) ToCampfire(members []MemberRecord) *Campfire {
	cf := &Campfire{
		PublicKey:             s.PublicKey,
		PrivateKey:            s.PrivateKey,
		JoinProtocol:          s.JoinProtocol,
		ReceptionRequirements: s.ReceptionRequirements,
		CreatedAt:             s.CreatedAt,
		Threshold:             s.Threshold,
		Encrypted:             s.Encrypted,
		KeyEpoch:              s.KeyEpoch,
		DeliveryModes:         s.DeliveryModes,
	}
	cf.Members = append(cf.Members, members...)
	return cf
}

// MemberRecord is the on-disk representation of a member in the transport
// directory. It has the same fields as Member; use MemberRecord(m) and
// Member(r) to convert between the two without copying.
type MemberRecord = Member

// New creates a new campfire with the given parameters.
// threshold=1 means any single member can sign provenance hops (default behavior).
// threshold>1 requires FROST multi-party signing (Phase 2).
func New(joinProtocol string, receptionReqs []string, threshold uint) (*Campfire, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating campfire keypair: %w", err)
	}
	if receptionReqs == nil {
		receptionReqs = []string{}
	}
	if threshold == 0 {
		threshold = 1
	}
	return &Campfire{
		PublicKey:             pub,
		PrivateKey:            priv,
		JoinProtocol:          joinProtocol,
		ReceptionRequirements: receptionReqs,
		Members:               []Member{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             threshold,
	}, nil
}

// PublicKeyHex returns the hex-encoded public key of the campfire.
func (c *Campfire) PublicKeyHex() string {
	return fmt.Sprintf("%x", c.PublicKey)
}

// AddMember adds a member to the campfire.
func (c *Campfire) AddMember(pubKey ed25519.PublicKey) {
	c.Members = append(c.Members, Member{
		PublicKey: pubKey,
		JoinedAt:  time.Now().UnixNano(),
	})
}

// RemoveMember removes a member by public key. Returns true if found.
func (c *Campfire) RemoveMember(pubKey ed25519.PublicKey) bool {
	for i, m := range c.Members {
		if bytes.Equal(m.PublicKey, pubKey) {
			c.Members = append(c.Members[:i], c.Members[i+1:]...)
			return true
		}
	}
	return false
}

// IsMember checks if a public key is a member.
func (c *Campfire) IsMember(pubKey ed25519.PublicKey) bool {
	for _, m := range c.Members {
		if bytes.Equal(m.PublicKey, pubKey) {
			return true
		}
	}
	return false
}

// MembershipHash computes the SHA-256 hash of sorted concatenated member public keys.
// The Role field is included in the hash per spec-encryption.md v0.2 §2.5:
// blind relay role must be visible and verifiable in the membership hash.
func (c *Campfire) MembershipHash() []byte {
	// Include role in hash input: sort by (pubkey, role) for determinism.
	type memberKey struct {
		pubkey []byte
		role   string
	}
	keys := make([]memberKey, len(c.Members))
	for i, m := range c.Members {
		keys[i] = memberKey{pubkey: m.PublicKey, role: m.Role}
	}
	sort.Slice(keys, func(i, j int) bool {
		cmp := bytes.Compare(keys[i].pubkey, keys[j].pubkey)
		if cmp != 0 {
			return cmp < 0
		}
		return keys[i].role < keys[j].role
	})
	h := sha256.New()
	for _, k := range keys {
		h.Write(k.pubkey)
		h.Write([]byte(k.role))
	}
	result := h.Sum(nil)
	return result
}

// State returns the on-disk state representation.
func (c *Campfire) State() CampfireState {
	return CampfireState{
		PublicKey:             c.PublicKey,
		PrivateKey:            c.PrivateKey,
		JoinProtocol:          c.JoinProtocol,
		ReceptionRequirements: c.ReceptionRequirements,
		CreatedAt:             c.CreatedAt,
		Threshold:             c.Threshold,
		Encrypted:             c.Encrypted,
		KeyEpoch:              c.KeyEpoch,
		DeliveryModes:         c.DeliveryModes,
	}
}

package campfire

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"
)

// Membership role constants.
const (
	// RoleObserver can read messages only. Cannot send. Client-side enforced.
	RoleObserver = "observer"
	// RoleWriter can read and send regular messages. Cannot send campfire:* system messages.
	RoleWriter = "writer"
	// RoleFull has full access: read, send, and sign system messages.
	// This is the default for backward compatibility (empty role = full).
	RoleFull = "full"
)

// EffectiveRole returns the canonical role string, defaulting empty/legacy
// role values to RoleFull for backward compatibility.
func EffectiveRole(role string) string {
	switch role {
	case RoleObserver, RoleWriter, RoleFull:
		return role
	default:
		// empty, "member", "creator", and any unknown legacy value → full
		return RoleFull
	}
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
}

// Member represents a campfire member.
type Member struct {
	PublicKey []byte `cbor:"1,keyasint" json:"public_key"`
	JoinedAt  int64  `cbor:"2,keyasint" json:"joined_at"`
	Role      string `cbor:"3,keyasint,omitempty" json:"role,omitempty"`
}

// CampfireState is the on-disk representation in the transport directory.
// Includes the campfire's private key (filesystem transport trust model).
type CampfireState struct {
	PublicKey             []byte   `cbor:"1,keyasint" json:"public_key"`
	PrivateKey            []byte   `cbor:"2,keyasint" json:"private_key"`
	JoinProtocol          string   `cbor:"3,keyasint" json:"join_protocol"`
	ReceptionRequirements []string `cbor:"4,keyasint" json:"reception_requirements"`
	CreatedAt             int64    `cbor:"5,keyasint" json:"created_at"`
	Threshold             uint     `cbor:"6,keyasint" json:"threshold"`
}

// MemberRecord is the on-disk representation of a member in the transport directory.
type MemberRecord struct {
	PublicKey []byte `cbor:"1,keyasint" json:"public_key"`
	JoinedAt  int64  `cbor:"2,keyasint" json:"joined_at"`
	Role      string `cbor:"3,keyasint,omitempty" json:"role,omitempty"`
}

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
		if equal(m.PublicKey, pubKey) {
			c.Members = append(c.Members[:i], c.Members[i+1:]...)
			return true
		}
	}
	return false
}

// IsMember checks if a public key is a member.
func (c *Campfire) IsMember(pubKey ed25519.PublicKey) bool {
	for _, m := range c.Members {
		if equal(m.PublicKey, pubKey) {
			return true
		}
	}
	return false
}

// MembershipHash computes the SHA-256 hash of sorted concatenated member public keys.
func (c *Campfire) MembershipHash() []byte {
	keys := make([][]byte, len(c.Members))
	for i, m := range c.Members {
		keys[i] = make([]byte, len(m.PublicKey))
		copy(keys[i], m.PublicKey)
	}
	sort.Slice(keys, func(i, j int) bool {
		for k := 0; k < len(keys[i]) && k < len(keys[j]); k++ {
			if keys[i][k] != keys[j][k] {
				return keys[i][k] < keys[j][k]
			}
		}
		return len(keys[i]) < len(keys[j])
	})
	h := sha256.New()
	for _, k := range keys {
		h.Write(k)
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
	}
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

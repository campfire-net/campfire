package campfire

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	"github.com/3dl-dev/campfire/pkg/identity"
)

// Campfire represents a campfire's state.
type Campfire struct {
	Identity              *identity.Identity `cbor:"1,keyasint" json:"-"`
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
}

// New creates a new campfire with the given parameters.
// threshold=1 means any single member can sign provenance hops (default behavior).
// threshold>1 requires FROST multi-party signing (Phase 2).
func New(joinProtocol string, receptionReqs []string, threshold uint) (*Campfire, error) {
	id, err := identity.Generate()
	if err != nil {
		return nil, fmt.Errorf("generating campfire identity: %w", err)
	}
	if receptionReqs == nil {
		receptionReqs = []string{}
	}
	if threshold == 0 {
		threshold = 1
	}
	return &Campfire{
		Identity:              id,
		JoinProtocol:          joinProtocol,
		ReceptionRequirements: receptionReqs,
		Members:               []Member{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             threshold,
	}, nil
}

// PublicKeyHex returns the hex-encoded public key of the campfire.
func (c *Campfire) PublicKeyHex() string {
	return fmt.Sprintf("%x", c.Identity.PublicKey)
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
		PublicKey:             c.Identity.PublicKey,
		PrivateKey:            c.Identity.PrivateKey,
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

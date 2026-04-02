package message

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/google/uuid"
)

// Message is a campfire protocol message.
type Message struct {
	ID          string          `cbor:"1,keyasint" json:"id"`
	Sender      []byte          `cbor:"2,keyasint" json:"sender"`
	Payload     []byte          `cbor:"3,keyasint" json:"payload"`
	Tags        []string        `cbor:"4,keyasint" json:"tags"`
	Antecedents []string        `cbor:"5,keyasint" json:"antecedents"`
	Timestamp   int64           `cbor:"6,keyasint" json:"timestamp"`
	Signature   []byte          `cbor:"7,keyasint" json:"signature"`
	Provenance  []ProvenanceHop `cbor:"8,keyasint" json:"provenance"`
	// Instance is tainted (sender-asserted, not verified) metadata identifying
	// the sender's role or instance name (e.g. "strategist", "cfo").
	// NOT covered by message signature — can be set to any string.
	// Empty string is the default for backward compatibility.
	Instance string `cbor:"9,keyasint,omitempty" json:"instance,omitempty"`
	// SenderCampfireID is the sender agent's self-campfire ID (identity address).
	// Informational — NOT included in MessageSignInput. Tainted initially: verifier
	// must check that Sender (agent pubkey) is member 0 of this self-campfire.
	// Empty for legacy messages and ephemeral agents without a home campfire.
	// Stored as raw bytes (32-byte Ed25519 public key of the self-campfire).
	SenderCampfireID []byte `cbor:"10,keyasint,omitempty" json:"sender_campfire_id,omitempty"`
}

// ProvenanceHop records a campfire's relay of a message.
type ProvenanceHop struct {
	CampfireID            []byte   `cbor:"1,keyasint" json:"campfire_id"`
	MembershipHash        []byte   `cbor:"2,keyasint" json:"membership_hash"`
	MemberCount           int      `cbor:"3,keyasint" json:"member_count"`
	JoinProtocol          string   `cbor:"4,keyasint" json:"join_protocol"`
	ReceptionRequirements []string `cbor:"5,keyasint" json:"reception_requirements"`
	Timestamp             int64    `cbor:"6,keyasint" json:"timestamp"`
	Signature             []byte   `cbor:"7,keyasint" json:"signature"`
	// Role is the campfire membership role of the relaying node (e.g. "full",
	// "blind-relay"). Covered by the hop signature so verifiers can distinguish
	// a blind-relay hop from a full-member hop. Empty string for legacy hops
	// (omitted from CBOR, preserving wire compatibility with pre-Role relays).
	Role string `cbor:"8,keyasint,omitempty" json:"role,omitempty"`
}

// MessageSignInput is the canonical form for message signing.
type MessageSignInput struct {
	ID          string   `cbor:"1,keyasint"`
	Payload     []byte   `cbor:"2,keyasint"`
	Tags        []string `cbor:"3,keyasint"`
	Antecedents []string `cbor:"4,keyasint"`
	Timestamp   int64    `cbor:"5,keyasint"`
}

// HopSignInput is the canonical form for provenance hop signing.
type HopSignInput struct {
	MessageID             string   `cbor:"1,keyasint"`
	CampfireID            []byte   `cbor:"2,keyasint"`
	MembershipHash        []byte   `cbor:"3,keyasint"`
	MemberCount           int      `cbor:"4,keyasint"`
	JoinProtocol          string   `cbor:"5,keyasint"`
	ReceptionRequirements []string `cbor:"6,keyasint"`
	Timestamp             int64    `cbor:"7,keyasint"`
	// Role is omitted when empty so that legacy hops (Role="") produce identical
	// signed bytes to pre-Role-field implementations (wire-compatible).
	Role string `cbor:"8,keyasint,omitempty"`
}

// NewMessage creates a new signed message.
func NewMessage(senderPriv ed25519.PrivateKey, senderPub ed25519.PublicKey, payload []byte, tags []string, antecedents []string) (*Message, error) {
	if tags == nil {
		tags = []string{}
	}
	if antecedents == nil {
		antecedents = []string{}
	}
	msg := &Message{
		ID:          uuid.New().String(),
		Sender:      senderPub,
		Payload:     payload,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   time.Now().UnixNano(),
		Provenance:  []ProvenanceHop{},
	}

	signInput := MessageSignInput{
		ID:          msg.ID,
		Payload:     msg.Payload,
		Tags:        msg.Tags,
		Antecedents: msg.Antecedents,
		Timestamp:   msg.Timestamp,
	}
	signBytes, err := cfencoding.Marshal(signInput)
	if err != nil {
		return nil, fmt.Errorf("encoding sign input: %w", err)
	}
	msg.Signature = ed25519.Sign(senderPriv, signBytes)

	return msg, nil
}

// VerifySignature checks the message sender's signature.
// Returns false (rather than panicking) if the sender public key or signature
// are absent or have the wrong length — which can occur when the CBOR body
// decodes into a zero-value Message (e.g., wrong CBOR structure).
func (m *Message) VerifySignature() bool {
	if len(m.Sender) != ed25519.PublicKeySize {
		return false
	}
	if len(m.Signature) != ed25519.SignatureSize {
		return false
	}
	signInput := MessageSignInput{
		ID:          m.ID,
		Payload:     m.Payload,
		Tags:        m.Tags,
		Antecedents: m.Antecedents,
		Timestamp:   m.Timestamp,
	}
	signBytes, err := cfencoding.Marshal(signInput)
	if err != nil {
		return false
	}
	return ed25519.Verify(m.Sender, signBytes, m.Signature)
}

// AddHop appends a provenance hop signed by the campfire.
// role is the campfire membership role of the relaying node (e.g. campfire.RoleFull,
// campfire.RoleBlindRelay). Pass an empty string for hops where role is not
// applicable or unknown — this produces wire-compatible output with legacy relays
// that predate the Role field.
func (m *Message) AddHop(
	campfirePriv ed25519.PrivateKey,
	campfirePub ed25519.PublicKey,
	membershipHash []byte,
	memberCount int,
	joinProtocol string,
	receptionReqs []string,
	role string,
) error {
	if receptionReqs == nil {
		receptionReqs = []string{}
	}

	hop := ProvenanceHop{
		CampfireID:            campfirePub,
		MembershipHash:        membershipHash,
		MemberCount:           memberCount,
		JoinProtocol:          joinProtocol,
		ReceptionRequirements: receptionReqs,
		Timestamp:             time.Now().UnixNano(),
		Role:                  role,
	}

	hopSignInput := HopSignInput{
		MessageID:             m.ID,
		CampfireID:            hop.CampfireID,
		MembershipHash:        hop.MembershipHash,
		MemberCount:           hop.MemberCount,
		JoinProtocol:          hop.JoinProtocol,
		ReceptionRequirements: hop.ReceptionRequirements,
		Timestamp:             hop.Timestamp,
		Role:                  hop.Role,
	}
	signBytes, err := cfencoding.Marshal(hopSignInput)
	if err != nil {
		return fmt.Errorf("encoding hop sign input: %w", err)
	}
	hop.Signature = ed25519.Sign(campfirePriv, signBytes)

	m.Provenance = append(m.Provenance, hop)
	return nil
}

// VerifyHop checks a provenance hop's signature.
func VerifyHop(messageID string, hop ProvenanceHop) bool {
	hopSignInput := HopSignInput{
		MessageID:             messageID,
		CampfireID:            hop.CampfireID,
		MembershipHash:        hop.MembershipHash,
		MemberCount:           hop.MemberCount,
		JoinProtocol:          hop.JoinProtocol,
		ReceptionRequirements: hop.ReceptionRequirements,
		Timestamp:             hop.Timestamp,
		Role:                  hop.Role,
	}
	signBytes, err := cfencoding.Marshal(hopSignInput)
	if err != nil {
		return false
	}
	return ed25519.Verify(hop.CampfireID, signBytes, hop.Signature)
}

// SenderHex returns the hex-encoded sender public key.
func (m *Message) SenderHex() string {
	return fmt.Sprintf("%x", m.Sender)
}

// VerifyMessageSignature verifies a message signature from stored fields.
// senderHex is the hex-encoded public key; tags and antecedents are typed slices
// (JSON deserialization is handled at the store boundary, not here).
func VerifyMessageSignature(id string, payload []byte, tags []string, antecedents []string, timestamp int64, senderHex string, signature []byte) bool {
	senderPub, err := hex.DecodeString(senderHex)
	if err != nil {
		return false
	}
	signInput := MessageSignInput{
		ID:          id,
		Payload:     payload,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   timestamp,
	}
	signBytes, err := cfencoding.Marshal(signInput)
	if err != nil {
		return false
	}
	return ed25519.Verify(senderPub, signBytes, signature)
}

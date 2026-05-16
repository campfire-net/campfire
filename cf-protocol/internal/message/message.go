package message

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync/atomic"
	"time"

	cfencoding "github.com/campfire-net/campfire/cf-protocol/internal/encoding"
	"github.com/google/uuid"
)

// messageIDRegex matches an RFC 4122 UUID — the canonical message.ID format
// produced by NewMessage (see uuid.New().String()). 8-4-4-4-12 lowercase or
// uppercase hex characters separated by hyphens.
//
// Validation is strict and defense-in-depth: the filesystem transport joins
// msg.ID into the on-disk filename, so any unconstrained value enables path
// traversal (an attacker-supplied ID like "../../etc/passwd" escapes the
// bucket directory via filepath.Join's cleaning). Restricting IDs to UUIDs
// makes traversal lexically impossible.
var messageIDRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ErrInvalidMessageID is returned by ValidateID and by transport writers when
// a message ID does not match the canonical UUID format. Callers receiving
// this error MUST reject the message — never fall back to a sanitized ID.
var ErrInvalidMessageID = errors.New("invalid message ID: must be UUID format")

// ValidateID reports whether id is a valid message ID (RFC 4122 UUID format).
// Returns ErrInvalidMessageID on mismatch. Use this at every ingress point
// that will eventually feed msg.ID into a filesystem path, an SQL query, or
// any other context where unconstrained input is dangerous.
func ValidateID(id string) error {
	if !messageIDRegex.MatchString(id) {
		return fmt.Errorf("%w: got %q", ErrInvalidMessageID, id)
	}
	return nil
}

// lastTimestamp is the last nanosecond timestamp returned by monotonicNano.
// Protected by CAS so concurrent callers never receive the same value.
var lastTimestamp int64

// monotonicNano returns a nanosecond timestamp that is strictly greater than
// every value it has previously returned in this process. It starts from
// time.Now().UnixNano() and increments by 1 if the clock hasn't advanced.
//
// This prevents two messages created in rapid succession (within the same
// nanosecond) from receiving identical Timestamps. Identical timestamps break
// the subscribe.go cursor: if a non-matching message and a matching message
// share the same timestamp T, and the subscription cursor has already advanced
// to T after seeing only the non-matching message, the matching message is
// missed because the store query uses "timestamp > cursor" (strict inequality).
func monotonicNano() int64 {
	for {
		now := time.Now().UnixNano()
		prev := atomic.LoadInt64(&lastTimestamp)
		next := now
		if next <= prev {
			next = prev + 1
		}
		if atomic.CompareAndSwapInt64(&lastTimestamp, prev, next) {
			return next
		}
	}
}

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
	//
	// TRUST BOUNDARY: Instance is NOT included in MessageSignInput and is NOT
	// covered by the message signature. Any sender can set Instance to any
	// arbitrary string — including spoofing another agent's role name. Consumers
	// MUST treat Instance as an untrusted display hint only. Never use Instance
	// for access control, routing decisions, or trust assertions. Use Sender
	// (the verified Ed25519 public key) for identity-sensitive operations.
	//
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
func NewMessage(signer Signer, payload []byte, tags []string, antecedents []string) (*Message, error) {
	if tags == nil {
		tags = []string{}
	}
	if antecedents == nil {
		antecedents = []string{}
	}
	msg := &Message{
		ID:          uuid.New().String(),
		Sender:      signer.PublicKey(),
		Payload:     payload,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   monotonicNano(),
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
	sig, err := signer.Sign(signBytes)
	if err != nil {
		return nil, fmt.Errorf("signing message: %w", err)
	}
	msg.Signature = sig

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

// VerifyProvenance checks that a message has at least one provenance hop and
// that all hops carry valid signatures. Returns false if Provenance is empty
// (an empty slice passes the hop loop trivially, bypassing relay accountability)
// or if any hop fails VerifyHop.
//
// This is the canonical validation used by syncIfFilesystem, syncFromFilesystem,
// and the bridge; callers should use this rather than open-coding the loop.
func (m *Message) VerifyProvenance() bool {
	if len(m.Provenance) == 0 {
		return false
	}
	for _, hop := range m.Provenance {
		if !VerifyHop(m.ID, hop) {
			return false
		}
	}
	return true
}

// SenderHex returns the hex-encoded sender public key.
func (m *Message) SenderHex() string {
	return fmt.Sprintf("%x", m.Sender)
}

// SenderIdentity returns the best available identity string for a message.
// When SenderCampfireID is set, it returns the hex-encoded campfire ID (the
// agent's stable identity address). Falls back to SenderHex() for legacy
// messages and ephemeral agents without a home campfire.
//
// Use SenderIdentity() for display and addressing. Use Sender (raw bytes) or
// SenderHex() for signature verification — the signing key is always the
// agent's Ed25519 public key, never the campfire ID.
func (m *Message) SenderIdentity() string {
	if len(m.SenderCampfireID) > 0 {
		return fmt.Sprintf("%x", m.SenderCampfireID)
	}
	return m.SenderHex()
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

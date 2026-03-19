// Package threshold provides FROST-based distributed key generation and
// threshold signing for campfire group identities.
//
// Serialization: DKGResult can be serialized to/from JSON using MarshalResult
// and UnmarshalResult. The SecretShare and Public types both support JSON
// marshaling natively via their MarshalJSON/UnmarshalJSON methods.
//
// Signatures produced by this package are standard Ed25519 signatures,
// verifiable with crypto/ed25519.Verify using the group public key.
//
// Re-sharing (redistributing key shares without changing the group public key)
// is NOT supported — no Go FROST library implements it. Membership changes
// (join/evict) require a new DKG, producing a new keypair (campfire:rekey).
package threshold

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/taurusgroup/frost-ed25519/pkg/eddsa"
	"github.com/taurusgroup/frost-ed25519/pkg/frost"
	frostkeygen "github.com/taurusgroup/frost-ed25519/pkg/frost/keygen"
	"github.com/taurusgroup/frost-ed25519/pkg/frost/party"
	frostsign "github.com/taurusgroup/frost-ed25519/pkg/frost/sign"
	"github.com/taurusgroup/frost-ed25519/pkg/messages"
	"github.com/taurusgroup/frost-ed25519/pkg/state"
)

// DKGResult holds the outputs of a successful distributed key generation.
type DKGResult struct {
	// SecretShare is this participant's secret share. Keep it private.
	SecretShare *eddsa.SecretShare
	// Public contains the group public key and all participants' public shares.
	Public *eddsa.Public
}

// GroupPublicKey returns the group Ed25519 public key as a standard byte slice.
func (r *DKGResult) GroupPublicKey() ed25519.PublicKey {
	return r.Public.GroupKey.ToEd25519()
}

// DKGParticipant manages one participant's state during a distributed key generation.
// The caller is responsible for transporting round messages between participants.
type DKGParticipant struct {
	s   *state.State
	out *frostkeygen.Output
}

// NewDKGParticipant initialises one participant's DKG state.
//
//   - myID: this participant's ID (must be unique across participantIDs, non-zero)
//   - participantIDs: all participant IDs (including myID)
//   - threshold: minimum number of signers required to produce a signature
//     (1 ≤ threshold < len(participantIDs)). This is the k in "k-of-n" semantics.
//     Internally, the FROST polynomial degree is threshold-1.
func NewDKGParticipant(myID uint32, participantIDs []uint32, threshold int) (*DKGParticipant, error) {
	if threshold < 1 {
		return nil, fmt.Errorf("threshold: NewDKGParticipant: threshold must be >= 1")
	}
	ids := make([]party.ID, len(participantIDs))
	for i, id := range participantIDs {
		ids[i] = party.ID(id)
	}
	set := party.NewIDSlice(ids)
	// FROST library uses polynomial degree = threshold-1, meaning threshold parties can reconstruct.
	s, out, err := frost.NewKeygenState(party.ID(myID), set, party.Size(threshold-1), 0)
	if err != nil {
		return nil, fmt.Errorf("threshold: NewDKGParticipant: %w", err)
	}
	return &DKGParticipant{s: s, out: out}, nil
}

// Start returns the initial round-1 outbound messages that must be broadcast
// to all other participants.
func (p *DKGParticipant) Start() []*messages.Message {
	return p.s.ProcessAll()
}

// Deliver delivers an inbound message from another participant.
func (p *DKGParticipant) Deliver(msg *messages.Message) error {
	if err := p.s.HandleMessage(msg); err != nil {
		return fmt.Errorf("threshold: DKGParticipant.Deliver: %w", err)
	}
	return nil
}

// ProcessAll processes any newly deliverable messages and returns outbound
// messages that must be forwarded. Call after every Deliver.
func (p *DKGParticipant) ProcessAll() []*messages.Message {
	return p.s.ProcessAll()
}

// Done returns a channel that is closed when the DKG is complete or aborted.
func (p *DKGParticipant) Done() <-chan struct{} {
	return p.s.Done()
}

// Result blocks until the DKG completes and returns the result, or returns an
// error if the protocol aborted.
func (p *DKGParticipant) Result() (*DKGResult, error) {
	if err := p.s.WaitForError(); err != nil {
		return nil, fmt.Errorf("threshold: DKG aborted: %w", err)
	}
	return &DKGResult{
		SecretShare: p.out.SecretKey,
		Public:      p.out.Public,
	}, nil
}

// cloneMsg serializes a message to bytes and deserializes it into a fresh
// *messages.Message. This is required because the FROST library mutates
// message fields during processing (e.g., zeroing shares after use), so
// each recipient must receive an independent copy.
func cloneMsg(msg *messages.Message) (*messages.Message, error) {
	b, err := msg.MarshalBinary()
	if err != nil {
		return nil, err
	}
	var m messages.Message
	if err := m.UnmarshalBinary(b); err != nil {
		return nil, err
	}
	return &m, nil
}

// routeDKG distributes msg to the appropriate per-participant byte-channel inboxes,
// serializing the message so each recipient gets an independent copy.
func routeDKG(msg *messages.Message, participantIDs []uint32, selfID uint32, inboxes map[uint32]chan []byte) error {
	b, err := msg.MarshalBinary()
	if err != nil {
		return fmt.Errorf("threshold: routeDKG marshal: %w", err)
	}
	if msg.IsBroadcast() {
		for _, id := range participantIDs {
			if id != selfID {
				dst := make([]byte, len(b))
				copy(dst, b)
				inboxes[id] <- dst
			}
		}
	} else {
		to := uint32(msg.To)
		if ch, ok := inboxes[to]; ok {
			dst := make([]byte, len(b))
			copy(dst, b)
			ch <- dst
		}
	}
	return nil
}

// RunDKG runs a complete DKG in-process, passing messages directly between
// participant states. Each message is serialized to bytes and deserialized per
// recipient to ensure independent copies (the FROST library mutates messages
// during processing). Intended for unit tests and local simulations; real
// deployments drive participants via the round-based API and a network transport.
func RunDKG(participantIDs []uint32, threshold int) (map[uint32]*DKGResult, error) {
	n := len(participantIDs)

	// Per-participant byte-level inboxes. Buffer conservatively.
	inboxes := make(map[uint32]chan []byte, n)
	for _, id := range participantIDs {
		inboxes[id] = make(chan []byte, n*n*8)
	}

	participants := make(map[uint32]*DKGParticipant, n)
	for _, id := range participantIDs {
		p, err := NewDKGParticipant(id, participantIDs, threshold)
		if err != nil {
			return nil, fmt.Errorf("threshold: RunDKG participant %d: %w", id, err)
		}
		participants[id] = p
		// Seed round-0 output messages.
		for _, msg := range p.Start() {
			if err := routeDKG(msg, participantIDs, id, inboxes); err != nil {
				return nil, err
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(n)

	drainDKG := func(p *DKGParticipant, uid uint32) {
		for {
			select {
			case <-p.Done():
				return
			default:
			}
			out := p.ProcessAll()
			if len(out) == 0 {
				return
			}
			for _, msg := range out {
				if err := routeDKG(msg, participantIDs, uid, inboxes); err != nil {
					continue
				}
			}
		}
	}

	for _, uid := range participantIDs {
		uid := uid
		p := participants[uid]
		inbox := inboxes[uid]
		go func() {
			defer wg.Done()
			// Advance any rounds that require 0 external messages.
			drainDKG(p, uid)
			for {
				select {
				case <-p.Done():
					return
				case raw, ok := <-inbox:
					if !ok {
						return
					}
					var msg messages.Message
					if err := msg.UnmarshalBinary(raw); err != nil {
						continue
					}
					if err := p.Deliver(&msg); err != nil {
						// Non-fatal: state validates internally.
						continue
					}
					drainDKG(p, uid)
				}
			}
		}()
	}

	wg.Wait()

	results := make(map[uint32]*DKGResult, n)
	for id, p := range participants {
		r, err := p.Result()
		if err != nil {
			return nil, fmt.Errorf("threshold: RunDKG participant %d: %w", id, err)
		}
		results[id] = r
	}
	return results, nil
}

// SigningSession manages one participant's state during a FROST signing round.
// The caller is responsible for transporting round messages between participants.
type SigningSession struct {
	s   *state.State
	out *frostsign.Output
}

// NewSigningSession initialises one participant's signing state.
//
//   - myShare: this participant's DKG secret share
//   - public: the group public data from DKG (contains all participant shares + group key)
//   - message: the message to sign
//   - signerIDs: IDs of participants taking part in this signing session (must be ≥ threshold+1)
func NewSigningSession(myShare *eddsa.SecretShare, public *eddsa.Public, message []byte, signerIDs []uint32) (*SigningSession, error) {
	ids := make([]party.ID, len(signerIDs))
	for i, id := range signerIDs {
		ids[i] = party.ID(id)
	}
	set := party.NewIDSlice(ids)
	s, out, err := frost.NewSignState(set, myShare, public, message, 0)
	if err != nil {
		return nil, fmt.Errorf("threshold: NewSigningSession: %w", err)
	}
	return &SigningSession{s: s, out: out}, nil
}

// Start returns the initial round-1 (commitment) messages to broadcast.
func (ss *SigningSession) Start() []*messages.Message {
	return ss.s.ProcessAll()
}

// Deliver delivers an inbound message from another participant.
func (ss *SigningSession) Deliver(msg *messages.Message) error {
	if err := ss.s.HandleMessage(msg); err != nil {
		return fmt.Errorf("threshold: SigningSession.Deliver: %w", err)
	}
	return nil
}

// ProcessAll processes deliverable messages and returns outbound messages.
func (ss *SigningSession) ProcessAll() []*messages.Message {
	return ss.s.ProcessAll()
}

// Done returns a channel that is closed when signing is complete or aborted.
func (ss *SigningSession) Done() <-chan struct{} {
	return ss.s.Done()
}

// Signature blocks until the signing session completes and returns the
// standard Ed25519 signature bytes (64 bytes), or an error if aborted.
func (ss *SigningSession) Signature() ([]byte, error) {
	if err := ss.s.WaitForError(); err != nil {
		return nil, fmt.Errorf("threshold: signing aborted: %w", err)
	}
	if ss.out.Signature == nil {
		return nil, fmt.Errorf("threshold: signing produced no signature")
	}
	return ss.out.Signature.ToEd25519(), nil
}

func routeSign(msg *messages.Message, signerIDs []uint32, selfID uint32, inboxes map[uint32]chan []byte) error {
	b, err := msg.MarshalBinary()
	if err != nil {
		return fmt.Errorf("threshold: routeSign marshal: %w", err)
	}
	if msg.IsBroadcast() {
		for _, id := range signerIDs {
			if id != selfID {
				dst := make([]byte, len(b))
				copy(dst, b)
				inboxes[id] <- dst
			}
		}
	} else {
		to := uint32(msg.To)
		if ch, ok := inboxes[to]; ok {
			dst := make([]byte, len(b))
			copy(dst, b)
			ch <- dst
		}
	}
	return nil
}

// Sign runs a complete signing session in-process for signerIDs participants.
// Messages are serialized per recipient to ensure independent copies.
//
// Returns a standard Ed25519 signature (64 bytes) verifiable with
// crypto/ed25519.Verify(groupPublicKey, message, sig).
func Sign(shares map[uint32]*DKGResult, signerIDs []uint32, message []byte) ([]byte, error) {
	if len(signerIDs) == 0 {
		return nil, fmt.Errorf("threshold: Sign: no signers")
	}

	// Pick any participant's Public (they're all identical post-DKG).
	var public *eddsa.Public
	for _, id := range signerIDs {
		r, ok := shares[id]
		if !ok {
			return nil, fmt.Errorf("threshold: Sign: signer %d not in DKG result set", id)
		}
		public = r.Public
		break
	}

	n := len(signerIDs)

	// Per-participant byte inboxes.
	inboxes := make(map[uint32]chan []byte, n)
	for _, id := range signerIDs {
		inboxes[id] = make(chan []byte, n*n*8)
	}

	sessions := make(map[uint32]*SigningSession, n)
	for _, id := range signerIDs {
		r, ok := shares[id]
		if !ok {
			return nil, fmt.Errorf("threshold: Sign: signer %d not in DKG result set", id)
		}
		ss, err := NewSigningSession(r.SecretShare, public, message, signerIDs)
		if err != nil {
			return nil, fmt.Errorf("threshold: Sign participant %d: %w", id, err)
		}
		sessions[id] = ss
		// Seed round-0 output (commitment) messages.
		for _, msg := range ss.Start() {
			if err := routeSign(msg, signerIDs, id, inboxes); err != nil {
				return nil, err
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(n)

	drainSign := func(ss *SigningSession, uid uint32) {
		for {
			select {
			case <-ss.Done():
				return
			default:
			}
			out := ss.ProcessAll()
			if len(out) == 0 {
				return
			}
			for _, msg := range out {
				if err := routeSign(msg, signerIDs, uid, inboxes); err != nil {
					continue
				}
			}
		}
	}

	for _, uid := range signerIDs {
		uid := uid
		ss := sessions[uid]
		inbox := inboxes[uid]
		go func() {
			defer wg.Done()
			// Advance any rounds that require 0 external messages (e.g., single-signer case).
			drainSign(ss, uid)
			for {
				select {
				case <-ss.Done():
					return
				case raw, ok := <-inbox:
					if !ok {
						return
					}
					var msg messages.Message
					if err := msg.UnmarshalBinary(raw); err != nil {
						continue
					}
					if err := ss.Deliver(&msg); err != nil {
						continue
					}
					drainSign(ss, uid)
				}
			}
		}()
	}

	wg.Wait()

	// Return the signature from any participant (all produce the same result).
	for _, ss := range sessions {
		return ss.Signature()
	}
	return nil, fmt.Errorf("threshold: Sign: no sessions")
}

// serializedResult holds the JSON-serialized form of a DKGResult for storage.
type serializedResult struct {
	ParticipantID uint32          `json:"participant_id"`
	SecretShare   json.RawMessage `json:"secret_share"`
	Public        json.RawMessage `json:"public"`
}

// MarshalResult serializes a DKGResult to JSON bytes for storage.
func MarshalResult(participantID uint32, r *DKGResult) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("threshold: MarshalResult: nil DKGResult")
	}
	shareBytes, err := json.Marshal(r.SecretShare)
	if err != nil {
		return nil, fmt.Errorf("threshold: MarshalResult: serializing secret share: %w", err)
	}
	pubBytes, err := json.Marshal(r.Public)
	if err != nil {
		return nil, fmt.Errorf("threshold: MarshalResult: serializing public data: %w", err)
	}
	return json.Marshal(serializedResult{
		ParticipantID: participantID,
		SecretShare:   shareBytes,
		Public:        pubBytes,
	})
}

// UnmarshalResult deserializes a DKGResult from JSON bytes.
func UnmarshalResult(data []byte) (uint32, *DKGResult, error) {
	var s serializedResult
	if err := json.Unmarshal(data, &s); err != nil {
		return 0, nil, fmt.Errorf("threshold: UnmarshalResult: %w", err)
	}
	var secretShare eddsa.SecretShare
	if err := json.Unmarshal(s.SecretShare, &secretShare); err != nil {
		return 0, nil, fmt.Errorf("threshold: UnmarshalResult: deserializing secret share: %w", err)
	}
	var public eddsa.Public
	if err := json.Unmarshal(s.Public, &public); err != nil {
		return 0, nil, fmt.Errorf("threshold: UnmarshalResult: deserializing public data: %w", err)
	}
	return s.ParticipantID, &DKGResult{
		SecretShare: &secretShare,
		Public:      &public,
	}, nil
}

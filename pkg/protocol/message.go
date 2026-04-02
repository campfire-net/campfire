package protocol

import (
	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// Message is the SDK-facing campfire message type.
// Sender is the hex-encoded Ed25519 public key of the message author.
// Tags, Antecedents, Instance, and SenderCampfireID are tainted (sender-asserted) metadata.
type Message struct {
	ID          string
	CampfireID  string
	Sender      string // hex pubkey
	Payload     []byte
	Tags        []string
	Antecedents []string
	Timestamp   int64
	Instance    string
	Signature   []byte
	// SenderCampfireID is the hex-encoded self-campfire ID of the sender agent.
	// Tainted (sender-asserted, not verified). Empty for legacy messages and
	// ephemeral agents without a home campfire.
	SenderCampfireID string
	// Provenance holds the verified relay hops from the underlying message.
	// Use IsBridged() to test for blind-relay hops.
	Provenance []message.ProvenanceHop
}

// IsBridged reports whether this message passed through a blind-relay hop,
// indicating it was bridged from an external system (e.g. Teams, Slack).
// A message is considered bridged if at least one provenance hop carries the
// "blind-relay" role (campfire.RoleBlindRelay).
func (m *Message) IsBridged() bool {
	for _, hop := range m.Provenance {
		if campfire.IsBlindRelay(hop.Role) {
			return true
		}
	}
	return false
}

// MessageFromRecord converts a store.MessageRecord to a protocol.Message.
// Use this when you need bridge-aware helpers (IsBridged) on messages
// returned by client.Read().
func MessageFromRecord(r store.MessageRecord) Message {
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	antecedents := r.Antecedents
	if antecedents == nil {
		antecedents = []string{}
	}
	provenance := r.Provenance
	if provenance == nil {
		provenance = []message.ProvenanceHop{}
	}
	return Message{
		ID:               r.ID,
		CampfireID:       r.CampfireID,
		Sender:           r.Sender,
		Payload:          r.Payload,
		Tags:             tags,
		Antecedents:      antecedents,
		Timestamp:        r.Timestamp,
		Instance:         r.Instance,
		Signature:        r.Signature,
		SenderCampfireID: r.SenderCampfireID,
		Provenance:       provenance,
	}
}

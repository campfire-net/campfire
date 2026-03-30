package protocol

import (
	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// Message is the SDK-facing campfire message type.
// Sender is the hex-encoded Ed25519 public key of the message author.
// Tags, Antecedents, and Instance are tainted (sender-asserted) metadata.
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

// OriginalSender returns the hex-encoded public key of the actual message signer.
// For bridged messages, this is the bridge's public key (the party that signed
// the protocol message). The external user's identity is encoded by the bridge
// in the message payload or Instance field, not in the protocol sender field.
func (m *Message) OriginalSender() string {
	return m.Sender
}

// messageFromRecord converts a store.MessageRecord to a protocol.Message.
// Sender is already stored as a hex string so no conversion is needed.
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
		ID:          r.ID,
		CampfireID:  r.CampfireID,
		Sender:      r.Sender,
		Payload:     r.Payload,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   r.Timestamp,
		Instance:    r.Instance,
		Signature:   r.Signature,
		Provenance:  provenance,
	}
}

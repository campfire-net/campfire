package protocol

import "github.com/campfire-net/campfire/pkg/store"

// Message is the SDK-facing campfire message type.
// Sender is the hex-encoded Ed25519 public key of the message author.
// Tags, Antecedents, and Instance are tainted (sender-asserted) metadata.
type Message struct {
	ID          string
	Sender      string // hex pubkey
	Payload     []byte
	Tags        []string
	Antecedents []string
	Timestamp   int64
	Instance    string
}

// messageFromRecord converts a store.MessageRecord to a protocol.Message.
// Sender is already stored as a hex string so no conversion is needed.
func messageFromRecord(r store.MessageRecord) Message {
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	antecedents := r.Antecedents
	if antecedents == nil {
		antecedents = []string{}
	}
	return Message{
		ID:          r.ID,
		Sender:      r.Sender,
		Payload:     r.Payload,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   r.Timestamp,
		Instance:    r.Instance,
	}
}

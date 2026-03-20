package store

import (
	"fmt"

	"github.com/campfire-net/campfire/pkg/message"
)

// MessageRecordFromMessage converts a message.Message to a MessageRecord ready for storage.
// campfireID is the campfire this message belongs to.
// receivedAt is the nanosecond timestamp when this node received the message (use NowNano()).
//
// Tags, Antecedents, and Provenance are stored as typed Go values on MessageRecord.
// JSON serialization to SQLite TEXT is handled by AddMessage at the store boundary.
func MessageRecordFromMessage(campfireID string, msg *message.Message, receivedAt int64) MessageRecord {
	tags := msg.Tags
	if tags == nil {
		tags = []string{}
	}
	antecedents := msg.Antecedents
	if antecedents == nil {
		antecedents = []string{}
	}
	provenance := msg.Provenance
	if provenance == nil {
		provenance = []message.ProvenanceHop{}
	}
	return MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      fmt.Sprintf("%x", msg.Sender),
		Payload:     msg.Payload,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
		Provenance:  provenance,
		ReceivedAt:  receivedAt,
		Instance:    msg.Instance,
	}
}

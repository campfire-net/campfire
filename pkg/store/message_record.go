package store

import (
	"encoding/json"
	"fmt"

	"github.com/campfire-net/campfire/pkg/message"
)

// MessageRecordFromMessage converts a message.Message to a MessageRecord ready for storage.
// campfireID is the campfire this message belongs to.
// receivedAt is the nanosecond timestamp when this node received the message (use NowNano()).
//
// This is the single canonical serialization path. All call sites that previously
// copy-pasted json.Marshal(msg.Tags/Antecedents/Provenance) + fmt.Sprintf("%x", msg.Sender)
// should use this function instead.
func MessageRecordFromMessage(campfireID string, msg *message.Message, receivedAt int64) MessageRecord {
	tagsJSON, _ := json.Marshal(msg.Tags)
	anteJSON, _ := json.Marshal(msg.Antecedents)
	provJSON, _ := json.Marshal(msg.Provenance)
	return MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      fmt.Sprintf("%x", msg.Sender),
		Payload:     msg.Payload,
		Tags:        string(tagsJSON),
		Antecedents: string(anteJSON),
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
		Provenance:  string(provJSON),
		ReceivedAt:  receivedAt,
		Instance:    msg.Instance,
	}
}

package beacon

import (
	"encoding/json"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// RegistrationEntry is a beacon:registration message with durability metadata.
type RegistrationEntry struct {
	MessageID  string
	CampfireID string
	Tags       []string
	Durability *BeaconDurability // nil if no durability tags present
	Payload    json.RawMessage
}

// ScanRegistrations reads beacon:registration messages from the store and
// validates durability tags as a post-pass. Messages with malformed durability
// tags are rejected (skipped). Valid registrations with their parsed durability
// metadata are returned for inclusion in directory index results.
func ScanRegistrations(s store.MessageStore, campfireID string, now time.Time) ([]RegistrationEntry, error) {
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{"beacon:registration"},
	})
	if err != nil {
		return nil, err
	}

	var entries []RegistrationEntry
	for _, msg := range msgs {
		dur, err := CheckBeaconDurability(msg.Tags, now)
		if err != nil {
			continue // malformed durability tags — reject
		}

		entry := RegistrationEntry{
			MessageID:  msg.ID,
			CampfireID: campfireID,
			Tags:       msg.Tags,
			Durability: dur,
			Payload:    msg.Payload,
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

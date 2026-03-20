package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// validFieldNames is the set of field names accepted by --fields.
var validFieldNames = map[string]bool{
	"id":          true,
	"sender":      true,
	"payload":     true,
	"tags":        true,
	"timestamp":   true,
	"antecedents": true,
	"signature":   true,
	"provenance":  true,
	"instance":    true,
	"campfire_id": true,
}

// parseFieldSet parses a comma-separated list of field names and returns a set.
// Returns (nil, nil) when s is empty (meaning all fields).
// Returns an error when any field name is unknown.
func parseFieldSet(s string) (map[string]bool, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	fs := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !validFieldNames[p] {
			return nil, fmt.Errorf("unknown field %q; valid fields: id, sender, payload, tags, timestamp, antecedents, signature, provenance, instance, campfire_id", p)
		}
		fs[p] = true
	}
	return fs, nil
}

// sanitizePayload strips terminal control characters (escape sequences and other
// non-printable bytes) from a message payload before displaying it. This prevents
// terminal injection via crafted message content.
func sanitizePayload(payload []byte) string {
	out := make([]byte, 0, len(payload))
	for _, b := range payload {
		// Allow printable ASCII (0x20-0x7E), tab (0x09), and newline (0x0A, 0x0D).
		// Reject ESC (0x1B) and all other control characters to block escape sequences.
		if b == 0x09 || b == 0x0A || b == 0x0D || (b >= 0x20 && b <= 0x7E) || b >= 0x80 {
			out = append(out, b)
		}
	}
	return string(out)
}

// printSingleMessage renders one message in the canonical human-readable format to w.
// This is the shared formatting kernel used by printMessagesWithFields (default path)
// and printNATMessages so the display logic lives in exactly one place.
func printSingleMessage(w io.Writer, cfShort, ts, senderDisplay string, tags, antecedents []string, payload []byte) {
	fmt.Fprintf(w, "[campfire:%s] %s %s\n", cfShort, ts, senderDisplay)
	if len(tags) > 0 {
		fmt.Fprintf(w, "  tags: %s\n", strings.Join(tags, ", "))
	}
	if len(antecedents) > 0 {
		shortAnts := make([]string, len(antecedents))
		for i, a := range antecedents {
			if len(a) > 8 {
				shortAnts[i] = a[:8]
			} else {
				shortAnts[i] = a
			}
		}
		fmt.Fprintf(w, "  antecedents: %s\n", strings.Join(shortAnts, ", "))
	}
	fmt.Fprintf(w, "  %s\n\n", sanitizePayload(payload))
}

// extractMessageFields returns the typed tags and antecedents from a MessageRecord.
func extractMessageFields(m store.MessageRecord) (tags []string, antecedents []string) {
	return m.Tags, m.Antecedents
}

// printMessagesWithFields prints messages in human-readable format, filtering to
// only the requested fields. When fields is nil, all fields are printed using the
// original output format (backward compatible). When fields is non-nil, only the
// requested fields are included.
func printMessagesWithFields(allMessages []store.MessageRecord, s *store.Store, fields map[string]bool) {
	if len(allMessages) == 0 {
		return
	}

	// Default path: nil fields means all fields, use the original output format exactly.
	if fields == nil {
		for _, m := range allMessages {
			tags, antecedents := extractMessageFields(m)

			cfShort := m.CampfireID
			if len(cfShort) > 6 {
				cfShort = cfShort[:6]
			}
			senderShort := m.Sender
			if len(senderShort) > 6 {
				senderShort = senderShort[:6]
			}
			senderDisplay := "agent:" + senderShort
			if m.Instance != "" {
				senderDisplay += " (" + m.Instance + ")"
			}
			ts := time.Unix(0, m.Timestamp).Format("2006-01-02 15:04:05")

			// Status markers (future/fulfilled) — appended to sender display.
			var markers []string
			if s != nil {
				for _, t := range tags {
					if t == "future" {
						refs, _ := s.ListReferencingMessages(m.ID)
						fulfilled := false
						for _, ref := range refs {
							for _, rt := range ref.Tags {
								if rt == "fulfills" {
									fulfilled = true
								}
							}
						}
						if fulfilled {
							markers = append(markers, "fulfilled")
						} else {
							markers = append(markers, "future")
						}
					}
				}
			}
			if len(markers) > 0 {
				senderDisplay += " [" + strings.Join(markers, ", ") + "]"
			}

			printSingleMessage(os.Stdout, cfShort, ts, senderDisplay, tags, antecedents, m.Payload)
		}
		return
	}

	// Projection path: only emit the requested fields.
	for _, m := range allMessages {
		tags, antecedents := extractMessageFields(m)

		// Header line — always printed so output is parseable, but only includes
		// requested header-level fields.
		cfShort := m.CampfireID
		if len(cfShort) > 6 {
			cfShort = cfShort[:6]
		}
		senderShort := m.Sender
		if len(senderShort) > 6 {
			senderShort = senderShort[:6]
		}

		var headerParts []string
		if fields["campfire_id"] {
			headerParts = append(headerParts, fmt.Sprintf("[campfire:%s]", cfShort))
		}
		if fields["timestamp"] {
			ts := time.Unix(0, m.Timestamp).Format("2006-01-02 15:04:05")
			headerParts = append(headerParts, ts)
		}
		if fields["sender"] {
			senderDisplay := "agent:" + senderShort
			if fields["instance"] && m.Instance != "" {
				senderDisplay += " (" + m.Instance + ")"
			}
			headerParts = append(headerParts, senderDisplay)
		} else if fields["instance"] && m.Instance != "" {
			headerParts = append(headerParts, "("+m.Instance+")")
		}

		if len(headerParts) > 0 {
			fmt.Println(strings.Join(headerParts, " "))
		}

		if fields["id"] {
			idDisplay := m.ID
			if len(idDisplay) > 8 {
				idDisplay = idDisplay[:8]
			}
			fmt.Printf("  id: %s\n", idDisplay)
		}
		if fields["tags"] && len(tags) > 0 {
			fmt.Printf("  tags: %s\n", strings.Join(tags, ", "))
		}
		if fields["antecedents"] && len(antecedents) > 0 {
			shortAnts := make([]string, len(antecedents))
			for i, a := range antecedents {
				if len(a) > 8 {
					shortAnts[i] = a[:8]
				} else {
					shortAnts[i] = a
				}
			}
			fmt.Printf("  antecedents: %s\n", strings.Join(shortAnts, ", "))
		}
		if fields["payload"] {
			fmt.Printf("  %s\n", sanitizePayload(m.Payload))
		}
		fmt.Println()
	}
}

// printMessages prints message records in the standard human-readable format.
// It is a backward-compatible wrapper around printMessagesWithFields with no field projection.
func printMessages(allMessages []store.MessageRecord, s *store.Store) {
	printMessagesWithFields(allMessages, s, nil)
}

// encodeMessagesJSONWithFields encodes messages to JSON on w, including only the
// fields specified in the fields set. When fields is nil, all fields are included.
func encodeMessagesJSONWithFields(allMessages []store.MessageRecord, fields map[string]bool, w io.Writer) error {
	all := fields == nil

	var out []map[string]interface{}
	for _, m := range allMessages {
		tags, antecedents := extractMessageFields(m)
		if antecedents == nil {
			antecedents = []string{}
		}

		obj := make(map[string]interface{})
		if all || fields["id"] {
			obj["id"] = m.ID
		}
		if all || fields["campfire_id"] {
			obj["campfire_id"] = m.CampfireID
		}
		if all || fields["sender"] {
			obj["sender"] = m.Sender
		}
		if all || fields["instance"] {
			if m.Instance != "" {
				obj["instance"] = m.Instance
			}
		}
		if all || fields["payload"] {
			obj["payload"] = string(m.Payload)
		}
		if all || fields["tags"] {
			if tags == nil {
				tags = []string{}
			}
			obj["tags"] = tags
		}
		if all || fields["antecedents"] {
			obj["antecedents"] = antecedents
		}
		if all || fields["timestamp"] {
			obj["timestamp"] = m.Timestamp
		}
		if all || fields["provenance"] {
			obj["provenance"] = m.Provenance
		}
		if all || fields["signature"] {
			obj["signature"] = m.Signature
		}
		out = append(out, obj)
	}
	if out == nil {
		out = []map[string]interface{}{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// printNATMessages prints messages received via long-poll to w in the same
// human-readable format as the direct-mode read path.
// campfireID is passed separately because message.Message has no CampfireID field.
func printNATMessages(campfireID string, msgs []message.Message, w io.Writer, s *store.Store) {
	cfShort := campfireID
	if len(cfShort) > 6 {
		cfShort = cfShort[:6]
	}
	for _, m := range msgs {
		senderHex := fmt.Sprintf("%x", m.Sender)
		senderShort := senderHex
		if len(senderShort) > 6 {
			senderShort = senderShort[:6]
		}
		senderDisplay := "agent:" + senderShort
		if m.Instance != "" {
			senderDisplay += " (" + m.Instance + ")"
		}
		ts := time.Unix(0, m.Timestamp).Format("2006-01-02 15:04:05")
		printSingleMessage(w, cfShort, ts, senderDisplay, m.Tags, m.Antecedents, m.Payload)
	}
}

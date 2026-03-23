// Package enrichment transforms raw campfire MessageRecords into EnrichedMessages
// ready for rendering to Teams Adaptive Cards.
// It performs no external API calls — all lookups are local.
package enrichment

import (
	"time"

	"github.com/campfire-net/campfire/bridge/state"
	"github.com/campfire-net/campfire/pkg/store"
)

// UrgencyLevel represents the computed urgency tier of a message.
type UrgencyLevel string

const (
	UrgencyLow    UrgencyLevel = "LOW"
	UrgencyMedium UrgencyLevel = "MEDIUM"
	UrgencyHigh   UrgencyLevel = "HIGH"
)

// EnrichedMessage holds all display-ready fields derived from a campfire MessageRecord.
type EnrichedMessage struct {
	// Sender display fields (resolved from identity_registry or fallback).
	SenderName  string
	SenderRole  string
	SenderColor string

	// Message metadata.
	Instance       string
	CampfireShortID string // first 12 chars of CampfireID
	CampfireID     string
	Tags           []string
	Payload        string
	Timestamp      time.Time
	MessageID      string
	Antecedents    []string

	// Computed urgency.
	Urgency UrgencyLevel
}

// EnrichOptions carries runtime context needed by the pipeline stages.
type EnrichOptions struct {
	// UrgentCampfires is the list of campfire IDs (or prefixes) that always
	// contribute +5 to the urgency score.
	UrgentCampfires []string

	// DB is the bridge state database used for identity lookups.
	// If nil, all sender lookups fall back to the hex prefix.
	DB *state.DB
}

// Enrich runs the full enrichment pipeline on a single MessageRecord and returns
// an EnrichedMessage. It never returns an error — all fallbacks are applied inline.
func Enrich(msg store.MessageRecord, opts EnrichOptions) EnrichedMessage {
	// Stage 1: sender resolution.
	name, role, color := resolveSender(msg.Sender, opts.DB)

	// Stage 2: urgency scoring.
	urgency := scoreUrgency(msg, opts.UrgentCampfires)

	short := msg.CampfireID
	if len(short) > 12 {
		short = short[:12]
	}

	return EnrichedMessage{
		SenderName:      name,
		SenderRole:      role,
		SenderColor:     color,
		Instance:        msg.Instance,
		CampfireShortID: short,
		CampfireID:      msg.CampfireID,
		Tags:            msg.Tags,
		Payload:         string(msg.Payload),
		Timestamp:       time.Unix(msg.Timestamp, 0),
		MessageID:       msg.ID,
		Antecedents:     msg.Antecedents,
		Urgency:         urgency,
	}
}

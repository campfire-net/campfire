package enrichment

import (
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// Urgency thresholds (from design doc §Enrichment Pipeline §Stage 3).
const (
	scoreBlockerGateEscalation = 10
	scoreUrgentCampfire        = 5
	scoreConversationHeat      = 3

	thresholdHigh   = 8
	thresholdMedium = 3

	heatWindow      = 60 * time.Second
	heatMinMessages = 3
)

// scoreUrgency computes the urgency level for a message.
//
// Scoring rules (from design doc):
//   - tags contain "blocker" or "gate":  +10
//   - tags contain "escalation":         +10
//   - campfire_id is in urgentCampfires: +5
//   - antecedent_count > 3 in last 60s:  +3  (conversation heat)
//
// HIGH >= 8, MEDIUM 3-7, LOW < 3.
func scoreUrgency(msg store.MessageRecord, urgentCampfires []string) UrgencyLevel {
	score := 0

	// Tag-based scoring.
	for _, tag := range msg.Tags {
		switch strings.ToLower(tag) {
		case "blocker", "gate":
			score += scoreBlockerGateEscalation
		case "escalation":
			score += scoreBlockerGateEscalation
		}
	}

	// Urgent campfire bonus.
	if isUrgentCampfire(msg.CampfireID, urgentCampfires) {
		score += scoreUrgentCampfire
	}

	// Conversation heat: more than 3 antecedents within the last 60 seconds.
	if conversationIsHot(msg) {
		score += scoreConversationHeat
	}

	return urgencyFromScore(score)
}

// isUrgentCampfire returns true if campfireID matches any entry in the list
// (exact match or prefix match).
func isUrgentCampfire(campfireID string, urgentCampfires []string) bool {
	for _, u := range urgentCampfires {
		if campfireID == u || strings.HasPrefix(campfireID, u) {
			return true
		}
	}
	return false
}

// conversationIsHot returns true when the message has more than 3 antecedents
// and the message timestamp falls within 60 seconds of the current time,
// indicating an active conversation thread.
//
// Note: the full heat calculation (counting antecedents in the last 60s across
// all messages in the campfire) would require a store query and is out of scope
// for the pure-transformation pipeline. We approximate with the antecedent count
// on the message itself and a recency window on the message timestamp.
func conversationIsHot(msg store.MessageRecord) bool {
	if len(msg.Antecedents) <= heatMinMessages {
		return false
	}
	msgTime := time.Unix(msg.Timestamp, 0)
	return time.Since(msgTime) <= heatWindow
}

func urgencyFromScore(score int) UrgencyLevel {
	switch {
	case score >= thresholdHigh:
		return UrgencyHigh
	case score >= thresholdMedium:
		return UrgencyMedium
	default:
		return UrgencyLow
	}
}

// Package cards builds Adaptive Card JSON (schema 1.4) from enriched campfire messages.
package cards

import (
	"fmt"
	"time"
)

// EnrichedMessage carries all display-relevant fields resolved from a raw campfire message.
type EnrichedMessage struct {
	SenderName     string    // display name from identity registry
	SenderRole     string    // role from identity registry
	SenderColor    string    // color for sender pill (Adaptive Card color name)
	Instance       string    // e.g. "ceo", "strategist"
	CampfireShortID string   // first 6 hex chars of campfire ID
	Tags           []string  // message tags
	Payload        string    // UTF-8 message body
	Timestamp      time.Time // message timestamp
	MessageID      string    // full message ID (for gate callback data)
	CampfireID     string    // full campfire ID (for gate callback data)
}

// BuildCard constructs a valid Adaptive Card 1.4 map for the given enriched message.
// The returned map can be JSON-marshaled as the "content" of an Adaptive Card attachment.
func BuildCard(msg EnrichedMessage) map[string]any {
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    buildBody(msg),
	}

	actions := buildActions(msg)
	if len(actions) > 0 {
		card["actions"] = actions
	}

	return card
}

// buildBody assembles the card body elements.
func buildBody(msg EnrichedMessage) []any {
	var body []any

	// Header row: sender pill | instance · short id | relative time
	header := buildHeader(msg)
	body = append(body, header)

	// Tag badges row (only when there are tags)
	if len(msg.Tags) > 0 {
		badges := buildTagBadges(msg.Tags)
		if badges != nil {
			body = append(body, badges)
		}
	}

	// Payload block — may be wrapped in a container for status collapse
	payloadBlock := buildPayloadBlock(msg)
	body = append(body, payloadBlock...)

	return body
}

// buildHeader returns the three-column header ColumnSet.
func buildHeader(msg EnrichedMessage) map[string]any {
	senderColor := msg.SenderColor
	if senderColor == "" {
		senderColor = "Default"
	}

	senderText := msg.SenderName
	if msg.SenderRole != "" {
		senderText = fmt.Sprintf("%s (%s)", msg.SenderName, msg.SenderRole)
	}

	instanceText := msg.Instance
	if msg.CampfireShortID != "" {
		instanceText = fmt.Sprintf("%s · %s", msg.Instance, msg.CampfireShortID)
	}

	colSender := map[string]any{
		"type":  "Column",
		"width": "auto",
		"items": []any{
			map[string]any{
				"type":   "TextBlock",
				"text":   senderText,
				"color":  senderColor,
				"weight": "Bolder",
				"size":   "Small",
			},
		},
	}
	colInstance := map[string]any{
		"type":  "Column",
		"width": "stretch",
		"items": []any{
			map[string]any{
				"type":     "TextBlock",
				"text":     instanceText,
				"color":    "Default",
				"size":     "Small",
				"isSubtle": true,
			},
		},
	}
	colTime := map[string]any{
		"type":  "Column",
		"width": "auto",
		"items": []any{
			map[string]any{
				"type":     "TextBlock",
				"text":     relativeTime(msg.Timestamp),
				"size":     "Small",
				"isSubtle": true,
			},
		},
	}
	return map[string]any{
		"type":    "ColumnSet",
		"columns": []any{colSender, colInstance, colTime},
	}
}

// buildTagBadges renders all tags as a row of badge TextBlocks in a ColumnSet.
// Returns nil when there are no tags.
func buildTagBadges(tags []string) map[string]any {
	if len(tags) == 0 {
		return nil
	}

	columns := make([]any, 0, len(tags))
	for _, tag := range tags {
		icon, color := tagBadgeStyle(tag)
		label := icon + tag
		columns = append(columns, map[string]any{
			"type":  "Column",
			"width": "auto",
			"items": []any{
				map[string]any{
					"type":   "TextBlock",
					"text":   label,
					"color":  color,
					"size":   "Small",
					"weight": "Bolder",
				},
			},
		})
	}

	return map[string]any{
		"type":    "ColumnSet",
		"columns": columns,
	}
}

// buildPayloadBlock returns the body elements for the message payload.
// For status tags the payload is wrapped in a collapsible container.
func buildPayloadBlock(msg EnrichedMessage) []any {
	isStatus := hasTag(msg.Tags, "status")
	isSchemaChange := hasTag(msg.Tags, "schema-change")
	isFinding := hasTag(msg.Tags, "finding")
	isBlocker := hasTag(msg.Tags, "blocker")
	isDirective := hasTag(msg.Tags, "directive")
	isTestFlaky := hasTag(msg.Tags, "test-flaky")

	payloadText := truncatePayload(msg.Payload)

	// Directive: bold header style
	payloadWeight := "Default"
	if isDirective {
		payloadWeight = "Bolder"
	}

	_ = isTestFlaky // test-flaky just adds a badge; payload is rendered normally

	basePayload := map[string]any{
		"type":   "TextBlock",
		"text":   payloadText,
		"wrap":   true,
		"weight": payloadWeight,
	}

	// schema-change: monospace via fontType
	if isSchemaChange {
		basePayload["fontType"] = "Monospace"
	}

	// blocker: @mention prominence via warning color
	if isBlocker {
		basePayload["color"] = "Attention"
	}

	// status: collapsed by default
	if isStatus {
		toggleID := "statusBody"
		container := map[string]any{
			"type":      "Container",
			"id":        toggleID,
			"isVisible": false,
			"items":     []any{basePayload},
		}
		toggleAction := map[string]any{
			"type":  "ActionSet",
			"items": []any{},
			// Toggle visibility button rendered inline
		}
		_ = toggleAction
		// Return both the toggle button (ActionSet in body) and the collapsible container
		toggleBtn := map[string]any{
			"type": "ActionSet",
			"actions": []any{
				map[string]any{
					"type":  "Action.ToggleVisibility",
					"title": "Show / Hide",
					"targetElements": []any{
						map[string]any{"elementId": toggleID},
					},
				},
			},
		}
		return []any{toggleBtn, container}
	}

	// finding: expandable detail block
	if isFinding {
		detailID := "findingDetail"
		container := map[string]any{
			"type":      "Container",
			"id":        detailID,
			"isVisible": false,
			"items":     []any{basePayload},
		}
		expandBtn := map[string]any{
			"type": "ActionSet",
			"actions": []any{
				map[string]any{
					"type":  "Action.ToggleVisibility",
					"title": "Expand Finding",
					"targetElements": []any{
						map[string]any{"elementId": detailID},
					},
				},
			},
		}
		return []any{expandBtn, container}
	}

	return []any{basePayload}
}

// buildActions returns top-level card actions (gate buttons).
func buildActions(msg EnrichedMessage) []any {
	if !hasTag(msg.Tags, "gate") {
		return nil
	}

	return []any{
		map[string]any{
			"type":  "Action.Submit",
			"title": "Approve",
			"style": "positive",
			"data": map[string]any{
				"action":      "gate-approve",
				"campfire_id": msg.CampfireID,
				"gate_msg_id": msg.MessageID,
			},
		},
		map[string]any{
			"type":  "Action.Submit",
			"title": "Reject",
			"style": "destructive",
			"data": map[string]any{
				"action":      "gate-reject",
				"campfire_id": msg.CampfireID,
				"gate_msg_id": msg.MessageID,
			},
		},
	}
}

// relativeTime formats a timestamp as a short relative string.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// truncatePayload cuts payload to ~20KB to keep cards under the 28KB Teams limit.
func truncatePayload(payload string) string {
	const maxBytes = 20 * 1024
	if len(payload) <= maxBytes {
		return payload
	}
	return payload[:maxBytes] + "\n\n[truncated — full message in campfire]"
}

// hasTag reports whether tag is present in tags.
func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

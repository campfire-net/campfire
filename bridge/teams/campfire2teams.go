// Package teams implements the campfire→Teams message flow.
package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/campfire-net/campfire/bridge/teams/botframework"
	"github.com/campfire-net/campfire/bridge/teams/cards"
	"github.com/campfire-net/campfire/bridge/enrichment"
	"github.com/campfire-net/campfire/bridge/state"
	"github.com/campfire-net/campfire/pkg/store"
)

// teamsPayload is the JSON body for a Teams incoming webhook POST.
type teamsPayload struct {
	Text string `json:"text"`
}

// FormatMessage formats a campfire MessageRecord as plain text for Teams.
// Format: "[sender_hex_8chars] (instance) tag1,tag2: payload"
// If instance is empty, the "(instance)" part is omitted.
// If there are no tags, the "tag1,tag2: " prefix on payload is omitted.
//
// NOTE: msg.Instance is tainted (sender-asserted, not verified). It is shown
// here as a display label only — not as a verified identity claim.
func FormatMessage(msg store.MessageRecord) string {
	// Truncate sender to 8 hex chars.
	sender := msg.Sender
	if len(sender) > 8 {
		sender = sender[:8]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s]", sender)

	if msg.Instance != "" {
		fmt.Fprintf(&sb, " (%s)", msg.Instance)
	}

	if len(msg.Tags) > 0 {
		fmt.Fprintf(&sb, " %s:", strings.Join(msg.Tags, ","))
	}

	payload := strings.TrimSpace(string(msg.Payload))
	if payload != "" {
		sb.WriteString(" ")
		sb.WriteString(payload)
	}

	return sb.String()
}

// WebhookHandler returns a poller.MessageHandler that posts messages to a
// Teams incoming webhook URL.
func WebhookHandler(webhookURL string, client *http.Client) func(msg store.MessageRecord) error {
	if client == nil {
		client = http.DefaultClient
	}
	return func(msg store.MessageRecord) error {
		text := FormatMessage(msg)
		body, err := json.Marshal(teamsPayload{Text: text})
		if err != nil {
			return fmt.Errorf("marshal teams payload: %w", err)
		}

		resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("post to teams webhook: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("teams webhook returned status %d", resp.StatusCode)
		}

		return nil
	}
}

// BotHandler handles campfire→Teams delivery via the Bot Framework REST API.
// It enriches each message, builds an Adaptive Card, resolves threading from
// message_map, and delivers via the BF client using stored conversation refs.
type BotHandler struct {
	enrichOpts enrichment.EnrichOptions
	bfClient   *botframework.Client
	bridgeDB   *state.DB
	campfireID string
}

// NewBotHandler creates a BotHandler for a single campfire.
func NewBotHandler(
	campfireID string,
	enrichOpts enrichment.EnrichOptions,
	bfClient *botframework.Client,
	bridgeDB *state.DB,
) *BotHandler {
	return &BotHandler{
		enrichOpts: enrichOpts,
		bfClient:   bfClient,
		bridgeDB:   bridgeDB,
		campfireID: campfireID,
	}
}

// Handle is a poller.MessageHandler that delivers one campfire message to Teams.
func (h *BotHandler) Handle(msg store.MessageRecord) error {
	ctx := context.Background()

	// 1. Enrich.
	enriched := enrichment.Enrich(msg, h.enrichOpts)

	// 2. Build Adaptive Card.
	cardContent := cards.BuildCard(enrichmentToCard(enriched))
	cardJSON, err := json.Marshal(cardContent)
	if err != nil {
		return fmt.Errorf("marshalling card: %w", err)
	}

	// 3. Look up conversation ref.
	ref, err := h.bridgeDB.GetConversationRef(h.campfireID)
	if err != nil {
		return fmt.Errorf("getting conversation ref: %w", err)
	}
	if ref == nil {
		log.Printf("bothandler: no conversation_ref for campfire %s — skipping (bot must receive a message first)", h.campfireID)
		return nil
	}

	// 4. Build the activity.
	activity := &botframework.Activity{
		Type: botframework.ActivityTypeMessage,
		Attachments: []botframework.Attachment{
			{
				ContentType: "application/vnd.microsoft.card.adaptive",
				Content:     json.RawMessage(cardJSON),
			},
		},
	}
	if enriched.Urgency == enrichment.UrgencyHigh {
		activity.Importance = "urgent"
	}

	// 5. Resolve threading: if antecedents[0] is in message_map, reply to it.
	var teamsActivityID string
	if len(msg.Antecedents) > 0 {
		parentTeamsID, _, lookupErr := h.bridgeDB.LookupTeamsActivity(msg.Antecedents[0])
		if lookupErr != nil {
			log.Printf("bothandler: lookup antecedent %s: %v — posting top-level", msg.Antecedents[0], lookupErr)
		} else if parentTeamsID != "" {
			// Reply into the thread.
			resp, sendErr := h.bfClient.ReplyToActivity(ctx, ref.ServiceURL, ref.TeamsConvID, parentTeamsID, activity)
			if sendErr != nil {
				return fmt.Errorf("reply to activity: %w", sendErr)
			}
			teamsActivityID = resp.ID
		}
	}

	// 6. If not a reply (no antecedent or antecedent not in map), post top-level.
	if teamsActivityID == "" {
		resp, sendErr := h.bfClient.SendActivity(ctx, ref.ServiceURL, ref.TeamsConvID, activity)
		if sendErr != nil {
			return fmt.Errorf("send activity: %w", sendErr)
		}
		teamsActivityID = resp.ID
	}

	// 7. Store message_map entry (campfire msg ID → Teams activity ID).
	// Do NOT return an error here. The Teams message has already been delivered;
	// returning an error would prevent the cursor from advancing, causing the
	// poller to retry and post a duplicate Teams message on the next tick.
	// A MapMessage failure degrades threading (future replies land top-level)
	// but does not cause duplicate delivery. Log it prominently instead.
	if err := h.bridgeDB.MapMessage(msg.ID, teamsActivityID, ref.TeamsConvID, h.campfireID); err != nil {
		log.Printf("bothandler: WARNING: failed to store message_map for %s→%s: %v (threading will be degraded for replies to this message)", msg.ID, teamsActivityID, err)
	}

	return nil
}

// enrichmentToCard converts an enrichment.EnrichedMessage to the cards.EnrichedMessage
// type consumed by the cards builder.
func enrichmentToCard(e enrichment.EnrichedMessage) cards.EnrichedMessage {
	return cards.EnrichedMessage{
		SenderName:      e.SenderName,
		SenderRole:      e.SenderRole,
		SenderColor:     e.SenderColor,
		Instance:        e.Instance,
		CampfireShortID: e.CampfireShortID,
		Tags:            e.Tags,
		Payload:         e.Payload,
		Timestamp:       e.Timestamp,
		MessageID:       e.MessageID,
		CampfireID:      e.CampfireID,
	}
}

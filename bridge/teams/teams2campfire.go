// Package teams implements message flows between Teams and campfire.
package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/campfire-net/campfire/bridge/teams/botframework"
	"github.com/campfire-net/campfire/bridge/state"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// ErrUnauthorized is returned when a Teams user is not in the ACL.
var ErrUnauthorized = errors.New("unauthorized: user not in ACL")

// ErrRateLimited is returned when a user exceeds the rate limit.
var ErrRateLimited = errors.New("rate limited: too many messages")

// ErrDuplicate is returned when an activity has already been processed.
var ErrDuplicate = errors.New("duplicate: activity already processed")

// rateLimitMax is the maximum number of messages per user per window.
const rateLimitMax = 10

// rateLimitWindow is the sliding window duration for rate limiting.
const rateLimitWindow = time.Minute

// atTagRe matches <at>...</at> tags used for @mentions in Teams messages.
var atTagRe = regexp.MustCompile(`<at[^>]*>.*?</at>`)

// htmlTagRe matches any remaining HTML tags.
var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// htmlEntityRe matches common HTML entities.
var htmlEntityRe = regexp.MustCompile(`&(?:amp|lt|gt|quot|apos|nbsp);`)

var htmlEntityMap = map[string]string{
	"&amp;":  "&",
	"&lt;":   "<",
	"&gt;":   ">",
	"&quot;": `"`,
	"&apos;": "'",
	"&nbsp;": " ",
}

// StripHTML removes <at>...</at> @mention tags, other HTML tags, and decodes
// common HTML entities from a Teams message text.
func StripHTML(text string) string {
	// Remove <at>...</at> first (they may contain HTML-like content).
	text = atTagRe.ReplaceAllString(text, "")
	// Remove any remaining HTML tags.
	text = htmlTagRe.ReplaceAllString(text, "")
	// Decode HTML entities.
	text = htmlEntityRe.ReplaceAllStringFunc(text, func(entity string) string {
		if repl, ok := htmlEntityMap[entity]; ok {
			return repl
		}
		return entity
	})
	return strings.TrimSpace(text)
}

// RateLimiter is a simple in-memory sliding-window rate limiter.
type RateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

// NewRateLimiter creates a new RateLimiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{hits: make(map[string][]time.Time)}
}

// Allow returns true if the user is within the rate limit, recording the attempt.
// Returns false if the user has exceeded rateLimitMax messages in the last rateLimitWindow.
func (r *RateLimiter) Allow(userID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rateLimitWindow)

	// Prune timestamps outside the window.
	hits := r.hits[userID]
	valid := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rateLimitMax {
		r.hits[userID] = valid
		return false
	}

	r.hits[userID] = append(valid, now)
	return true
}

// InboundHandler handles incoming Teams activities and translates them to
// signed campfire messages.
type InboundHandler struct {
	ident       *identity.Identity
	cfStore     *store.Store
	bridgeDB    *state.DB
	fsTransport *fs.Transport
	validator   *botframework.Validator
	rateLimiter *RateLimiter
	bfClient    *botframework.Client // may be nil when BF credentials are absent
}

// NewInboundHandler creates an InboundHandler.
// fsTransport is optional — if nil, messages are written to the store only.
func NewInboundHandler(
	ident *identity.Identity,
	cfStore *store.Store,
	bridgeDB *state.DB,
	fsTransport *fs.Transport,
	validator *botframework.Validator,
) *InboundHandler {
	return &InboundHandler{
		ident:       ident,
		cfStore:     cfStore,
		bridgeDB:    bridgeDB,
		fsTransport: fsTransport,
		validator:   validator,
		rateLimiter: NewRateLimiter(),
	}
}

// WithBFClient attaches a Bot Framework client to the handler, enabling card
// updates on gate invoke responses.
func (h *InboundHandler) WithBFClient(client *botframework.Client) *InboundHandler {
	h.bfClient = client
	return h
}

// HandleActivity processes a raw Teams activity JSON payload.
// It validates the JWT token (from authHeader), enforces ACL, dedup, and rate
// limits, then writes a signed campfire message via the filesystem transport.
// Returns the campfire message ID on success.
//
// For invoke activities (Adaptive Card action submissions), gate-approve and
// gate-reject actions are handled specially: a signed campfire message with the
// appropriate tag is posted and the Teams card is updated to show the decision.
func (h *InboundHandler) HandleActivity(ctx context.Context, authHeader string, body []byte) (string, error) {
	// 1. Parse the activity.
	activity, err := botframework.ParseActivity(body)
	if err != nil {
		return "", fmt.Errorf("parsing activity: %w", err)
	}

	// Route invoke activities to the gate handler.
	if activity.Type == botframework.ActivityTypeInvoke {
		return h.handleGateInvoke(ctx, authHeader, activity)
	}

	// Only handle message activities beyond this point.
	if activity.Type != botframework.ActivityTypeMessage {
		return "", fmt.Errorf("unsupported activity type: %s", activity.Type)
	}

	// 2. Validate the JWT.
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if err := h.validator.ValidateToken(ctx, token); err != nil {
		return "", fmt.Errorf("JWT validation failed: %w", err)
	}

	fromID := activity.From.ID
	convID := activity.Conversation.ID
	// Strip ";messageid=..." suffix — Teams appends it for channel messages.
	if idx := strings.Index(convID, ";messageid="); idx != -1 {
		convID = convID[:idx]
	}
	activityID := activity.ID
	replyToID := activity.ReplyToID

	// 3. Resolve which campfire this conversation maps to.
	campfireID, err := h.resolveCampfire(convID)
	if err != nil {
		return "", fmt.Errorf("resolving campfire: %w", err)
	}

	// 4. ACL check.
	allowed, err := h.bridgeDB.CheckACL(fromID, campfireID)
	if err != nil {
		return "", fmt.Errorf("ACL check: %w", err)
	}
	if !allowed {
		return "", ErrUnauthorized
	}

	// 5. Dedup check.
	dup, err := h.bridgeDB.CheckDedup(activityID)
	if err != nil {
		return "", fmt.Errorf("dedup check: %w", err)
	}
	if dup {
		return "", ErrDuplicate
	}

	// 6. Rate limit.
	if !h.rateLimiter.Allow(fromID) {
		return "", ErrRateLimited
	}

	// 7. Strip HTML/@mentions from text.
	payload := []byte(StripHTML(activity.Text))

	// 8. Resolve antecedents from replyToId.
	var antecedents []string
	if replyToID != "" {
		cfMsgID, err := h.bridgeDB.LookupCampfireMsg(replyToID)
		if err != nil {
			return "", fmt.Errorf("looking up antecedent: %w", err)
		}
		if cfMsgID != "" {
			antecedents = []string{cfMsgID}
		}
	}

	// 9. Build tags.
	tags := []string{"from:teams"}

	// 10. Create the signed campfire message.
	msg, err := message.NewMessage(h.ident.PrivateKey, h.ident.PublicKey, payload, tags, antecedents)
	if err != nil {
		return "", fmt.Errorf("creating message: %w", err)
	}
	msg.Instance = "teams-bridge"

	// 11. Record dedup BEFORE writing to campfire.
	// If we write first and then crash before recording dedup, a Teams retry
	// will re-deliver the same activity and we'll write a duplicate campfire
	// message. Recording dedup first means a crash after this step is safe: the
	// activity won't be reprocessed on retry. The campfire message may be absent
	// from message_map (so threading degrades to top-level), but no duplicate
	// is written to the campfire store.
	if err := h.bridgeDB.RecordDedup(activityID, msg.ID); err != nil {
		return "", fmt.Errorf("recording dedup: %w", err)
	}

	// 12. Write to campfire — prefer store (poller reads from it), fall back to fs transport.
	if err := h.writeMessage(campfireID, msg, payload, tags, antecedents); err != nil {
		return "", err
	}

	// 13. Record message_map for future antecedent resolution.
	if err := h.bridgeDB.MapMessage(msg.ID, activityID, convID, campfireID); err != nil {
		return "", fmt.Errorf("recording message map: %w", err)
	}

	return msg.ID, nil
}

// writeMessage writes a campfire message via the store (preferred) or fs transport (fallback).
func (h *InboundHandler) writeMessage(campfireID string, msg *message.Message, payload []byte, tags, antecedents []string) error {
	if h.cfStore != nil {
		rec := store.MessageRecord{
			ID:          msg.ID,
			CampfireID:  campfireID,
			Sender:      fmt.Sprintf("%x", h.ident.PublicKey),
			Payload:     payload,
			Tags:        tags,
			Antecedents: antecedents,
			Timestamp:   msg.Timestamp,
			Signature:   msg.Signature,
			Instance:    msg.Instance,
			ReceivedAt:  msg.Timestamp,
		}
		if _, err := h.cfStore.AddMessage(rec); err == nil {
			return nil
		}
	}
	if h.fsTransport != nil {
		if err := h.fsTransport.WriteMessage(campfireID, msg); err != nil {
			return fmt.Errorf("writing message to fs transport: %w", err)
		}
		return nil
	}
	return fmt.Errorf("no write path available (store and fs transport both nil/failed)")
}

// resolveCampfire looks up the campfire ID for a Teams conversation ID.
// It queries conversation_refs to find the mapping established when the campfire
// was first bridged to this conversation.
func (h *InboundHandler) resolveCampfire(teamsConvID string) (string, error) {
	// We look up by scanning conversation_refs for the given Teams conversation.
	// The DB exposes GetConversationRef keyed by campfireID, so we use the
	// reverse lookup from message_map as a shortcut: if any message in this
	// conversation has been bridged, the campfire ID is in the map.
	// For new conversations (no messages yet), the conversation must have been
	// seeded by a campfire2teams flow which calls UpsertConversationRef.
	campfireID, err := h.bridgeDB.GetCampfireForConversation(teamsConvID)
	if err != nil {
		return "", err
	}
	if campfireID == "" {
		return "", fmt.Errorf("no campfire mapped to teams conversation %q", teamsConvID)
	}
	return campfireID, nil
}

// gateActionData is the payload embedded in Adaptive Card gate button actions.
type gateActionData struct {
	Action    string `json:"action"`     // "gate-approve" or "gate-reject"
	CampfireID string `json:"campfire_id"`
	GateMsgID string `json:"gate_msg_id"`
}

// ErrUnknownGateAction is returned for invoke payloads with unrecognised action values.
var ErrUnknownGateAction = errors.New("unknown gate action")

// handleGateInvoke processes an Adaptive Card invoke activity originating from
// an Approve or Reject button on a gate-tagged campfire message card.
//
// Flow:
//  1. Validate the JWT bearer token.
//  2. Parse the gate action data from activity.Value.
//  3. Post a signed campfire message with tag "gate-approved" or "gate-rejected"
//     and antecedent pointing to the original gate message.
//  4. Update the Teams card to show the decision (if a BF client is available).
func (h *InboundHandler) handleGateInvoke(ctx context.Context, authHeader string, activity *botframework.Activity) (string, error) {
	// 1. Validate the JWT.
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if err := h.validator.ValidateToken(ctx, token); err != nil {
		return "", fmt.Errorf("JWT validation failed: %w", err)
	}

	// 2. Parse gate action data.
	var data gateActionData
	if err := json.Unmarshal(activity.Value, &data); err != nil {
		return "", fmt.Errorf("parsing gate action data: %w", err)
	}

	var resultTag string
	switch data.Action {
	case "gate-approve":
		resultTag = "gate-approved"
	case "gate-reject":
		resultTag = "gate-rejected"
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownGateAction, data.Action)
	}

	// 3. Resolve the authoritative campfire ID from the DB using the gate message
	// ID. We do NOT trust data.CampfireID from the card payload — it is
	// attacker-controlled. The message_map entry was written by the bridge when
	// the original gate card was delivered, so it is the ground truth.
	_, trustedConvID, err := h.bridgeDB.LookupTeamsActivity(data.GateMsgID)
	if err != nil {
		return "", fmt.Errorf("resolving gate message campfire: %w", err)
	}
	if trustedConvID == "" {
		return "", fmt.Errorf("gate message %q not found in message map", data.GateMsgID)
	}
	trustedCampfireID, err := h.bridgeDB.GetCampfireForConversation(trustedConvID)
	if err != nil {
		return "", fmt.Errorf("resolving campfire for gate invoke: %w", err)
	}
	if trustedCampfireID == "" {
		return "", fmt.Errorf("no campfire mapped to conversation %q", trustedConvID)
	}

	// 4. ACL check — gate actions require write permission on the resolved campfire.
	fromID := activity.From.ID
	allowed, err := h.bridgeDB.CheckACL(fromID, trustedCampfireID)
	if err != nil {
		return "", fmt.Errorf("ACL check for gate invoke: %w", err)
	}
	if !allowed {
		return "", ErrUnauthorized
	}

	// 5. Rate limit.
	if !h.rateLimiter.Allow(fromID) {
		return "", ErrRateLimited
	}

	// 6. Build the campfire message text: who acted.
	actor := activity.From.Name
	if actor == "" {
		actor = fromID
	}
	payload := fmt.Sprintf("%s by %s", resultTag, actor)

	tags := []string{resultTag}
	antecedents := []string{data.GateMsgID}

	msg, err := message.NewMessage(h.ident.PrivateKey, h.ident.PublicKey, []byte(payload), tags, antecedents)
	if err != nil {
		return "", fmt.Errorf("creating gate response message: %w", err)
	}
	msg.Instance = "teams-bridge"

	if err := h.writeMessage(trustedCampfireID, msg, []byte(payload), tags, antecedents); err != nil {
		return "", err
	}

	// 7. Update the original Teams card to reflect the decision.
	if h.bfClient != nil {
		// We already resolved trustedConvID above (step 3); look up the Teams
		// activity ID for the original gate message using the same gate_msg_id.
		origTeamsID, convID, lookupErr := h.bridgeDB.LookupTeamsActivity(data.GateMsgID)
		if lookupErr != nil {
			// Non-fatal — card update is cosmetic. Log and continue.
			log.Printf("bothandler: WARNING: lookup Teams activity for gate msg %s: %v — card will not be updated", data.GateMsgID, lookupErr)
		} else if origTeamsID != "" && convID != "" {
			serviceURL := activity.ServiceURL
			decisonText := fmt.Sprintf("**%s** by %s", resultTag, actor)
			updatedCard := map[string]any{
				"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
				"type":    "AdaptiveCard",
				"version": "1.4",
				"body": []any{
					map[string]any{
						"type": "TextBlock",
						"text": decisonText,
						"wrap": true,
					},
				},
			}
			cardJSON, marshalErr := json.Marshal(updatedCard)
			if marshalErr != nil {
				log.Printf("bothandler: WARNING: failed to marshal gate decision card: %v — skipping card update", marshalErr)
			} else {
				updateActivity := &botframework.Activity{
					Type: botframework.ActivityTypeMessage,
					Attachments: []botframework.Attachment{
						{
							ContentType: "application/vnd.microsoft.card.adaptive",
							Content:     json.RawMessage(cardJSON),
						},
					},
				}
				_, _ = h.bfClient.UpdateActivity(ctx, serviceURL, convID, origTeamsID, updateActivity)
			}
		}
	}

	return msg.ID, nil
}

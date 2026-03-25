// cmd/cf-ui/send.go — POST /c/{id}/send handler.
//
// Accepts a message text and optional tag from the compose box, stores the
// message via the MessageSender interface, notifies the SSE hub so connected
// tabs receive the new message in real time, and returns an HTML fragment that
// htmx appends to the message feed.
//
// The route is protected by SessionMiddleware (identity injected into context)
// and CSRFMiddleware (token validated before reaching this handler).
package main

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

// validSendTags lists the tag values offered by the compose box tag selector.
// This is the canonical set — the handler rejects any tag not in this list.
var validSendTags = []string{
	"status",
	"finding",
	"blocker",
	"schema-change",
	"decision",
	"escalation",
}

// MessageSender is the interface the send handler uses to deliver a message
// to a campfire. The production implementation calls the campfire store.
// Tests inject a fake.
type MessageSender interface {
	// Send stores a message in the given campfire and returns the assigned
	// message ID. Returns a non-nil error if delivery fails.
	Send(campfireID, senderEmail, text string, tags []string) (msgID string, err error)
}

// noopMessageSender is the default MessageSender used when no real store is
// wired. It accepts all messages and returns a synthetic ID.
type noopMessageSender struct{}

func (noopMessageSender) Send(_, _, _ string, _ []string) (string, error) {
	return fmt.Sprintf("msg-%d", time.Now().UnixNano()), nil
}

// SentMessage holds the data passed to the message fragment template.
type SentMessage struct {
	ID         string
	SenderName string
	Text       string
	Tag        string
	SentAt     string
}

// msgFragmentTmpl is the HTML fragment returned after a successful send.
// htmx appends it to #message-feed.
var msgFragmentTmpl = template.Must(template.New("msg-fragment").Parse(`<div class="message-card new" id="msg-{{.ID}}">
  <div class="message-meta">
    <span class="member-badge">{{.SenderName}}</span>
    {{if .Tag}}<span class="tag-chip" data-tag="{{.Tag}}">{{.Tag}}</span>{{end}}
    <span class="message-time">{{.SentAt}}</span>
  </div>
  <div class="message-body">{{.Text}}</div>
</div>`))

// handleSend returns the POST /c/{id}/send handler.
// logger, sender, hub, and membership are injected at construction time.
// membership may be nil (open/dev mode — no membership check performed).
func handleSend(logger interface{ Error(string, ...any) }, sender MessageSender, hub *SSEHub, membership MembershipChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		campfireID := r.PathValue("id")

		// Identity is injected by SessionMiddleware.
		identity, ok := IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}

		// BOLA/IDOR guard: only campfire members may post messages.
		if membership != nil && !membership.IsMember(campfireID, identity.Email) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// Form is already parsed by CSRFMiddleware.
		text := strings.TrimSpace(r.FormValue("message"))
		if text == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `<span class="send-error text-ember">Message cannot be empty.</span>`)
			return
		}

		// Validate tag (empty = no tag, which is fine).
		tag := r.FormValue("tag")
		if tag != "" && !isValidTag(tag) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `<span class="send-error text-ember">Invalid tag.</span>`)
			return
		}

		var tags []string
		if tag != "" {
			tags = []string{tag}
		}

		msgID, err := sender.Send(campfireID, identity.Email, text, tags)
		if err != nil {
			logger.Error("send: message delivery failed", "campfire", campfireID, "err", err)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `<span class="send-error text-ember">Failed to send message. Please try again.</span>`)
			return
		}

		// Notify SSE hub so other connected tabs receive the new message.
		hub.Publish(campfireID, SSEEvent{
			Type: SSEEventMessage,
			Data: map[string]any{
				"id":     msgID,
				"sender": identity.Email,
				"text":   text,
				"tag":    tag,
			},
		})

		senderName := identity.DisplayName
		if senderName == "" {
			senderName = identity.Email
		}

		msg := SentMessage{
			ID:         msgID,
			SenderName: senderName,
			Text:       text,
			Tag:        tag,
			SentAt:     time.Now().UTC().Format("15:04"),
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if err := msgFragmentTmpl.Execute(w, msg); err != nil {
			logger.Error("send: fragment template error", "err", err)
		}
	}
}

// isValidTag reports whether tag is in the allowed tag list.
func isValidTag(tag string) bool {
	for _, v := range validSendTags {
		if v == tag {
			return true
		}
	}
	return false
}

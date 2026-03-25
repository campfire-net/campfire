// cmd/cf-ui/sse.go — Server-Sent Events (SSE) hub and /events handler.
//
// The SSEHub multiplexes campfire events across all of an operator's campfires
// into a single SSE stream per browser connection. Each connection:
//   - Is authenticated via session cookie (validated on open + re-checked every 60s).
//   - Counts against a per-operator budget (max 3 concurrent SSE connections).
//   - Receives keepalive SSE comments every 30 seconds to prevent ACA idle-timeout.
//   - Is cleaned up gracefully when the server shuts down.
//
// Other handlers call hub.Publish(campfireID, event) to push events to all
// connections belonging to operators who are members of that campfire.
// (Campfire membership lookup is a future item — for now, Publish fans out
// by operator email to support wiring by campfire-agent-cbz / campfire-agent-wp4.)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	// sseMaxConnsPerOperator is the maximum number of concurrent SSE connections
	// per operator (by email). The 3rd connection is the last accepted; the 4th
	// returns 429 Too Many Requests.
	sseMaxConnsPerOperator = 3

	// sseKeepaliveInterval is the period between SSE keepalive comments.
	// Must be less than ACA's idle-disconnect timeout (~240s).
	sseKeepaliveInterval = 30 * time.Second

	// sseSessionRecheckInterval is how often the hub re-validates the session
	// while a connection is open. If the session has expired, the connection
	// is closed.
	sseSessionRecheckInterval = 60 * time.Second
)

// SSEEventType is a valid SSE event name for the /events stream.
type SSEEventType string

const (
	SSEEventMessage  SSEEventType = "message"
	SSEEventUnread   SSEEventType = "unread"
	SSEEventPresence SSEEventType = "presence"
	SSEEventFuture   SSEEventType = "future"
	SSEEventSystem   SSEEventType = "system"
)

// SSEEvent is a single event to be published to connected clients.
type SSEEvent struct {
	// Type is the SSE event name (e.g. "message", "unread").
	Type SSEEventType
	// CampfireID is the campfire that generated the event. Required.
	CampfireID string
	// Data is arbitrary event-specific fields. Must be JSON-serialisable.
	// CampfireID is automatically merged into the JSON payload.
	Data map[string]any
}

// sseConn is one open SSE connection.
type sseConn struct {
	// ch receives events to write to the client.
	ch chan SSEEvent
	// operatorEmail identifies which operator owns this connection.
	operatorEmail string
	// done is closed when the connection should be torn down.
	done chan struct{}
}

// SSEHub manages all open SSE connections and fans out published events.
type SSEHub struct {
	mu sync.Mutex
	// conns maps operatorEmail → list of open connections.
	conns map[string][]*sseConn

	// sessions is used for periodic session re-validation.
	sessions SessionStore

	// logger is the server logger.
	logger *slog.Logger

	// keepaliveInterval and sessionRecheckInterval are injectable for tests.
	keepaliveInterval      time.Duration
	sessionRecheckInterval time.Duration
}

// NewSSEHub creates an SSEHub.
func NewSSEHub(sessions SessionStore, logger *slog.Logger) *SSEHub {
	return &SSEHub{
		conns:                  make(map[string][]*sseConn),
		sessions:               sessions,
		logger:                 logger,
		keepaliveInterval:      sseKeepaliveInterval,
		sessionRecheckInterval: sseSessionRecheckInterval,
	}
}

// register adds conn to the hub for the given operator.
// It returns false (and does NOT add the connection) if the budget is exceeded.
func (h *SSEHub) register(conn *sseConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	email := conn.operatorEmail
	if len(h.conns[email]) >= sseMaxConnsPerOperator {
		return false
	}
	h.conns[email] = append(h.conns[email], conn)
	return true
}

// unregister removes conn from the hub.
func (h *SSEHub) unregister(conn *sseConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	email := conn.operatorEmail
	list := h.conns[email]
	for i, c := range list {
		if c == conn {
			h.conns[email] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(h.conns[email]) == 0 {
		delete(h.conns, email)
	}
}

// Publish sends event to all connections belonging to operatorEmail.
// If operatorEmail is empty, the event is broadcast to ALL connected operators.
// (Campfire membership scoping is a future item.)
func (h *SSEHub) Publish(campfireID string, event SSEEvent) {
	event.CampfireID = campfireID

	h.mu.Lock()
	// Collect target channels outside the lock to avoid holding it during sends.
	var targets []chan SSEEvent
	for _, conns := range h.conns {
		for _, c := range conns {
			targets = append(targets, c.ch)
		}
	}
	h.mu.Unlock()

	for _, ch := range targets {
		select {
		case ch <- event:
		default:
			// Drop if the client is slow — do not block the publisher.
		}
	}
}

// PublishToOperator sends event to all connections belonging to operatorEmail.
func (h *SSEHub) PublishToOperator(operatorEmail, campfireID string, event SSEEvent) {
	event.CampfireID = campfireID

	h.mu.Lock()
	var targets []chan SSEEvent
	for _, c := range h.conns[operatorEmail] {
		targets = append(targets, c.ch)
	}
	h.mu.Unlock()

	for _, ch := range targets {
		select {
		case ch <- event:
		default:
		}
	}
}

// Shutdown closes all active SSE connections gracefully.
// It signals each connection's done channel so the fanout goroutine exits.
func (h *SSEHub) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, conns := range h.conns {
		for _, c := range conns {
			select {
			case <-c.done:
				// already closed
			default:
				close(c.done)
			}
		}
	}
}

// handleEvents is the HTTP handler for GET /events.
// It upgrades the connection to an SSE stream, registers with the hub,
// and fans events out until the client disconnects, session expires,
// or the server shuts down.
func (h *SSEHub) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Identity is injected by SessionMiddleware.
	identity, ok := IdentityFromContext(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	// Verify the response writer supports flushing (required for SSE).
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Register connection (budget check).
	conn := &sseConn{
		ch:            make(chan SSEEvent, 16),
		operatorEmail: identity.Email,
		done:          make(chan struct{}),
	}
	if !h.register(conn) {
		http.Error(w, "too many concurrent SSE connections", http.StatusTooManyRequests)
		return
	}
	defer h.unregister(conn)

	// Set SSE headers before writing anything.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	w.WriteHeader(http.StatusOK)

	// Send an initial system event so the client knows the stream is live.
	writeSSEEvent(w, SSEEvent{
		Type:       SSEEventSystem,
		CampfireID: "",
		Data:       map[string]any{"status": "connected", "operator": identity.Email},
	})
	flusher.Flush()

	// Timers for keepalive and session re-validation.
	keepalive := time.NewTicker(h.keepaliveInterval)
	recheck := time.NewTicker(h.sessionRecheckInterval)
	defer keepalive.Stop()
	defer recheck.Stop()

	// clientGone fires when the HTTP client disconnects.
	clientGone := r.Context().Done()

	for {
		select {
		case <-clientGone:
			// Client disconnected — exit cleanly.
			return

		case <-conn.done:
			// Hub shutdown — close the stream.
			writeSSEComment(w, "shutdown")
			flusher.Flush()
			return

		case <-keepalive.C:
			writeSSEComment(w, "keepalive")
			flusher.Flush()

		case <-recheck.C:
			// Re-validate the session via the original request cookie.
			// If the session has expired, close the stream.
			sessionToken := sessionTokenFromRequest(r)
			if sessionToken == "" {
				h.logger.Info("sse: session token gone, closing connection", "operator", identity.Email)
				writeSSEComment(w, "session-expired")
				flusher.Flush()
				return
			}
			if _, valid := h.sessions.Lookup(sessionToken); !valid {
				h.logger.Info("sse: session expired mid-stream, closing connection", "operator", identity.Email)
				writeSSEComment(w, "session-expired")
				flusher.Flush()
				return
			}

		case event := <-conn.ch:
			if err := writeSSEEvent(w, event); err != nil {
				h.logger.Debug("sse: write error, closing connection", "operator", identity.Email, "err", err)
				return
			}
			flusher.Flush()
		}
	}
}

// sessionTokenFromRequest extracts the session cookie value from the request.
func sessionTokenFromRequest(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// writeSSEEvent serialises and writes a single SSE event to w.
// The payload is the event.Data map merged with campfire_id.
func writeSSEEvent(w http.ResponseWriter, event SSEEvent) error {
	payload := make(map[string]any, len(event.Data)+1)
	for k, v := range event.Data {
		payload[k] = v
	}
	if event.CampfireID != "" {
		payload["campfire_id"] = event.CampfireID
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sse: marshal event: %w", err)
	}

	if event.Type != "" {
		fmt.Fprintf(w, "event: %s\n", event.Type)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	return nil
}

// writeSSEComment writes an SSE comment line (used for keepalives).
func writeSSEComment(w http.ResponseWriter, text string) {
	fmt.Fprintf(w, ":%s\n\n", text)
}

// handleEventsHandler wraps hub.handleEvents as an http.HandlerFunc so it can
// be registered directly with http.ServeMux.
func handleEventsHandler(hub *SSEHub) http.HandlerFunc {
	return hub.handleEvents
}

// contextKeySSEHub is the context key used to pass the hub to handlers.
// (Not currently used — hub is passed by closure via handleEventsHandler.)
type contextKeySSEHub struct{}

// SSEHubFromContext retrieves the SSEHub from the context (if present).
func SSEHubFromContext(ctx context.Context) (*SSEHub, bool) {
	hub, ok := ctx.Value(contextKeySSEHub{}).(*SSEHub)
	return hub, ok
}

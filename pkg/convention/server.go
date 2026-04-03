package convention

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/protocol"
)

// Request holds a parsed incoming convention operation request.
type Request struct {
	// MessageID is the ID of the incoming message.
	MessageID string
	// Sender is the public key hex of the sender.
	Sender string
	// CampfireID is the campfire this message was received on.
	CampfireID string
	// Args are the parsed, typed arguments from the message payload.
	Args map[string]any
	// Tags are the raw tags on the incoming message.
	Tags []string
}

// Response holds the payload and tags to send back to the caller.
type Response struct {
	// Payload is the JSON-serializable response body. May be nil.
	Payload any
	// Tags are additional tags to include on the response message (beyond "fulfills").
	Tags []string
	// TokensConsumed is the number of LLM tokens consumed by the handler.
	// If > 0, the dispatcher records it on the dispatch record for billing.
	TokensConsumed int64
}

// HandlerFunc is the signature for convention operation handlers.
// ctx is the per-request context. req contains the parsed request.
// Return a non-nil Response to send a reply threaded back to the request message.
// Return (nil, nil) to silently skip (no response sent).
// Return a non-nil error to surface a processing failure; the error is logged but
// no automatic error response is sent to the campfire.
type HandlerFunc func(ctx context.Context, req *Request) (*Response, error)

// Server dispatches incoming convention operations to registered handlers and
// sends auto-threaded responses back to the campfire.
//
// Usage:
//
//	srv := convention.NewServer(client, decl)
//	srv.RegisterHandler("post", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
//	    // process req.Args["text"], etc.
//	    return &convention.Response{Payload: map[string]any{"ok": true}}, nil
//	})
//	srv.Serve(ctx, campfireID)
// Server intentionally does not access the underlying store or identity
// directly. All operations go through protocol.Client's public API
// (Send, Subscribe) — Store and Identity are unexported on Client (SDK 0.12).
type Server struct {
	client   *protocol.Client
	decl     *Declaration
	handlers map[string]HandlerFunc

	// pollInterval controls how often Serve polls for new messages.
	// Defaults to 2 seconds.
	pollInterval time.Duration

	// errFn is called when a handler or send returns an error.
	// Defaults to discarding errors.
	errFn func(err error)
}

// NewServer creates a Server for the given convention Declaration.
// All message operations use client.
func NewServer(client *protocol.Client, decl *Declaration) *Server {
	return &Server{
		client:       client,
		decl:         decl,
		handlers:     make(map[string]HandlerFunc),
		pollInterval: 2 * time.Second,
		errFn:        func(err error) {},
	}
}

// WithPollInterval sets the polling interval for Serve.
// The default is 2 seconds.
func (s *Server) WithPollInterval(d time.Duration) *Server {
	s.pollInterval = d
	return s
}

// WithErrorHandler sets a callback that is invoked whenever a handler or
// response-send returns an error. Use this for logging.
func (s *Server) WithErrorHandler(fn func(err error)) *Server {
	s.errFn = fn
	return s
}

// RegisterHandler registers fn as the handler for the given operation name.
// If a handler for operationName was already registered, it is replaced.
func (s *Server) RegisterHandler(operationName string, fn HandlerFunc) {
	s.handlers[operationName] = fn
}

// operationTags returns the set of tags to poll when waiting for incoming
// convention operation requests. The tags are derived from the declaration's
// ProducesTags: all static (non-glob) exactly_one entries, plus the canonical
// convention:operation tag "convention:operation". Callers send messages bearing
// these tags when executing this operation.
//
// Fallback: if no exactly_one static tag is found, poll with the convention+operation
// compound tag (convention:operation style used by the executor's antecedent resolver).
func (s *Server) operationTags() []string {
	var tags []string
	for _, rule := range s.decl.ProducesTags {
		if rule.Cardinality == "exactly_one" && !hasGlob(rule.Tag) {
			tags = append(tags, rule.Tag)
		}
	}
	if len(tags) == 0 {
		// Fall back to convention:operation compound tag.
		tags = []string{s.decl.Convention + ":" + s.decl.Operation}
	}
	return tags
}

// hasGlob reports whether a tag pattern contains a wildcard.
func hasGlob(tag string) bool {
	return len(tag) > 0 && tag[len(tag)-1] == '*'
}

// Serve subscribes to campfireID for incoming convention operation messages and
// dispatches them to registered handlers. Responses are sent auto-threaded
// (antecedent = incoming message ID, tag "fulfills").
//
// Serve blocks until ctx is cancelled. It uses client.Subscribe() internally,
// advancing the cursor via the Subscription.Messages() channel.
//
// Returns ctx.Err() when the context is cancelled.
func (s *Server) Serve(ctx context.Context, campfireID string) error {
	pollTags := s.operationTags()

	sub := s.client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   campfireID,
		Tags:         pollTags,
		PollInterval: s.pollInterval,
	})

	for msg := range sub.Messages() {
		s.dispatch(ctx, campfireID, msg)
	}

	// If the subscription ended due to a transport error, surface it.
	if err := sub.Err(); err != nil {
		s.errFn(fmt.Errorf("convention server: subscription error: %w", err))
	}

	return ctx.Err()
}

// dispatch parses one message and calls the registered handler, then sends the
// response if any.
func (s *Server) dispatch(ctx context.Context, campfireID string, msg protocol.Message) {
	handler, ok := s.handlers[s.decl.Operation]
	if !ok {
		// No handler registered for this operation — skip silently.
		return
	}

	// Parse payload into args map.
	args, err := parseMessageArgs(s.decl, msg.Payload)
	if err != nil {
		s.errFn(fmt.Errorf("convention server: parse args (msg %s): %w", msg.ID, err))
		return
	}

	req := &Request{
		MessageID:  msg.ID,
		Sender:     msg.Sender,
		CampfireID: campfireID,
		Args:       args,
		Tags:       msg.Tags,
	}

	resp, err := handler(ctx, req)
	if err != nil {
		s.errFn(fmt.Errorf("convention server: handler error (msg %s): %w", msg.ID, err))
		// Send an error fulfillment so callers using Await fail fast instead of timing out.
		if sendErr := s.sendErrorResponse(campfireID, msg.ID, err); sendErr != nil {
			s.errFn(fmt.Errorf("convention server: send error response (msg %s): %w", msg.ID, sendErr))
		}
		return
	}
	if resp == nil {
		return
	}

	// Send auto-threaded response.
	if err := s.sendResponse(campfireID, msg.ID, resp); err != nil {
		s.errFn(fmt.Errorf("convention server: send response (msg %s): %w", msg.ID, err))
	}
}

// sendResponse sends a response message threaded back to requestMsgID.
// The response always includes the "fulfills" tag and lists requestMsgID as
// an antecedent, making it discoverable via Client.Await.
func (s *Server) sendResponse(campfireID, requestMsgID string, resp *Response) error {
	var payload []byte
	if resp.Payload != nil {
		var err error
		payload, err = json.Marshal(resp.Payload)
		if err != nil {
			return fmt.Errorf("marshal response payload: %w", err)
		}
	}

	tags := append([]string{"fulfills"}, resp.Tags...)
	_, err := s.client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     payload,
		Tags:        tags,
		Antecedents: []string{requestMsgID},
	})
	return err
}

// sendErrorResponse sends an error fulfillment message threaded back to requestMsgID.
// The message is tagged with both "fulfills" and "convention:error" and carries a
// JSON payload of the form {"error": "<message>"}.
func (s *Server) sendErrorResponse(campfireID, requestMsgID string, handlerErr error) error {
	payload, err := json.Marshal(ErrorResponse{Error: handlerErr.Error()})
	if err != nil {
		return fmt.Errorf("marshal error response: %w", err)
	}
	_, err = s.client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     payload,
		Tags:        []string{"fulfills", "convention:error"},
		Antecedents: []string{requestMsgID},
	})
	return err
}

// ErrorResponse represents a convention operation that failed.
// It is the payload carried by messages tagged "convention:error".
type ErrorResponse struct {
	Error string `json:"error"`
}

// IsErrorResponse reports whether msg is a convention error response by
// checking for the "convention:error" tag.
func IsErrorResponse(msg *protocol.Message) bool {
	for _, tag := range msg.Tags {
		if tag == "convention:error" {
			return true
		}
	}
	return false
}

// ParseErrorResponse extracts the error message from a convention error response.
// Returns an error if the message is not a valid error response payload.
func ParseErrorResponse(msg *protocol.Message) (string, error) {
	var resp ErrorResponse
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		return "", fmt.Errorf("parse error response: %w", err)
	}
	return resp.Error, nil
}

// parseMessageArgs decodes the JSON payload of a message into a typed args map
// validated against the declaration's arg descriptors.
// Unknown keys in the payload are silently dropped (strict allow-listing).
func parseMessageArgs(decl *Declaration, payload []byte) (map[string]any, error) {
	if len(payload) == 0 {
		return validateArgs(decl.Args, nil)
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}
	return validateArgs(decl.Args, raw)
}

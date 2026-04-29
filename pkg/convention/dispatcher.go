package convention

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	cfprotocol "github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// ErrReservedOp is returned by RegisterTier1Handler and RegisterTier2Handler
// when the operationName is one of the ten L1-frozen reserved operations.
// It is also used as the sentinel for Dispatch to short-circuit reserved ops
// at the L2 enforcement point (campfireagent-935, protocol-spec.md §Reserved-Op Floor).
var ErrReservedOp = fmt.Errorf("reserved-op-floor: operation is a reserved L1 operation and cannot be dispatched by a convention server")

// conventionKey is the composite key used to look up dispatch registrations.
type conventionKey struct {
	CampfireID string
	Convention string
	Operation  string
}

// dispatchEntry holds the registration details for a single convention operation.
type dispatchEntry struct {
	// Tier identifies the handler type: 1 = inline Go, 2 = HTTP POST.
	Tier int

	// Handler is set for Tier 1 handlers.
	Handler HandlerFunc

	// HandlerURL is set for Tier 2 handlers.
	HandlerURL string

	// ServerID is the convention server identity (pubkey hex). Used for store
	// operations that key on (serverID, campfireID).
	ServerID string

	// ForgeAccountID is the Forge billing account for the convention server owner.
	// Used by the billing sweep and metering hook to attribute usage to the correct customer.
	ForgeAccountID string

	// Client is the protocol.Client used by the server to post fulfillment messages.
	Client *protocol.Client
}

// MeteringHook is an optional callback fired after dispatch completes (for billing).
// Set ConventionDispatcher.MeteringHook to enable.
type MeteringHook func(ctx context.Context, event ConventionMeterEvent)

// ConventionMeterEvent carries billing metadata for one dispatched convention operation.
type ConventionMeterEvent struct {
	CampfireID     string
	Convention     string
	Operation      string
	Tier           int
	ServerID       string
	ForgeAccountID string
	MessageID      string
	Status         string // "dispatched", "fulfilled", "failed"
	TokensConsumed int64
}

// ConventionDispatcher checks incoming messages for convention operation tags and
// dispatches to registered handlers. It provides deduplication via DispatchStore
// cursors. Dispatch() is non-blocking — it spawns goroutines for actual work.
type ConventionDispatcher struct {
	mu       sync.RWMutex
	registry map[conventionKey]*dispatchEntry
	store    DispatchStore
	logger   *log.Logger

	// MeteringHook is called after each dispatch attempt. Set to enable metering.
	MeteringHook MeteringHook

	// httpClient is used for Tier 2 HTTP POST dispatches. Configurable for testing.
	httpClient *http.Client
}

// NewConventionDispatcher creates a dispatcher with the given store and logger.
// If logger is nil, a default logger is used.
func NewConventionDispatcher(s DispatchStore, logger *log.Logger) *ConventionDispatcher {
	if logger == nil {
		logger = log.Default()
	}
	return &ConventionDispatcher{
		registry: make(map[conventionKey]*dispatchEntry),
		store:    s,
		logger:   logger,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// RegisterTier1Handler registers a pure-Go convention handler for a specific
// (campfireID, conventionName, operationName) triple.
// If a handler was already registered for that triple, it is replaced.
//
// Returns ErrReservedOp if operationName is one of the ten L1-frozen reserved
// operations (campfireagent-935: Stage 1 reserved-op enforcement). The reserved
// floor cannot be lowered by any convention declaration or parent grant.
func (d *ConventionDispatcher) RegisterTier1Handler(
	campfireID, conventionName, operationName string,
	serverClient *protocol.Client,
	handler HandlerFunc,
	serverID string,
	forgeAccountID string,
) error {
	if cfprotocol.IsReservedOp(operationName) {
		return fmt.Errorf("convention dispatcher: operation %q is reserved (reserved-op floor, L1 §§2.4): %w", operationName, ErrReservedOp)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.registry[conventionKey{
		CampfireID: campfireID,
		Convention: conventionName,
		Operation:  operationName,
	}] = &dispatchEntry{
		Tier:           1,
		Handler:        handler,
		ServerID:       serverID,
		ForgeAccountID: forgeAccountID,
		Client:         serverClient,
	}
	return nil
}

// RegisterTier2Handler registers an HTTP-based convention handler for a specific
// (campfireID, conventionName, operationName) triple.
// If a handler was already registered for that triple, it is replaced.
//
// Returns ErrReservedOp if operationName is one of the ten L1-frozen reserved
// operations (campfireagent-935: Stage 1 reserved-op enforcement).
func (d *ConventionDispatcher) RegisterTier2Handler(
	campfireID, conventionName, operationName string,
	handlerURL string,
	serverClient *protocol.Client,
	serverID string,
	forgeAccountID string,
) error {
	if cfprotocol.IsReservedOp(operationName) {
		return fmt.Errorf("convention dispatcher: operation %q is reserved (reserved-op floor, L1 §§2.4): %w", operationName, ErrReservedOp)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.registry[conventionKey{
		CampfireID: campfireID,
		Convention: conventionName,
		Operation:  operationName,
	}] = &dispatchEntry{
		Tier:           2,
		HandlerURL:     handlerURL,
		ServerID:       serverID,
		ForgeAccountID: forgeAccountID,
		Client:         serverClient,
	}
	return nil
}

// conventionOpPayload is the JSON payload for a convention:operation invocation message.
type conventionOpPayload struct {
	Convention string         `json:"convention"`
	Version    string         `json:"version,omitempty"`
	Operation  string         `json:"operation"`
	Args       map[string]any `json:"args,omitempty"`
}

// tier2RequestBody is the HTTP request body sent to Tier 2 handlers.
type tier2RequestBody struct {
	MessageID  string         `json:"message_id"`
	CampfireID string         `json:"campfire_id"`
	Sender     string         `json:"sender"`
	Convention string         `json:"convention"`
	Operation  string         `json:"operation"`
	Args       map[string]any `json:"args"`
	Tags       []string       `json:"tags"`
}

// hasConventionOperationInvocationTag reports whether the message has a tag
// matching the pattern "<convention>:<operation>" (i.e. a convention invocation
// tag, not the declaration tag "convention:operation").
//
// Convention invocation tags look like "myconvention:myop", NOT "convention:operation"
// (which is the declaration tag). We detect invocations by looking for tags that
// contain exactly one ":" and do NOT equal the reserved declaration tag.
func hasConventionInvocationTag(tags []string) bool {
	for _, t := range tags {
		if isConventionInvocationTag(t) {
			return true
		}
	}
	return false
}

// isConventionInvocationTag returns true if tag looks like "name:op" and is
// not the reserved ConventionOperationTag ("convention:operation").
func isConventionInvocationTag(tag string) bool {
	if tag == ConventionOperationTag {
		return false
	}
	idx := strings.Index(tag, ":")
	return idx > 0 && idx < len(tag)-1
}

// Dispatch checks a message for convention operation invocation tags and dispatches
// to the appropriate registered handler. It is non-blocking — actual dispatch work
// runs in a goroutine. Returns true if a handler was found and dispatch was initiated.
//
// Reserved-op enforcement (campfireagent-935): if the operation name is one of the
// ten L1-frozen reserved operations, Dispatch returns false without invoking any
// handler. This is the L2 enforcement point (the dispatch interceptor). The check
// fires even when a handler has been registered for the operation — defence-in-depth
// against Registration bypass paths.
func (d *ConventionDispatcher) Dispatch(ctx context.Context, campfireID string, msg *store.MessageRecord) bool {
	if !hasConventionInvocationTag(msg.Tags) {
		return false
	}

	// Parse the convention operation payload.
	var op conventionOpPayload
	if err := json.Unmarshal(msg.Payload, &op); err != nil {
		return false
	}
	if op.Convention == "" || op.Operation == "" {
		return false
	}

	// L2 reserved-op enforcement: block dispatch of any reserved operation.
	// This enforces the protocol-spec.md §Reserved-Op Floor at the dispatch
	// interceptor layer regardless of what is in the registry.
	if cfprotocol.IsReservedOp(op.Operation) {
		d.logger.Printf("convention dispatcher: blocked reserved op %q (campfire=%s, msg=%s)", op.Operation, campfireID, msg.ID)
		return false
	}

	d.mu.RLock()
	entry, ok := d.registry[conventionKey{
		CampfireID: campfireID,
		Convention: op.Convention,
		Operation:  op.Operation,
	}]
	d.mu.RUnlock()
	if !ok {
		return false
	}

	// Snapshot entry fields for the goroutine (entry pointer is stable after registration).
	go d.dispatch(ctx, nil, campfireID, msg, op, entry)
	return true
}

// DispatchWithCancel is like Dispatch but accepts a context.CancelFunc that is
// called when the spawned goroutine completes. This allows callers that create
// a timeout context to properly release the timer when dispatch finishes,
// without leaking the cancel func (campfire-agent-n34).
func (d *ConventionDispatcher) DispatchWithCancel(ctx context.Context, cancel context.CancelFunc, campfireID string, msg *store.MessageRecord) bool {
	if !hasConventionInvocationTag(msg.Tags) {
		return false
	}

	var op conventionOpPayload
	if err := json.Unmarshal(msg.Payload, &op); err != nil {
		return false
	}
	if op.Convention == "" || op.Operation == "" {
		return false
	}

	// L2 reserved-op enforcement (campfireagent-935): block reserved ops here too.
	if cfprotocol.IsReservedOp(op.Operation) {
		if cancel != nil {
			cancel()
		}
		d.logger.Printf("convention dispatcher: blocked reserved op %q (campfire=%s, msg=%s)", op.Operation, campfireID, msg.ID)
		return false
	}

	d.mu.RLock()
	entry, ok := d.registry[conventionKey{
		CampfireID: campfireID,
		Convention: op.Convention,
		Operation:  op.Operation,
	}]
	d.mu.RUnlock()
	if !ok {
		return false
	}

	go d.dispatch(ctx, cancel, campfireID, msg, op, entry)
	return true
}

// dispatch runs the actual dispatch logic for one message, in a goroutine.
// If cancel is non-nil it is deferred so timeout contexts are released promptly.
func (d *ConventionDispatcher) dispatch(
	ctx context.Context,
	cancel context.CancelFunc,
	campfireID string,
	msg *store.MessageRecord,
	op conventionOpPayload,
	entry *dispatchEntry,
) {
	if cancel != nil {
		defer cancel()
	}
	// Deduplication: mark as dispatched (insert-if-not-exists).
	// Use the request context here: if it is already cancelled, the dedup
	// insert may fail fast (on real stores), which is fine — we just skip.
	inserted, err := d.store.MarkDispatched(ctx, campfireID, msg.ID, entry.ServerID, entry.ForgeAccountID, op.Convention, op.Operation)
	if err != nil {
		d.logger.Printf("convention dispatcher: MarkDispatched(%s/%s): %v", campfireID, msg.ID, err)
		return
	}
	if !inserted {
		// Already dispatched — skip.
		return
	}

	// cleanupCtx is detached from the request context so that post-handler
	// bookkeeping (CAS status updates, cursor advancement) always completes
	// even when ctx is cancelled. This prevents the race between a cancelled
	// handler returning and the test/caller observing the "failed" status
	// while the goroutine is still writing to the store under a closed context.
	cleanupCtx := context.Background()
	d.invokeHandler(ctx, cleanupCtx, campfireID, msg, op, entry)
}

// invokeHandler calls the registered handler for a message and updates the
// dispatch store. It is called from dispatch() (after deduplication) and from
// the fallback sweep (bypassing deduplication, which tracks attempts separately
// via RedispatchCount). Must be called in a goroutine.
//
// ctx is the request context passed to the handler. cleanupCtx is used for all
// post-handler store operations (CAS updates, cursor advancement, metering) and
// must NOT be cancelled when ctx is cancelled — it is typically context.Background().
// This separation prevents the race where ctx cancellation causes store writes to
// fail or race with test cleanup after the "failed" status is already visible.
//
// To guard against double-dispatch (a slow original handler completing after
// the sweep has re-dispatched), invokeHandler snapshots the RedispatchCount
// (generation) before calling the handler and uses CAS methods to mark the
// result. If a re-dispatch occurred while the handler was running, the CAS
// check fails and the stale handler's result is silently discarded.
func (d *ConventionDispatcher) invokeHandler(
	ctx context.Context,
	cleanupCtx context.Context,
	campfireID string,
	msg *store.MessageRecord,
	op conventionOpPayload,
	entry *dispatchEntry,
) {
	// Snapshot the current generation before invoking the handler.
	// Use cleanupCtx here: the record was just inserted by MarkDispatched so it
	// exists; using a non-cancellable context avoids a spurious failure if ctx
	// is already cancelled before we reach this point.
	gen, err := d.store.GetRedispatchCount(cleanupCtx, campfireID, msg.ID)
	if err != nil {
		d.logger.Printf("convention dispatcher: GetRedispatchCount(%s/%s): %v", campfireID, msg.ID, err)
		return
	}

	status := "dispatched"
	var tokensConsumed int64

	if entry.Tier == 1 {
		status, tokensConsumed = d.dispatchTier1(ctx, cleanupCtx, campfireID, msg, op, entry, gen)
	} else {
		status, tokensConsumed = d.dispatchTier2(ctx, cleanupCtx, campfireID, msg, op, entry, gen)
	}

	// If the handler's result was rejected by CAS (generation mismatch),
	// status will be "stale" — skip metering and cursor advancement.
	// If the dispatch record was not found, status will be "not_found" —
	// also skip metering and cursor advancement (campfire-agent-43r).
	if status == "stale" {
		d.logger.Printf("convention dispatcher: stale handler discarded for %s/%s (gen %d superseded)", campfireID, msg.ID, gen)
		return
	}
	if status == "not_found" {
		d.logger.Printf("convention dispatcher: dispatch record not found for %s/%s, skipping metering/cursor", campfireID, msg.ID)
		return
	}

	// Fire metering hook. Use cleanupCtx so the hook fires even if the request
	// context was cancelled (billing must be accurate regardless of caller state).
	if d.MeteringHook != nil {
		d.MeteringHook(cleanupCtx, ConventionMeterEvent{
			CampfireID:     campfireID,
			Convention:     op.Convention,
			Operation:      op.Operation,
			Tier:           entry.Tier,
			ServerID:       entry.ServerID,
			ForgeAccountID: entry.ForgeAccountID,
			MessageID:      msg.ID,
			Status:         status,
			TokensConsumed: tokensConsumed,
		})
	}

	// Advance cursor using cleanupCtx: cursor advancement is bookkeeping that
	// must succeed regardless of the request context state.
	if _, err := d.store.AdvanceCursor(cleanupCtx, entry.ServerID, campfireID, msg.Timestamp); err != nil {
		d.logger.Printf("convention dispatcher: AdvanceCursor(%s/%s): %v", campfireID, msg.ID, err)
	}
}

// dispatchTier1 calls a registered Go handler and sends a fulfillment response.
// The gen parameter is the RedispatchCount snapshot taken before the handler was
// invoked; it is used for CAS-guarded status updates to prevent double-dispatch.
//
// ctx is the request context passed to the handler.
// cleanupCtx is used for all post-handler store operations and must not be
// cancelled when ctx is cancelled (typically context.Background()).
//
// Returns the final status string ("fulfilled", "failed", "stale", or "not_found")
// and the number of tokens consumed by the handler (0 if not reported).
func (d *ConventionDispatcher) dispatchTier1(
	ctx context.Context,
	cleanupCtx context.Context,
	campfireID string,
	msg *store.MessageRecord,
	op conventionOpPayload,
	entry *dispatchEntry,
	gen int,
) (string, int64) {
	args := op.Args
	if args == nil {
		args = make(map[string]any)
	}

	// Prefer SenderCampfireID (stable identity address) when present.
	// Falls back to Sender (agent pubkey hex) for legacy messages.
	senderIdentity := msg.Sender
	if msg.SenderCampfireID != "" {
		senderIdentity = msg.SenderCampfireID
	}
	req := &Request{
		MessageID:  msg.ID,
		Sender:     senderIdentity,
		CampfireID: campfireID,
		Args:       args,
		Tags:       msg.Tags,
	}

	resp, err := entry.Handler(ctx, req)
	if err != nil {
		d.logger.Printf("convention dispatcher: handler error (msg %s): %v", msg.ID, err)
		// Mark failed only if we still own this generation.
		// Use cleanupCtx so the CAS write succeeds even when ctx is cancelled.
		ok, notFound, casErr := d.store.MarkFailedCAS(cleanupCtx, campfireID, msg.ID, gen)
		if casErr != nil {
			d.logger.Printf("convention dispatcher: MarkFailedCAS (msg %s): %v", msg.ID, casErr)
			return "failed", 0
		}
		if notFound {
			return "not_found", 0
		}
		if !ok {
			return "stale", 0
		}
		// Skip error fulfillment when the handler returned because the request
		// context was cancelled. The requester already knows they cancelled —
		// sending an error fulfillment would race with test/caller cleanup and
		// provides no useful information to a context that is no longer active.
		if ctx.Err() == nil {
			if sendErr := d.sendErrorFulfillment(campfireID, msg.ID, err, entry.Client); sendErr != nil {
				d.logger.Printf("convention dispatcher: send error fulfillment (msg %s): %v", msg.ID, sendErr)
			}
		}
		return "failed", 0
	}

	// CAS-guard the fulfillment: only proceed if no re-dispatch has occurred.
	// Use cleanupCtx so the CAS write is not affected by ctx cancellation.
	ok, notFound, casErr := d.store.MarkFulfilledCAS(cleanupCtx, campfireID, msg.ID, gen)
	if casErr != nil {
		d.logger.Printf("convention dispatcher: MarkFulfilledCAS (msg %s): %v", msg.ID, casErr)
		return "failed", 0
	}
	if notFound {
		return "not_found", 0
	}
	if !ok {
		return "stale", 0
	}

	var tokensConsumed int64
	if resp != nil {
		if sendErr := d.sendFulfillment(campfireID, msg.ID, resp, entry.Client); sendErr != nil {
			d.logger.Printf("convention dispatcher: send fulfillment (msg %s): %v", msg.ID, sendErr)
			// Revert to failed since the fulfillment message couldn't be sent.
			// Must use CAS to avoid overwriting a newer generation's status.
			if ok, notFound, markErr := d.store.MarkFailedCAS(cleanupCtx, campfireID, msg.ID, gen); markErr != nil {
				d.logger.Printf("convention dispatcher: MarkFailedCAS (msg %s): %v", msg.ID, markErr)
			} else if notFound {
				// Record was deleted between MarkFulfilledCAS and sendFulfillment.
				// The dispatch is orphaned — skip metering and cursor advancement.
				d.logger.Printf("convention dispatcher: send fulfillment failure revert: dispatch record not found (msg %s) — record deleted between CAS and send", msg.ID)
				return "not_found", 0
			} else if !ok {
				return "stale", 0
			}
			return "failed", 0
		}
		tokensConsumed = resp.TokensConsumed
		// Record handler-reported token consumption for billing.
		if resp.TokensConsumed > 0 {
			if err := d.store.SetTokensConsumed(cleanupCtx, campfireID, msg.ID, resp.TokensConsumed); err != nil {
				d.logger.Printf("convention dispatcher: SetTokensConsumed (msg %s): %v", msg.ID, err)
			}
		}
	}

	return "fulfilled", tokensConsumed
}

// dispatchTier2 POSTs a message to a registered HTTP handler URL.
// The gen parameter is the RedispatchCount snapshot for CAS-guarded status updates.
//
// ctx is used for the HTTP request (respects cancellation).
// cleanupCtx is used for all store CAS updates (must not be cancelled with ctx).
//
// Returns the final status string ("fulfilled", "failed", "stale", or "not_found")
// and tokens consumed (always 0 for Tier 2 — the handler self-reports via the store).
func (d *ConventionDispatcher) dispatchTier2(
	ctx context.Context,
	cleanupCtx context.Context,
	campfireID string,
	msg *store.MessageRecord,
	op conventionOpPayload,
	entry *dispatchEntry,
	gen int,
) (string, int64) {
	args := op.Args
	if args == nil {
		args = make(map[string]any)
	}

	// Prefer SenderCampfireID (stable identity address) when present.
	senderIdentityT2 := msg.Sender
	if msg.SenderCampfireID != "" {
		senderIdentityT2 = msg.SenderCampfireID
	}
	body := tier2RequestBody{
		MessageID:  msg.ID,
		CampfireID: campfireID,
		Sender:     senderIdentityT2,
		Convention: op.Convention,
		Operation:  op.Operation,
		Args:       args,
		Tags:       msg.Tags,
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		d.logger.Printf("convention dispatcher: tier2 marshal (msg %s): %v", msg.ID, err)
		if ok, notFound, casErr := d.store.MarkFailedCAS(cleanupCtx, campfireID, msg.ID, gen); casErr != nil {
			d.logger.Printf("convention dispatcher: MarkFailedCAS (msg %s): %v", msg.ID, casErr)
		} else if notFound {
			return "not_found", 0
		} else if !ok {
			return "stale", 0
		}
		return "failed", 0
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.HandlerURL, bytes.NewReader(bodyBytes))
	if err != nil {
		d.logger.Printf("convention dispatcher: tier2 build request (msg %s): %v", msg.ID, err)
		if ok, notFound, casErr := d.store.MarkFailedCAS(cleanupCtx, campfireID, msg.ID, gen); casErr != nil {
			d.logger.Printf("convention dispatcher: MarkFailedCAS (msg %s): %v", msg.ID, casErr)
		} else if notFound {
			return "not_found", 0
		} else if !ok {
			return "stale", 0
		}
		return "failed", 0
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		d.logger.Printf("convention dispatcher: tier2 POST (msg %s): %v", msg.ID, err)
		if ok, notFound, casErr := d.store.MarkFailedCAS(cleanupCtx, campfireID, msg.ID, gen); casErr != nil {
			d.logger.Printf("convention dispatcher: MarkFailedCAS (msg %s): %v", msg.ID, casErr)
		} else if notFound {
			return "not_found", 0
		} else if !ok {
			return "stale", 0
		}
		return "failed", 0
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		ok, notFound, casErr := d.store.MarkFulfilledCAS(cleanupCtx, campfireID, msg.ID, gen)
		if casErr != nil {
			d.logger.Printf("convention dispatcher: MarkFulfilledCAS (msg %s): %v", msg.ID, casErr)
			return "failed", 0
		}
		if notFound {
			return "not_found", 0
		}
		if !ok {
			return "stale", 0
		}
		return "fulfilled", 0
	}

	// Non-202 response is treated as failure.
	d.logger.Printf("convention dispatcher: tier2 POST status %d (msg %s)", resp.StatusCode, msg.ID)
	if ok, notFound, casErr := d.store.MarkFailedCAS(cleanupCtx, campfireID, msg.ID, gen); casErr != nil {
		d.logger.Printf("convention dispatcher: MarkFailedCAS (msg %s): %v", msg.ID, casErr)
	} else if notFound {
		return "not_found", 0
	} else if !ok {
		return "stale", 0
	}
	return "failed", 0
}

// sendFulfillment sends a response message threaded back to requestMsgID.
func (d *ConventionDispatcher) sendFulfillment(campfireID, requestMsgID string, resp *Response, client *protocol.Client) error {
	var payload []byte
	if resp.Payload != nil {
		var err error
		payload, err = json.Marshal(resp.Payload)
		if err != nil {
			return fmt.Errorf("marshal response payload: %w", err)
		}
	}
	tags := append([]string{"fulfills"}, resp.Tags...)
	_, err := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     payload,
		Tags:        tags,
		Antecedents: []string{requestMsgID},
	})
	return err
}

// sendErrorFulfillment sends an error fulfillment message threaded back to requestMsgID.
func (d *ConventionDispatcher) sendErrorFulfillment(campfireID, requestMsgID string, handlerErr error, client *protocol.Client) error {
	payload, err := json.Marshal(ErrorResponse{Error: handlerErr.Error()})
	if err != nil {
		return fmt.Errorf("marshal error response: %w", err)
	}
	_, err = client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     payload,
		Tags:        []string{"fulfills", "convention:error"},
		Antecedents: []string{requestMsgID},
	})
	return err
}

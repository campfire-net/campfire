package convention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/protocol"
)

// executorTransport is the internal interface used by the Executor for sending and
// reading messages. In production code the executor is always backed by a
// *protocol.Client (via clientAdapter). The interface is kept unexported so that
// production callers must use NewExecutor(*protocol.Client, …).
//
// External packages that need to inject a mock for unit testing should use
// NewExecutorForTest, which accepts the exported ExecutorBackend interface.
type executorTransport interface {
	sendMessage(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string, campfireKey bool) (msgID string, err error)
	readMessages(ctx context.Context, campfireID string, tags []string) ([]MessageRecord, error)
	sendFutureAndAwait(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string, timeout time.Duration) (msgID string, fulfillmentPayload []byte, err error)
}

// ExecutorBackend is the exported interface for injecting a test double into the
// Executor via NewExecutorForTest. It mirrors the four send/read operations that
// the Executor requires. Production code always uses NewExecutor(*protocol.Client, …).
type ExecutorBackend interface {
	SendMessage(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (msgID string, err error)
	SendCampfireKeySigned(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (msgID string, err error)
	ReadMessages(ctx context.Context, campfireID string, tags []string) ([]MessageRecord, error)
	SendFutureAndAwait(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string, timeout time.Duration) (msgID string, fulfillmentPayload []byte, err error)
}

// backendAdapter bridges ExecutorBackend to executorTransport. Used by
// NewExecutorForTest so external test doubles can implement the exported interface.
type backendAdapter struct{ b ExecutorBackend }

func (a *backendAdapter) sendMessage(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string, campfireKey bool) (string, error) {
	if campfireKey {
		return a.b.SendCampfireKeySigned(ctx, campfireID, payload, tags, antecedents)
	}
	return a.b.SendMessage(ctx, campfireID, payload, tags, antecedents)
}

func (a *backendAdapter) readMessages(ctx context.Context, campfireID string, tags []string) ([]MessageRecord, error) {
	return a.b.ReadMessages(ctx, campfireID, tags)
}

func (a *backendAdapter) sendFutureAndAwait(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string, timeout time.Duration) (string, []byte, error) {
	return a.b.SendFutureAndAwait(ctx, campfireID, payload, tags, antecedents, timeout)
}

// clientAdapter bridges *protocol.Client to executorTransport.
type clientAdapter struct {
	client *protocol.Client
}

func (a *clientAdapter) sendMessage(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string, campfireKey bool) (string, error) {
	mode := protocol.SigningModeMemberKey
	if campfireKey {
		mode = protocol.SigningModeCampfireKey
	}
	msg, err := a.client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     payload,
		Tags:        tags,
		Antecedents: antecedents,
		SigningMode: mode,
	})
	if err != nil {
		return "", err
	}
	return msg.ID, nil
}

func (a *clientAdapter) readMessages(_ context.Context, campfireID string, tags []string) ([]MessageRecord, error) {
	result, err := a.client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       tags,
	})
	if err != nil {
		return nil, err
	}
	records := make([]MessageRecord, len(result.Messages))
	for i, m := range result.Messages {
		records[i] = MessageRecord{
			ID:     m.ID,
			Sender: m.Sender,
			Tags:   m.Tags,
		}
	}
	return records, nil
}

func (a *clientAdapter) sendFutureAndAwait(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string, timeout time.Duration) (string, []byte, error) {
	msg, err := a.client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     payload,
		Tags:        append(tags, "future"),
		Antecedents: antecedents,
	})
	if err != nil {
		return "", nil, fmt.Errorf("sending future: %w", err)
	}

	fulfillment, err := a.client.Await(ctx, protocol.AwaitRequest{
		CampfireID:  campfireID,
		TargetMsgID: msg.ID,
		Timeout:     timeout,
	})
	if err != nil {
		return msg.ID, nil, fmt.Errorf("awaiting fulfillment: %w", err)
	}
	return msg.ID, fulfillment.Payload, nil
}

// MessageRecord is a minimal message record for executor use.
type MessageRecord struct {
	ID     string
	Sender string
	Tags   []string
}

// ProvenanceChecker returns the operator provenance level for a given public key.
// The Executor calls this before dispatching gated operations.
// See Operator Provenance Convention v0.1 §8.1.
type ProvenanceChecker interface {
	// Level returns the provenance level (0–3) for the given public key.
	Level(key string) int
}

// Executor runs convention operations: validates args, composes tags, and sends messages.
type Executor struct {
	transport   executorTransport
	selfKey     string
	rateLimiter *rateLimiter
	provenance  ProvenanceChecker
}

// globalRateLimiter is a process-level singleton so that rate limit state persists
// across multiple Executor instances within the same process. Without this, each CLI
// invocation (or programmatic Executor construction) would start with a fresh limiter,
// defeating rate limiting entirely.
var (
	globalRateLimiter     *rateLimiter
	globalRateLimiterOnce sync.Once
)

func sharedRateLimiter() *rateLimiter {
	globalRateLimiterOnce.Do(func() {
		globalRateLimiter = newRateLimiter()
	})
	return globalRateLimiter
}

// NewExecutor creates an Executor backed by the given protocol.Client and agent public key.
// All Executors created within the same process share a single rate limiter so that
// rate limits are enforced across multiple sequential CLI calls.
func NewExecutor(client *protocol.Client, selfKey string) *Executor {
	return &Executor{
		transport:   &clientAdapter{client: client},
		selfKey:     selfKey,
		rateLimiter: sharedRateLimiter(),
	}
}

// NewExecutorForTest creates an Executor backed by an ExecutorBackend test double.
// Use this in test packages that need to inject a mock transport. Production code
// should always use NewExecutor(*protocol.Client, …).
func NewExecutorForTest(backend ExecutorBackend, selfKey string) *Executor {
	return &Executor{
		transport:   &backendAdapter{b: backend},
		selfKey:     selfKey,
		rateLimiter: sharedRateLimiter(),
	}
}

// WithProvenance attaches a ProvenanceChecker to the Executor.
// When set, the executor enforces min_operator_level gates declared in convention
// operations. Operations with min_operator_level > 0 are rejected unless the
// sender's provenance level meets or exceeds the declared minimum.
// See Operator Provenance Convention v0.1 §8.
func (e *Executor) WithProvenance(checker ProvenanceChecker) *Executor {
	e.provenance = checker
	return e
}

// newExecutorWithLimiter creates an Executor with an explicit rate limiter.
// Use this in tests that need isolated rate limit state.
func newExecutorWithLimiter(transport executorTransport, selfKey string, rl *rateLimiter) *Executor {
	return &Executor{
		transport:   transport,
		selfKey:     selfKey,
		rateLimiter: rl,
	}
}

// newExecutorFromBackendWithLimiter creates an Executor from a test backend with an
// explicit rate limiter. Use this in tests that need isolated rate limit state and a mock.
func newExecutorFromBackendWithLimiter(backend ExecutorBackend, selfKey string, rl *rateLimiter) *Executor {
	return newExecutorWithLimiter(&backendAdapter{b: backend}, selfKey, rl)
}

// newExecutorWithSharedLimiter creates an Executor backed by the given transport using
// the process-level shared rate limiter. This is the internal equivalent of NewExecutor
// but accepts any executorTransport — used by same-package tests to verify singleton
// rate limiter behaviour without requiring a real *protocol.Client.
func newExecutorWithSharedLimiter(transport executorTransport, selfKey string) *Executor {
	return &Executor{
		transport:   transport,
		selfKey:     selfKey,
		rateLimiter: sharedRateLimiter(),
	}
}

// ExecuteResult holds the result of a successful Execute call.
type ExecuteResult struct {
	// MessageID is the ID of the message that was sent.
	MessageID string
	// Response is the fulfillment payload for sync declarations. Nil for async/none.
	Response []byte
	// Elapsed is the round-trip time for sync declarations.
	Elapsed time.Duration
}

// ErrResponseTimeout is returned by Execute when a sync operation's response
// times out waiting for fulfillment. Callers can check with errors.Is.
var ErrResponseTimeout = fmt.Errorf("convention: response timeout waiting for fulfillment")

// Execute validates args, composes tags, enforces rate limits, and sends messages.
func (e *Executor) Execute(ctx context.Context, decl *Declaration, campfireID string, args map[string]any) (*ExecuteResult, error) {
	// Operator provenance gate: reject if sender's level is below the declared minimum.
	// See Operator Provenance Convention v0.1 §8.1.
	if decl.MinOperatorLevel > 0 {
		senderLevel := 0
		if e.provenance != nil {
			senderLevel = e.provenance.Level(e.selfKey)
		}
		if senderLevel < decl.MinOperatorLevel {
			return nil, fmt.Errorf("operator provenance level %d insufficient: operation %q requires level %d",
				senderLevel, decl.Convention+":"+decl.Operation, decl.MinOperatorLevel)
		}
	}

	if len(decl.Steps) > 0 {
		return e.executeWorkflow(ctx, decl, campfireID, args)
	}
	return e.executeSingle(ctx, decl, campfireID, args)
}

// executeSingle runs a single-step convention operation.
func (e *Executor) executeSingle(ctx context.Context, decl *Declaration, campfireID string, args map[string]any) (*ExecuteResult, error) {
	// 1. Validate and apply defaults.
	resolved, err := validateArgs(decl.Args, args)
	if err != nil {
		return nil, err
	}

	// 2. Compose tags.
	composed, err := composeTags(decl, resolved)
	if err != nil {
		return nil, err
	}

	// 3. Tag denylist check.
	// Exception: the convention-extension convention may produce convention:operation
	// and convention:revoke tags — it IS the convention management protocol.
	isConventionExtension := decl.Convention == InfrastructureConvention
	for _, tag := range composed {
		if isConventionExtension && (tag == ConventionOperationTag || tag == conventionRevokeTag) {
			continue
		}
		if err := checkDeniedTag(tag); err != nil {
			return nil, fmt.Errorf("composed tag rejected by denylist: %w", err)
		}
	}

	// 4. Construct antecedents.
	antecedents, err := e.resolveAntecedents(ctx, decl, campfireID, resolved)
	if err != nil {
		return nil, err
	}

	// 5. Rate limit enforcement.
	if decl.RateLimit != nil {
		key := rateLimitKey(decl, campfireID, e.selfKey)
		if err := e.rateLimiter.Check(key, decl.RateLimit); err != nil {
			return nil, err
		}
	}

	// 6. Build payload.
	payload, err := json.Marshal(resolved)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	// 7. Send (or send-as-future for sync response declarations).
	// Only use the future/await path when response="sync" was explicitly declared.
	// Declarations that defaulted to "sync" (ResponseExplicit=false) use the normal path.
	if decl.Response == "sync" && decl.ResponseExplicit {
		// Sync: send as future and await fulfillment.
		start := time.Now()
		timeout := decl.ResponseTimeout
		if timeout == 0 {
			timeout = stepTimeout
		}
		msgID, respPayload, err := e.transport.sendFutureAndAwait(ctx, campfireID, payload, composed, antecedents, timeout)
		elapsed := time.Since(start)
		if err != nil {
			// Check if it's a timeout — context deadline exceeded from sendFutureAndAwait.
			if ctx.Err() != nil || isTimeoutErr(err) {
				// Return partial result with ErrResponseTimeout sentinel.
				return &ExecuteResult{MessageID: msgID, Elapsed: elapsed}, ErrResponseTimeout
			}
			return nil, fmt.Errorf("send message: %w", err)
		}
		// Record rate limit usage after successful send.
		if decl.RateLimit != nil {
			key := rateLimitKey(decl, campfireID, e.selfKey)
			e.rateLimiter.Record(key, decl.RateLimit)
		}
		return &ExecuteResult{MessageID: msgID, Response: respPayload, Elapsed: elapsed}, nil
	}

	// Async or none (or empty/unset): send normally and return msgID.
	msgID, err := e.transport.sendMessage(ctx, campfireID, payload, composed, antecedents, decl.Signing == "campfire_key")
	if err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}

	// 8. Record rate limit usage after successful send.
	if decl.RateLimit != nil {
		key := rateLimitKey(decl, campfireID, e.selfKey)
		e.rateLimiter.Record(key, decl.RateLimit)
	}

	return &ExecuteResult{MessageID: msgID}, nil
}

// isTimeoutErr returns true if err looks like a deadline/timeout error.
// It checks errors.Is(err, context.DeadlineExceeded) first so that wrapped
// sentinel errors are caught correctly, then falls back to string matching for
// timeout errors that originate outside the standard library.
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "context deadline") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "timed out")
}

// stepTimeout is the per-step timeout for workflow execution.
// It is also used by executeQueryStep for sync convention awaits, so it is
// defined at package level rather than inside executeWorkflow.
const stepTimeout = 30 * time.Second

// executeWorkflow runs a multi-step convention operation.
func (e *Executor) executeWorkflow(ctx context.Context, decl *Declaration, campfireID string, args map[string]any) (*ExecuteResult, error) {
	const totalTimeout = 120 * time.Second

	wfCtx, wfCancel := context.WithTimeout(ctx, totalTimeout)
	defer wfCancel()

	// Variable bindings: binding name → map of fields.
	bindings := make(map[string]map[string]any)

	for i, step := range decl.Steps {
		stepCtx, stepCancel := context.WithTimeout(wfCtx, stepTimeout)
		err := e.executeStep(stepCtx, step, campfireID, bindings)
		stepCancel()
		if err != nil {
			return nil, fmt.Errorf("step[%d] (%s): %w", i, step.Action, err)
		}
	}
	// Multi-step workflows handle their own await internally; return empty result.
	return &ExecuteResult{}, nil
}

func (e *Executor) executeStep(ctx context.Context, step Step, campfireID string, bindings map[string]map[string]any) error {
	switch step.Action {
	case "query":
		return e.executeQueryStep(ctx, step, campfireID, bindings)
	case "send":
		return e.executeSendStep(ctx, step, campfireID, bindings)
	default:
		return fmt.Errorf("unknown step action %q", step.Action)
	}
}

func (e *Executor) executeQueryStep(ctx context.Context, step Step, campfireID string, bindings map[string]map[string]any) error {
	// Resolve future_tags variables.
	futureTags := resolveStringSlice(step.FutureTags, bindings, "")

	// Resolve future_payload.
	var futurePayload []byte
	if len(step.FuturePayload) > 0 {
		resolved := resolvePayloadMap(step.FuturePayload, bindings, "")
		var err error
		futurePayload, err = json.Marshal(resolved)
		if err != nil {
			return fmt.Errorf("marshal future_payload: %w", err)
		}
	}

	_, result, err := e.transport.sendFutureAndAwait(ctx, campfireID, futurePayload, futureTags, nil, stepTimeout)
	if err != nil {
		return fmt.Errorf("query future: %w", err)
	}

	if step.ResultBinding != "" {
		var bound map[string]any
		if len(result) > 0 {
			if err := json.Unmarshal(result, &bound); err != nil {
				// Store raw result under "raw" key if not JSON.
				bound = map[string]any{"raw": string(result)}
			}
		} else {
			bound = make(map[string]any)
		}
		bindings[step.ResultBinding] = bound
	}
	return nil
}

func (e *Executor) executeSendStep(ctx context.Context, step Step, campfireID string, bindings map[string]map[string]any) error {
	// Resolve tags.
	stepTags := resolveStringSlice(step.Tags, bindings, e.selfKey)

	// Resolve antecedents.
	antecedents := make([]string, 0, len(step.AntecedentRefs))
	for _, ref := range step.AntecedentRefs {
		resolved := resolveVar(ref, bindings, e.selfKey)
		if resolved != "" {
			antecedents = append(antecedents, resolved)
		}
	}

	_, err := e.transport.sendMessage(ctx, campfireID, nil, stepTags, antecedents, false)
	return err
}

// resolveAntecedents computes the antecedents slice for a single-step operation.
func (e *Executor) resolveAntecedents(ctx context.Context, decl *Declaration, campfireID string, args map[string]any) ([]string, error) {
	switch decl.Antecedents {
	case "none", "":
		return nil, nil
	case "exactly_one(target)":
		// Find the message_id arg.
		for _, argDesc := range decl.Args {
			if argDesc.Type == "message_id" {
				if val, ok := args[argDesc.Name]; ok {
					if msgID, ok := val.(string); ok && msgID != "" {
						return []string{msgID}, nil
					}
				}
			}
		}
		return nil, fmt.Errorf("antecedents=exactly_one(target) requires a message_id arg to be provided")
	case "exactly_one(self_prior)":
		// Query campfire for agent's prior message with matching operation tags.
		opTag := decl.Convention + ":" + decl.Operation
		msgs, err := e.transport.readMessages(ctx, campfireID, []string{opTag})
		if err != nil {
			return nil, fmt.Errorf("read messages for self_prior: %w", err)
		}
		// Find most recent message from self.
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Sender == e.selfKey {
				return []string{msgs[i].ID}, nil
			}
		}
		return nil, nil
	case "zero_or_one(self_prior)":
		// Like exactly_one(self_prior) but genesis is allowed: if no prior exists,
		// send with no antecedent (nil). Subsequent messages must reference the prior.
		opTag := decl.Convention + ":" + decl.Operation
		msgs, err := e.transport.readMessages(ctx, campfireID, []string{opTag})
		if err != nil {
			return nil, fmt.Errorf("read messages for self_prior: %w", err)
		}
		// Find most recent message from self; nil antecedents for genesis.
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Sender == e.selfKey {
				return []string{msgs[i].ID}, nil
			}
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unrecognized antecedent rule: %q", decl.Antecedents)
	}
}

// ------- Tag composition -------

// composeTags builds the outgoing tag list from a declaration and resolved args.
func composeTags(decl *Declaration, args map[string]any) ([]string, error) {
	var result []string

	for _, rule := range decl.ProducesTags {
		tagBase, isGlob := parseTagGlob(rule.Tag)

		if !isGlob {
			// Static tag — always present for exactly_one rules.
			if rule.Cardinality == "exactly_one" {
				result = append(result, rule.Tag)
			}
			// at_most_one and zero_to_many static tags are optional; skip if no arg maps to them.
			continue
		}

		// Glob tag — map from args.
		argValues := collectArgValuesForPrefix(decl.Args, args, tagBase)
		switch rule.Cardinality {
		case "exactly_one":
			if len(argValues) != 1 {
				// Only enforce if any values present; skip if no arg.
				if len(argValues) > 1 {
					return nil, fmt.Errorf("tag rule %q (exactly_one) got %d values", rule.Tag, len(argValues))
				}
			}
			result = append(result, argValues...)
		case "at_most_one":
			if len(argValues) > 1 {
				return nil, fmt.Errorf("tag rule %q (at_most_one) got %d values", rule.Tag, len(argValues))
			}
			result = append(result, argValues...)
		case "zero_to_many":
			if rule.Max > 0 && len(argValues) > rule.Max {
				return nil, fmt.Errorf("tag rule %q (zero_to_many) got %d values, max %d", rule.Tag, len(argValues), rule.Max)
			}
			result = append(result, argValues...)
		}
	}

	return result, nil
}

// parseTagGlob splits "topic:*" into ("topic:", true) and "social:post" into ("social:post", false).
func parseTagGlob(tag string) (string, bool) {
	if strings.HasSuffix(tag, "*") {
		return strings.TrimSuffix(tag, "*"), true
	}
	return tag, false
}

// collectArgValuesForPrefix looks for args whose names or values map to the tag prefix.
// E.g., prefix "topic:" matches arg "topics" (repeated string) → "topic:<value>".
// Also handles enum args where the value IS the full tag (e.g., "social:upvote").
func collectArgValuesForPrefix(argDescs []ArgDescriptor, args map[string]any, prefix string) []string {
	var result []string

	// Derive the base name from the prefix (strip trailing colon for matching).
	// E.g., "topic:" → "topic" → match arg "topics".
	prefixBase := strings.TrimSuffix(prefix, ":")

	for _, desc := range argDescs {
		val, ok := args[desc.Name]
		if !ok {
			continue
		}

		// Enum args: if the value starts with the prefix, use it directly.
		if desc.Type == "enum" {
			if desc.Repeated {
				strs := toStringSlice(val)
				for _, s := range strs {
					if strings.HasPrefix(s, prefix) {
						result = append(result, s)
					}
				}
			} else {
				if s, ok := val.(string); ok && strings.HasPrefix(s, prefix) {
					result = append(result, s)
				}
			}
			continue
		}

		// Name-based matching: arg "topics" → prefix "topic:".
		// Also matches when prefix ends with the arg name: "naming:name:" → arg "name".
		argBase := strings.TrimSuffix(desc.Name, "s") // simple pluralization
		if argBase == prefixBase || desc.Name == prefixBase || strings.HasSuffix(prefix, desc.Name+":") {
			if desc.Repeated {
				strs := toStringSlice(val)
				for _, s := range strs {
					result = append(result, prefix+s)
				}
			} else {
				if s, ok := val.(string); ok {
					result = append(result, prefix+s)
				}
			}
		}
	}
	return result
}

// ------- Arg validation -------

// validateArgs checks all declared args against provided values, applies defaults,
// strips undeclared args (strict allow-listing), and returns the cleaned resolved args map.
// Any argument not present in descs is silently stripped.
func validateArgs(descs []ArgDescriptor, provided map[string]any) (map[string]any, error) {
	// Build allow-list of declared arg names.
	declared := make(map[string]struct{}, len(descs))
	for _, desc := range descs {
		declared[desc.Name] = struct{}{}
	}

	// Copy only declared args; strip undeclared ones.
	resolved := make(map[string]any, len(descs))
	for k, v := range provided {
		if _, ok := declared[k]; ok {
			resolved[k] = v
		}
		// Undeclared args are silently dropped -- strict allow-listing.
	}

	for _, desc := range descs {
		val, present := resolved[desc.Name]

		// Apply default if missing.
		if !present && desc.Default != nil {
			resolved[desc.Name] = desc.Default
			val = desc.Default
			present = true
		}

		if !present {
			if desc.Required {
				return nil, fmt.Errorf("missing required arg %q", desc.Name)
			}
			continue
		}

		if err := validateArgValue(desc, val); err != nil {
			return nil, fmt.Errorf("arg %q: %w", desc.Name, err)
		}
	}
	return resolved, nil
}

func validateArgValue(desc ArgDescriptor, val any) error {
	if desc.Repeated {
		strs := toStringSlice(val)
		if desc.MaxCount > 0 && len(strs) > desc.MaxCount {
			return fmt.Errorf("too many values: %d > %d", len(strs), desc.MaxCount)
		}
		for _, s := range strs {
			if err := validateSingleValue(desc, s); err != nil {
				return err
			}
		}
		return nil
	}
	return validateSingleValue(desc, val)
}

func validateSingleValue(desc ArgDescriptor, val any) error {
	switch desc.Type {
	case "string":
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("expected string, got %T", val)
		}
		if desc.MaxLength > 0 && len(s) > desc.MaxLength {
			return fmt.Errorf("value length %d exceeds max_length %d", len(s), desc.MaxLength)
		}
		if desc.Pattern != "" {
			if err := matchPattern(desc.Pattern, s); err != nil {
				return err
			}
		}

	case "integer":
		var n int
		switch v := val.(type) {
		case int:
			n = v
		case int64:
			n = int(v)
		case float64:
			n = int(v)
		case json.Number:
			i64, err := v.Int64()
			if err != nil {
				return fmt.Errorf("invalid integer: %w", err)
			}
			n = int(i64)
		default:
			return fmt.Errorf("expected integer, got %T", val)
		}
		// Only enforce min when it was explicitly declared in the convention JSON
		// (MinSet=true). When undeclared, Min is the zero value (0) and must not
		// impose a floor — callers should be free to pass negative integers.
		// Regression fix: campfire-agent-bnq.
		if desc.MinSet && n < desc.Min {
			return fmt.Errorf("value %d is below min %d", n, desc.Min)
		}
		if desc.Max != 0 && n > desc.Max {
			return fmt.Errorf("value %d out of range [%d, %d]", n, desc.Min, desc.Max)
		}

	case "enum":
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("expected string (enum), got %T", val)
		}
		if len(desc.Values) > 0 && !contains(desc.Values, s) {
			return fmt.Errorf("value %q not in enum %v", s, desc.Values)
		}

	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("expected bool, got %T", val)
		}

	case "duration":
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("expected duration string, got %T", val)
		}
		if _, err := parseDuration(s); err != nil {
			return fmt.Errorf("invalid duration: %w", err)
		}

	case "key":
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("expected string (key), got %T", val)
		}
		if len(s) != 64 {
			return fmt.Errorf("key must be 64 hex chars, got %d", len(s))
		}
		for _, c := range s {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return fmt.Errorf("key contains non-hex character %q", c)
			}
		}

	case "campfire", "message_id", "json", "tag_set":
		// Minimal validation — just ensure non-nil.
		if val == nil {
			return fmt.Errorf("expected non-nil value for type %q", desc.Type)
		}
		if desc.Type == "json" {
			s, ok := val.(string)
			if !ok {
				return fmt.Errorf("expected JSON string, got %T", val)
			}
			if !json.Valid([]byte(s)) {
				return fmt.Errorf("invalid JSON value")
			}
		}
		if desc.Type == "tag_set" {
			if _, ok := val.([]string); !ok {
				return fmt.Errorf("expected []string for tag_set, got %T", val)
			}
		}
	}
	return nil
}

// matchPatternTimeout is the deadline for a single regex match.
// The previous 1ms deadline caused false rejections under CPU load (campfire-agent-3bx).
const matchPatternTimeout = 100 * time.Millisecond

// matchPattern validates s against pattern with a deadline to guard against
// catastrophic backtracking. The goroutine is signalled via a done channel so
// it exits promptly when the timeout fires instead of leaking (campfire-agent-i3p).
func matchPattern(pattern, s string) error {
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}

	type result struct {
		matched bool
	}
	ch := make(chan result, 1)
	done := make(chan struct{})
	go func() {
		matched := re.MatchString(s)
		select {
		case ch <- result{matched: matched}:
		case <-done:
			// Caller timed out; discard result and exit cleanly.
		}
	}()

	select {
	case r := <-ch:
		close(done)
		if !r.matched {
			return fmt.Errorf("value %q does not match pattern %q", s, pattern)
		}
		return nil
	case <-time.After(matchPatternTimeout):
		close(done)
		return fmt.Errorf("pattern match timeout for pattern %q", pattern)
	}
}

// ------- Rate limiter -------

type rateLimiter struct {
	mu      sync.Mutex
	windows map[string]*rateLimitWindow
}

type rateLimitWindow struct {
	count     int
	windowEnd time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{windows: make(map[string]*rateLimitWindow)}
}

func (rl *rateLimiter) Check(key string, limit *RateLimit) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	win, ok := rl.windows[key]
	if !ok || now.After(win.windowEnd) {
		return nil
	}
	if win.count >= limit.Max {
		return fmt.Errorf("rate limit exceeded: max %d per %s", limit.Max, limit.Window)
	}
	return nil
}

func (rl *rateLimiter) Record(key string, limit *RateLimit) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	win, ok := rl.windows[key]
	if !ok || now.After(win.windowEnd) {
		dur, _ := parseDuration(limit.Window)
		if dur == 0 {
			dur = time.Minute
		}
		rl.windows[key] = &rateLimitWindow{count: 1, windowEnd: now.Add(dur)}
		return
	}
	win.count++
}

// rateLimitKey returns a unique key for the rate limit window.
// The Executor's selfKey is needed for sender-scoped keys; pass it here.
func rateLimitKey(decl *Declaration, campfireID, selfKey string) string {
	per := "sender"
	if decl.RateLimit != nil {
		per = decl.RateLimit.Per
	}
	base := decl.Convention + ":" + decl.Operation
	switch per {
	case "campfire_id":
		return base + "@" + campfireID
	case "sender_and_campfire_id":
		return base + "@" + campfireID + "+" + selfKey
	default: // "sender"
		return base + "+" + selfKey
	}
}

// ------- Variable substitution -------

// resolveVar performs $self_key and $binding.field substitution on a single string.
func resolveVar(s string, bindings map[string]map[string]any, selfKey string) string {
	if !strings.Contains(s, "$") {
		return s
	}
	// Replace $self_key.
	s = strings.ReplaceAll(s, "$self_key", selfKey)

	// Replace $<binding>.<field>.
	for name, fields := range bindings {
		for field, val := range fields {
			placeholder := "$" + name + "." + field
			s = strings.ReplaceAll(s, placeholder, fmt.Sprintf("%v", val))
		}
		// Also replace $<binding>.msg_id as alias for the id field.
		if msgID, ok := fields["msg_id"]; ok {
			s = strings.ReplaceAll(s, "$"+name+".msg_id", fmt.Sprintf("%v", msgID))
		}
	}
	return s
}

func resolveStringSlice(ss []string, bindings map[string]map[string]any, selfKey string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = resolveVar(s, bindings, selfKey)
	}
	return out
}

func resolvePayloadMap(m map[string]any, bindings map[string]map[string]any, selfKey string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = resolveVar(s, bindings, selfKey)
		} else {
			out[k] = v
		}
	}
	return out
}

// ------- Helpers -------

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func toStringSlice(val any) []string {
	switch v := val.(type) {
	case []string:
		return v
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	case string:
		return []string{v}
	}
	return nil
}

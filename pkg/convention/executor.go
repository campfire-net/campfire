package convention

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ExecutorTransport abstracts message sending for testability.
type ExecutorTransport interface {
	SendMessage(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (msgID string, err error)
	SendCampfireKeySigned(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (msgID string, err error)
	ReadMessages(ctx context.Context, campfireID string, tags []string) ([]MessageRecord, error)
	SendFutureAndAwait(ctx context.Context, campfireID string, payload []byte, tags []string, timeout time.Duration) (fulfillmentPayload []byte, err error)
}

// MessageRecord is a minimal message record for executor use.
type MessageRecord struct {
	ID     string
	Sender string
	Tags   []string
}

// Executor runs convention operations: validates args, composes tags, and sends messages.
type Executor struct {
	transport   ExecutorTransport
	selfKey     string
	rateLimiter *rateLimiter
}

// NewExecutor creates an Executor using the given transport and agent public key.
func NewExecutor(transport ExecutorTransport, selfKey string) *Executor {
	return &Executor{
		transport:   transport,
		selfKey:     selfKey,
		rateLimiter: newRateLimiter(),
	}
}

// Execute validates args, composes tags, enforces rate limits, and sends messages.
func (e *Executor) Execute(ctx context.Context, decl *Declaration, campfireID string, args map[string]any) error {
	if len(decl.Steps) > 0 {
		return e.executeWorkflow(ctx, decl, campfireID, args)
	}
	return e.executeSingle(ctx, decl, campfireID, args)
}

// executeSingle runs a single-step convention operation.
func (e *Executor) executeSingle(ctx context.Context, decl *Declaration, campfireID string, args map[string]any) error {
	// 1. Validate and apply defaults.
	resolved, err := validateArgs(decl.Args, args)
	if err != nil {
		return err
	}

	// 2. Compose tags.
	composed, err := composeTags(decl, resolved)
	if err != nil {
		return err
	}

	// 3. Tag denylist check.
	for _, tag := range composed {
		if err := checkDeniedTag(tag); err != nil {
			return fmt.Errorf("composed tag rejected by denylist: %w", err)
		}
	}

	// 4. Construct antecedents.
	antecedents, err := e.resolveAntecedents(ctx, decl, campfireID, resolved)
	if err != nil {
		return err
	}

	// 5. Rate limit enforcement.
	if decl.RateLimit != nil {
		key := rateLimitKey(decl, campfireID, e.selfKey)
		if err := e.rateLimiter.Check(key, decl.RateLimit); err != nil {
			return err
		}
	}

	// 6. Build payload.
	payload, err := json.Marshal(resolved)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	// 7. Send.
	if decl.Signing == "campfire_key" {
		_, err = e.transport.SendCampfireKeySigned(ctx, campfireID, payload, composed, antecedents)
	} else {
		_, err = e.transport.SendMessage(ctx, campfireID, payload, composed, antecedents)
	}
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	// 8. Record rate limit usage after successful send.
	if decl.RateLimit != nil {
		key := rateLimitKey(decl, campfireID, e.selfKey)
		e.rateLimiter.Record(key, decl.RateLimit)
	}

	return nil
}

// executeWorkflow runs a multi-step convention operation.
func (e *Executor) executeWorkflow(ctx context.Context, decl *Declaration, campfireID string, args map[string]any) error {
	const totalTimeout = 120 * time.Second
	const stepTimeout = 30 * time.Second

	wfCtx, wfCancel := context.WithTimeout(ctx, totalTimeout)
	defer wfCancel()

	// Variable bindings: binding name → map of fields.
	bindings := make(map[string]map[string]any)

	for i, step := range decl.Steps {
		stepCtx, stepCancel := context.WithTimeout(wfCtx, stepTimeout)
		err := e.executeStep(stepCtx, step, campfireID, bindings)
		stepCancel()
		if err != nil {
			return fmt.Errorf("step[%d] (%s): %w", i, step.Action, err)
		}
	}
	return nil
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

	result, err := e.transport.SendFutureAndAwait(ctx, campfireID, futurePayload, futureTags, 30*time.Second)
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

	_, err := e.transport.SendMessage(ctx, campfireID, nil, stepTags, antecedents)
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
		msgs, err := e.transport.ReadMessages(ctx, campfireID, []string{opTag})
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
		argBase := strings.TrimSuffix(desc.Name, "s") // simple pluralization
		if argBase == prefixBase || desc.Name == prefixBase {
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
// and returns the resolved args map.
func validateArgs(descs []ArgDescriptor, provided map[string]any) (map[string]any, error) {
	resolved := make(map[string]any, len(provided))
	// Copy provided values.
	for k, v := range provided {
		resolved[k] = v
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
		if desc.Min != 0 || desc.Max != 0 {
			if n < desc.Min || (desc.Max != 0 && n > desc.Max) {
				return fmt.Errorf("value %d out of range [%d, %d]", n, desc.Min, desc.Max)
			}
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
		if _, err := parseDurationLocal(s); err != nil {
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

// matchPattern validates s against pattern with a 1ms deadline.
func matchPattern(pattern, s string) error {
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}

	// Use a channel with timeout to guard against catastrophic backtracking.
	type result struct {
		matched bool
	}
	ch := make(chan result, 1)
	go func() {
		ch <- result{matched: re.MatchString(s)}
	}()

	select {
	case r := <-ch:
		if !r.matched {
			return fmt.Errorf("value %q does not match pattern %q", s, pattern)
		}
		return nil
	case <-time.After(time.Millisecond):
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
		dur, _ := parseDurationLocal(limit.Window)
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

// parseDurationLocal is a local copy of parseDuration to avoid depending on the unexported parser.go version.
func parseDurationLocal(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	switch s[i:] {
	case "s":
		return time.Duration(n) * time.Second, nil
	case "m":
		return time.Duration(n) * time.Minute, nil
	case "h":
		return time.Duration(n) * time.Hour, nil
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown duration unit %q in %q", s[i:], s)
	}
}

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

package convention

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockTransport implements ExecutorTransport for testing.
type mockTransport struct {
	sentMessages []sentMessage
	readResults  []MessageRecord
	futureResult []byte
	futureErr    error
	futureDelay  time.Duration
}

type sentMessage struct {
	campfireID  string
	payload     []byte
	tags        []string
	antecedents []string
	campfireKey bool
}

func (m *mockTransport) SendMessage(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (string, error) {
	m.sentMessages = append(m.sentMessages, sentMessage{
		campfireID:  campfireID,
		payload:     payload,
		tags:        tags,
		antecedents: antecedents,
		campfireKey: false,
	})
	return "msg-sent-" + campfireID, nil
}

func (m *mockTransport) SendCampfireKeySigned(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (string, error) {
	m.sentMessages = append(m.sentMessages, sentMessage{
		campfireID:  campfireID,
		payload:     payload,
		tags:        tags,
		antecedents: antecedents,
		campfireKey: true,
	})
	return "msg-ck-" + campfireID, nil
}

func (m *mockTransport) ReadMessages(ctx context.Context, campfireID string, tags []string) ([]MessageRecord, error) {
	return m.readResults, nil
}

func (m *mockTransport) SendFutureAndAwait(ctx context.Context, campfireID string, payload []byte, tags []string, timeout time.Duration) ([]byte, error) {
	if m.futureDelay > 0 {
		select {
		case <-time.After(m.futureDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.futureResult, m.futureErr
}

// socialPostDecl returns the §16.1 Declaration.
func socialPostDecl() *Declaration {
	decl, _, err := Parse(tags("convention:operation"), socialPostPayload, testSenderKey, testCampfireKey)
	if err != nil {
		panic("socialPostDecl: " + err.Error())
	}
	return decl
}

// voteDecl returns a vote/upvote Declaration (§16.2 style).
func voteDecl() *Declaration {
	payload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.3",
		"operation":   "upvote",
		"description": "Upvote a post",
		"antecedents": "exactly_one(target)",
		"produces_tags": []any{
			map[string]any{"tag": "social:upvote", "cardinality": "exactly_one"},
		},
		"args": []any{
			map[string]any{"name": "target_msg_id", "type": "message_id", "required": true},
			map[string]any{
				"name":   "direction",
				"type":   "enum",
				"values": []any{"social:upvote", "social:downvote"},
			},
		},
		"signing": "member_key",
	})
	decl, _, err := Parse(tags("convention:operation"), payload, testSenderKey, testCampfireKey)
	if err != nil {
		panic("voteDecl: " + err.Error())
	}
	return decl
}

// profileUpdateDecl returns a multi-step profile update Declaration (§16.4 style).
func profileUpdateDecl() *Declaration {
	payload := mustJSON(map[string]any{
		"convention":  "profile-management",
		"version":     "0.1",
		"operation":   "update-profile",
		"description": "Update user profile with lookup",
		"signing":     "member_key",
		"steps": []any{
			map[string]any{
				"action":         "query",
				"description":    "Look up current profile",
				"future_tags":    []any{"profile:lookup"},
				"result_binding": "current",
			},
			map[string]any{
				"action":      "send",
				"description": "Send updated profile",
				"tags":        []any{"profile:update"},
				"antecedents": []any{"$current.msg_id"},
			},
		},
	})
	decl, _, err := Parse(tags("convention:operation"), payload, testSenderKey, testCampfireKey)
	if err != nil {
		panic("profileUpdateDecl: " + err.Error())
	}
	return decl
}

// campfireKeyDecl returns a campfire_key-signed declaration.
func campfireKeyDecl() *Declaration {
	payload := mustJSON(map[string]any{
		"convention":  "beacon-registry",
		"version":     "0.1",
		"operation":   "register",
		"description": "Register a beacon",
		"signing":     "campfire_key",
		"produces_tags": []any{
			map[string]any{"tag": "beacon:registered", "cardinality": "exactly_one"},
		},
	})
	key := "same-key-hex"
	decl, _, err := Parse(tags("convention:operation"), payload, key, key)
	if err != nil {
		panic("campfireKeyDecl: " + err.Error())
	}
	return decl
}

// selfPriorDecl returns a declaration with antecedents=exactly_one(self_prior).
func selfPriorDecl() *Declaration {
	payload := mustJSON(map[string]any{
		"convention":  "status-update",
		"version":     "0.1",
		"operation":   "update",
		"description": "Update status, replacing prior",
		"antecedents": "exactly_one(self_prior)",
		"produces_tags": []any{
			map[string]any{"tag": "status:update", "cardinality": "exactly_one"},
		},
		"signing": "member_key",
	})
	decl, _, err := Parse(tags("convention:operation"), payload, testSenderKey, testCampfireKey)
	if err != nil {
		panic("selfPriorDecl: " + err.Error())
	}
	return decl
}

// TestExecute_SocialPost verifies the §16.1 social post path.
func TestExecute_SocialPost(t *testing.T) {
	tr := &mockTransport{}
	ex := NewExecutor(tr, testSenderKey)
	decl := socialPostDecl()

	args := map[string]any{
		"text":   "hello",
		"topics": []string{"ai"},
	}

	if err := ex.Execute(context.Background(), decl, "cf-abc", args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tr.sentMessages) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(tr.sentMessages))
	}
	msg := tr.sentMessages[0]
	if msg.campfireID != "cf-abc" {
		t.Errorf("campfireID = %q, want %q", msg.campfireID, "cf-abc")
	}

	// Check tags include social:post and topic:ai
	tagsMap := make(map[string]bool)
	for _, tg := range msg.tags {
		tagsMap[tg] = true
	}
	if !tagsMap["social:post"] {
		t.Errorf("expected tag social:post; got %v", msg.tags)
	}
	if !tagsMap["topic:ai"] {
		t.Errorf("expected tag topic:ai; got %v", msg.tags)
	}

	// Check payload contains "hello"
	if !strings.Contains(string(msg.payload), "hello") {
		t.Errorf("expected payload to contain 'hello'; got %s", msg.payload)
	}
}

// TestExecute_Vote verifies the §16.2 vote path.
func TestExecute_Vote(t *testing.T) {
	tr := &mockTransport{}
	ex := NewExecutor(tr, testSenderKey)
	decl := voteDecl()

	args := map[string]any{
		"target_msg_id": "msg-123",
		"direction":     "social:upvote",
	}

	if err := ex.Execute(context.Background(), decl, "cf-vote", args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tr.sentMessages) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(tr.sentMessages))
	}
	msg := tr.sentMessages[0]
	if len(msg.antecedents) == 0 || msg.antecedents[0] != "msg-123" {
		t.Errorf("expected antecedents=[msg-123]; got %v", msg.antecedents)
	}
	tagsMap := make(map[string]bool)
	for _, tg := range msg.tags {
		tagsMap[tg] = true
	}
	if !tagsMap["social:upvote"] {
		t.Errorf("expected tag social:upvote; got %v", msg.tags)
	}
}

// TestExecute_MissingRequiredArg verifies required arg enforcement.
func TestExecute_MissingRequiredArg(t *testing.T) {
	tr := &mockTransport{}
	ex := NewExecutor(tr, testSenderKey)
	decl := socialPostDecl() // "text" is required

	err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing required arg 'text'")
	}
	if !strings.Contains(err.Error(), "text") {
		t.Errorf("error should mention 'text'; got %v", err)
	}
}

// TestExecute_MaxLengthExceeded verifies max_length enforcement.
func TestExecute_MaxLengthExceeded(t *testing.T) {
	tr := &mockTransport{}
	ex := NewExecutor(tr, testSenderKey)
	decl := socialPostDecl() // text has max_length=65536

	longText := strings.Repeat("a", 70000)
	err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{"text": longText})
	if err == nil {
		t.Fatal("expected error for text exceeding max_length")
	}
}

// TestExecute_PatternMismatch verifies pattern validation.
func TestExecute_PatternMismatch(t *testing.T) {
	tr := &mockTransport{}
	ex := NewExecutor(tr, testSenderKey)
	decl := socialPostDecl() // topics has pattern [a-z0-9-]{1,64}

	err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{
		"text":   "hello",
		"topics": []string{"INVALID!"},
	})
	if err == nil {
		t.Fatal("expected error for pattern mismatch on topics")
	}
}

// TestExecute_EnumInvalid verifies enum validation.
func TestExecute_EnumInvalid(t *testing.T) {
	tr := &mockTransport{}
	ex := NewExecutor(tr, testSenderKey)
	decl := socialPostDecl() // content_type enum: text/plain, text/markdown, application/json

	err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{
		"text":         "hello",
		"content_type": "application/xml",
	})
	if err == nil {
		t.Fatal("expected error for invalid enum value")
	}
}

// TestExecute_TagDenylist verifies denylist enforcement on composed tags.
func TestExecute_TagDenylist(t *testing.T) {
	// Build a declaration that would produce "future" tag.
	// We bypass Parse by constructing the Declaration directly.
	decl := &Declaration{
		Convention:  "test",
		Version:     "0.1",
		Operation:   "bad",
		Signing:     "member_key",
		Antecedents: "none",
		ProducesTags: []TagRule{
			{Tag: "future", Cardinality: "exactly_one"},
		},
	}

	tr := &mockTransport{}
	ex := NewExecutor(tr, testSenderKey)
	err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{})
	if err == nil {
		t.Fatal("expected error for denylist tag 'future'")
	}
	if !strings.Contains(err.Error(), "denylist") && !strings.Contains(err.Error(), "reserved") && !strings.Contains(err.Error(), "denied") {
		t.Errorf("error should mention denylist/reserved/denied; got %v", err)
	}
}

// TestExecute_RateLimitExceeded verifies rate limiting within a single executor.
func TestExecute_RateLimitExceeded(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "test",
		"version":    "0.1",
		"operation":  "limited-exceeded",
		"signing":    "member_key",
		"rate_limit": map[string]any{
			"max":    2,
			"per":    "sender",
			"window": "1m",
		},
	})
	decl, _, err := Parse(tags("convention:operation"), payload, testSenderKey, testCampfireKey)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	tr := &mockTransport{}
	// Use an isolated limiter so this test is not affected by other tests' state.
	ex := NewExecutorWithLimiter(tr, testSenderKey, newRateLimiter())

	if err := ex.Execute(context.Background(), decl, "cf-rl-exceeded", map[string]any{}); err != nil {
		t.Fatalf("call 1 unexpected error: %v", err)
	}
	if err := ex.Execute(context.Background(), decl, "cf-rl-exceeded", map[string]any{}); err != nil {
		t.Fatalf("call 2 unexpected error: %v", err)
	}
	err = ex.Execute(context.Background(), decl, "cf-rl-exceeded", map[string]any{})
	if err == nil {
		t.Fatal("expected error on 3rd call (rate limit exceeded)")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("error should mention 'rate limit'; got %v", err)
	}
}

// TestExecute_CampfireKeyOp verifies campfire_key uses SendCampfireKeySigned.
func TestExecute_CampfireKeyOp(t *testing.T) {
	tr := &mockTransport{}
	ex := NewExecutor(tr, testSenderKey)
	decl := campfireKeyDecl()

	if err := ex.Execute(context.Background(), decl, "cf-ck", map[string]any{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tr.sentMessages) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(tr.sentMessages))
	}
	if !tr.sentMessages[0].campfireKey {
		t.Error("expected SendCampfireKeySigned to be called")
	}
}

// TestExecute_MultiStep_ProfileUpdate verifies multi-step execution binding.
func TestExecute_MultiStep_ProfileUpdate(t *testing.T) {
	// futureResult simulates profile lookup returning a message with an ID.
	futurePayload, _ := json.Marshal(map[string]any{"msg_id": "prior-profile-msg"})
	tr := &mockTransport{
		futureResult: futurePayload,
	}
	ex := NewExecutor(tr, testSenderKey)
	decl := profileUpdateDecl()

	if err := ex.Execute(context.Background(), decl, "cf-profile", map[string]any{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The second step should be a send with antecedent from the binding.
	if len(tr.sentMessages) != 1 {
		t.Fatalf("expected 1 sent message (step 2), got %d", len(tr.sentMessages))
	}
	msg := tr.sentMessages[0]
	if len(msg.antecedents) == 0 || msg.antecedents[0] != "prior-profile-msg" {
		t.Errorf("expected antecedent 'prior-profile-msg'; got %v", msg.antecedents)
	}
	tagsMap := make(map[string]bool)
	for _, tg := range msg.tags {
		tagsMap[tg] = true
	}
	if !tagsMap["profile:update"] {
		t.Errorf("expected tag profile:update; got %v", msg.tags)
	}
}

// TestExecute_MultiStep_Timeout verifies per-step timeout enforcement.
func TestExecute_MultiStep_Timeout(t *testing.T) {
	tr := &mockTransport{
		futureDelay: 35 * time.Second,
	}
	ex := NewExecutor(tr, testSenderKey)
	decl := profileUpdateDecl()

	ctx := context.Background()
	err := ex.Execute(ctx, decl, "cf-timeout", map[string]any{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "context") {
		t.Errorf("error should indicate timeout; got %v", err)
	}
}

// TestExecute_RateLimitSenderAndCampfire verifies per=sender_and_campfire_id scopes
// each sender×campfire pair independently. Two different senders should not share a quota.
func TestExecute_RateLimitSenderAndCampfire(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "test",
		"version":    "0.1",
		"operation":  "limited-combined",
		"signing":    "member_key",
		"rate_limit": map[string]any{
			"max":    1,
			"per":    "sender_and_campfire_id",
			"window": "1m",
		},
	})
	declA, _, err := Parse(tags("convention:operation"), payload, "senderA", "campfireX")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	declB, _, err := Parse(tags("convention:operation"), payload, "senderB", "campfireX")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// Use a shared isolated limiter (same as what the singleton provides in production)
	// to verify that sender-key scoping still isolates quotas correctly.
	sharedLimiter := newRateLimiter()
	trA := &mockTransport{}
	exA := NewExecutorWithLimiter(trA, "senderA", sharedLimiter)
	trB := &mockTransport{}
	exB := NewExecutorWithLimiter(trB, "senderB", sharedLimiter)

	// senderA uses their 1 quota on campfireX.
	if err := exA.Execute(context.Background(), declA, "campfireX", map[string]any{}); err != nil {
		t.Fatalf("senderA call 1: %v", err)
	}
	// senderA second call should be rate-limited.
	if err := exA.Execute(context.Background(), declA, "campfireX", map[string]any{}); err == nil {
		t.Fatal("expected senderA to be rate-limited on 2nd call")
	}
	// senderB has a separate quota — should succeed even with shared limiter.
	if err := exB.Execute(context.Background(), declB, "campfireX", map[string]any{}); err != nil {
		t.Fatalf("senderB call 1 should not be rate-limited: %v", err)
	}
}

// TestExecute_RateLimitSharedAcrossExecutors is the regression test for the bug where
// a new rate limiter was created per Executor, allowing the same sender to bypass rate
// limits by constructing a new Executor (as the CLI does on every invocation).
//
// This test calls NewExecutor (the real CLI path — not NewExecutorWithLimiter) to prove
// that the globalRateLimiterOnce singleton is actually wired. Two separate NewExecutor()
// calls must share limiter state so the second invocation is throttled after the first
// saturates the quota.
//
// Isolation: the singleton is reset before the test and restored on cleanup so that
// this test does not bleed state into other tests (or inherit state from them).
func TestExecute_RateLimitSharedAcrossExecutors(t *testing.T) {
	// Reset the process-level singleton so this test starts with a clean slate,
	// regardless of what other tests have executed. Restore on cleanup.
	origLimiter := globalRateLimiter
	origOnce := globalRateLimiterOnce
	globalRateLimiter = nil
	globalRateLimiterOnce = sync.Once{}
	t.Cleanup(func() {
		globalRateLimiter = origLimiter
		globalRateLimiterOnce = origOnce
	})

	payload := mustJSON(map[string]any{
		"convention": "test",
		"version":    "0.1",
		"operation":  "limited-shared",
		"signing":    "member_key",
		"rate_limit": map[string]any{
			"max":    1,
			"per":    "sender",
			"window": "1m",
		},
	})
	decl, _, err := Parse(tags("convention:operation"), payload, testSenderKey, testCampfireKey)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// First "invocation": NewExecutor uses sharedRateLimiter() — initialises the singleton.
	tr1 := &mockTransport{}
	ex1 := NewExecutor(tr1, testSenderKey)
	if err := ex1.Execute(context.Background(), decl, "cf-shared-rl", map[string]any{}); err != nil {
		t.Fatalf("first invocation unexpected error: %v", err)
	}

	// Second "invocation": a new Executor constructed via NewExecutor (as the CLI does).
	// It must pick up the same singleton and be throttled.
	tr2 := &mockTransport{}
	ex2 := NewExecutor(tr2, testSenderKey)
	err = ex2.Execute(context.Background(), decl, "cf-shared-rl", map[string]any{})
	if err == nil {
		t.Fatal("second invocation should be rate-limited because the first invocation saturated the quota")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("error should mention 'rate limit'; got %v", err)
	}

	// Verify both executors reference the same singleton (belt-and-suspenders).
	if ex1.rateLimiter != ex2.rateLimiter {
		t.Error("ex1 and ex2 should share the same rateLimiter singleton")
	}
}

// TestExecute_SelfPriorAntecedent verifies self_prior antecedent resolution.
func TestExecute_SelfPriorAntecedent(t *testing.T) {
	tr := &mockTransport{
		readResults: []MessageRecord{
			{ID: "prior-msg-999", Sender: testSenderKey, Tags: []string{"status:update"}},
		},
	}
	ex := NewExecutor(tr, testSenderKey)
	decl := selfPriorDecl()

	if err := ex.Execute(context.Background(), decl, "cf-self-prior", map[string]any{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tr.sentMessages) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(tr.sentMessages))
	}
	msg := tr.sentMessages[0]
	if len(msg.antecedents) == 0 || msg.antecedents[0] != "prior-msg-999" {
		t.Errorf("expected antecedent 'prior-msg-999'; got %v", msg.antecedents)
	}
}

// intRangeDecl builds a Declaration with a single integer arg with the given Min/Max.
func intRangeDecl(min, max int) *Declaration {
	return &Declaration{
		Convention:  "test",
		Version:     "0.1",
		Operation:   "int-range-op",
		Signing:     "member_key",
		Antecedents: "none",
		ProducesTags: []TagRule{
			{Tag: "test:int-range-op", Cardinality: "exactly_one"},
		},
		Args: []ArgDescriptor{
			{Name: "count", Type: "integer", Required: true, Min: min, Max: max},
		},
	}
}

// TestIntegerRange_MaxEnforcedWhenMinIsZero verifies that Max is enforced even when Min=0.
// Regression test for the bug where `if desc.Min != 0 || desc.Max != 0` short-circuited
// Max enforcement when Min was explicitly set to zero (the zero value of int).
func TestIntegerRange_MaxEnforcedWhenMinIsZero(t *testing.T) {
	tr := &mockTransport{}
	ex := NewExecutorWithLimiter(tr, testSenderKey, newRateLimiter())
	decl := intRangeDecl(0, 10)

	// Value within range should pass.
	if err := ex.Execute(context.Background(), decl, "cf-test", map[string]any{"count": 5}); err != nil {
		t.Errorf("value=5 with Min=0,Max=10: expected no error, got %v", err)
	}

	// Value exceeding Max should fail.
	if err := ex.Execute(context.Background(), decl, "cf-test", map[string]any{"count": 15}); err == nil {
		t.Error("value=15 with Min=0,Max=10: expected range error, got nil")
	}
}

// TestIntegerRange_NegativeRejectedWhenMinIsZero verifies that negative values are
// rejected when Min=0 (no Max set). Before the fix, the guard `if desc.Min != 0 || desc.Max != 0`
// evaluated false for Min=0,Max=0, skipping all range validation.
func TestIntegerRange_NegativeRejectedWhenMinIsZero(t *testing.T) {
	tr := &mockTransport{}
	ex := NewExecutorWithLimiter(tr, testSenderKey, newRateLimiter())
	decl := intRangeDecl(0, 0) // Min=0, no Max

	// Zero should pass.
	if err := ex.Execute(context.Background(), decl, "cf-test", map[string]any{"count": 0}); err != nil {
		t.Errorf("value=0 with Min=0: expected no error, got %v", err)
	}

	// Negative value should fail.
	if err := ex.Execute(context.Background(), decl, "cf-test", map[string]any{"count": -1}); err == nil {
		t.Error("value=-1 with Min=0: expected range error, got nil")
	}
}

// TestCollectArgValuesForPrefix_NamingNameGlob verifies that the naming:name:* glob
// correctly matches a single-arg "name" through the HasSuffix fallback.
func TestCollectArgValuesForPrefix_NamingNameGlob(t *testing.T) {
	args := []ArgDescriptor{
		{Name: "name", Type: "string"},
	}
	provided := map[string]any{"name": "social"}
	got := collectArgValuesForPrefix(args, provided, "naming:name:")
	if len(got) != 1 || got[0] != "naming:name:social" {
		t.Errorf("expected [naming:name:social], got %v", got)
	}
}

package convention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockTransport implements executorTransport for testing.
type mockTransport struct {
	sentMessages []sentMessage
	futureCalls  []sentMessage // records sendFutureAndAwait calls separately
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

func (m *mockTransport) sendMessage(_ context.Context, campfireID string, payload []byte, tags []string, antecedents []string, campfireKey bool) (string, error) {
	m.sentMessages = append(m.sentMessages, sentMessage{
		campfireID:  campfireID,
		payload:     payload,
		tags:        tags,
		antecedents: antecedents,
		campfireKey: campfireKey,
	})
	if campfireKey {
		return "msg-ck-" + campfireID, nil
	}
	return "msg-sent-" + campfireID, nil
}

func (m *mockTransport) readMessages(_ context.Context, _ string, _ []string) ([]MessageRecord, error) {
	return m.readResults, nil
}

func (m *mockTransport) sendFutureAndAwait(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string, _ time.Duration) (string, []byte, error) {
	// Record in both sentMessages (so existing tests that check sentMessages see the call)
	// and futureCalls (so tests can distinguish futures from normal sends).
	call := sentMessage{
		campfireID:  campfireID,
		payload:     payload,
		tags:        tags,
		antecedents: antecedents,
	}
	m.futureCalls = append(m.futureCalls, call)
	m.sentMessages = append(m.sentMessages, call)
	if m.futureDelay > 0 {
		select {
		case <-time.After(m.futureDelay):
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
	}
	msgID := "future-msg-" + campfireID
	return msgID, m.futureResult, m.futureErr
}

// socialPostDecl returns the §16.1 Declaration.
func socialPostDecl() *Declaration {
	decl, _, err := Parse(tags(ConventionOperationTag), socialPostPayload, testSenderKey, testCampfireKey)
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
	decl, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
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
	decl, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
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
	decl, _, err := Parse(tags(ConventionOperationTag), payload, key, key)
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
	decl, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err != nil {
		panic("selfPriorDecl: " + err.Error())
	}
	return decl
}

// TestExecute_SocialPost verifies the §16.1 social post path.
func TestExecute_SocialPost(t *testing.T) {
	tr := &mockTransport{}
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := socialPostDecl()

	args := map[string]any{
		"text":   "hello",
		"topics": []string{"ai"},
	}

	if _, err := ex.Execute(context.Background(), decl, "cf-abc", args); err != nil {
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
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := voteDecl()

	args := map[string]any{
		"target_msg_id": "msg-123",
		"direction":     "social:upvote",
	}

	if _, err := ex.Execute(context.Background(), decl, "cf-vote", args); err != nil {
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
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := socialPostDecl() // "text" is required

	_, err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{})
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
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := socialPostDecl() // text has max_length=65536

	longText := strings.Repeat("a", 70000)
	_, err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{"text": longText})
	if err == nil {
		t.Fatal("expected error for text exceeding max_length")
	}
}

// TestExecute_PatternMismatch verifies pattern validation.
func TestExecute_PatternMismatch(t *testing.T) {
	tr := &mockTransport{}
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := socialPostDecl() // topics has pattern [a-z0-9-]{1,64}

	_, err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{
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
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := socialPostDecl() // content_type enum: text/plain, text/markdown, application/json

	_, err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{
		"text":         "hello",
		"content_type": "application/xml",
	})
	if err == nil {
		t.Fatal("expected error for invalid enum value")
	}
}

// TestExecute_EnumRejectsShortForm verifies that the executor requires the full
// tag-prefixed enum value. Short-form expansion is a CLI-layer concern, not an
// executor concern — the executor must see canonical values so tag composition works.
func TestExecute_EnumRejectsShortForm(t *testing.T) {
	tr := &mockTransport{}
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := voteDecl() // direction enum: social:upvote, social:downvote

	_, err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{
		"target_msg_id": "msg-123",
		"direction":     "upvote", // short form — should be rejected
	})
	if err == nil {
		t.Fatal("executor should reject short enum form 'upvote'")
	}
	if !strings.Contains(err.Error(), "not in enum") {
		t.Errorf("expected 'not in enum' error, got: %v", err)
	}
}

// TestExecute_EnumFullFormAccepted verifies that the full tag-prefixed enum value
// passes validation and produces the correct tag.
func TestExecute_EnumFullFormAccepted(t *testing.T) {
	tr := &mockTransport{}
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := voteDecl() // direction enum: social:upvote, social:downvote

	_, err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{
		"target_msg_id": "msg-123",
		"direction":     "social:upvote", // full form
	})
	if err != nil {
		t.Fatalf("full enum form should be accepted: %v", err)
	}
	if len(tr.sentMessages) == 0 {
		t.Fatal("expected a sent message")
	}
	// Verify the tag was composed correctly.
	msg := tr.sentMessages[len(tr.sentMessages)-1]
	foundTag := false
	for _, tag := range msg.tags {
		if tag == "social:upvote" {
			foundTag = true
		}
	}
	if !foundTag {
		t.Errorf("expected social:upvote tag, got tags: %v", msg.tags)
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
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	_, err := ex.Execute(context.Background(), decl, "cf-abc", map[string]any{})
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
	decl, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	tr := &mockTransport{}
	// Use an isolated limiter so this test is not affected by other tests' state.
	ex := newExecutorWithLimiter(tr, testSenderKey, newRateLimiter())

	if _, err := ex.Execute(context.Background(), decl, "cf-rl-exceeded", map[string]any{}); err != nil {
		t.Fatalf("call 1 unexpected error: %v", err)
	}
	if _, err := ex.Execute(context.Background(), decl, "cf-rl-exceeded", map[string]any{}); err != nil {
		t.Fatalf("call 2 unexpected error: %v", err)
	}
	_, err = ex.Execute(context.Background(), decl, "cf-rl-exceeded", map[string]any{})
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
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := campfireKeyDecl()

	if _, err := ex.Execute(context.Background(), decl, "cf-ck", map[string]any{}); err != nil {
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
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := profileUpdateDecl()

	if _, err := ex.Execute(context.Background(), decl, "cf-profile", map[string]any{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The second step should be a send with antecedent from the binding.
	// sentMessages includes both the query future (step 1) and the send (step 2).
	// We want the non-future send: filter by checking sentMessages not in futureCalls.
	normalSends := len(tr.sentMessages) - len(tr.futureCalls)
	if normalSends != 1 {
		t.Fatalf("expected 1 normal sent message (step 2), got %d (total=%d, futures=%d)",
			normalSends, len(tr.sentMessages), len(tr.futureCalls))
	}
	// The normal send is the last message in sentMessages (after the future).
	msg := tr.sentMessages[len(tr.sentMessages)-1]
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
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := profileUpdateDecl()

	ctx := context.Background()
	_, err := ex.Execute(ctx, decl, "cf-timeout", map[string]any{})
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
	declA, _, err := Parse(tags(ConventionOperationTag), payload, "senderA", "campfireX")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	declB, _, err := Parse(tags(ConventionOperationTag), payload, "senderB", "campfireX")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// Use a shared isolated limiter (same as what the singleton provides in production)
	// to verify that sender-key scoping still isolates quotas correctly.
	sharedLimiter := newRateLimiter()
	trA := &mockTransport{}
	exA := newExecutorWithLimiter(trA, "senderA", sharedLimiter)
	trB := &mockTransport{}
	exB := newExecutorWithLimiter(trB, "senderB", sharedLimiter)

	// senderA uses their 1 quota on campfireX.
	if _, err := exA.Execute(context.Background(), declA, "campfireX", map[string]any{}); err != nil {
		t.Fatalf("senderA call 1: %v", err)
	}
	// senderA second call should be rate-limited.
	if _, err := exA.Execute(context.Background(), declA, "campfireX", map[string]any{}); err == nil {
		t.Fatal("expected senderA to be rate-limited on 2nd call")
	}
	// senderB has a separate quota — should succeed even with shared limiter.
	if _, err := exB.Execute(context.Background(), declB, "campfireX", map[string]any{}); err != nil {
		t.Fatalf("senderB call 1 should not be rate-limited: %v", err)
	}
}

// TestExecute_RateLimitSharedAcrossExecutors is the regression test for the bug where
// a new rate limiter was created per Executor, allowing the same sender to bypass rate
// limits by constructing a new Executor (as the CLI does on every invocation).
//
// This test calls newExecutorWithSharedLimiter (which mirrors the real production path)
// to prove that the globalRateLimiterOnce singleton is actually wired. Two separate
// executor instances must share limiter state so the second invocation is throttled
// after the first saturates the quota.
//
// Isolation: the singleton is reset before the test and restored on cleanup so that
// this test does not bleed state into other tests (or inherit state from them).
func TestExecute_RateLimitSharedAcrossExecutors(t *testing.T) {
	// Reset the process-level singleton so this test starts with a clean slate,
	// regardless of what other tests have executed. Restore on cleanup.
	// We do NOT copy sync.Once by value (go vet rejects it); instead we save
	// only the limiter pointer and reset the Once to a fresh value in both
	// the setup and the cleanup.
	origLimiter := globalRateLimiter
	globalRateLimiter = nil
	globalRateLimiterOnce = sync.Once{}
	t.Cleanup(func() {
		globalRateLimiter = origLimiter
		globalRateLimiterOnce = sync.Once{}
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
	decl, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// First "invocation": newExecutorWithSharedLimiter uses sharedRateLimiter() — initialises the singleton.
	tr1 := &mockTransport{}
	ex1 := newExecutorWithSharedLimiter(tr1, testSenderKey)
	if _, err := ex1.Execute(context.Background(), decl, "cf-shared-rl", map[string]any{}); err != nil {
		t.Fatalf("first invocation unexpected error: %v", err)
	}

	// Second "invocation": a new Executor constructed via newExecutorWithSharedLimiter (as the CLI does).
	// It must pick up the same singleton and be throttled.
	tr2 := &mockTransport{}
	ex2 := newExecutorWithSharedLimiter(tr2, testSenderKey)
	_, err = ex2.Execute(context.Background(), decl, "cf-shared-rl", map[string]any{})
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
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := selfPriorDecl()

	if _, err := ex.Execute(context.Background(), decl, "cf-self-prior", map[string]any{}); err != nil {
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

// zeroOrOneSelfPriorDecl returns a declaration with antecedents=zero_or_one(self_prior).
// Models the rate-publish operation: genesis has no antecedent, subsequent ones do.
func zeroOrOneSelfPriorDecl() *Declaration {
	payload := mustJSON(map[string]any{
		"convention":  "dontguess-exchange",
		"version":     "0.1",
		"operation":   "scrip:rate-publish",
		"description": "Publish x402-to-scrip rate",
		"antecedents": "zero_or_one(self_prior)",
		"produces_tags": []any{
			map[string]any{"tag": "dontguess:scrip-rate", "cardinality": "exactly_one"},
		},
		"signing": "campfire_key",
	})
	key := "campfire-key-xyz"
	decl, _, err := Parse(tags(ConventionOperationTag), payload, key, key)
	if err != nil {
		panic("zeroOrOneSelfPriorDecl: " + err.Error())
	}
	return decl
}

// TestExecute_ZeroOrOneSelfPrior_Genesis verifies that the first rate-publish
// (no prior message on campfire) sends with no antecedents — the genesis case.
func TestExecute_ZeroOrOneSelfPrior_Genesis(t *testing.T) {
	// No prior messages on campfire.
	tr := &mockTransport{readResults: nil}
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := zeroOrOneSelfPriorDecl()

	if _, err := ex.Execute(context.Background(), decl, "cf-rate", map[string]any{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tr.sentMessages) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(tr.sentMessages))
	}
	msg := tr.sentMessages[0]
	if len(msg.antecedents) != 0 {
		t.Errorf("genesis rate-publish: expected no antecedents; got %v", msg.antecedents)
	}
}

// TestExecute_ZeroOrOneSelfPrior_Subsequent verifies that a subsequent rate-publish
// (prior message exists) sends with the prior message ID as antecedent.
func TestExecute_ZeroOrOneSelfPrior_Subsequent(t *testing.T) {
	// One prior rate-publish from self already on campfire.
	tr := &mockTransport{
		readResults: []MessageRecord{
			{ID: "rate-msg-001", Sender: testSenderKey, Tags: []string{"dontguess-exchange:scrip:rate-publish"}},
		},
	}
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := zeroOrOneSelfPriorDecl()

	if _, err := ex.Execute(context.Background(), decl, "cf-rate", map[string]any{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tr.sentMessages) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(tr.sentMessages))
	}
	msg := tr.sentMessages[0]
	if len(msg.antecedents) == 0 || msg.antecedents[0] != "rate-msg-001" {
		t.Errorf("subsequent rate-publish: expected antecedent 'rate-msg-001'; got %v", msg.antecedents)
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
	ex := newExecutorWithLimiter(tr, testSenderKey, newRateLimiter())
	decl := intRangeDecl(0, 10)

	// Value within range should pass.
	if _, err := ex.Execute(context.Background(), decl, "cf-test", map[string]any{"count": 5}); err != nil {
		t.Errorf("value=5 with Min=0,Max=10: expected no error, got %v", err)
	}

	// Value exceeding Max should fail.
	if _, err := ex.Execute(context.Background(), decl, "cf-test", map[string]any{"count": 15}); err == nil {
		t.Error("value=15 with Min=0,Max=10: expected range error, got nil")
	}
}

// TestIntegerRange_NegativeAllowedWhenMinUndeclared is a regression test for
// campfire-agent-bnq: when no "min" is declared, negative values must be allowed.
// The bug was that Min's zero value (int) imposed an implicit floor of 0.
// intRangeDecl builds a struct literal with Min:0 and MinSet:false (not via JSON),
// so the executor must not enforce any lower bound.
func TestIntegerRange_NegativeAllowedWhenMinUndeclared(t *testing.T) {
	tr := &mockTransport{}
	ex := newExecutorWithLimiter(tr, testSenderKey, newRateLimiter())
	decl := intRangeDecl(0, 0) // Min=0, MinSet=false, no Max

	// Zero should pass.
	if _, err := ex.Execute(context.Background(), decl, "cf-test", map[string]any{"count": 0}); err != nil {
		t.Errorf("value=0 with min undeclared: expected no error, got %v", err)
	}

	// Negative must also pass — min is not declared so no floor applies.
	if _, err := ex.Execute(context.Background(), decl, "cf-test", map[string]any{"count": -1}); err != nil {
		t.Errorf("value=-1 with min undeclared: expected no error, got %v", err)
	}
}

// TestIntegerRange_NegativeRejectedWhenMinDeclaredZero verifies that when "min":0
// is explicitly present in the convention JSON, negative values are rejected.
// Uses JSON round-trip to ensure ArgDescriptor.MinSet is populated.
func TestIntegerRange_NegativeRejectedWhenMinDeclaredZero(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention":  "test",
		"version":     "0.1",
		"operation":   "int-min-zero-op",
		"signing":     "member_key",
		"antecedents": "none",
		"produces_tags": []any{
			map[string]any{"tag": "test:int-min-zero-op", "cardinality": "exactly_one"},
		},
		"args": []any{
			map[string]any{"name": "count", "type": "integer", "required": true, "min": 0},
		},
	})
	decl, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	tr := &mockTransport{}
	ex := newExecutorWithLimiter(tr, testSenderKey, newRateLimiter())

	// Zero should pass (at boundary).
	if _, err := ex.Execute(context.Background(), decl, "cf-test", map[string]any{"count": 0}); err != nil {
		t.Errorf("value=0 with min:0 declared: expected no error, got %v", err)
	}

	// Negative must fail — min was explicitly declared as 0.
	if _, err := ex.Execute(context.Background(), decl, "cf-test", map[string]any{"count": -1}); err == nil {
		t.Error("value=-1 with min:0 declared: expected error, got nil")
	}
}

// TestMatchPattern_TimeoutIsReasonable is a regression test for campfire-agent-3bx:
// the previous 1ms timeout caused false rejections under CPU load. Verify that
// simple patterns complete successfully even under repeated invocations.
func TestMatchPattern_TimeoutIsReasonable(t *testing.T) {
	for i := 0; i < 50; i++ {
		if err := matchPattern(`[a-z]+`, "hello"); err != nil {
			t.Fatalf("iteration %d: matchPattern returned unexpected error: %v", i, err)
		}
	}
}

// TestMatchPattern_GoroutineExitsOnTimeout is a regression test for campfire-agent-i3p:
// the goroutine spawned by matchPattern must not leak after the function returns.
// The done channel ensures the goroutine exits when the caller times out.
// We verify the API contract: the function is reentrant and does not deadlock
// or exhaust resources after many sequential calls.
func TestMatchPattern_GoroutineExitsOnTimeout(t *testing.T) {
	for i := 0; i < 20; i++ {
		_ = matchPattern(`[a-z]+`, "hello")
	}
	if err := matchPattern(`[a-z]+`, "world"); err != nil {
		t.Errorf("matchPattern failed after repeated calls: %v", err)
	}
}

// TestValidateArgs_StripUndeclared verifies that undeclared args are stripped and
// only allow-listed (declared) args pass through validation.
func TestValidateArgs_StripUndeclared(t *testing.T) {
	descs := []ArgDescriptor{
		{Name: "text", Type: "string", Required: true},
		{Name: "channel", Type: "string"},
	}
	provided := map[string]any{
		"text":     "hello",
		"channel":  "general",
		"injected": "evil_value",  // undeclared — must be stripped
		"extra":    42,            // undeclared — must be stripped
	}

	resolved, err := validateArgs(descs, provided)
	if err != nil {
		t.Fatalf("validateArgs returned unexpected error: %v", err)
	}
	if _, ok := resolved["injected"]; ok {
		t.Error("undeclared arg 'injected' should have been stripped but was present in resolved args")
	}
	if _, ok := resolved["extra"]; ok {
		t.Error("undeclared arg 'extra' should have been stripped but was present in resolved args")
	}
	if v, ok := resolved["text"]; !ok || v != "hello" {
		t.Errorf("declared arg 'text' should be present with value 'hello', got %v (ok=%v)", v, ok)
	}
	if v, ok := resolved["channel"]; !ok || v != "general" {
		t.Errorf("declared arg 'channel' should be present with value 'general', got %v (ok=%v)", v, ok)
	}
	// Exactly declared args only.
	if len(resolved) != 2 {
		t.Errorf("expected 2 resolved args (declared only), got %d: %v", len(resolved), resolved)
	}
}

// TestValidateArgs_StripUndeclared_OnlyExtra verifies stripping when all provided args
// are undeclared — resolved map should be empty after stripping.
func TestValidateArgs_StripUndeclared_OnlyExtra(t *testing.T) {
	descs := []ArgDescriptor{
		{Name: "body", Type: "string"},
	}
	provided := map[string]any{
		"undeclared_key": "value",
	}

	resolved, err := validateArgs(descs, provided)
	if err != nil {
		t.Fatalf("validateArgs returned unexpected error: %v", err)
	}
	if _, ok := resolved["undeclared_key"]; ok {
		t.Error("undeclared arg 'undeclared_key' must be stripped")
	}
	if len(resolved) != 0 {
		t.Errorf("expected 0 resolved args after stripping all undeclared, got %d: %v", len(resolved), resolved)
	}
}

// TestValidateArgs_StripUndeclared_ViaExecute verifies that undeclared args are stripped
// end-to-end through the Executor.Execute path, not just via validateArgs directly.
func TestValidateArgs_StripUndeclared_ViaExecute(t *testing.T) {
	tr := &mockTransport{}
	ex := newExecutorWithLimiter(tr, testSenderKey, newRateLimiter())

	decl := &Declaration{
		Convention: "social",
		Operation:  "post",
		Args: []ArgDescriptor{
			{Name: "text", Type: "string", Required: true},
		},
		ProducesTags: []TagRule{
			{Tag: "social:post", Cardinality: "exactly_one"},
		},
	}

	_, err := ex.Execute(context.Background(), decl, "cf-testfire", map[string]any{
		"text":          "hello world",
		"injected_arg":  "should_not_appear",
	})
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if len(tr.sentMessages) == 0 {
		t.Fatal("expected a message to be sent")
	}

	// Decode the payload and verify injected_arg is absent.
	var payload map[string]any
	if err := json.Unmarshal(tr.sentMessages[0].payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, ok := payload["injected_arg"]; ok {
		t.Error("undeclared arg 'injected_arg' must not appear in outgoing message payload")
	}
	if v, ok := payload["text"]; !ok || v != "hello world" {
		t.Errorf("declared arg 'text' should be in payload with value 'hello world', got %v (ok=%v)", v, ok)
	}
}

// ---- ExecuteResult tests (response=sync/async/none) ----

// syncDecl returns a declaration with response="sync" explicitly set.
func syncDecl() *Declaration {
	payload := mustJSON(map[string]any{
		"convention":       "test-sync",
		"version":          "0.1",
		"operation":        "ask",
		"description":      "Ask and await a sync response",
		"signing":          "member_key",
		"response":         "sync",
		"response_timeout": "5s",
		"produces_tags": []any{
			map[string]any{"tag": "test-sync:ask", "cardinality": "exactly_one"},
		},
	})
	decl, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err != nil {
		panic("syncDecl: " + err.Error())
	}
	return decl
}

// asyncDecl returns a declaration with response="async" explicitly set.
func asyncDecl() *Declaration {
	payload := mustJSON(map[string]any{
		"convention":  "test-async",
		"version":     "0.1",
		"operation":   "fire",
		"description": "Fire-and-forget async operation",
		"signing":     "member_key",
		"response":    "async",
		"produces_tags": []any{
			map[string]any{"tag": "test-async:fire", "cardinality": "exactly_one"},
		},
	})
	decl, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err != nil {
		panic("asyncDecl: " + err.Error())
	}
	return decl
}

// noneDecl returns a declaration with response="none" explicitly set.
func noneDecl() *Declaration {
	payload := mustJSON(map[string]any{
		"convention":  "test-none",
		"version":     "0.1",
		"operation":   "emit",
		"description": "Emit with no response",
		"signing":     "member_key",
		"response":    "none",
		"produces_tags": []any{
			map[string]any{"tag": "test-none:emit", "cardinality": "exactly_one"},
		},
	})
	decl, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err != nil {
		panic("noneDecl: " + err.Error())
	}
	return decl
}

// TestExecuteResult_Sync verifies that a sync declaration returns ExecuteResult
// with the fulfillment payload in Response and MessageID populated from the sent message.
func TestExecuteResult_Sync(t *testing.T) {
	respPayload := []byte(`{"answer":"42"}`)
	tr := &mockTransport{futureResult: respPayload}
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := syncDecl()

	result, err := ex.Execute(context.Background(), decl, "cf-sync", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil ExecuteResult")
	}
	if string(result.Response) != string(respPayload) {
		t.Errorf("Response = %s, want %s", result.Response, respPayload)
	}
	// Sync path went through sendFutureAndAwait — should be recorded in futureCalls.
	if len(tr.futureCalls) != 1 {
		t.Errorf("expected 1 future call, got %d", len(tr.futureCalls))
	}
	// Tags must be composed correctly.
	foundTag := false
	for _, tag := range tr.futureCalls[0].tags {
		if tag == "test-sync:ask" {
			foundTag = true
		}
	}
	if !foundTag {
		t.Errorf("expected test-sync:ask tag in future call, got %v", tr.futureCalls[0].tags)
	}
	// MessageID must be populated on sync path from the returned msgID.
	if result.MessageID == "" {
		t.Error("expected non-empty MessageID on sync path")
	}
	wantMsgID := "future-msg-cf-sync"
	if result.MessageID != wantMsgID {
		t.Errorf("MessageID = %q, want %q", result.MessageID, wantMsgID)
	}
}

// TestExecuteResult_Sync_AntecedentsPassedThrough verifies that antecedents resolved
// by executeSingle are forwarded to sendFutureAndAwait on the sync path.
func TestExecuteResult_Sync_AntecedentsPassedThrough(t *testing.T) {
	// Provide a prior message so that self_prior antecedent resolution finds one.
	// The opTag for resolution is "test-sync-prior:ask" (convention:operation).
	priorMsg := MessageRecord{ID: "prior-sync-msg", Sender: testSenderKey, Tags: []string{"test-sync-prior:ask"}}
	respPayload := []byte(`{"ok":true}`)
	tr := &mockTransport{
		futureResult: respPayload,
		readResults:  []MessageRecord{priorMsg},
	}
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)

	// Build a sync decl with exactly_one(self_prior) antecedents so we can verify pass-through.
	syncSelfPriorPayload := mustJSON(map[string]any{
		"convention":  "test-sync-prior",
		"version":     "0.1",
		"operation":   "ask",
		"description": "Sync ask with self_prior antecedent",
		"args":        []any{},
		"response":    "sync",
		"antecedents": "exactly_one(self_prior)",
		"signing":     "member_key",
	})
	decl, _, err := Parse(tags(ConventionOperationTag), syncSelfPriorPayload, testSenderKey, testCampfireKey)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	result, err := ex.Execute(context.Background(), decl, "cf-sync-ant", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil ExecuteResult")
	}
	// The antecedent should have been passed to sendFutureAndAwait.
	if len(tr.futureCalls) != 1 {
		t.Fatalf("expected 1 future call, got %d", len(tr.futureCalls))
	}
	got := tr.futureCalls[0].antecedents
	if len(got) != 1 || got[0] != priorMsg.ID {
		t.Errorf("antecedents passed to sendFutureAndAwait = %v, want [%s]", got, priorMsg.ID)
	}
	// MessageID must still be populated.
	if result.MessageID == "" {
		t.Error("expected non-empty MessageID on sync path with antecedents")
	}
}

// TestExecuteResult_Async verifies that an async declaration returns ExecuteResult
// with MessageID set and Response nil.
func TestExecuteResult_Async(t *testing.T) {
	tr := &mockTransport{}
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := asyncDecl()

	result, err := ex.Execute(context.Background(), decl, "cf-async", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil ExecuteResult")
	}
	if result.MessageID == "" {
		t.Error("expected non-empty MessageID for async send")
	}
	if result.Response != nil {
		t.Errorf("expected nil Response for async, got %s", result.Response)
	}
	// async path uses sendMessage, not sendFutureAndAwait.
	if len(tr.futureCalls) != 0 {
		t.Errorf("expected 0 future calls for async, got %d", len(tr.futureCalls))
	}
}

// TestExecuteResult_None verifies that a none declaration returns ExecuteResult
// with MessageID set and Response nil.
func TestExecuteResult_None(t *testing.T) {
	tr := &mockTransport{}
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := noneDecl()

	result, err := ex.Execute(context.Background(), decl, "cf-none", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil ExecuteResult")
	}
	if result.MessageID == "" {
		t.Error("expected non-empty MessageID for none-response send")
	}
	if result.Response != nil {
		t.Errorf("expected nil Response for none, got %s", result.Response)
	}
	if len(tr.futureCalls) != 0 {
		t.Errorf("expected 0 future calls for none, got %d", len(tr.futureCalls))
	}
}

// TestExecuteResult_SyncTimeout verifies that when sendFutureAndAwait times out
// (context deadline), Execute returns ErrResponseTimeout sentinel.
func TestExecuteResult_SyncTimeout(t *testing.T) {
	tr := &mockTransport{
		futureDelay: 35 * time.Second, // longer than the step timeout
	}
	ex := newExecutorWithSharedLimiter(tr, testSenderKey)
	decl := syncDecl()

	// Use a context that expires quickly to trigger the timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	result, err := ex.Execute(ctx, decl, "cf-sync-timeout", map[string]any{})
	if err == nil {
		t.Fatal("expected ErrResponseTimeout, got nil")
	}
	if !errors.Is(err, ErrResponseTimeout) {
		t.Errorf("expected errors.Is(err, ErrResponseTimeout), got: %v", err)
	}
	// Result should be non-nil with Response nil (no fulfillment arrived).
	if result == nil {
		t.Fatal("expected non-nil ExecuteResult even on timeout")
	}
	if result.Response != nil {
		t.Errorf("expected nil Response on timeout, got %s", result.Response)
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

// ---- Item 2: toStringSlice — []any and string branches ----

// TestToStringSlice_AnySlice exercises the []any branch.
func TestToStringSlice_AnySlice(t *testing.T) {
	input := []any{"x", "y", 42} // 42 is not a string — should be skipped
	got := toStringSlice(input)
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("[]any branch: expected [x y], got %v", got)
	}
}

// TestToStringSlice_StringScalar exercises the single-string branch.
func TestToStringSlice_StringScalar(t *testing.T) {
	got := toStringSlice("hello")
	if len(got) != 1 || got[0] != "hello" {
		t.Errorf("string branch: expected [hello], got %v", got)
	}
}

// TestToStringSlice_Unknown verifies that unrecognised types return nil.
func TestToStringSlice_Unknown(t *testing.T) {
	got := toStringSlice(42)
	if got != nil {
		t.Errorf("unknown type: expected nil, got %v", got)
	}
}

// ---- Item 3: validateSingleValue — missing type branches ----

// TestValidateSingleValue_Boolean verifies bool type validation.
func TestValidateSingleValue_Boolean(t *testing.T) {
	desc := ArgDescriptor{Name: "flag", Type: "boolean"}
	if err := validateSingleValue(desc, true); err != nil {
		t.Errorf("true: unexpected error: %v", err)
	}
	if err := validateSingleValue(desc, false); err != nil {
		t.Errorf("false: unexpected error: %v", err)
	}
	if err := validateSingleValue(desc, "not-a-bool"); err == nil {
		t.Error("string for boolean: expected error, got nil")
	}
}

// TestValidateSingleValue_Duration verifies duration string validation.
func TestValidateSingleValue_Duration(t *testing.T) {
	desc := ArgDescriptor{Name: "ttl", Type: "duration"}
	if err := validateSingleValue(desc, "5m"); err != nil {
		t.Errorf("valid duration: unexpected error: %v", err)
	}
	if err := validateSingleValue(desc, "invalid"); err == nil {
		t.Error("invalid duration: expected error, got nil")
	}
	if err := validateSingleValue(desc, 42); err == nil {
		t.Error("non-string duration: expected error, got nil")
	}
}

// TestValidateSingleValue_Key verifies key type: 64 hex chars required.
func TestValidateSingleValue_Key(t *testing.T) {
	desc := ArgDescriptor{Name: "agent_key", Type: "key"}
	validKey := strings.Repeat("a", 64)
	if err := validateSingleValue(desc, validKey); err != nil {
		t.Errorf("valid key: unexpected error: %v", err)
	}
	if err := validateSingleValue(desc, strings.Repeat("a", 32)); err == nil {
		t.Error("too-short key: expected error, got nil")
	}
	if err := validateSingleValue(desc, strings.Repeat("Z", 64)); err == nil {
		t.Error("non-hex key (uppercase Z): expected error, got nil")
	}
	if err := validateSingleValue(desc, 99); err == nil {
		t.Error("non-string key: expected error, got nil")
	}
}

// TestValidateSingleValue_CampfireAndMessageID verifies campfire/message_id types.
func TestValidateSingleValue_CampfireAndMessageID(t *testing.T) {
	for _, typ := range []string{"campfire", "message_id"} {
		desc := ArgDescriptor{Name: "x", Type: typ}
		if err := validateSingleValue(desc, "some-id"); err != nil {
			t.Errorf("%s with value: unexpected error: %v", typ, err)
		}
		if err := validateSingleValue(desc, nil); err == nil {
			t.Errorf("%s with nil: expected error, got nil", typ)
		}
	}
}

// TestValidateSingleValue_JSON verifies json type: must be a valid JSON string.
func TestValidateSingleValue_JSON(t *testing.T) {
	desc := ArgDescriptor{Name: "data", Type: "json"}
	if err := validateSingleValue(desc, `{"key":"val"}`); err != nil {
		t.Errorf("valid JSON: unexpected error: %v", err)
	}
	if err := validateSingleValue(desc, `{bad json`); err == nil {
		t.Error("invalid JSON: expected error, got nil")
	}
	if err := validateSingleValue(desc, 42); err == nil {
		t.Error("non-string JSON: expected error, got nil")
	}
	if err := validateSingleValue(desc, nil); err == nil {
		t.Error("nil JSON: expected error, got nil")
	}
}

// TestValidateSingleValue_TagSet verifies tag_set type: must be []string.
func TestValidateSingleValue_TagSet(t *testing.T) {
	desc := ArgDescriptor{Name: "tags", Type: "tag_set"}
	if err := validateSingleValue(desc, []string{"a:b", "c:d"}); err != nil {
		t.Errorf("valid tag_set: unexpected error: %v", err)
	}
	if err := validateSingleValue(desc, "not-a-slice"); err == nil {
		t.Error("string for tag_set: expected error, got nil")
	}
	if err := validateSingleValue(desc, nil); err == nil {
		t.Error("nil tag_set: expected error, got nil")
	}
}

// TestValidateSingleValue_Integer_Types exercises int64 and json.Number paths.
func TestValidateSingleValue_Integer_Types(t *testing.T) {
	desc := ArgDescriptor{Name: "n", Type: "integer"}
	// int64
	if err := validateSingleValue(desc, int64(5)); err != nil {
		t.Errorf("int64: unexpected error: %v", err)
	}
	// json.Number valid
	if err := validateSingleValue(desc, json.Number("7")); err != nil {
		t.Errorf("json.Number valid: unexpected error: %v", err)
	}
	// json.Number invalid
	if err := validateSingleValue(desc, json.Number("not-a-number")); err == nil {
		t.Error("json.Number invalid: expected error, got nil")
	}
	// completely wrong type
	if err := validateSingleValue(desc, "text"); err == nil {
		t.Error("string for integer: expected error, got nil")
	}
}

// ---- Item 4: baseProperty — 7 arg types not exercised ----

// TestBaseProperty_AllTypes verifies baseProperty for all arg types including
// the default fallback for unknown types.
func TestBaseProperty_AllTypes(t *testing.T) {
	cases := []struct {
		arg      ArgDescriptor
		wantType string
		wantKey  string // optional extra key to check
	}{
		{ArgDescriptor{Name: "s", Type: "string"}, "string", ""},
		{ArgDescriptor{Name: "s", Type: "string", MaxLength: 5, Pattern: "^a$"}, "string", "maxLength"},
		{ArgDescriptor{Name: "i", Type: "integer"}, "integer", ""},
		{ArgDescriptor{Name: "i", Type: "integer", Max: 10}, "integer", "maximum"},
		{ArgDescriptor{Name: "d", Type: "duration"}, "string", "pattern"},
		{ArgDescriptor{Name: "b", Type: "boolean"}, "boolean", ""},
		{ArgDescriptor{Name: "k", Type: "key"}, "string", "pattern"},
		{ArgDescriptor{Name: "c", Type: "campfire"}, "string", "description"},
		{ArgDescriptor{Name: "m", Type: "message_id"}, "string", "description"},
		{ArgDescriptor{Name: "j", Type: "json"}, "object", ""},
		{ArgDescriptor{Name: "ts", Type: "tag_set"}, "array", ""},
		{ArgDescriptor{Name: "e", Type: "enum", Values: []string{"a", "b"}}, "string", "enum"},
		{ArgDescriptor{Name: "u", Type: "unknown-type"}, "string", ""},
	}
	for _, c := range cases {
		t.Run(c.arg.Type, func(t *testing.T) {
			prop := baseProperty(c.arg)
			if prop["type"] != c.wantType {
				t.Errorf("type: want %q, got %q", c.wantType, prop["type"])
			}
			if c.wantKey != "" {
				if _, ok := prop[c.wantKey]; !ok {
					t.Errorf("expected key %q in property map %v", c.wantKey, prop)
				}
			}
		})
	}
}

// TestBaseProperty_Integer_MinSet verifies that minimum is only emitted when MinSet=true.
func TestBaseProperty_Integer_MinSet(t *testing.T) {
	withMin := ArgDescriptor{Name: "n", Type: "integer", Min: 3, MinSet: true}
	p := baseProperty(withMin)
	if p["minimum"] != 3 {
		t.Errorf("MinSet=true: expected minimum=3, got %v", p["minimum"])
	}

	withoutMin := ArgDescriptor{Name: "n", Type: "integer", Min: 0, MinSet: false}
	p2 := baseProperty(withoutMin)
	if _, ok := p2["minimum"]; ok {
		t.Errorf("MinSet=false: minimum should not be present, got %v", p2)
	}
}

// ---- Item 7: composeTags — static at_most_one, zero_to_many max, exactly_one empty ----

// TestComposeTags_StaticAtMostOne verifies that a static (non-glob) at_most_one tag
// is not emitted (it's optional and no arg maps to it).
func TestComposeTags_StaticAtMostOne(t *testing.T) {
	decl := &Declaration{
		Convention:  "test",
		Version:     "0.1",
		Operation:   "op",
		Signing:     "member_key",
		Antecedents: "none",
		ProducesTags: []TagRule{
			{Tag: "social:post", Cardinality: "exactly_one"},
			{Tag: "optional:flag", Cardinality: "at_most_one"}, // static, optional
		},
	}
	tagList, err := composeTags(decl, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, tg := range tagList {
		if tg == "optional:flag" {
			t.Errorf("static at_most_one tag should not be emitted; got %v", tagList)
		}
	}
}

// TestComposeTags_ZeroToMany_MaxExceeded verifies that zero_to_many with a max
// returns an error when the limit is breached.
func TestComposeTags_ZeroToMany_MaxExceeded(t *testing.T) {
	decl := &Declaration{
		Convention:  "test",
		Version:     "0.1",
		Operation:   "op",
		Signing:     "member_key",
		Antecedents: "none",
		ProducesTags: []TagRule{
			{Tag: "topic:*", Cardinality: "zero_to_many", Max: 2},
		},
		Args: []ArgDescriptor{
			{Name: "topics", Type: "string", Repeated: true},
		},
	}
	args := map[string]any{"topics": []string{"a", "b", "c"}} // 3 > max 2
	_, err := composeTags(decl, args)
	if err == nil {
		t.Error("expected error for zero_to_many exceeding max, got nil")
	}
	if !strings.Contains(err.Error(), "max") {
		t.Errorf("error should mention 'max'; got %v", err)
	}
}

// TestComposeTags_ExactlyOne_EmptyArgValues verifies that an exactly_one glob
// with no matching arg values emits nothing (no error).
func TestComposeTags_ExactlyOne_EmptyArgValues(t *testing.T) {
	decl := &Declaration{
		Convention:  "test",
		Version:     "0.1",
		Operation:   "op",
		Signing:     "member_key",
		Antecedents: "none",
		ProducesTags: []TagRule{
			{Tag: "social:*", Cardinality: "exactly_one"},
		},
		Args: []ArgDescriptor{
			{Name: "coord", Type: "enum", Values: []string{"social:upvote"}},
		},
	}
	// No "coord" arg provided — collectArgValuesForPrefix returns empty.
	_, err := composeTags(decl, map[string]any{})
	if err != nil {
		t.Errorf("exactly_one with zero values should not error; got %v", err)
	}
}

// TestComposeTags_AtMostOne_TooMany verifies at_most_one returns error with >1 value.
func TestComposeTags_AtMostOne_TooMany(t *testing.T) {
	decl := &Declaration{
		Convention:  "test",
		Version:     "0.1",
		Operation:   "op",
		Signing:     "member_key",
		Antecedents: "none",
		ProducesTags: []TagRule{
			{Tag: "type:*", Cardinality: "at_most_one"},
		},
		Args: []ArgDescriptor{
			{Name: "types", Type: "string", Repeated: true},
		},
	}
	args := map[string]any{"types": []string{"a", "b"}} // 2 > at_most_one
	_, err := composeTags(decl, args)
	if err == nil {
		t.Error("expected error for at_most_one with 2 values, got nil")
	}
}

// ---- Item 8: executeStep default branch and resolveAntecedents unrecognized rule ----

// TestExecuteStep_UnknownAction verifies that executeStep returns an error for
// an unrecognized step action (the default branch).
func TestExecuteStep_UnknownAction(t *testing.T) {
	tr := &mockTransport{}
	ex := newExecutorWithLimiter(tr, testSenderKey, newRateLimiter())
	step := Step{Action: "unsupported-action"}
	err := ex.executeStep(context.Background(), step, "cf-test", make(map[string]map[string]any))
	if err == nil {
		t.Error("expected error for unknown step action, got nil")
	}
	if !strings.Contains(err.Error(), "unknown step action") {
		t.Errorf("error should mention 'unknown step action'; got %v", err)
	}
}

// TestResolveAntecedents_UnrecognizedRule verifies that an unrecognized antecedent
// rule returns an appropriate error.
func TestResolveAntecedents_UnrecognizedRule(t *testing.T) {
	tr := &mockTransport{}
	ex := newExecutorWithLimiter(tr, testSenderKey, newRateLimiter())
	decl := &Declaration{
		Convention:  "test",
		Version:     "0.1",
		Operation:   "op",
		Signing:     "member_key",
		Antecedents: "unrecognized_rule",
	}
	_, err := ex.resolveAntecedents(context.Background(), decl, "cf-test", map[string]any{})
	if err == nil {
		t.Error("expected error for unrecognized antecedent rule, got nil")
	}
	if !strings.Contains(err.Error(), "unrecognized") {
		t.Errorf("error should mention 'unrecognized'; got %v", err)
	}
}

// ---- Item 10: executeQueryStep — future_payload marshal path and non-JSON raw result ----

// TestExecuteQueryStep_FuturePayload verifies that a step with future_payload
// correctly marshals and passes the payload to SendFutureAndAwait.
func TestExecuteQueryStep_FuturePayload(t *testing.T) {
	futureResult, _ := json.Marshal(map[string]any{"msg_id": "response-msg-123"})
	tr := &mockTransport{futureResult: futureResult}
	ex := newExecutorWithLimiter(tr, testSenderKey, newRateLimiter())

	step := Step{
		Action: "query",
		FuturePayload: map[string]any{
			"lookup_key": "some-value",
		},
		FutureTags:    []string{"lookup:request"},
		ResultBinding: "lookup_result",
	}
	bindings := make(map[string]map[string]any)
	if err := ex.executeStep(context.Background(), step, "cf-test", bindings); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The result should be bound under "lookup_result"
	if bound, ok := bindings["lookup_result"]; !ok {
		t.Error("expected result binding 'lookup_result' to be set")
	} else if bound["msg_id"] != "response-msg-123" {
		t.Errorf("expected msg_id=response-msg-123, got %v", bound["msg_id"])
	}
}

// TestExecuteQueryStep_NonJSONRawResult verifies that when SendFutureAndAwait returns
// non-JSON bytes, the result is stored under the "raw" key.
func TestExecuteQueryStep_NonJSONRawResult(t *testing.T) {
	tr := &mockTransport{futureResult: []byte("plain text response")}
	ex := newExecutorWithLimiter(tr, testSenderKey, newRateLimiter())

	step := Step{
		Action:        "query",
		FutureTags:    []string{"lookup:request"},
		ResultBinding: "lookup_result",
	}
	bindings := make(map[string]map[string]any)
	if err := ex.executeStep(context.Background(), step, "cf-test", bindings); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bound, ok := bindings["lookup_result"]
	if !ok {
		t.Fatal("expected result binding 'lookup_result' to be set")
	}
	if raw, ok := bound["raw"]; !ok {
		t.Error("expected 'raw' key in binding for non-JSON result")
	} else if raw != "plain text response" {
		t.Errorf("expected raw='plain text response', got %v", raw)
	}
}

// TestIsTimeoutErr_ContextDeadlineExceeded verifies that isTimeoutErr detects
// context.DeadlineExceeded via errors.Is, including when wrapped.
func TestIsTimeoutErr_ContextDeadlineExceeded(t *testing.T) {
	// Direct sentinel.
	if !isTimeoutErr(context.DeadlineExceeded) {
		t.Error("expected isTimeoutErr(context.DeadlineExceeded) == true")
	}
	// Wrapped via fmt.Errorf %w — errors.Is must unwrap and match.
	wrapped := fmt.Errorf("operation failed: %w", context.DeadlineExceeded)
	if !isTimeoutErr(wrapped) {
		t.Error("expected isTimeoutErr(wrapped context.DeadlineExceeded) == true")
	}
}

// TestIsTimeoutErr_StringFallback verifies that isTimeoutErr still catches
// timeout errors from non-standard packages via string matching.
func TestIsTimeoutErr_StringFallback(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"deadline exceeded", true},
		{"context deadline", true},
		{"request timeout", true},
		{"operation timed out", true},
		{"connection refused", false},
		{"", false},
	}
	for _, c := range cases {
		err := errors.New(c.msg)
		if got := isTimeoutErr(err); got != c.want {
			t.Errorf("isTimeoutErr(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

// TestIsTimeoutErr_Nil verifies that isTimeoutErr handles nil gracefully.
func TestIsTimeoutErr_Nil(t *testing.T) {
	if isTimeoutErr(nil) {
		t.Error("expected isTimeoutErr(nil) == false")
	}
}

// TestExecuteQueryStep_NoResultBinding verifies that when ResultBinding is empty,
// the step completes without error and no bindings are created.
func TestExecuteQueryStep_NoResultBinding(t *testing.T) {
	tr := &mockTransport{futureResult: []byte(`{"msg_id":"x"}`)}
	ex := newExecutorWithLimiter(tr, testSenderKey, newRateLimiter())

	step := Step{
		Action:     "query",
		FutureTags: []string{"lookup:request"},
		// No ResultBinding
	}
	bindings := make(map[string]map[string]any)
	if err := ex.executeStep(context.Background(), step, "cf-test", bindings); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bindings) != 0 {
		t.Errorf("expected no bindings when ResultBinding is empty, got %v", bindings)
	}
}

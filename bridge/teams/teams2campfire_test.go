package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/bridge/teams/botframework"
	"github.com/campfire-net/campfire/bridge/state"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// --- StripHTML tests ---

func TestStripHTML_AtMention(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple at-mention",
			input: `Hello <at>Alice</at> how are you?`,
			want:  "Hello  how are you?",
		},
		{
			name:  "at-mention with attributes",
			input: `<at id="29:abc123">Bob</at> please review`,
			want:  "please review",
		},
		{
			name:  "multiple at-mentions",
			input: `<at>Alice</at> and <at>Bob</at> are here`,
			want:  "and  are here",
		},
		{
			name:  "html entities",
			input: `me &amp; you &lt;3 &gt;`,
			want:  `me & you <3 >`,
		},
		{
			name:  "nbsp entity",
			input: `hello&nbsp;world`,
			want:  `hello world`,
		},
		{
			name:  "plain text unchanged",
			input: `Just a normal message`,
			want:  `Just a normal message`,
		},
		{
			name:  "mixed html and at-mention",
			input: `<p><at>Alice</at> said &quot;hello&quot;</p>`,
			want:  `said "hello"`,
		},
		{
			name:  "trim whitespace",
			input: `  <at>Bot</at>   hello  `,
			want:  `hello`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripHTML(tc.input)
			if got != tc.want {
				t.Errorf("StripHTML(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// --- RateLimiter tests ---

func TestRateLimiter_Allow10Reject11th(t *testing.T) {
	rl := NewRateLimiter()
	userID := "user-alice"

	// First 10 should be allowed.
	for i := 0; i < 10; i++ {
		if !rl.Allow(userID) {
			t.Fatalf("expected Allow on call %d, got false", i+1)
		}
	}

	// 11th should be rejected.
	if rl.Allow(userID) {
		t.Error("expected 11th call to be rejected, got true")
	}
}

func TestRateLimiter_DifferentUsers(t *testing.T) {
	rl := NewRateLimiter()

	// Exhaust rate limit for user A.
	for i := 0; i < 10; i++ {
		rl.Allow("user-a")
	}
	if rl.Allow("user-a") {
		t.Error("user-a: expected rejection after 10 messages")
	}

	// User B is unaffected.
	if !rl.Allow("user-b") {
		t.Error("user-b: should not be rate limited")
	}
}

func TestRateLimiter_WindowExpiry(t *testing.T) {
	rl := NewRateLimiter()
	userID := "user-expiry"

	// Inject 10 hits that are just outside the window (older than 1 minute).
	past := time.Now().Add(-rateLimitWindow - time.Second)
	rl.mu.Lock()
	rl.hits[userID] = make([]time.Time, 10)
	for i := range rl.hits[userID] {
		rl.hits[userID][i] = past
	}
	rl.mu.Unlock()

	// All 10 old hits should be pruned; first new one should be allowed.
	if !rl.Allow(userID) {
		t.Error("expected Allow after window expiry")
	}
}

// --- ACL integration test using real SQLite state DB ---

func openTestDB(t *testing.T) *state.DB {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestACLIntegration(t *testing.T) {
	db := openTestDB(t)

	campfireID := "deadbeef01"
	allowedUser := "user-allowed"
	deniedUser := "user-denied"

	// Seed ACL for the allowed user.
	if err := db.SeedACL(allowedUser, campfireID, "Allowed User"); err != nil {
		t.Fatal(err)
	}

	// Allowed user passes.
	ok, err := db.CheckACL(allowedUser, campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("allowed user should pass ACL")
	}

	// Denied user fails.
	ok, err = db.CheckACL(deniedUser, campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("denied user should fail ACL")
	}

	// Allowed user is blocked from a different campfire.
	ok, err = db.CheckACL(allowedUser, "other-campfire")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("allowed user should not access other campfire")
	}

	// Wildcard user passes any campfire.
	if err := db.SeedACL("user-wild", "*", "Wild User"); err != nil {
		t.Fatal(err)
	}
	ok, err = db.CheckACL("user-wild", campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("wildcard user should pass ACL for any campfire")
	}
}

// --- Helper to build Activity JSON ---

func makeTestActivity(t *testing.T, activityID, fromID, convID, text, replyToID string) []byte {
	t.Helper()
	act := botframework.Activity{
		Type:      botframework.ActivityTypeMessage,
		ID:        activityID,
		Text:      text,
		ReplyToID: replyToID,
		From:      botframework.ChannelAccount{ID: fromID, Name: "Test User"},
		Conversation: botframework.ConversationAccount{
			ID: convID,
		},
	}
	data, err := json.Marshal(act)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// stubHandler mirrors InboundHandler logic but accepts a plain function for JWT
// validation, so tests run without network calls.
type stubHandler struct {
	ident       *identity.Identity
	bridgeDB    *state.DB
	fsTransport *fs.Transport
	rateLimiter *RateLimiter
}

func newStubHandler(
	ident *identity.Identity,
	bridgeDB *state.DB,
	fsTransport *fs.Transport,
) *stubHandler {
	return &stubHandler{
		ident:       ident,
		bridgeDB:    bridgeDB,
		fsTransport: fsTransport,
		rateLimiter: NewRateLimiter(),
	}
}

func (h *stubHandler) HandleActivity(ctx context.Context, body []byte) (string, error) {
	activity, err := botframework.ParseActivity(body)
	if err != nil {
		return "", fmt.Errorf("parsing activity: %w", err)
	}
	if activity.Type != botframework.ActivityTypeMessage {
		return "", fmt.Errorf("unsupported activity type: %s", activity.Type)
	}

	fromID := activity.From.ID
	convID := activity.Conversation.ID
	activityID := activity.ID
	replyToID := activity.ReplyToID

	campfireID, err := h.bridgeDB.GetCampfireForConversation(convID)
	if err != nil {
		return "", fmt.Errorf("resolving campfire: %w", err)
	}
	if campfireID == "" {
		return "", fmt.Errorf("no campfire mapped to teams conversation %q", convID)
	}

	allowed, err := h.bridgeDB.CheckACL(fromID, campfireID)
	if err != nil {
		return "", fmt.Errorf("ACL check: %w", err)
	}
	if !allowed {
		return "", ErrUnauthorized
	}

	dup, err := h.bridgeDB.CheckDedup(activityID)
	if err != nil {
		return "", fmt.Errorf("dedup check: %w", err)
	}
	if dup {
		return "", ErrDuplicate
	}

	if !h.rateLimiter.Allow(fromID) {
		return "", ErrRateLimited
	}

	payload := []byte(StripHTML(activity.Text))

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

	tags := []string{"from:teams"}

	msg, err := message.NewMessage(h.ident.PrivateKey, h.ident.PublicKey, payload, tags, antecedents)
	if err != nil {
		return "", fmt.Errorf("creating message: %w", err)
	}
	msg.Instance = "teams-bridge"

	if err := h.fsTransport.WriteMessage(campfireID, msg); err != nil {
		return "", fmt.Errorf("writing message: %w", err)
	}

	if err := h.bridgeDB.RecordDedup(activityID, msg.ID); err != nil {
		return "", fmt.Errorf("recording dedup: %w", err)
	}
	if err := h.bridgeDB.MapMessage(msg.ID, activityID, convID, campfireID); err != nil {
		return "", fmt.Errorf("recording message map: %w", err)
	}

	return msg.ID, nil
}

func setupTestCampfire(t *testing.T, db *state.DB, dir, campfireID, convID, fromID string) {
	t.Helper()
	if err := db.UpsertConversationRef(state.ConversationRef{
		CampfireID:  campfireID,
		TeamsConvID: convID,
		ServiceURL:  "https://example.com",
		TenantID:    "tenant-1",
		BotID:       "bot-1",
	}); err != nil {
		t.Fatal(err)
	}
	if fromID != "" {
		if err := db.SeedACL(fromID, campfireID, "Test User"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, campfireID, "messages"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestHandleActivity_HappyPath(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	transport := fs.New(dir)
	ident, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	campfireID := "testcampfire01"
	convID := "19:test@thread"
	fromID := "teams-user-001"
	setupTestCampfire(t, db, dir, campfireID, convID, fromID)

	h := newStubHandler(ident, db, transport)
	body := makeTestActivity(t, "act-001", fromID, convID, "Hello campfire!", "")

	msgID, err := h.HandleActivity(context.Background(), body)
	if err != nil {
		t.Fatalf("HandleActivity: %v", err)
	}
	if msgID == "" {
		t.Error("expected non-empty message ID")
	}

	// Verify message written to filesystem.
	msgs, err := transport.ListMessages(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Instance != "teams-bridge" {
		t.Errorf("Instance = %q, want teams-bridge", msgs[0].Instance)
	}
	if string(msgs[0].Payload) != "Hello campfire!" {
		t.Errorf("Payload = %q, want Hello campfire!", string(msgs[0].Payload))
	}
	if !msgs[0].VerifySignature() {
		t.Error("message signature invalid")
	}
}

func TestHandleActivity_Unauthorized(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	transport := fs.New(dir)
	ident, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	campfireID := "testcampfire02"
	convID := "19:auth-test@thread"
	// No ACL seeded (pass empty fromID).
	setupTestCampfire(t, db, dir, campfireID, convID, "")

	h := newStubHandler(ident, db, transport)
	body := makeTestActivity(t, "act-unauth", "unauthorized-user", convID, "Hello", "")

	_, err = h.HandleActivity(context.Background(), body)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestHandleActivity_Dedup(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	transport := fs.New(dir)
	ident, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	campfireID := "testcampfire03"
	convID := "19:dedup-test@thread"
	fromID := "teams-user-003"
	setupTestCampfire(t, db, dir, campfireID, convID, fromID)

	h := newStubHandler(ident, db, transport)
	body := makeTestActivity(t, "act-dup-001", fromID, convID, "Test message", "")

	// First delivery succeeds.
	msgID, err := h.HandleActivity(context.Background(), body)
	if err != nil {
		t.Fatalf("first HandleActivity: %v", err)
	}
	if msgID == "" {
		t.Error("expected non-empty message ID on first delivery")
	}

	// Second delivery (same activity ID) should fail with ErrDuplicate.
	_, err = h.HandleActivity(context.Background(), body)
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("expected ErrDuplicate on second delivery, got %v", err)
	}
}

func TestHandleActivity_RateLimit(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	transport := fs.New(dir)
	ident, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	campfireID := "testcampfire04"
	convID := "19:ratelimit-test@thread"
	fromID := "teams-user-004"
	setupTestCampfire(t, db, dir, campfireID, convID, fromID)

	h := newStubHandler(ident, db, transport)

	// Send 10 unique activities — all should succeed.
	for i := 0; i < 10; i++ {
		actID := fmt.Sprintf("act-rl-%03d", i)
		body := makeTestActivity(t, actID, fromID, convID, fmt.Sprintf("msg %d", i), "")
		if _, err := h.HandleActivity(context.Background(), body); err != nil {
			t.Fatalf("activity %d: unexpected error: %v", i, err)
		}
	}

	// 11th (new activity ID) should be rate limited.
	body := makeTestActivity(t, "act-rl-overflow", fromID, convID, "too many", "")
	_, err = h.HandleActivity(context.Background(), body)
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited, got %v", err)
	}
}

func TestHandleActivity_ReplyAntecedent(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	transport := fs.New(dir)
	ident, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	campfireID := "testcampfire05"
	convID := "19:antecedent-test@thread"
	fromID := "teams-user-005"
	setupTestCampfire(t, db, dir, campfireID, convID, fromID)

	// Seed a prior message mapping: Teams activity "act-original" → campfire msg "cf-original-id".
	if err := db.MapMessage("cf-original-id", "act-original", convID, campfireID); err != nil {
		t.Fatal(err)
	}

	h := newStubHandler(ident, db, transport)
	// Send a reply to act-original.
	body := makeTestActivity(t, "act-reply-001", fromID, convID, "This is a reply", "act-original")

	_, err = h.HandleActivity(context.Background(), body)
	if err != nil {
		t.Fatalf("HandleActivity (reply): %v", err)
	}

	msgs, err := transport.ListMessages(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs[0].Antecedents) != 1 || msgs[0].Antecedents[0] != "cf-original-id" {
		t.Errorf("antecedents = %v, want [cf-original-id]", msgs[0].Antecedents)
	}
}

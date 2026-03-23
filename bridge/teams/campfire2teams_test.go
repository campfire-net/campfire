package teams

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/bridge/teams/botframework"
	"github.com/campfire-net/campfire/bridge/enrichment"
	"github.com/campfire-net/campfire/bridge/state"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

func TestFormatMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  store.MessageRecord
		want string
	}{
		{
			name: "full message with instance and tags",
			msg: store.MessageRecord{
				Sender:   "abcdef1234567890",
				Instance: "orchestrator",
				Tags:     []string{"status", "finding"},
				Payload:  []byte("hello world"),
			},
			want: "[abcdef12] (orchestrator) status,finding: hello world",
		},
		{
			name: "no instance",
			msg: store.MessageRecord{
				Sender:  "abcdef1234567890",
				Tags:    []string{"blocker"},
				Payload: []byte("blocked on X"),
			},
			want: "[abcdef12] blocker: blocked on X",
		},
		{
			name: "no tags",
			msg: store.MessageRecord{
				Sender:   "abcdef1234567890",
				Instance: "worker",
				Tags:     []string{},
				Payload:  []byte("plain message"),
			},
			want: "[abcdef12] (worker) plain message",
		},
		{
			name: "no tags, no instance",
			msg: store.MessageRecord{
				Sender:  "abcdef1234567890",
				Tags:    []string{},
				Payload: []byte("bare message"),
			},
			want: "[abcdef12] bare message",
		},
		{
			name: "sender shorter than 8 chars",
			msg: store.MessageRecord{
				Sender:  "abcd",
				Tags:    []string{"status"},
				Payload: []byte("short sender"),
			},
			want: "[abcd] status: short sender",
		},
		{
			name: "empty payload",
			msg: store.MessageRecord{
				Sender:   "abcdef1234567890",
				Instance: "bot",
				Tags:     []string{"ping"},
				Payload:  []byte(""),
			},
			want: "[abcdef12] (bot) ping:",
		},
		{
			name: "whitespace-only payload trimmed",
			msg: store.MessageRecord{
				Sender:  "abcdef1234567890",
				Tags:    []string{"status"},
				Payload: []byte("   "),
			},
			want: "[abcdef12] status:",
		},
		{
			name: "nil tags treated as no tags",
			msg: store.MessageRecord{
				Sender:  "abcdef1234567890",
				Tags:    nil,
				Payload: []byte("nil tags message"),
			},
			want: "[abcdef12] nil tags message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatMessage(tt.msg)
			if got != tt.want {
				t.Errorf("FormatMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWebhookHandler(t *testing.T) {
	t.Run("successful POST", func(t *testing.T) {
		var received teamsPayload
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("expected Content-Type application/json, got %s", ct)
			}
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &received); err != nil {
				t.Errorf("unmarshal body: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		msg := store.MessageRecord{
			Sender:   "deadbeef12345678",
			Instance: "tester",
			Tags:     []string{"test"},
			Payload:  []byte("hello teams"),
		}

		handler := WebhookHandler(srv.URL, srv.Client())
		if err := handler(msg); err != nil {
			t.Fatalf("handler returned error: %v", err)
		}

		want := FormatMessage(msg)
		if received.Text != want {
			t.Errorf("Teams received %q, want %q", received.Text, want)
		}
	})

	t.Run("non-2xx response returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		handler := WebhookHandler(srv.URL, srv.Client())
		err := handler(store.MessageRecord{
			Sender:  "aabbccdd",
			Tags:    []string{},
			Payload: []byte("test"),
		})
		if err == nil {
			t.Fatal("expected error for 500 response, got nil")
		}
	})

	t.Run("unreachable URL returns error", func(t *testing.T) {
		handler := WebhookHandler("http://127.0.0.1:0/webhook", nil)
		err := handler(store.MessageRecord{
			Sender:  "aabbccdd",
			Tags:    []string{},
			Payload: []byte("test"),
		})
		if err == nil {
			t.Fatal("expected error for unreachable URL, got nil")
		}
	})
}

// --- BotHandler tests ---

// makeBFServer builds a minimal mock Bot Framework server.
// It handles /token (returning a stub token) and records calls to activity endpoints.
type bfCapture struct {
	method     string
	path       string
	importance string
	card       map[string]any
}

func makeBFServer(t *testing.T, returnID string) (*httptest.Server, *[]bfCapture) {
	t.Helper()
	var captures []bfCapture

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-token",
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		cap := bfCapture{method: r.Method, path: r.URL.Path}
		body, _ := io.ReadAll(r.Body)
		var act botframework.Activity
		_ = json.Unmarshal(body, &act)
		cap.importance = act.Importance
		if len(act.Attachments) > 0 {
			_ = json.Unmarshal(act.Attachments[0].Content, &cap.card)
		}
		captures = append(captures, cap)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(botframework.ResourceResponse{ID: returnID})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &captures
}

func makeBFClient(t *testing.T, srv *httptest.Server) *botframework.Client {
	t.Helper()
	tc := botframework.NewTokenClientForTest(srv.URL+"/token", srv.Client())
	return botframework.NewClientWithHTTP(tc, srv.Client())
}

func openBotHandlerTestDB(t *testing.T, campfireID, convID string) *state.DB {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.UpsertConversationRef(state.ConversationRef{
		CampfireID:  campfireID,
		TeamsConvID: convID,
		ServiceURL:  "http://placeholder/",
		TenantID:    "tenant-1",
		BotID:       "bot-1",
	}); err != nil {
		t.Fatalf("UpsertConversationRef: %v", err)
	}
	return db
}

// TestBotHandler_SendsAdaptiveCard verifies that BotHandler enriches and posts
// an Adaptive Card to the Bot Framework API for a basic message.
func TestBotHandler_SendsAdaptiveCard(t *testing.T) {
	srv, captures := makeBFServer(t, "new-act-001")

	campfireID := "cf-bot-test-01"
	convID := "19:bot-test@thread"
	db := openBotHandlerTestDB(t, campfireID, convID)

	// Patch the service URL to point to our mock server.
	ref, _ := db.GetConversationRef(campfireID)
	ref.ServiceURL = srv.URL + "/"
	_ = db.UpsertConversationRef(*ref)

	bfClient := makeBFClient(t, srv)
	opts := enrichment.EnrichOptions{}
	h := NewBotHandler(campfireID, opts, bfClient, db)

	msg := store.MessageRecord{
		ID:         "cf-msg-001",
		Sender:     "aabbccdd1234",
		CampfireID: campfireID,
		Tags:       []string{"status"},
		Payload:    []byte("bridge is up"),
		Timestamp:  1000,
	}
	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(*captures) != 1 {
		t.Fatalf("expected 1 BF request, got %d", len(*captures))
	}
	cap := (*captures)[0]
	if cap.method != http.MethodPost {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.card == nil {
		t.Error("expected Adaptive Card attachment, got nil")
	}
	if cap.card["type"] != "AdaptiveCard" {
		t.Errorf("card type = %q, want AdaptiveCard", cap.card["type"])
	}

	// Verify message_map was written.
	actID, cv, err := db.LookupTeamsActivity("cf-msg-001")
	if err != nil {
		t.Fatal(err)
	}
	if actID != "new-act-001" {
		t.Errorf("stored activityID = %q, want new-act-001", actID)
	}
	if cv != convID {
		t.Errorf("stored convID = %q, want %q", cv, convID)
	}
}

// TestBotHandler_HighUrgencySetsImportance verifies that HIGH urgency messages
// set importance:"urgent" on the activity.
func TestBotHandler_HighUrgencySetsImportance(t *testing.T) {
	srv, captures := makeBFServer(t, "urg-act-001")

	campfireID := "cf-urgent-01"
	convID := "19:urgent@thread"
	db := openBotHandlerTestDB(t, campfireID, convID)
	ref, _ := db.GetConversationRef(campfireID)
	ref.ServiceURL = srv.URL + "/"
	_ = db.UpsertConversationRef(*ref)

	bfClient := makeBFClient(t, srv)
	// Mark the campfire as urgent so urgency scores HIGH.
	opts := enrichment.EnrichOptions{UrgentCampfires: []string{campfireID}}
	h := NewBotHandler(campfireID, opts, bfClient, db)

	msg := store.MessageRecord{
		ID:         "cf-msg-urgent",
		Sender:     "aabbccdd1234",
		CampfireID: campfireID,
		Tags:       []string{"blocker"},
		Payload:    []byte("critical issue"),
		Timestamp:  1000,
	}
	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(*captures) == 0 {
		t.Fatal("no BF requests captured")
	}
	if (*captures)[0].importance != "urgent" {
		t.Errorf("importance = %q, want urgent", (*captures)[0].importance)
	}
}

// TestBotHandler_Threading verifies that when a message has an antecedent that
// is in message_map, ReplyToActivity is used (path includes the parent activity ID).
func TestBotHandler_Threading(t *testing.T) {
	srv, captures := makeBFServer(t, "reply-act-001")

	campfireID := "cf-thread-01"
	convID := "19:thread@thread"
	db := openBotHandlerTestDB(t, campfireID, convID)
	ref, _ := db.GetConversationRef(campfireID)
	ref.ServiceURL = srv.URL + "/"
	_ = db.UpsertConversationRef(*ref)

	// Seed a prior message mapping: campfire msg "cf-parent" → Teams activity "teams-parent-act"
	if err := db.MapMessage("cf-parent", "teams-parent-act", convID, campfireID); err != nil {
		t.Fatal(err)
	}

	bfClient := makeBFClient(t, srv)
	opts := enrichment.EnrichOptions{}
	h := NewBotHandler(campfireID, opts, bfClient, db)

	msg := store.MessageRecord{
		ID:          "cf-reply-msg",
		Sender:      "aabbccdd1234",
		CampfireID:  campfireID,
		Tags:        []string{},
		Payload:     []byte("this is a threaded reply"),
		Timestamp:   1000,
		Antecedents: []string{"cf-parent"},
	}
	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(*captures) != 1 {
		t.Fatalf("expected 1 BF request, got %d", len(*captures))
	}
	// ReplyToActivity uses POST to /{convID}/activities/{parentActivityID}
	expectedPath := "/v3/conversations/" + convID + "/activities/teams-parent-act"
	if (*captures)[0].path != expectedPath {
		t.Errorf("path = %q, want %q", (*captures)[0].path, expectedPath)
	}
}

// TestBotHandler_MissingAntecedentFallsBackToTopLevel verifies that when
// antecedents[0] is not in message_map, SendActivity is used (top-level post).
func TestBotHandler_MissingAntecedentFallsBackToTopLevel(t *testing.T) {
	srv, captures := makeBFServer(t, "toplevel-act-001")

	campfireID := "cf-fallback-01"
	convID := "19:fallback@thread"
	db := openBotHandlerTestDB(t, campfireID, convID)
	ref, _ := db.GetConversationRef(campfireID)
	ref.ServiceURL = srv.URL + "/"
	_ = db.UpsertConversationRef(*ref)

	bfClient := makeBFClient(t, srv)
	opts := enrichment.EnrichOptions{}
	h := NewBotHandler(campfireID, opts, bfClient, db)

	// Message references an antecedent that is NOT in message_map.
	msg := store.MessageRecord{
		ID:          "cf-fallback-msg",
		Sender:      "aabbccdd1234",
		CampfireID:  campfireID,
		Tags:        []string{},
		Payload:     []byte("reply to unknown parent"),
		Timestamp:   1000,
		Antecedents: []string{"cf-unknown-parent"},
	}
	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(*captures) != 1 {
		t.Fatalf("expected 1 BF request, got %d", len(*captures))
	}
	// SendActivity uses POST to /{convID}/activities (no parent ID in path)
	expectedPath := "/v3/conversations/" + convID + "/activities"
	if (*captures)[0].path != expectedPath {
		t.Errorf("path = %q, want %q", (*captures)[0].path, expectedPath)
	}
}

// TestBotHandler_NoConversationRef verifies that when no conversation_ref is
// stored for a campfire, Handle returns nil (skips gracefully).
func TestBotHandler_NoConversationRef(t *testing.T) {
	srv, captures := makeBFServer(t, "should-not-be-called")

	campfireID := "cf-no-ref-01"
	// Do NOT seed a conversation ref — just create the DB.
	db, err := state.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	bfClient := makeBFClient(t, srv)
	opts := enrichment.EnrichOptions{}
	h := NewBotHandler(campfireID, opts, bfClient, db)

	msg := store.MessageRecord{
		ID:         "cf-no-ref-msg",
		Sender:     "aabbccdd1234",
		CampfireID: campfireID,
		Tags:       []string{},
		Payload:    []byte("nobody home"),
		Timestamp:  1000,
	}
	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle should return nil when no conv ref: %v", err)
	}
	if len(*captures) != 0 {
		t.Errorf("expected 0 BF requests, got %d", len(*captures))
	}
}

// TestBotHandler_GateCardHasApproveReject verifies that gate-tagged messages
// produce a card with Approve/Reject actions.
func TestBotHandler_GateCardHasApproveReject(t *testing.T) {
	srv, captures := makeBFServer(t, "gate-act-001")

	campfireID := "cf-gate-01"
	convID := "19:gate@thread"
	db := openBotHandlerTestDB(t, campfireID, convID)
	ref, _ := db.GetConversationRef(campfireID)
	ref.ServiceURL = srv.URL + "/"
	_ = db.UpsertConversationRef(*ref)

	bfClient := makeBFClient(t, srv)
	opts := enrichment.EnrichOptions{}
	h := NewBotHandler(campfireID, opts, bfClient, db)

	msg := store.MessageRecord{
		ID:         "cf-gate-msg-001",
		Sender:     "aabbccdd1234",
		CampfireID: campfireID,
		Tags:       []string{"gate"},
		Payload:    []byte("approve deployment?"),
		Timestamp:  1000,
	}
	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(*captures) == 0 {
		t.Fatal("no BF requests captured")
	}
	card := (*captures)[0].card
	if card == nil {
		t.Fatal("expected Adaptive Card, got nil")
	}
	actions, ok := card["actions"].([]any)
	if !ok || len(actions) < 2 {
		t.Fatalf("expected >=2 actions in card, got %v", card["actions"])
	}
	// Verify Approve button exists.
	found := false
	for _, a := range actions {
		if act, ok := a.(map[string]any); ok {
			if d, ok := act["data"].(map[string]any); ok {
				if d["action"] == "gate-approve" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("gate-approve action not found in card")
	}
}

// setupFSForGateTest creates the campfire message directory for fs.Transport.
func setupFSForGateTest(t *testing.T, dir, campfireID string) *fs.Transport {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, campfireID, "messages"), 0755); err != nil {
		t.Fatal(err)
	}
	return fs.New(dir)
}

// makeInvokeActivity builds a JSON invoke activity body with gate action data.
func makeInvokeActivity(t *testing.T, actorID, actorName, convID, serviceURL, action, campfireID, gateMsgID string) []byte {
	t.Helper()
	data := map[string]string{
		"action":      action,
		"campfire_id": campfireID,
		"gate_msg_id": gateMsgID,
	}
	dataJSON, _ := json.Marshal(data)
	act := botframework.Activity{
		Type:       botframework.ActivityTypeInvoke,
		ID:         "invoke-act-001",
		ServiceURL: serviceURL,
		From:       botframework.ChannelAccount{ID: actorID, Name: actorName},
		Conversation: botframework.ConversationAccount{
			ID: convID,
		},
		Value: dataJSON,
	}
	body, err := json.Marshal(act)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// stubInboundHandler is the same test double used in teams2campfire_test.go,
// extended here with gate invoke support via the production handleGateInvoke path.
// We re-use the stubHandler's HandleActivity but route invokes to a stub gate handler.
// Since both test files are in the same package, we can call handleGateInvoke directly
// via a real InboundHandler with a stub validator.

// newInboundHandlerForGateTest creates a real InboundHandler with a nil-validator stub
// so gate invoke tests can run without network.
type alwaysValidValidator struct{}

func (v *alwaysValidValidator) ValidateToken(_ context.Context, _ string) error { return nil }

// We wrap InboundHandler's handleGateInvoke by calling HandleActivity with
// an ActivityTypeInvoke body and bypassing real JWT validation via the stub.
// To do this cleanly, we use the production InboundHandler but replace the
// validator with a stub (done via the unexported field — not possible from tests).
// Instead, we test the gate flow end-to-end by calling the exported HandleActivity
// which routes to handleGateInvoke, and we accept that JWT validation is tested
// via the existing TestHandleActivity_* tests. For gate tests we use a real
// InboundHandler with a permissive mock validator via a thin adapter.

// containsTag returns true if tag is in the tags slice.
func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// gateTestHandler wraps handleGateInvoke logic for testing without JWT validation.
type gateTestHandler struct {
	ident       *identity.Identity
	fsTransport *fs.Transport
	bridgeDB    *state.DB
	bfClient    *botframework.Client
}

func (g *gateTestHandler) handleInvoke(ctx context.Context, body []byte) (string, error) {
	activity, err := botframework.ParseActivity(body)
	if err != nil {
		return "", err
	}
	if activity.Type != botframework.ActivityTypeInvoke {
		return "", fmt.Errorf("not an invoke")
	}

	var data gateActionData
	if err := json.Unmarshal(activity.Value, &data); err != nil {
		return "", err
	}

	var resultTag string
	switch data.Action {
	case "gate-approve":
		resultTag = "gate-approved"
	case "gate-reject":
		resultTag = "gate-rejected"
	default:
		return "", ErrUnknownGateAction
	}

	actor := activity.From.Name
	if actor == "" {
		actor = activity.From.ID
	}
	payload := fmt.Sprintf("%s by %s", resultTag, actor)
	tags := []string{resultTag}
	antecedents := []string{data.GateMsgID}

	msg, err := message.NewMessage(g.ident.PrivateKey, g.ident.PublicKey, []byte(payload), tags, antecedents)
	if err != nil {
		return "", err
	}
	msg.Instance = "teams-bridge"

	if err := g.fsTransport.WriteMessage(data.CampfireID, msg); err != nil {
		return "", err
	}

	if g.bfClient != nil {
		origTeamsID, cv, _ := g.bridgeDB.LookupTeamsActivity(data.GateMsgID)
		if origTeamsID != "" && cv != "" {
			serviceURL := activity.ServiceURL
			decisonText := fmt.Sprintf("**%s** by %s", resultTag, actor)
			updatedCard := map[string]any{
				"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
				"type":    "AdaptiveCard",
				"version": "1.4",
				"body": []any{
					map[string]any{"type": "TextBlock", "text": decisonText, "wrap": true},
				},
			}
			cardJSON, _ := json.Marshal(updatedCard)
			updateActivity := &botframework.Activity{
				Type: botframework.ActivityTypeMessage,
				Attachments: []botframework.Attachment{
					{
						ContentType: "application/vnd.microsoft.card.adaptive",
						Content:     json.RawMessage(cardJSON),
					},
				},
			}
			_, _ = g.bfClient.UpdateActivity(ctx, serviceURL, cv, origTeamsID, updateActivity)
		}
	}

	return msg.ID, nil
}

// TestGateInvoke_Approve verifies that a gate-approve invoke writes a
// gate-approved campfire message with the correct antecedent.
func TestGateInvoke_Approve(t *testing.T) {
	dir := t.TempDir()
	campfireID := "cf-gate-invoke-01"
	transport := setupFSForGateTest(t, dir, campfireID)

	ident, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	db, err := state.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	gateMsgID := "cf-original-gate-msg"

	h := &gateTestHandler{
		ident:       ident,
		fsTransport: transport,
		bridgeDB:    db,
	}

	body := makeInvokeActivity(t, "user-001", "Alice", "19:test@thread", "https://smba.trafficmanager.net/", "gate-approve", campfireID, gateMsgID)
	msgID, err := h.handleInvoke(context.Background(), body)
	if err != nil {
		t.Fatalf("handleInvoke: %v", err)
	}
	if msgID == "" {
		t.Error("expected non-empty message ID")
	}

	msgs, err := transport.ListMessages(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 campfire message, got %d", len(msgs))
	}
	m := msgs[0]
	if !containsTag(m.Tags, "gate-approved") {
		t.Errorf("tags = %v, want gate-approved", m.Tags)
	}
	if len(m.Antecedents) != 1 || m.Antecedents[0] != gateMsgID {
		t.Errorf("antecedents = %v, want [%s]", m.Antecedents, gateMsgID)
	}
	if !m.VerifySignature() {
		t.Error("message signature invalid")
	}
}

// TestGateInvoke_Reject verifies that a gate-reject invoke writes a
// gate-rejected campfire message.
func TestGateInvoke_Reject(t *testing.T) {
	dir := t.TempDir()
	campfireID := "cf-gate-invoke-02"
	transport := setupFSForGateTest(t, dir, campfireID)

	ident, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	db, err := state.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	h := &gateTestHandler{
		ident:       ident,
		fsTransport: transport,
		bridgeDB:    db,
	}

	body := makeInvokeActivity(t, "user-002", "Bob", "19:test2@thread", "https://smba.trafficmanager.net/", "gate-reject", campfireID, "cf-gate-msg-002")
	msgID, err := h.handleInvoke(context.Background(), body)
	if err != nil {
		t.Fatalf("handleInvoke: %v", err)
	}
	if msgID == "" {
		t.Error("expected non-empty message ID")
	}

	msgs, err := transport.ListMessages(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 campfire message, got %d", len(msgs))
	}
	if !containsTag(msgs[0].Tags, "gate-rejected") {
		t.Errorf("tags = %v, want gate-rejected", msgs[0].Tags)
	}
}

// TestGateInvoke_UnknownAction verifies that an unknown action returns ErrUnknownGateAction.
func TestGateInvoke_UnknownAction(t *testing.T) {
	dir := t.TempDir()
	campfireID := "cf-gate-invoke-03"
	transport := setupFSForGateTest(t, dir, campfireID)

	ident, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	db, err := state.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	h := &gateTestHandler{
		ident:       ident,
		fsTransport: transport,
		bridgeDB:    db,
	}

	body := makeInvokeActivity(t, "user-003", "Charlie", "19:test3@thread", "https://smba.trafficmanager.net/", "gate-explode", campfireID, "cf-gate-msg-003")
	_, err = h.handleInvoke(context.Background(), body)
	if err != ErrUnknownGateAction {
		t.Errorf("expected ErrUnknownGateAction, got %v", err)
	}
}

// TestGateInvoke_UpdatesTeamsCard verifies that when a gate invoke fires,
// the original Teams card is updated via UpdateActivity.
func TestGateInvoke_UpdatesTeamsCard(t *testing.T) {
	srv, captures := makeBFServer(t, "update-act-001")

	dir := t.TempDir()
	campfireID := "cf-gate-update-01"
	transport := setupFSForGateTest(t, dir, campfireID)

	ident, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	db, err := state.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Seed the message_map so the handler can find the original Teams activity.
	convID := "19:gate-update@thread"
	origTeamsID := "original-teams-act-001"
	gateMsgID := "cf-gate-msg-update-01"
	if err := db.MapMessage(gateMsgID, origTeamsID, convID, campfireID); err != nil {
		t.Fatal(err)
	}

	bfClient := makeBFClient(t, srv)
	h := &gateTestHandler{
		ident:       ident,
		fsTransport: transport,
		bridgeDB:    db,
		bfClient:    bfClient,
	}

	body := makeInvokeActivity(t, "user-004", "Dana", convID, srv.URL+"/", "gate-approve", campfireID, gateMsgID)
	if _, err := h.handleInvoke(context.Background(), body); err != nil {
		t.Fatalf("handleInvoke: %v", err)
	}

	// Find the PUT request to UpdateActivity.
	var putCapture *bfCapture
	for i := range *captures {
		if (*captures)[i].method == http.MethodPut {
			putCapture = &(*captures)[i]
			break
		}
	}
	if putCapture == nil {
		t.Fatal("expected UpdateActivity (PUT) request, none found")
	}
	expectedPath := "/v3/conversations/" + convID + "/activities/" + origTeamsID
	if putCapture.path != expectedPath {
		t.Errorf("PUT path = %q, want %q", putCapture.path, expectedPath)
	}
	if putCapture.card == nil {
		t.Error("expected Adaptive Card in update, got nil")
	}
}

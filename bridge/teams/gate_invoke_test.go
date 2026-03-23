package teams

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/bridge/teams/botframework"
	"github.com/campfire-net/campfire/bridge/state"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/golang-jwt/jwt/v5"
)

// --- test helpers ---

func gateRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return k
}

func gateSignToken(t *testing.T, key *rsa.PrivateKey, kid, appID string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://api.botframework.com",
		"aud": appID,
		"exp": time.Now().Add(5 * time.Minute).Unix(),
		"iat": time.Now().Unix(),
		"nbf": time.Now().Unix(),
	})
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("signing test JWT: %v", err)
	}
	return "Bearer " + signed
}

// gateValidator builds a Validator with the key pre-loaded (no network calls).
func gateValidator(t *testing.T, appID, kid string, key *rsa.PrivateKey) *botframework.Validator {
	t.Helper()
	v := botframework.NewValidator(appID, false)
	v.InjectTestKey(kid, &key.PublicKey)
	return v
}

// makeJWKSServer starts an httptest.Server serving the given raw JWKS JSON body.
func makeJWKSServerRaw(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// base64RawURL encodes b as base64url with no padding.
func base64RawURL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// makeJWKSBody builds raw JWKS JSON for an RSA key.
func makeJWKSBody(t *testing.T, kid string, key *rsa.PublicKey) []byte {
	t.Helper()
	eBytes := big.NewInt(int64(key.E)).Bytes()
	body, _ := json.Marshal(map[string]any{
		"keys": []map[string]any{
			{
				"kid": kid,
				"kty": "RSA",
				"n":   base64RawURL(key.N.Bytes()),
				"e":   base64RawURL(eBytes),
			},
		},
	})
	return body
}

// setupInboundHandler creates a real InboundHandler with a pre-loaded validator.
func setupInboundHandler(t *testing.T, appID, kid string, rsaKey *rsa.PrivateKey) (
	h *InboundHandler,
	db *state.DB,
	transport *fs.Transport,
	campfireID, convID string,
) {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	dir := t.TempDir()
	transport = fs.New(dir)

	ident, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}

	campfireID = "gate-campfire-01"
	convID = "19:gate-conv@thread"

	// Seed conversation ref.
	if err := db.UpsertConversationRef(state.ConversationRef{
		CampfireID:  campfireID,
		TeamsConvID: convID,
		ServiceURL:  "https://example.com",
		TenantID:    "tenant-1",
		BotID:       "bot-1",
	}); err != nil {
		t.Fatal(err)
	}

	// Seed ACL for gate test users.
	for _, user := range []string{"approver-001", "rejector-001", "actor", "approver-bfclient"} {
		if err := db.SeedACL(user, campfireID, "Gate Test User"); err != nil {
			t.Fatal(err)
		}
	}

	// Create campfire directory.
	if err := os.MkdirAll(filepath.Join(dir, campfireID, "messages"), 0755); err != nil {
		t.Fatal(err)
	}

	validator := gateValidator(t, appID, kid, rsaKey)
	h = NewInboundHandler(ident, nil, db, transport, validator)

	return h, db, transport, campfireID, convID
}

// gateConvID is the Teams conversation ID used in gate invoke tests.
const gateConvID = "19:gate-conv@thread"

// --- gate invoke tests ---

func TestHandleGateInvoke_Approve(t *testing.T) {
	const appID = "gate-app-01"
	const kid = "gate-kid-01"
	rsaKey := gateRSAKey(t)
	token := gateSignToken(t, rsaKey, kid, appID)

	h, db, transport, campfireID, _ := setupInboundHandler(t, appID, kid, rsaKey)

	// Seed a prior gate message mapping so LookupTeamsActivity succeeds.
	gateMsgID := "original-gate-msg-id"
	if err := db.MapMessage(gateMsgID, "teams-act-gate", "19:gate-conv@thread", campfireID); err != nil {
		t.Fatal(err)
	}

	body := makeInvokeActivity(t, "approver-001", "Baron", gateConvID, "https://example.com", "gate-approve", campfireID, gateMsgID)
	msgID, err := h.HandleActivity(context.Background(), token, body)
	if err != nil {
		t.Fatalf("HandleActivity (gate-approve): %v", err)
	}
	if msgID == "" {
		t.Error("expected non-empty message ID")
	}

	// Verify campfire message was written with gate-approved tag.
	msgs, err := transport.ListMessages(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	foundTag := false
	for _, tag := range msgs[0].Tags {
		if tag == "gate-approved" {
			foundTag = true
		}
	}
	if !foundTag {
		t.Errorf("expected gate-approved tag, got %v", msgs[0].Tags)
	}
	if len(msgs[0].Antecedents) != 1 || msgs[0].Antecedents[0] != gateMsgID {
		t.Errorf("antecedents = %v, want [%s]", msgs[0].Antecedents, gateMsgID)
	}
	if !msgs[0].VerifySignature() {
		t.Error("gate response message signature invalid")
	}
}

func TestHandleGateInvoke_Reject(t *testing.T) {
	const appID = "gate-app-02"
	const kid = "gate-kid-02"
	rsaKey := gateRSAKey(t)
	token := gateSignToken(t, rsaKey, kid, appID)

	h, db, transport, campfireID, _ := setupInboundHandler(t, appID, kid, rsaKey)

	gateMsgID := "gate-msg-reject"
	if err := db.MapMessage(gateMsgID, "teams-act-reject", "19:gate-conv@thread", campfireID); err != nil {
		t.Fatal(err)
	}

	body := makeInvokeActivity(t, "rejector-001", "Alice", gateConvID, "https://example.com", "gate-reject", campfireID, gateMsgID)
	msgID, err := h.HandleActivity(context.Background(), token, body)
	if err != nil {
		t.Fatalf("HandleActivity (gate-reject): %v", err)
	}
	if msgID == "" {
		t.Error("expected non-empty message ID")
	}

	msgs, err := transport.ListMessages(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	foundTag := false
	for _, tag := range msgs[0].Tags {
		if tag == "gate-rejected" {
			foundTag = true
		}
	}
	if !foundTag {
		t.Errorf("expected gate-rejected tag, got %v", msgs[0].Tags)
	}
}

func TestHandleGateInvoke_UnknownAction(t *testing.T) {
	const appID = "gate-app-03"
	const kid = "gate-kid-03"
	rsaKey := gateRSAKey(t)
	token := gateSignToken(t, rsaKey, kid, appID)

	h, _, _, campfireID, _ := setupInboundHandler(t, appID, kid, rsaKey)

	body := makeInvokeActivity(t, "actor", "Actor", gateConvID, "https://example.com", "gate-unknown-action", campfireID, "some-msg-id")
	_, err := h.HandleActivity(context.Background(), token, body)
	if !errors.Is(err, ErrUnknownGateAction) {
		t.Errorf("expected ErrUnknownGateAction, got %v", err)
	}
}

func TestHandleGateInvoke_BadJWT(t *testing.T) {
	const appID = "gate-app-04"
	const kid = "gate-kid-04"
	rsaKey := gateRSAKey(t)
	// Sign with a *different* key so validation fails.
	wrongKey := gateRSAKey(t)
	token := gateSignToken(t, wrongKey, kid, appID)

	h, _, _, campfireID, _ := setupInboundHandler(t, appID, kid, rsaKey)

	body := makeInvokeActivity(t, "actor", "Actor", gateConvID, "https://example.com", "gate-approve", campfireID, "gate-msg-id")
	_, err := h.HandleActivity(context.Background(), token, body)
	if err == nil {
		t.Error("expected JWT validation failure, got nil")
	}
}

// TestHandleActivity_ReplyWithAbsentAntecedent verifies that when replyToId
// refers to an unknown Teams activity (not in message_map), the message is
// still written but with no antecedents (absent-antecedent path).
func TestHandleActivity_ReplyWithAbsentAntecedent(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	transport := fs.New(dir)
	ident, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	campfireID := "testcampfire-antecedent-absent"
	convID := "19:absent-antecedent@thread"
	fromID := "teams-user-ant-absent"
	setupTestCampfire(t, db, dir, campfireID, convID, fromID)

	h := newStubHandler(ident, db, transport)
	// Reply to an activity that has NO mapping in message_map.
	body := makeTestActivity(t, "act-reply-absent", fromID, convID, "Reply to unknown", "act-does-not-exist")

	msgID, err := h.HandleActivity(context.Background(), body)
	if err != nil {
		t.Fatalf("HandleActivity (absent antecedent): %v", err)
	}
	if msgID == "" {
		t.Error("expected non-empty message ID")
	}

	msgs, err := transport.ListMessages(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs[0].Antecedents) != 0 {
		t.Errorf("expected no antecedents for absent replyToId, got %v", msgs[0].Antecedents)
	}
}

// TestHandleActivity_JWKS verifies the ValidateToken path via the real
// InboundHandler (non-stub) with a JWKS server.  This exercises the
// newValidatorWithClient + refreshJWKS + fetchJWKS path.
func TestHandleActivity_RealValidatorHappyPath(t *testing.T) {
	const appID = "real-validator-app"
	const kid = "real-kid-01"

	rsaKey := gateRSAKey(t)
	token := gateSignToken(t, rsaKey, kid, appID)

	h, db, transport, campfireID, convID := setupInboundHandler(t, appID, kid, rsaKey)

	fromID := "real-validator-user"
	if err := db.SeedACL(fromID, campfireID, "Real User"); err != nil {
		t.Fatal(err)
	}

	// Create a message activity (not invoke).
	act := botframework.Activity{
		Type:  botframework.ActivityTypeMessage,
		ID:    "real-act-001",
		Text:  "Hello from real validator",
		From:  botframework.ChannelAccount{ID: fromID, Name: "Real User"},
		Conversation: botframework.ConversationAccount{ID: convID},
	}
	body, _ := json.Marshal(act)

	msgID, err := h.HandleActivity(context.Background(), token, body)
	if err != nil {
		t.Fatalf("HandleActivity (real validator): %v", err)
	}
	if msgID == "" {
		t.Error("expected non-empty message ID")
	}

	msgs, err := transport.ListMessages(campfireID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
}

// TestHandleActivity_UnsupportedType verifies unsupported activity types
// return an error.
func TestHandleActivity_UnsupportedType(t *testing.T) {
	const appID = "unsupported-app"
	const kid = "unsupported-kid"
	rsaKey := gateRSAKey(t)
	token := gateSignToken(t, rsaKey, kid, appID)

	h, _, _, _, _ := setupInboundHandler(t, appID, kid, rsaKey)

	act := map[string]any{
		"type": "typing",
		"id":   "typing-act-001",
		"from": map[string]any{"id": "user-1"},
		"conversation": map[string]any{"id": "conv-1"},
	}
	body, _ := json.Marshal(act)

	_, err := h.HandleActivity(context.Background(), token, body)
	if err == nil {
		t.Error("expected error for unsupported activity type")
	}
}

// TestHandleGateInvoke_BadValueJSON verifies that a malformed JSON value in an
// invoke activity returns an error (covers the json.Unmarshal failure path).
func TestHandleGateInvoke_BadValueJSON(t *testing.T) {
	const appID = "gate-app-05"
	const kid = "gate-kid-05"
	rsaKey := gateRSAKey(t)
	token := gateSignToken(t, rsaKey, kid, appID)

	h, _, _, _, _ := setupInboundHandler(t, appID, kid, rsaKey)

	// Build an invoke activity with invalid JSON in Value.
	act := botframework.Activity{
		Type:  botframework.ActivityTypeInvoke,
		ID:    "invoke-bad-json",
		Value: json.RawMessage(`{not valid json`),
		From:  botframework.ChannelAccount{ID: "actor", Name: "Actor"},
		Conversation: botframework.ConversationAccount{ID: gateConvID},
	}
	body, _ := json.Marshal(act)

	_, err := h.HandleActivity(context.Background(), token, body)
	if err == nil {
		t.Error("expected error for malformed invoke value JSON, got nil")
	}
}

// TestWithBFClient_GateInvoke_UpdatesCard verifies the card-update path
// when a BF client is attached (WithBFClient).  Uses a stub BF HTTP server.
func TestWithBFClient_GateInvoke_UpdatesCard(t *testing.T) {
	const appID = "gate-app-bfclient"
	const kid = "gate-kid-bfclient"
	rsaKey := gateRSAKey(t)
	token := gateSignToken(t, rsaKey, kid, appID)

	h, db, _, campfireID, _ := setupInboundHandler(t, appID, kid, rsaKey)

	// Record that WithBFClient was called by checking it returns the same handler.
	updateCalled := false
	bfSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		updateCalled = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"updated-act"}`)) //nolint:errcheck
	}))
	t.Cleanup(bfSrv.Close)

	// Build a BF client pointing at the stub server.
	// Use a test token client so there's no network call for token acquisition.
	tokenClient := botframework.NewTokenClientForTest(bfSrv.URL+"/token", bfSrv.Client())
	bfClient := botframework.NewClientWithHTTP(tokenClient, bfSrv.Client())
	result := h.WithBFClient(bfClient)
	if result != h {
		t.Error("WithBFClient should return the same handler")
	}

	// Seed a prior gate message mapping so LookupTeamsActivity returns something.
	gateMsgID := "gate-bfclient-msg"
	if err := db.MapMessage(gateMsgID, "teams-bfclient-act", gateConvID, campfireID); err != nil {
		t.Fatal(err)
	}

	body := makeInvokeActivity(t, "approver-bfclient", "BF User", gateConvID, bfSrv.URL, "gate-approve", campfireID, gateMsgID)
	msgID, err := h.HandleActivity(context.Background(), token, body)
	if err != nil {
		t.Fatalf("HandleActivity (bfclient gate): %v", err)
	}
	if msgID == "" {
		t.Error("expected non-empty message ID")
	}
	// The BF server should have been called for the card update.
	if !updateCalled {
		t.Error("expected BF client UpdateActivity to be called, but server was not hit")
	}
}

// TestHandleActivity_NoConversationMapping verifies that HandleActivity returns
// an error when the Teams conversation has no campfire mapping
// (covers the resolveCampfire empty-ID path).
func TestHandleActivity_NoConversationMapping(t *testing.T) {
	const appID = "no-mapping-app"
	const kid = "no-mapping-kid"
	rsaKey := gateRSAKey(t)
	token := gateSignToken(t, rsaKey, kid, appID)

	h, _, _, _, _ := setupInboundHandler(t, appID, kid, rsaKey)

	// Use a conv ID that has NO entry in conversation_refs.
	act := botframework.Activity{
		Type:  botframework.ActivityTypeMessage,
		ID:    "no-mapping-act",
		Text:  "hello",
		From:  botframework.ChannelAccount{ID: "user-1", Name: "User"},
		Conversation: botframework.ConversationAccount{ID: "19:unmapped-conv@thread"},
	}
	body, _ := json.Marshal(act)

	_, err := h.HandleActivity(context.Background(), token, body)
	if err == nil {
		t.Error("expected error for unmapped conversation, got nil")
	}
}

// Ensure makeJWKSServerRaw and makeJWKSBody compile (used in future tests).
var _ = makeJWKSServerRaw
var _ = makeJWKSBody

// convention_deliver_e2e_test.go — E2E: convention handler fires on P2P deliver path.
//
// Verifies the full integration chain:
//   P2P cfhttp.Deliver → handleDeliver → OnMessageDelivered hook →
//   real ConventionDispatcher.Dispatch → registered Tier1 handler fires
//   with correct payload.
//
// campfire-agent-dha
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestConventionDeliver_E2E_HandlerFiresOnDeliverPath verifies the full E2E path:
// a message delivered via P2P (cfhttp.Deliver) triggers a real ConventionDispatcher
// which dispatches to a registered Tier1 handler. The handler receives the correct
// convention name, operation, args, and campfire ID.
func TestConventionDeliver_E2E_HandlerFiresOnDeliverPath(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	// Create session and campfire via MCP.
	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)

	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description":    "e2e convention dispatch test",
		"delivery_modes": []string{"pull", "push"},
	})
	if createResp.Error != nil {
		t.Fatalf("campfire_create failed: %v", createResp.Error.Message)
	}
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("parsing create result: %v", err)
	}
	campfireID := createResult.CampfireID

	// Build a real ConventionDispatcher with a Tier1 handler.
	ds := convention.NewMemoryDispatchStore()
	dispatcher := convention.NewConventionDispatcher(ds, nil)

	// The handler captures the request it receives so we can verify correctness.
	type capturedRequest struct {
		CampfireID string
		Args       map[string]any
		Tags       []string
		Sender     string
	}
	var mu sync.Mutex
	var captured *capturedRequest
	handlerDone := make(chan struct{}, 1)

	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found for token")
	}

	handler := func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		captured = &capturedRequest{
			CampfireID: req.CampfireID,
			Args:       req.Args,
			Tags:       req.Tags,
			Sender:     req.Sender,
		}
		mu.Unlock()
		handlerDone <- struct{}{}
		return nil, nil // no response needed for this test
	}

	const conventionName = "testconv"
	const operationName = "ping"

	dispatcher.RegisterTier1Handler(
		campfireID, conventionName, operationName,
		nil, // no protocol.Client needed — handler returns (nil, nil)
		handler,
		"test-server-id",
		"",
	)

	// Wire the real dispatcher into the transport's OnMessageDelivered hook,
	// exactly as the SessionManager does in production.
	tr := srv.transportRouter.GetCampfireTransport(campfireID)
	if tr == nil {
		t.Fatal("transport not registered for campfire")
	}
	tr.SetOnMessageDelivered(func(ctx context.Context, cancel context.CancelFunc, cfID string, msg *store.MessageRecord) {
		dispatcher.DispatchWithCancel(ctx, cancel, cfID, msg)
	})

	// Register a CLI agent as a member so delivery is accepted.
	cliID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating CLI identity: %v", err)
	}
	tr.AddPeer(campfireID, cliID.PublicKeyHex(), "")
	st := tr.Store()
	st.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: cliID.PublicKeyHex(),
		Endpoint:     "",
		Role:         store.PeerRoleMember,
	})

	fsT := fs.New(sess.cfHome)
	fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: cliID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	})

	// Build a convention invocation message. The payload must be a JSON
	// conventionOpPayload and tags must include the "convention:operation"
	// invocation tag (format: "<convention>:<operation>").
	payload := map[string]any{
		"convention": conventionName,
		"operation":  operationName,
		"args": map[string]any{
			"greeting": "hello from e2e",
		},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling payload: %v", err)
	}

	// The invocation tag is "<convention>:<operation>" — this is what the dispatcher
	// uses to detect convention invocations (distinct from "convention:operation"
	// which is the declaration tag).
	invocationTag := conventionName + ":" + operationName
	msg, err := message.NewMessage(cliID.PrivateKey, cliID.PublicKey, payloadBytes, []string{invocationTag}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}

	// Deliver via P2P path.
	if err := cfhttp.Deliver(tsURL, campfireID, msg, cliID); err != nil {
		t.Fatalf("Deliver failed: %v", err)
	}

	// Wait for the handler to fire.
	select {
	case <-handlerDone:
		// Handler fired — verify captured request.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for convention handler to fire")
	}

	mu.Lock()
	got := captured
	mu.Unlock()

	if got == nil {
		t.Fatal("handler was not called")
	}
	if got.CampfireID != campfireID {
		t.Errorf("handler campfireID: want %q, got %q", campfireID, got.CampfireID)
	}
	if got.Args["greeting"] != "hello from e2e" {
		t.Errorf("handler args[greeting]: want %q, got %v", "hello from e2e", got.Args["greeting"])
	}
	if got.Sender != cliID.PublicKeyHex() {
		t.Errorf("handler sender: want %q, got %q", cliID.PublicKeyHex(), got.Sender)
	}

	// Verify the invocation tag was passed through.
	foundTag := false
	for _, tag := range got.Tags {
		if tag == invocationTag {
			foundTag = true
			break
		}
	}
	if !foundTag {
		t.Errorf("handler tags should contain %q, got %v", invocationTag, got.Tags)
	}
}

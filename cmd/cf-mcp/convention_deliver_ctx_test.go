// convention_deliver_ctx_test.go — Regression tests for campfire-agent-n34:
// handleDeliver dispatch uses a server-lifetime context with timeout.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestConventionDeliver_DispatchContextCancelsOnServerStop verifies that the
// dispatch context is cancelled when the transport is stopped, preventing
// goroutine leaks from unbounded context.Background() usage.
func TestConventionDeliver_DispatchContextCancelsOnServerStop(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	// Create session and campfire.
	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)

	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description":    "server stop ctx cancellation test",
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

	// Install a hook that blocks until its context is done, then reports.
	type hookResult struct {
		ctxErr error
	}
	resultCh := make(chan hookResult, 1)

	tr := srv.transportRouter.GetCampfireTransport(campfireID)
	if tr == nil {
		t.Fatal("transport not registered for campfire")
	}
	tr.SetOnMessageDelivered(func(ctx context.Context, cancel context.CancelFunc, cfID string, msg *store.MessageRecord) {
		// Spawn a goroutine that blocks until context is cancelled.
		// The hook returns immediately so the HTTP handler completes.
		go func() {
			<-ctx.Done()
			resultCh <- hookResult{ctxErr: ctx.Err()}
		}()
	})

	// Register CLI peer and deliver a message.
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

	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found for token")
	}
	fsT := fs.New(sess.cfHome)
	fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: cliID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	})

	msg, err := message.NewMessage(cliID.PrivateKey, cliID.PublicKey, []byte("stop ctx test"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	if err := cfhttp.Deliver(tsURL, campfireID, msg, cliID); err != nil {
		t.Fatalf("Deliver failed: %v", err)
	}

	// Give the hook goroutine a moment to start blocking.
	time.Sleep(50 * time.Millisecond)

	// Stop the transport — this should cancel the server-lifetime context.
	if err := tr.Stop(); err != nil {
		t.Fatalf("transport Stop failed: %v", err)
	}

	// The hook should unblock because the context was cancelled.
	select {
	case res := <-resultCh:
		if res.ctxErr == nil {
			t.Error("dispatch context was NOT cancelled after transport Stop(); expected cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for dispatch context to cancel after transport Stop()")
	}
}

// TestConventionDeliver_DispatchContextHasTimeout verifies that the dispatch
// context has a bounded timeout (not unbounded context.Background()).
func TestConventionDeliver_DispatchContextHasTimeout(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	// Create session and campfire.
	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)

	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description":    "dispatch timeout test",
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

	// Install a hook that checks for a deadline on the context.
	type hookResult struct {
		hasDeadline bool
	}
	resultCh := make(chan hookResult, 1)

	tr := srv.transportRouter.GetCampfireTransport(campfireID)
	if tr == nil {
		t.Fatal("transport not registered for campfire")
	}
	tr.SetOnMessageDelivered(func(ctx context.Context, cancel context.CancelFunc, cfID string, msg *store.MessageRecord) {
		if cancel != nil {
			defer cancel()
		}
		_, hasDeadline := ctx.Deadline()
		resultCh <- hookResult{hasDeadline: hasDeadline}
	})

	// Register CLI peer and deliver a message.
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

	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found for token")
	}
	fsT := fs.New(sess.cfHome)
	fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: cliID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	})

	msg, err := message.NewMessage(cliID.PrivateKey, cliID.PublicKey, []byte("timeout test"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	if err := cfhttp.Deliver(tsURL, campfireID, msg, cliID); err != nil {
		t.Fatalf("Deliver failed: %v", err)
	}

	select {
	case res := <-resultCh:
		if !res.hasDeadline {
			t.Error("dispatch context has no deadline; expected a bounded timeout (not context.Background())")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OnMessageDelivered hook")
	}
}

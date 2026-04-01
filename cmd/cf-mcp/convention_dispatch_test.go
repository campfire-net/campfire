// convention_dispatch_test.go — Tests for T4: ConventionDispatcher wiring in handleSend.
//
// Tests verify:
//   - Dispatch is called after AddMessage for convention-tagged messages.
//   - nil conventionDispatcher does not panic.
//   - loadConventionServersForCampfire is a no-op when dispatcher or store is nil.
package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
)

// ---------------------------------------------------------------------------
// TestConventionDispatch_NilDispatcherNoPanic
// ---------------------------------------------------------------------------

// TestConventionDispatch_NilDispatcherNoPanic verifies that handleSend does not
// panic when conventionDispatcher is nil (development / stdio mode).
func TestConventionDispatch_NilDispatcherNoPanic(t *testing.T) {
	srv := newTestServer(t)
	// conventionDispatcher is nil by default on newTestServer.
	if srv.conventionDispatcher != nil {
		t.Fatal("expected nil conventionDispatcher on newTestServer")
	}

	// Initialize so identity exists.
	r := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if r.Error != nil {
		t.Fatalf("campfire_init: %+v", r.Error)
	}

	// Sending to a campfire with nil dispatcher must not panic.
	// We expect an error (not a member) — not a crash.
	params := map[string]interface{}{
		"campfire_id": "nonexistent-campfire-for-dispatch-nil-test",
		"message":     "hello",
	}
	resp := srv.handleSend(float64(1), params)
	if resp.Error == nil {
		t.Fatal("expected error response for non-member campfire, got nil")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("unexpected error code: %d", resp.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// TestConventionDispatch_DispatchCalledAfterAddMessage
// ---------------------------------------------------------------------------

// TestConventionDispatch_DispatchCalledAfterAddMessage verifies that
// ConventionDispatcher.Dispatch is invoked after a successful AddMessage in
// handleSend, and that a registered Tier 1 handler fires.
func TestConventionDispatch_DispatchCalledAfterAddMessage(t *testing.T) {
	srv := newTestServer(t)

	// Track handler invocations.
	var mu sync.Mutex
	var dispatched []string

	// Wire a ConventionDispatcher.
	ds := convention.NewMemoryDispatchStore()
	dispatcher := convention.NewConventionDispatcher(ds, nil)
	srv.conventionDispatcher = dispatcher

	// Initialize identity.
	r := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if r.Error != nil {
		t.Fatalf("campfire_init: %+v", r.Error)
	}

	// Create a campfire.
	cr := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{"description":"dispatch-test"}}`))
	if cr.Error != nil {
		t.Fatalf("campfire_create: %+v", cr.Error)
	}
	campfireID := extractCampfireIDFromResp(t, cr)

	// Register a Tier 1 handler for "testconv:testop".
	dispatcher.RegisterTier1Handler(
		campfireID,
		"testconv",
		"testop",
		nil, // no client; handler returns nil response (no fulfillment message sent)
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			mu.Lock()
			dispatched = append(dispatched, req.MessageID)
			mu.Unlock()
			return nil, nil
		},
		"test-server-id",
	)

	// Send a convention-tagged message.
	payload, _ := json.Marshal(map[string]interface{}{
		"convention": "testconv",
		"operation":  "testop",
		"args":       map[string]interface{}{},
	})
	params := map[string]interface{}{
		"campfire_id": campfireID,
		"message":     string(payload),
		"tags":        []interface{}{"testconv:testop"},
	}
	resp := srv.handleSend(float64(1), params)
	if resp.Error != nil {
		t.Fatalf("handleSend: %+v", resp.Error)
	}

	// Wait for the dispatch goroutine (Dispatch is non-blocking).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(dispatched)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	n := len(dispatched)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 dispatch call, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// TestLoadConventionServers_NilStoreNoOp
// ---------------------------------------------------------------------------

// TestLoadConventionServers_NilStoreNoOp verifies loadConventionServersForCampfire
// is a no-op when conventionServerStore is nil.
func TestLoadConventionServers_NilStoreNoOp(t *testing.T) {
	srv := newTestServer(t)

	ds := convention.NewMemoryDispatchStore()
	dispatcher := convention.NewConventionDispatcher(ds, nil)
	srv.conventionDispatcher = dispatcher
	// Leave srv.conventionServerStore nil.

	// Must not panic.
	srv.loadConventionServersForCampfire(context.Background(), "campfire-xyz-nil-store")
}

// ---------------------------------------------------------------------------
// TestLoadConventionServers_NilDispatcherNoOp
// ---------------------------------------------------------------------------

// TestLoadConventionServers_NilDispatcherNoOp verifies loadConventionServersForCampfire
// is a no-op when conventionDispatcher is nil.
func TestLoadConventionServers_NilDispatcherNoOp(t *testing.T) {
	srv := newTestServer(t)
	// conventionDispatcher is nil. conventionServerStore is also nil.

	// Must not panic.
	srv.loadConventionServersForCampfire(context.Background(), "campfire-xyz-nil-dispatcher")
}

// ---------------------------------------------------------------------------
// extractCampfireIDFromResp helper
// ---------------------------------------------------------------------------

// extractCampfireIDFromResp pulls campfire_id from a campfire_create JSON-RPC response.
func extractCampfireIDFromResp(t *testing.T, resp jsonRPCResponse) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("response has error: %+v", resp.Error)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("cannot parse campfire_create result: %s", b)
	}
	var payload struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("cannot parse campfire_create text payload: %v", err)
	}
	if payload.CampfireID == "" {
		t.Fatal("campfire_id is empty in campfire_create result")
	}
	return payload.CampfireID
}

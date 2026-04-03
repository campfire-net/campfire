package convention

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// newTestProtocolClient creates a minimal *protocol.Client backed by a temporary
// SQLite store. No identity is set (read-only / identity-less client), which is
// sufficient for testing context cancellation at the clientAdapter layer.
func newTestProtocolClient(t *testing.T) *protocol.Client {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return protocol.New(s, nil)
}

// TestClientAdapter_SendFutureAndAwait_PreCancelledCtx verifies that
// clientAdapter.sendFutureAndAwait returns ctx.Err() immediately when the
// context is already cancelled before the call, without attempting to send.
//
// Regression test for campfire-agent-h8g: client.Send does not accept a context,
// so cancellation must be checked explicitly before the call.
func TestClientAdapter_SendFutureAndAwait_PreCancelledCtx(t *testing.T) {
	client := newTestProtocolClient(t)
	adapter := &clientAdapter{client: client}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	msgID, payload, err := adapter.sendFutureAndAwait(ctx, "test-campfire", []byte("{}"), nil, nil, 0)

	if err == nil {
		t.Fatal("expected error for pre-cancelled context, got nil")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if msgID != "" {
		t.Errorf("expected empty msgID for pre-cancelled ctx, got %q", msgID)
	}
	if payload != nil {
		t.Errorf("expected nil payload for pre-cancelled ctx, got %v", payload)
	}
}

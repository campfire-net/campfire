package main

import (
	"context"
	"strings"
	"testing"
)

// TestMCPTransportSendCampfireKeySignedStub documents the current stub behavior:
// SendCampfireKeySigned returns an error indicating the feature is not yet
// implemented. This test pins that behavior so regressions are caught and
// the stub cannot silently change to a no-op.
func TestMCPTransportSendCampfireKeySignedStub(t *testing.T) {
	srv := newTestServer(t)
	adapter := &conventionTransportAdapter{server: srv}
	ctx := context.Background()

	msgID, err := adapter.SendCampfireKeySigned(ctx, "test-campfire-id", []byte(`{}`), []string{"test:tag"}, nil)
	if err == nil {
		t.Fatal("SendCampfireKeySigned: expected error for unimplemented stub, got nil")
	}
	if msgID != "" {
		t.Errorf("SendCampfireKeySigned: expected empty msgID on error, got %q", msgID)
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("SendCampfireKeySigned: expected 'not yet implemented' in error, got: %v", err)
	}
}

// TestMCPTransportSendFutureAndAwaitStub documents the current stub behavior:
// SendFutureAndAwait returns an error indicating the feature is not yet
// implemented. This test pins that behavior so regressions are caught.
func TestMCPTransportSendFutureAndAwaitStub(t *testing.T) {
	srv := newTestServer(t)
	adapter := &conventionTransportAdapter{server: srv}
	ctx := context.Background()

	result, err := adapter.SendFutureAndAwait(ctx, "test-campfire-id", []byte(`{}`), []string{"test:future"}, 0)
	if err == nil {
		t.Fatal("SendFutureAndAwait: expected error for unimplemented stub, got nil")
	}
	if result != nil {
		t.Errorf("SendFutureAndAwait: expected nil result on error, got %v", result)
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("SendFutureAndAwait: expected 'not yet implemented' in error, got: %v", err)
	}
}

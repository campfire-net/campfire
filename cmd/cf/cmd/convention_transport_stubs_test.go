package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
)

// TestCLITransportSendCampfireKeySignedStub documents the current stub behavior:
// SendCampfireKeySigned returns an error indicating the feature is not yet
// implemented. This test pins that behavior so regressions are caught and
// the stub cannot silently change to a no-op.
func TestCLITransportSendCampfireKeySignedStub(t *testing.T) {
	dir := t.TempDir()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	adapter := &cliTransportAdapter{agentID: id, store: s}
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

// TestCLITransportSendFutureAndAwaitStub documents the current stub behavior:
// SendFutureAndAwait returns an error indicating the feature is not yet
// implemented. This test pins that behavior so regressions are caught.
func TestCLITransportSendFutureAndAwaitStub(t *testing.T) {
	dir := t.TempDir()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	adapter := &cliTransportAdapter{agentID: id, store: s}
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

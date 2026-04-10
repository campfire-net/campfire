package cmd

// Regression tests for campfireagent-9c4: StoreSyncer.Sync swallows transport errors.
//
// Before the fix: syncCampfire returned void, so StoreSyncer.Sync always returned nil
// after the membership lookup, even when the filesystem transport directory had been
// deleted. This meant the Subscribe error path in subscribe.go:154 was unreachable
// when a StoreSyncer was installed — subscriptions on deleted transports ran forever.
//
// Fix: syncFromFilesystem now returns an error when ListMessages fails.
// syncCampfire propagates filesystem errors and returns error.
// StoreSyncer.Sync propagates the error from syncCampfire.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// TestStoreSyncer_ReturnsErrorOnBrokenTransport verifies that StoreSyncer.Sync
// returns a non-nil error when the filesystem transport directory has been deleted.
// Before the fix, it always returned nil.
func TestStoreSyncer_ReturnsErrorOnBrokenTransport(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, campfire.RoleFull)

	// Verify Sync works before deleting the transport dir.
	syncer := NewStoreSyncer(agentID, s)
	if err := syncer.Sync(campfireID); err != nil {
		t.Fatalf("Sync before deletion: %v", err)
	}

	// Delete the transport directory.
	cfDir := filepath.Join(transportBaseDir, campfireID)
	if err := os.RemoveAll(cfDir); err != nil {
		t.Fatalf("removing transport dir: %v", err)
	}

	// Sync must now return an error — this is the core of the bug fix.
	if err := syncer.Sync(campfireID); err == nil {
		t.Error("StoreSyncer.Sync returned nil after transport dir deletion; expected non-nil error")
	}
}

// TestStoreSyncer_Subscribe_TerminatesOnBrokenTransport verifies that a Subscribe
// call using a StoreSyncer closes the Messages() channel when the filesystem
// transport directory is deleted. Before the fix, the subscription ran forever
// because StoreSyncer.Sync always returned nil, so subscribe.go:154 was never
// reached.
func TestStoreSyncer_Subscribe_TerminatesOnBrokenTransport(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, campfire.RoleFull)

	// Install a StoreSyncer — this is the production code path used by cmd/cf.
	client := protocol.New(s, agentID)
	client.SetSyncer(NewStoreSyncer(agentID, s))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   campfireID,
		PollInterval: 50 * time.Millisecond,
	})

	// Let the subscription start polling.
	time.Sleep(100 * time.Millisecond)

	// Delete the transport directory to inject a permanent transport failure.
	cfDir := filepath.Join(transportBaseDir, campfireID)
	if err := os.RemoveAll(cfDir); err != nil {
		t.Fatalf("removing transport dir: %v", err)
	}

	// The subscription MUST close Messages() within 2 seconds.
	// Before the fix, it ran forever because StoreSyncer.Sync returned nil.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range sub.Messages() {
		}
	}()

	select {
	case <-done:
		// Channel closed — correct. Err() must be non-nil.
		if err := sub.Err(); err == nil {
			t.Error("sub.Err() is nil after transport dir deletion; expected non-nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Messages() channel did not close within 2 seconds after transport dir deletion: subscription ran forever (StoreSyncer.Sync is still swallowing errors)")
	}
}


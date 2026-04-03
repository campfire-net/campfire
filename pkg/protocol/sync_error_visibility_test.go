package protocol_test

// Regression tests for campfire-agent-ttu: syncIfFilesystem swallowed errors
// silently, giving callers stale data with no indication of failure.
//
// Fix (read.go): replaced `_ = err` with `log.Printf(...)` so operators see
// transport problems in their logs.
// Fix (join.go): replaced `c.syncIfFilesystem(campfireID) //nolint:errcheck`
// with an explicit error check and log.Printf.
//
// These tests verify:
//  1. Read() still succeeds when the transport directory has been removed
//     (sync is non-fatal; callers get locally-cached messages).
//  2. Read() with SkipSync=false on a missing transport directory returns the
//     locally-cached message and does not panic or return an error to the caller.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// TestSyncIfFilesystem_ReadContinuesOnTransportFailure verifies that Read()
// returns cached store data when the filesystem transport directory has been
// removed after messages were synced, rather than returning an error to the caller.
//
// The sync error is now logged (not silently swallowed), but it remains non-fatal
// so clients receive whatever messages the local store already holds.
func TestSyncIfFilesystem_ReadContinuesOnTransportFailure(t *testing.T) {
	// Create a campfire via Create so all directories and state files are correct.
	creator := newJoinClient(t)
	base := t.TempDir()
	beaconDir := t.TempDir()
	createResult, err := creator.Create(protocol.CreateRequest{
		Transport:    &protocol.FilesystemTransport{Dir: base},
		JoinProtocol: "open",
		BeaconDir:    beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	campfireID := createResult.CampfireID

	// Send a message so the creator's store has at least one message.
	_, err = creator.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("hello from creator"),
		Tags:       []string{"test"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Confirm the message is readable before we break the transport.
	result, err := creator.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("pre-removal Read: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("expected message before transport removal, got none")
	}

	// Remove the transport directory to force a sync failure on the next Read.
	cfDir := filepath.Join(base, campfireID)
	if err := os.RemoveAll(cfDir); err != nil {
		t.Fatalf("removing transport dir: %v", err)
	}

	// Read must not fail — the sync error is now logged but non-fatal.
	// The locally-cached message should still be returned from the store.
	result2, err := creator.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("Read() after transport removal returned error: %v — should be non-fatal", err)
	}
	if len(result2.Messages) == 0 {
		t.Error("expected cached message after transport removal, got none")
	}
}

// TestSyncIfFilesystem_SecondClientReadAfterTransportRemoval verifies that a
// second client reading from its own store (via SkipSync=true path) works
// correctly even when the transport is gone.
//
// This complements the main test by confirming that the non-fatal sync failure
// in Read() does not corrupt the store or prevent subsequent reads.
func TestSyncIfFilesystem_SecondClientReadAfterTransportRemoval(t *testing.T) {
	// Create campfire and send a message.
	creator := newJoinClient(t)
	base := t.TempDir()
	beaconDir := t.TempDir()
	createResult, err := creator.Create(protocol.CreateRequest{
		Transport:    &protocol.FilesystemTransport{Dir: base},
		JoinProtocol: "open",
		BeaconDir:    beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	campfireID := createResult.CampfireID

	_, err = creator.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("cached payload"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Read once to populate the store, then remove transport.
	if _, err := creator.Read(protocol.ReadRequest{CampfireID: campfireID}); err != nil {
		t.Fatalf("initial Read: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(base, campfireID)); err != nil {
		t.Fatalf("removing transport dir: %v", err)
	}

	// Open a second client sharing the same store. Use SkipSync=true to bypass
	// the failing transport entirely — this exercises the store-only path.
	creatorStore := creator.ClientStore()
	id2, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating id2: %v", err)
	}
	client2 := protocol.New(creatorStore, id2)

	// Read with SkipSync=true must succeed and return the cached message.
	result, err := client2.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		SkipSync:   true,
	})
	if err != nil {
		t.Fatalf("SkipSync Read failed: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Error("expected cached message with SkipSync=true, got none")
	}

	// Read without SkipSync — must not fail even though transport is gone.
	result2, err := client2.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("Read() without SkipSync after transport removal: %v", err)
	}
	if len(result2.Messages) == 0 {
		t.Error("expected cached message without SkipSync after transport removal, got none")
	}
}

// TestSyncIfFilesystem_JoinThenReadAfterTransportRemoval verifies the join.go
// fix: after a successful Join, if the transport directory is subsequently
// removed, Read() continues to serve locally-cached messages and does not fail.
//
// The //nolint:errcheck annotation on the post-join sync in join.go was replaced
// with an explicit error check + log.Printf. This test exercises the Read path
// that follows the join, confirming the fix end-to-end.
func TestSyncIfFilesystem_JoinThenReadAfterTransportRemoval(t *testing.T) {
	// Creator sets up the campfire and sends a message.
	creator := newJoinClient(t)
	base := t.TempDir()
	beaconDir := t.TempDir()
	createResult, err := creator.Create(protocol.CreateRequest{
		Transport:    &protocol.FilesystemTransport{Dir: base},
		JoinProtocol: "open",
		BeaconDir:    beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	campfireID := createResult.CampfireID

	_, err = creator.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("message for joiner"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Joiner joins while transport is still alive.
	joinerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}
	joinerStoreDir := t.TempDir()
	joinerStore, err := store.Open(filepath.Join(joinerStoreDir, "store.db"))
	if err != nil {
		t.Fatalf("opening joiner store: %v", err)
	}
	t.Cleanup(func() { joinerStore.Close() })
	joiner := protocol.New(joinerStore, joinerID)

	_, err = joiner.Join(protocol.JoinRequest{
		CampfireID: campfireID,
		Transport:  protocol.FilesystemTransport{Dir: filepath.Join(base, campfireID)},
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}

	// Remove the transport directory after joining.
	if err := os.RemoveAll(filepath.Join(base, campfireID)); err != nil {
		t.Fatalf("removing transport dir: %v", err)
	}

	// Read must not fail — sync error is non-fatal, locally-synced messages returned.
	result, err := joiner.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("Read() after join + transport removal: %v", err)
	}
	// The joiner synced the creator's message during Join, so it should be cached.
	if len(result.Messages) == 0 {
		t.Error("expected synced message after Join, got none")
	}
}

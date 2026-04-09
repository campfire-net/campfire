package protocol_test

// Tests for the Syncer interface: SetSyncer, syncViaInterface, and the nil-safe
// fallback path. These tests verify campfireagent-eac: transport-agnostic sync
// for protocol.Client so that Read/Subscribe/Await work for all transport types.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// --- Mock syncers ---

// countingSyncer implements protocol.Syncer and records how many times Sync was called.
type countingSyncer struct {
	count int64
	calls []string
}

func (s *countingSyncer) Sync(campfireID string) error {
	atomic.AddInt64(&s.count, 1)
	s.calls = append(s.calls, campfireID)
	return nil
}

// failSyncer implements protocol.Syncer and always returns an error.
type failSyncer struct {
	err error
}

func (s *failSyncer) Sync(_ string) error { return s.err }

// fsSyncer is a test Syncer that reads from a given filesystem transport into
// the store. This simulates what a production HTTP syncer does: pull from remote
// peers and store messages locally before Read queries the store.
type fsSyncer struct {
	tr  *fs.Transport
	s   store.Store
	cfID *identity.Identity // campfire identity for signature verification
}

func (f *fsSyncer) Sync(campfireID string) error {
	msgs, err := f.tr.ListMessages(campfireID)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if !m.VerifySignature() {
			continue
		}
		if !m.VerifyProvenance() {
			continue
		}
		f.s.AddMessage(store.MessageRecordFromMessage(campfireID, &m, store.NowNano())) //nolint:errcheck
	}
	return nil
}

// --- Tests ---

// TestSyncer_NilSyncer_DoesNotPanic verifies that a Client with no Syncer set
// (nil syncer) does not panic on Read — it falls back to syncIfFilesystem.
func TestSyncer_NilSyncer_DoesNotPanic(t *testing.T) {
	campfireID, _, _, s := setupTestCampfire(t)
	defer s.Close()

	// Client with no syncer — nil syncer is the default.
	client := protocol.New(s, nil)

	// Read should not panic.
	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read with nil syncer: %v", err)
	}
	_ = result
}

// TestSyncer_SetSyncer_CalledOnRead verifies that when a Syncer is set,
// it is called during Read (before the store query).
func TestSyncer_SetSyncer_CalledOnRead(t *testing.T) {
	campfireID, _, _, s := setupTestCampfire(t)
	defer s.Close()

	syn := &countingSyncer{}
	client := protocol.New(s, nil)
	client.SetSyncer(syn)

	_, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if atomic.LoadInt64(&syn.count) == 0 {
		t.Error("expected Syncer.Sync to be called at least once, but was not called")
	}
	if len(syn.calls) > 0 && syn.calls[0] != campfireID {
		t.Errorf("Syncer.Sync called with campfireID=%q, want %q", syn.calls[0], campfireID)
	}
}

// TestSyncer_SetSyncer_NotCalledWhenSkipSync verifies that SkipSync=true
// suppresses the Syncer call.
func TestSyncer_SetSyncer_NotCalledWhenSkipSync(t *testing.T) {
	campfireID, _, _, s := setupTestCampfire(t)
	defer s.Close()

	syn := &countingSyncer{}
	client := protocol.New(s, nil)
	client.SetSyncer(syn)

	_, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
		SkipSync:         true,
	})
	if err != nil {
		t.Fatalf("Read with SkipSync: %v", err)
	}

	if atomic.LoadInt64(&syn.count) != 0 {
		t.Errorf("expected Syncer.Sync NOT called with SkipSync=true, got %d calls", syn.count)
	}
}

// TestSyncer_ReplaceSyncer verifies that SetSyncer replaces a previously set syncer.
func TestSyncer_ReplaceSyncer(t *testing.T) {
	campfireID, _, _, s := setupTestCampfire(t)
	defer s.Close()

	first := &countingSyncer{}
	second := &countingSyncer{}

	client := protocol.New(s, nil)
	client.SetSyncer(first)
	client.SetSyncer(second) // replaces first

	_, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if atomic.LoadInt64(&first.count) != 0 {
		t.Errorf("first syncer should not be called after replacement, got %d calls", first.count)
	}
	if atomic.LoadInt64(&second.count) == 0 {
		t.Error("second syncer should be called after replacement")
	}
}

// TestSyncer_NilResetsFallback verifies that SetSyncer(nil) restores the
// filesystem-only fallback behaviour — filesystem messages still sync.
func TestSyncer_NilResetsFallback(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	msg := writeTransportMessage(t, cfID, tr, campfireID, "transport message", []string{"note"})

	syn := &countingSyncer{}
	client := protocol.New(s, nil)
	client.SetSyncer(syn)
	client.SetSyncer(nil) // reset to nil → falls back to syncIfFilesystem

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read after reset to nil: %v", err)
	}

	// Filesystem fallback should have synced the message.
	found := false
	for _, m := range result.Messages {
		if m.ID == msg.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected message %q after nil reset (filesystem fallback), got %d messages", msg.ID, len(result.Messages))
	}

	// The counting syncer should NOT have been called.
	if atomic.LoadInt64(&syn.count) != 0 {
		t.Errorf("counting syncer should not be called after reset to nil, got %d calls", syn.count)
	}
}

// TestSyncer_SyncErrorNonFatalForRead verifies that a Syncer error does not
// cause Read to fail — it serves from the local store cache.
func TestSyncer_SyncErrorNonFatalForRead(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Pre-populate the store by syncing once.
	msg := writeTransportMessage(t, cfID, tr, campfireID, "cached message", []string{"note"})
	_, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("initial Read: %v", err)
	}

	// Install a failing syncer.
	client.SetSyncer(&failSyncer{err: context.DeadlineExceeded})

	// Read should succeed (non-fatal) and return cached messages.
	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read with failing syncer: %v", err)
	}

	found := false
	for _, m := range result.Messages {
		if m.ID == msg.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected cached message %q after non-fatal sync error, got %d messages", msg.ID, len(result.Messages))
	}
}

// TestSyncer_InjectedSyncerDeliversMessages is the core integration scenario:
// a Syncer that injects messages into the store causes Read to return those
// messages. This simulates what a real HTTP syncer does.
//
// Without Syncer: Read on an HTTP campfire returns empty (current behaviour
// before this fix). With Syncer set: Read returns messages from the store after
// the syncer has populated it.
func TestSyncer_InjectedSyncerDeliversMessages(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	// Write a message to the filesystem transport only (not in store).
	// This simulates a message that arrived at a remote peer over HTTP.
	msg := writeTransportMessage(t, cfID, tr, campfireID, "remote message", []string{"status"})

	// Verify the message is NOT yet in the store (no sync has occurred).
	noSyncClient := protocol.New(s, nil)
	resultEmpty, err := noSyncClient.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
		SkipSync:         true, // bypass sync entirely
	})
	if err != nil {
		t.Fatalf("Read without sync: %v", err)
	}
	for _, m := range resultEmpty.Messages {
		if m.ID == msg.ID {
			t.Fatal("message should NOT be visible before sync (proving sync is required)")
		}
	}

	// Install an fsSyncer (simulates HTTP syncer behaviour).
	syn := &fsSyncer{tr: tr, s: s, cfID: cfID}
	client := protocol.New(s, nil)
	client.SetSyncer(syn)

	// Read with syncer set — message should appear.
	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read with syncer: %v", err)
	}

	found := false
	for _, m := range result.Messages {
		if m.ID == msg.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("message %q should be visible after Syncer.Sync, got %d messages", msg.ID, len(result.Messages))
	}
}

// TestSyncer_Subscribe_UsesInjectedSyncer verifies that Subscribe calls the
// Syncer on each poll iteration.
func TestSyncer_Subscribe_UsesInjectedSyncer(t *testing.T) {
	campfireID, _, _, s := setupTestCampfire(t)
	defer s.Close()

	syn := &countingSyncer{}
	client := protocol.New(s, nil)
	client.SetSyncer(syn)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   campfireID,
		PollInterval: 50 * time.Millisecond, // fast for test
	})

	// Wait for at least one poll cycle.
	select {
	case <-ctx.Done():
	case <-time.After(300 * time.Millisecond):
	}

	cancel()
	// Drain the channel to let the goroutine exit.
	for range sub.Messages() {
	}

	if atomic.LoadInt64(&syn.count) == 0 {
		t.Error("expected Syncer.Sync to be called during Subscribe poll, but was not called")
	}
}

// TestSyncer_Await_UsesInjectedSyncer verifies that Await calls the Syncer
// and finds a fulfilling message injected by the Syncer.
func TestSyncer_Await_UsesInjectedSyncer(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	// Sync a future message into the store.
	futureMsg := writeTransportMessage(t, cfID, tr, campfireID, "pending future", []string{"future"})
	_, err := protocol.New(s, nil).Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// Write a fulfillment to the transport (not yet in store).
	fulfillMsg := writeTransportMessageWithAntecedents(
		t, cfID, tr, campfireID,
		"fulfillment",
		[]string{"fulfills"},
		[]string{futureMsg.ID},
	)

	// Syncer reads from filesystem transport into store.
	syn := &fsSyncer{tr: tr, s: s, cfID: cfID}
	client := protocol.New(s, nil)
	client.SetSyncer(syn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.Await(ctx, protocol.AwaitRequest{
		CampfireID:   campfireID,
		TargetMsgID:  futureMsg.ID,
		Timeout:      4 * time.Second,
		PollInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil fulfillment from Await")
	}
	if result.ID != fulfillMsg.ID {
		t.Errorf("Await returned wrong fulfillment: got %q, want %q", result.ID, fulfillMsg.ID)
	}
}

// TestSyncer_FilesystemTransport_StillWorks verifies that existing filesystem
// transport tests still pass when no Syncer is set (regression guard).
func TestSyncer_FilesystemTransport_StillWorks(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	// No SetSyncer call — uses syncIfFilesystem fallback.
	client := protocol.New(s, nil)

	msg := writeTransportMessage(t, cfID, tr, campfireID, "filesystem message", []string{"note"})

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	found := false
	for _, m := range result.Messages {
		if m.ID == msg.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("filesystem message %q should be visible via syncIfFilesystem fallback", msg.ID)
	}
}

// TestSyncer_Interface_Satisfaction verifies that the types we intend to use
// as Syncers actually satisfy the protocol.Syncer interface at compile time.
// This is a compile-time assertion, not a runtime test.
var _ protocol.Syncer = (*countingSyncer)(nil)
var _ protocol.Syncer = (*failSyncer)(nil)
var _ protocol.Syncer = (*fsSyncer)(nil)


package protocol_test

import (
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/internal/store"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// newPendingTestCampfire spins up an Init-backed client (store wrapped in the
// projection middleware, the real Init path) with a filesystem campfire, and
// returns the client, campfire ID, and config dir (for reopen tests).
func newPendingTestCampfire(t *testing.T) (*protocol.Client, string, string) {
	t.Helper()
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	configDir := t.TempDir()

	client, _, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	res, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return client, res.CampfireID, configDir
}

func readByTag(t *testing.T, client *protocol.Client, campfireID, tag string) []protocol.Message {
	t.Helper()
	res, err := client.Read(protocol.ReadRequest{CampfireID: campfireID, Tags: []string{tag}})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return res.Messages
}

// TestBuildPending_BuffersThenFlushDelivers is the core ground-source test:
// BuildPending commits offline (no transport write), and FlushPending later
// delivers the same message — with a stable ID and a provenance hop — to the
// real filesystem transport.
func TestBuildPending_BuffersThenFlushDelivers(t *testing.T) {
	client, campfireID, _ := newPendingTestCampfire(t)
	t.Cleanup(func() { client.Close() })

	id, err := client.BuildPending(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("hello offline"),
		Tags:       []string{"pending:t1"},
	})
	if err != nil {
		t.Fatalf("BuildPending: %v", err)
	}
	if id == "" {
		t.Fatal("BuildPending returned empty ID")
	}

	// Not yet delivered: a read of the campfire returns nothing for this tag.
	if msgs := readByTag(t, client, campfireID, "pending:t1"); len(msgs) != 0 {
		t.Fatalf("before flush: got %d messages, want 0 (message must not be delivered yet)", len(msgs))
	}

	// It is buffered.
	ps := client.ClientStore().(store.PendingMessageStore)
	buffered, err := ps.ListPendingMessages(campfireID)
	if err != nil {
		t.Fatalf("ListPendingMessages: %v", err)
	}
	if len(buffered) != 1 || buffered[0].ID != id {
		t.Fatalf("buffered = %+v, want exactly the built message %s", buffered, id)
	}

	// Flush delivers it.
	n, err := client.FlushPending(campfireID)
	if err != nil {
		t.Fatalf("FlushPending: %v", err)
	}
	if n != 1 {
		t.Fatalf("FlushPending delivered %d, want 1", n)
	}

	msgs := readByTag(t, client, campfireID, "pending:t1")
	if len(msgs) != 1 {
		t.Fatalf("after flush: got %d messages, want 1", len(msgs))
	}
	if msgs[0].ID != id {
		t.Errorf("delivered ID = %q, want stable ID %q (must match BuildPending)", msgs[0].ID, id)
	}
	if string(msgs[0].Payload) != "hello offline" {
		t.Errorf("payload = %q, want %q", msgs[0].Payload, "hello offline")
	}
	if len(msgs[0].Provenance) == 0 {
		t.Error("delivered message has no provenance hop (hop must be added at flush)")
	}

	// Buffer is drained; a second flush is a no-op.
	if n, err := client.FlushPending(campfireID); err != nil || n != 0 {
		t.Errorf("second FlushPending = (%d, %v), want (0, nil)", n, err)
	}
}

// TestFlushPending_CrashRecovery verifies a message buffered before a restart is
// delivered after reopening the store at the same config dir (durable buffer).
func TestFlushPending_CrashRecovery(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	configDir := t.TempDir()

	client, _, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	res, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	campfireID := res.CampfireID

	id, err := client.BuildPending(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("survive restart"),
		Tags:       []string{"pending:crash"},
	})
	if err != nil {
		t.Fatalf("BuildPending: %v", err)
	}
	client.Close() // simulate restart before flush

	client2, _, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("reopen Init: %v", err)
	}
	t.Cleanup(func() { client2.Close() })

	n, err := client2.FlushPending(campfireID)
	if err != nil {
		t.Fatalf("FlushPending after reopen: %v", err)
	}
	if n != 1 {
		t.Fatalf("delivered %d after reopen, want 1", n)
	}
	msgs := readByTag(t, client2, campfireID, "pending:crash")
	if len(msgs) != 1 || msgs[0].ID != id {
		t.Errorf("after reopen+flush: got %d msgs (want 1) with stable ID %s", len(msgs), id)
	}
}

// TestFlushPending_RedeliverIdempotent verifies the crash-window guard: if a
// message is flushed and then re-buffered (simulating a crash between transport
// write and buffer removal), re-flushing it does NOT create a duplicate on the
// campfire — delivery is idempotent on the stable ID.
func TestFlushPending_RedeliverIdempotent(t *testing.T) {
	client, campfireID, _ := newPendingTestCampfire(t)
	t.Cleanup(func() { client.Close() })

	if _, err := client.BuildPending(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("once"),
		Tags:       []string{"pending:idem"},
	}); err != nil {
		t.Fatalf("BuildPending: %v", err)
	}

	ps := client.ClientStore().(store.PendingMessageStore)
	buffered, err := ps.ListPendingMessages(campfireID)
	if err != nil || len(buffered) != 1 {
		t.Fatalf("ListPendingMessages = (%v, %v), want 1 buffered", buffered, err)
	}
	saved := buffered[0]

	if n, err := client.FlushPending(campfireID); err != nil || n != 1 {
		t.Fatalf("first FlushPending = (%d, %v), want (1, nil)", n, err)
	}

	// Re-buffer the already-delivered message (crash between deliver and delete).
	if err := ps.AddPendingMessage(saved); err != nil {
		t.Fatalf("re-AddPendingMessage: %v", err)
	}
	if n, err := client.FlushPending(campfireID); err != nil || n != 1 {
		t.Fatalf("re-FlushPending = (%d, %v), want (1, nil)", n, err)
	}

	// Despite two deliveries, exactly one message exists on the campfire.
	if msgs := readByTag(t, client, campfireID, "pending:idem"); len(msgs) != 1 {
		t.Errorf("got %d messages after re-delivery, want 1 (delivery must be idempotent on stable ID)", len(msgs))
	}
}

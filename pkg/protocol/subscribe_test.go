package protocol_test

// Tests for protocol.Client.Subscribe() — campfire-agent-5wi.
//
// All tests use a real store and real filesystem transport. No mocks.
// Tests verify cursor advancement, context cancellation, goroutine cleanup,
// error recovery, convention server integration, and empty campfire delivery.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// collectN drains up to n messages from the subscription within timeout.
// Returns the messages collected. Fails the test if fewer than n arrive.
func collectN(t *testing.T, sub *protocol.Subscription, n int, timeout time.Duration) []string {
	t.Helper()
	var ids []string
	deadline := time.After(timeout)
	for len(ids) < n {
		select {
		case msg, ok := <-sub.Messages():
			if !ok {
				t.Fatalf("channel closed after %d messages, expected %d", len(ids), n)
			}
			ids = append(ids, msg.ID)
		case <-deadline:
			t.Fatalf("timeout: collected %d/%d messages", len(ids), n)
		}
	}
	return ids
}

// TestSubscribe_CursorAdvancement sends 3 messages, reads them all, sends 2 more,
// and asserts only 2 more arrive (cursor must have advanced past the first 3).
func TestSubscribe_CursorAdvancement(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	// Send 3 initial messages.
	var first3IDs []string
	for i := 0; i < 3; i++ {
		msg, err := client.Send(protocol.SendRequest{
			CampfireID: campfireID,
			Payload:    []byte("initial"),
			Tags:       []string{"status"},
		})
		if err != nil {
			t.Fatalf("Send initial %d: %v", i, err)
		}
		first3IDs = append(first3IDs, msg.ID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:     campfireID,
		AfterTimestamp: 0,
		PollInterval:   50 * time.Millisecond,
	})

	// Read all 3 initial messages.
	got3 := collectN(t, sub, 3, 5*time.Second)

	// Verify IDs match.
	first3Set := map[string]bool{}
	for _, id := range first3IDs {
		first3Set[id] = true
	}
	for _, id := range got3 {
		if !first3Set[id] {
			t.Errorf("unexpected message ID %q in first batch", id)
		}
	}

	// Send 2 more messages.
	var next2IDs []string
	for i := 0; i < 2; i++ {
		msg, err := client.Send(protocol.SendRequest{
			CampfireID: campfireID,
			Payload:    []byte("follow-up"),
			Tags:       []string{"status"},
		})
		if err != nil {
			t.Fatalf("Send follow-up %d: %v", i, err)
		}
		next2IDs = append(next2IDs, msg.ID)
	}

	// Read exactly 2 more — must NOT re-deliver the first 3.
	got2 := collectN(t, sub, 2, 5*time.Second)

	next2Set := map[string]bool{}
	for _, id := range next2IDs {
		next2Set[id] = true
	}
	for _, id := range got2 {
		if !next2Set[id] {
			t.Errorf("cursor did not advance: got unexpected message ID %q (should be one of %v)", id, next2IDs)
		}
	}

	// Verify no extra messages arrive in the channel within 200ms.
	time.Sleep(200 * time.Millisecond)
	cancel()
	// Drain any remaining (should be none).
	extra := 0
	for range sub.Messages() {
		extra++
	}
	if extra != 0 {
		t.Errorf("expected 0 extra messages after cursor advancement, got %d", extra)
	}
}

// TestSubscribe_ContextCancellation verifies that cancelling the context closes
// the Messages() channel within 1 second and the goroutine does not leak.
func TestSubscribe_ContextCancellation(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	goroutinesBefore := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   campfireID,
		PollInterval: 50 * time.Millisecond,
	})

	// Give the goroutine a moment to start.
	time.Sleep(100 * time.Millisecond)

	cancel()

	// Channel must close within 1 second.
	select {
	case _, ok := <-sub.Messages():
		if ok {
			// Drain more if needed.
			for range sub.Messages() {
			}
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Messages() channel did not close within 1 second after context cancellation")
	}

	// Wait for goroutine cleanup. Give the runtime a moment to reap the goroutine.
	time.Sleep(100 * time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= goroutinesBefore+1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	goroutinesAfter := runtime.NumGoroutine()
	// Allow a tolerance of +1 goroutine only (runtime GC goroutine transient).
	// A single leaked Subscribe goroutine must cause this test to fail.
	if goroutinesAfter > goroutinesBefore+1 {
		t.Errorf("goroutine leak: before=%d, after=%d (tolerance=1)", goroutinesBefore, goroutinesAfter)
	}
}

// TestSubscribe_ErrorRecovery verifies that deleting the transport dir mid-subscribe
// causes the subscription to close the Messages() channel within a reasonable timeout.
// A subscription that keeps running forever after transport deletion is a failure.
func TestSubscribe_ErrorRecovery(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   campfireID,
		PollInterval: 50 * time.Millisecond,
	})

	// Let the subscription start.
	time.Sleep(100 * time.Millisecond)

	// Delete the transport directory to inject an error mid-subscribe.
	cfTransportDir := filepath.Join(transportDir, campfireID)
	if err := os.RemoveAll(cfTransportDir); err != nil {
		t.Fatalf("removing transport dir: %v", err)
	}

	// The subscription MUST close the channel within 2 seconds.
	// A subscription that keeps running forever is a failure — not an acceptable outcome.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range sub.Messages() {
		}
	}()

	select {
	case <-done:
		// Channel closed — correct. The error from Read() must be non-nil.
		if err := sub.Err(); err == nil {
			t.Error("expected Err() to be non-nil after transport dir deletion, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Messages() channel did not close within 2 seconds after transport dir deletion: subscription kept running forever")
	}
}

// TestSubscribe_ConventionServerIntegration sends a convention operation message
// and verifies it arrives via the subscription channel with matching payload.
func TestSubscribe_ConventionServerIntegration(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   campfireID,
		PollInterval: 50 * time.Millisecond,
	})

	// Give subscription a moment to start.
	time.Sleep(100 * time.Millisecond)

	// Send a convention operation message.
	wantPayload := `{"jsonrpc":"2.0","method":"tools/list","id":1}`
	sent, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(wantPayload),
		Tags:       []string{"convention:operation"},
	})
	if err != nil {
		t.Fatalf("Send convention op: %v", err)
	}

	// Receive it via subscription.
	select {
	case msg, ok := <-sub.Messages():
		if !ok {
			t.Fatal("channel closed before receiving convention message")
		}
		if msg.ID != sent.ID {
			t.Errorf("message ID mismatch: got %q, want %q", msg.ID, sent.ID)
		}
		if string(msg.Payload) != wantPayload {
			t.Errorf("payload mismatch: got %q, want %q", string(msg.Payload), wantPayload)
		}
		foundTag := false
		for _, tg := range msg.Tags {
			if tg == "convention:operation" {
				foundTag = true
			}
		}
		if !foundTag {
			t.Errorf("expected tag 'convention:operation' in message, got %v", msg.Tags)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: convention message not received via subscription")
	}
}

// TestSubscribe_EmptyCampfire subscribes to an empty campfire, then sends one
// message. The subscription must deliver it — it must not return immediately
// just because the campfire is empty at subscribe time.
func TestSubscribe_EmptyCampfire(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   campfireID,
		PollInterval: 50 * time.Millisecond,
	})

	// Let the subscription poll once on an empty campfire.
	time.Sleep(150 * time.Millisecond)

	// Now send a message.
	sent, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("first message"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// The subscription must deliver this message.
	select {
	case msg, ok := <-sub.Messages():
		if !ok {
			t.Fatal("channel closed before delivering first message")
		}
		if msg.ID != sent.ID {
			t.Errorf("ID mismatch: got %q, want %q", msg.ID, sent.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: message not delivered on empty campfire subscription")
	}
}

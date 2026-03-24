package poller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

func openTestStore(t *testing.T) store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addMembership(t *testing.T, s store.Store, campfireID string) {
	t.Helper()
	err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: t.TempDir(),
		JoinProtocol: "open",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	})
	if err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
}

func insertMessage(t *testing.T, s store.Store, campfireID string, tags []string, ts int64) string {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	msg, err := message.NewMessage(priv, pub, []byte("test payload"), tags, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	// Override timestamp for test ordering.
	msg.Timestamp = ts

	rec := store.MessageRecordFromMessage(campfireID, msg, time.Now().UnixNano())
	if _, err := s.AddMessage(rec); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	return msg.ID
}

func TestPollerReadsMessages(t *testing.T) {
	s := openTestStore(t)
	campfireID := "test-campfire-001"
	addMembership(t, s, campfireID)

	// Insert 3 messages with increasing timestamps.
	var insertedIDs []string
	for i := 0; i < 3; i++ {
		id := insertMessage(t, s, campfireID, []string{"status"}, int64(1000+i))
		insertedIDs = append(insertedIDs, id)
	}

	// Collect messages via handler.
	var mu sync.Mutex
	var received []string
	handler := func(msg store.MessageRecord) error {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, msg.ID)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	p := New(s, Config{
		CampfireID:   campfireID,
		PollInterval: 50 * time.Millisecond,
	}, handler)

	go p.Run(ctx)

	// Wait for messages to be processed.
	deadline := time.After(1500 * time.Millisecond)
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timeout: received %d of 3 messages", n)
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Stop poller before checking state to avoid SQLite contention.
	cancel()
	time.Sleep(50 * time.Millisecond) // let goroutine exit

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 3 {
		t.Errorf("received %d messages, want 3", len(received))
	}
	for i, id := range insertedIDs {
		if i < len(received) && received[i] != id {
			t.Errorf("received[%d] = %q, want %q", i, received[i], id)
		}
	}

	// Verify cursor was advanced.
	cursor, err := s.GetReadCursor(campfireID)
	if err != nil {
		t.Fatalf("get cursor: %v", err)
	}
	if cursor != 1002 { // last message timestamp
		t.Errorf("cursor = %d, want 1002", cursor)
	}
}

func TestPollerCursorAfterConfirm(t *testing.T) {
	s := openTestStore(t)
	campfireID := "test-campfire-002"
	addMembership(t, s, campfireID)

	insertMessage(t, s, campfireID, nil, 2000)
	insertMessage(t, s, campfireID, nil, 2001)
	insertMessage(t, s, campfireID, nil, 2002)

	// Handler fails on second message.
	callCount := 0
	handler := func(msg store.MessageRecord) error {
		callCount++
		if callCount == 2 {
			return fmt.Errorf("simulated failure")
		}
		return nil
	}

	p := New(s, Config{
		CampfireID:   campfireID,
		PollInterval: 50 * time.Millisecond,
	}, handler)

	// Run one poll cycle manually.
	_, err := p.poll(context.Background())
	if err == nil {
		t.Fatal("expected error from handler")
	}

	// Cursor should be at first message only (handler succeeded for that one).
	cursor, _ := s.GetReadCursor(campfireID)
	if cursor != 2000 {
		t.Errorf("cursor = %d, want 2000 (only first message confirmed)", cursor)
	}
}

func TestPollerUrgentTags(t *testing.T) {
	s := openTestStore(t)
	campfireID := "test-campfire-003"
	addMembership(t, s, campfireID)

	// Non-urgent message.
	insertMessage(t, s, campfireID, []string{"status"}, 3000)

	handler := func(msg store.MessageRecord) error { return nil }
	p := New(s, Config{
		CampfireID: campfireID,
		PollInterval: 100 * time.Millisecond,
		UrgentTags:   []string{"blocker", "gate"},
	}, handler)

	urgent, err := p.poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if urgent {
		t.Error("expected non-urgent for status tag")
	}

	// Now insert an urgent message.
	insertMessage(t, s, campfireID, []string{"blocker"}, 3001)

	urgent, err = p.poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !urgent {
		t.Error("expected urgent for blocker tag")
	}
}

// TestRunAll verifies that RunAll starts multiple pollers and they all
// receive messages.  When ctx is cancelled, RunAll returns.
func TestRunAll_CancelsCleanly(t *testing.T) {
	s := openTestStore(t)

	campfireIDs := []string{"runall-001", "runall-002", "runall-003"}
	for _, id := range campfireIDs {
		addMembership(t, s, id)
		insertMessage(t, s, id, nil, 9000)
	}

	var mu sync.Mutex
	seen := make(map[string]bool)
	handler := func(msg store.MessageRecord) error {
		mu.Lock()
		seen[msg.CampfireID] = true
		mu.Unlock()
		return nil
	}

	cfgs := make([]Config, len(campfireIDs))
	for i, id := range campfireIDs {
		cfgs[i] = Config{
			CampfireID:   id,
			PollInterval: 30 * time.Millisecond,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- RunAll(ctx, s, cfgs, handler)
	}()

	// Wait for all three campfires to be seen.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timeout: only %d of 3 campfires seen", n)
		case <-time.After(20 * time.Millisecond):
		}
	}

	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("RunAll returned %v, want context.Canceled", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("RunAll did not return after context cancellation")
	}
}

// TestRunAll_EmptyConfigs verifies that RunAll with no campfires returns
// when the context is cancelled.
func TestRunAll_EmptyConfigs(t *testing.T) {
	s := openTestStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := RunAll(ctx, s, nil, func(msg store.MessageRecord) error { return nil })
	// With no goroutines started, only the ctx.Done channel fires.
	if err != context.DeadlineExceeded && err != context.Canceled {
		t.Errorf("RunAll (empty) returned %v, want context error", err)
	}
}

func TestPollerEmptyStore(t *testing.T) {
	s := openTestStore(t)
	campfireID := "test-campfire-004"
	addMembership(t, s, campfireID)

	handler := func(msg store.MessageRecord) error {
		t.Error("handler should not be called for empty store")
		return nil
	}

	p := New(s, Config{
		CampfireID:   campfireID,
		PollInterval: 50 * time.Millisecond,
	}, handler)

	urgent, err := p.poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if urgent {
		t.Error("expected non-urgent for empty store")
	}
}

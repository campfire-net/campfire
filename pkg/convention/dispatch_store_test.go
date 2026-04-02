package convention

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestMemoryDispatchStore_GetCursor_DefaultZero(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()

	val, err := store.GetCursor(ctx, "server1", "campfire1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 0 {
		t.Fatalf("expected 0 for missing cursor, got %d", val)
	}
}

func TestMemoryDispatchStore_AdvanceCursor_ForwardOnly(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()

	// First advance: 0 → 100, should succeed.
	advanced, err := store.AdvanceCursor(ctx, "server1", "campfire1", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !advanced {
		t.Fatal("expected cursor to advance from 0 to 100")
	}

	val, _ := store.GetCursor(ctx, "server1", "campfire1")
	if val != 100 {
		t.Fatalf("expected cursor 100, got %d", val)
	}

	// Same timestamp: should not advance.
	advanced, err = store.AdvanceCursor(ctx, "server1", "campfire1", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if advanced {
		t.Fatal("expected no advance for equal timestamp")
	}

	// Earlier timestamp: should not advance.
	advanced, err = store.AdvanceCursor(ctx, "server1", "campfire1", 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if advanced {
		t.Fatal("expected no advance for earlier timestamp")
	}

	// Later timestamp: should advance.
	advanced, err = store.AdvanceCursor(ctx, "server1", "campfire1", 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !advanced {
		t.Fatal("expected cursor to advance to 200")
	}

	val, _ = store.GetCursor(ctx, "server1", "campfire1")
	if val != 200 {
		t.Fatalf("expected cursor 200, got %d", val)
	}
}

func TestMemoryDispatchStore_AdvanceCursor_IndependentKeys(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()

	store.AdvanceCursor(ctx, "server1", "campfire1", 500)
	store.AdvanceCursor(ctx, "server2", "campfire1", 100)
	store.AdvanceCursor(ctx, "server1", "campfire2", 300)

	v1, _ := store.GetCursor(ctx, "server1", "campfire1")
	v2, _ := store.GetCursor(ctx, "server2", "campfire1")
	v3, _ := store.GetCursor(ctx, "server1", "campfire2")

	if v1 != 500 {
		t.Errorf("expected 500, got %d", v1)
	}
	if v2 != 100 {
		t.Errorf("expected 100, got %d", v2)
	}
	if v3 != 300 {
		t.Errorf("expected 300, got %d", v3)
	}
}

func TestMemoryDispatchStore_AdvanceCursor_ConcurrentSafety(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := int64(1); i <= goroutines; i++ {
		ts := i
		go func() {
			defer wg.Done()
			store.AdvanceCursor(ctx, "server1", "campfire1", ts)
		}()
	}
	wg.Wait()

	val, err := store.GetCursor(ctx, "server1", "campfire1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != goroutines {
		t.Fatalf("expected cursor %d, got %d", int64(goroutines), val)
	}
}

func TestMemoryDispatchStore_MarkDispatched_InsertIfNotExists(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()

	// First call: should succeed.
	inserted, err := store.MarkDispatched(ctx, "campfire1", "msg1", "server1", "", "testconv", "testop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !inserted {
		t.Fatal("expected first MarkDispatched to return true")
	}

	// Second call with same message: should return false.
	inserted, err = store.MarkDispatched(ctx, "campfire1", "msg1", "server1", "", "testconv", "testop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inserted {
		t.Fatal("expected duplicate MarkDispatched to return false")
	}

	// Different messageID: should succeed.
	inserted, err = store.MarkDispatched(ctx, "campfire1", "msg2", "server1", "", "testconv", "testop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !inserted {
		t.Fatal("expected MarkDispatched with different messageID to return true")
	}
}

func TestMemoryDispatchStore_StatusTransitions(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()

	store.MarkDispatched(ctx, "campfire1", "msg1", "server1", "", "testconv", "testop")
	store.MarkDispatched(ctx, "campfire1", "msg2", "server1", "", "testconv", "testop")

	// Initial status.
	status, err := store.GetDispatchStatus(ctx, "campfire1", "msg1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "dispatched" {
		t.Fatalf("expected 'dispatched', got %q", status)
	}

	// Transition to fulfilled.
	if err := store.MarkFulfilled(ctx, "campfire1", "msg1"); err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}
	status, _ = store.GetDispatchStatus(ctx, "campfire1", "msg1")
	if status != "fulfilled" {
		t.Fatalf("expected 'fulfilled', got %q", status)
	}

	// Transition to failed.
	if err := store.MarkFailed(ctx, "campfire1", "msg2"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	status, _ = store.GetDispatchStatus(ctx, "campfire1", "msg2")
	if status != "failed" {
		t.Fatalf("expected 'failed', got %q", status)
	}
}

func TestMemoryDispatchStore_GetDispatchStatus_Missing(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()

	status, err := store.GetDispatchStatus(ctx, "campfire1", "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "" {
		t.Fatalf("expected empty string for missing record, got %q", status)
	}
}

func TestMemoryDispatchStore_MarkFulfilled_NoRecord(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()
	// Should not error when no record exists.
	if err := store.MarkFulfilled(ctx, "campfire1", "nonexistent"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMemoryDispatchStore_MarkFailed_NoRecord(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()
	// Should not error when no record exists.
	if err := store.MarkFailed(ctx, "campfire1", "nonexistent"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMemoryDispatchStore_ListStaleDispatches(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()

	// Insert a message and immediately backdate its DispatchedAt.
	store.MarkDispatched(ctx, "campfire1", "old-msg", "server1", "", "testconv", "testop")
	store.mu.Lock()
	k := dispatchKey{campfireID: "campfire1", messageID: "old-msg"}
	store.dispatches[k].DispatchedAt = time.Now().Add(-2 * time.Hour)
	store.mu.Unlock()

	// Insert a recent message.
	store.MarkDispatched(ctx, "campfire1", "new-msg", "server1", "", "testconv", "testop")

	// Insert a fulfilled old message — should not appear as stale.
	store.MarkDispatched(ctx, "campfire1", "old-fulfilled", "server1", "", "testconv", "testop")
	store.mu.Lock()
	k2 := dispatchKey{campfireID: "campfire1", messageID: "old-fulfilled"}
	store.dispatches[k2].DispatchedAt = time.Now().Add(-2 * time.Hour)
	store.dispatches[k2].Status = "fulfilled"
	store.mu.Unlock()

	stale, err := store.ListStaleDispatches(ctx, 1*time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale record, got %d", len(stale))
	}
	if stale[0].MessageID != "old-msg" {
		t.Fatalf("expected 'old-msg', got %q", stale[0].MessageID)
	}
}

func TestMemoryDispatchStore_CleanupOldDispatches(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()

	// Old fulfilled — should be cleaned.
	store.MarkDispatched(ctx, "campfire1", "old-fulfilled", "server1", "", "testconv", "testop")
	store.mu.Lock()
	k1 := dispatchKey{campfireID: "campfire1", messageID: "old-fulfilled"}
	store.dispatches[k1].DispatchedAt = time.Now().Add(-2 * time.Hour)
	store.dispatches[k1].Status = "fulfilled"
	store.mu.Unlock()

	// Old failed — should be cleaned.
	store.MarkDispatched(ctx, "campfire1", "old-failed", "server1", "", "testconv", "testop")
	store.mu.Lock()
	k2 := dispatchKey{campfireID: "campfire1", messageID: "old-failed"}
	store.dispatches[k2].DispatchedAt = time.Now().Add(-2 * time.Hour)
	store.dispatches[k2].Status = "failed"
	store.mu.Unlock()

	// Old dispatched — should NOT be cleaned (only fulfilled/failed are removed).
	store.MarkDispatched(ctx, "campfire1", "old-dispatched", "server1", "", "testconv", "testop")
	store.mu.Lock()
	k3 := dispatchKey{campfireID: "campfire1", messageID: "old-dispatched"}
	store.dispatches[k3].DispatchedAt = time.Now().Add(-2 * time.Hour)
	store.mu.Unlock()

	// Recent fulfilled — should NOT be cleaned.
	store.MarkDispatched(ctx, "campfire1", "recent-fulfilled", "server1", "", "testconv", "testop")
	store.mu.Lock()
	k4 := dispatchKey{campfireID: "campfire1", messageID: "recent-fulfilled"}
	store.dispatches[k4].Status = "fulfilled"
	store.mu.Unlock()

	removed, err := store.CleanupOldDispatches(ctx, 1*time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removed, got %d", removed)
	}

	// Verify old-dispatched and recent-fulfilled still exist.
	s1, _ := store.GetDispatchStatus(ctx, "campfire1", "old-dispatched")
	if s1 != "dispatched" {
		t.Errorf("old-dispatched should survive cleanup, got %q", s1)
	}
	s2, _ := store.GetDispatchStatus(ctx, "campfire1", "recent-fulfilled")
	if s2 != "fulfilled" {
		t.Errorf("recent-fulfilled should survive cleanup, got %q", s2)
	}

	// Verify cleaned records are gone.
	s3, _ := store.GetDispatchStatus(ctx, "campfire1", "old-fulfilled")
	if s3 != "" {
		t.Errorf("old-fulfilled should be removed, got %q", s3)
	}
	s4, _ := store.GetDispatchStatus(ctx, "campfire1", "old-failed")
	if s4 != "" {
		t.Errorf("old-failed should be removed, got %q", s4)
	}
}

func TestMemoryDispatchStore_ConcurrentMarkDispatched(t *testing.T) {
	store := NewMemoryDispatchStore()
	ctx := context.Background()
	const goroutines = 50

	var wg sync.WaitGroup
	insertedCount := int64(0)
	var mu sync.Mutex

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			inserted, _ := store.MarkDispatched(ctx, "campfire1", "msg1", "server1", "", "testconv", "testop")
			if inserted {
				mu.Lock()
				insertedCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if insertedCount != 1 {
		t.Fatalf("expected exactly 1 goroutine to insert, got %d", insertedCount)
	}
}

func TestMemoryDispatchStore_InterfaceCompliance(t *testing.T) {
	// Compile-time check that MemoryDispatchStore satisfies DispatchStore.
	var _ DispatchStore = (*MemoryDispatchStore)(nil)
}

package http

import (
	"fmt"
	"testing"
	"time"
)

// Unit tests for DedupTable. Port block: 460-479 reserved for future integration tests.

// TestDedupMissOnNewID verifies that a fresh message ID is not a duplicate.
func TestDedupMissOnNewID(t *testing.T) {
	d := newDedupTable(100, time.Hour)
	if d.See("msg-1") {
		t.Error("new ID should not be a duplicate")
	}
}

// TestDedupHitOnRepeat verifies that the same ID is a duplicate on second call.
func TestDedupHitOnRepeat(t *testing.T) {
	d := newDedupTable(100, time.Hour)
	if d.See("msg-1") {
		t.Fatal("first See should return false (new)")
	}
	if !d.See("msg-1") {
		t.Error("second See of same ID should return true (duplicate)")
	}
}

// TestDedupMultipleIDs verifies independent tracking of multiple IDs.
func TestDedupMultipleIDs(t *testing.T) {
	d := newDedupTable(100, time.Hour)
	ids := []string{"alpha", "beta", "gamma", "delta"}
	for _, id := range ids {
		if d.See(id) {
			t.Errorf("first See(%q) should be new", id)
		}
	}
	for _, id := range ids {
		if !d.See(id) {
			t.Errorf("second See(%q) should be duplicate", id)
		}
	}
}

// TestDedupTTLExpiry verifies that an entry is forgotten after TTL elapses.
// Uses a very short TTL to avoid slow tests.
func TestDedupTTLExpiry(t *testing.T) {
	d := newDedupTable(100, 10*time.Millisecond)
	if d.See("expiry-id") {
		t.Fatal("first See should be new")
	}
	// Wait for TTL to elapse.
	time.Sleep(20 * time.Millisecond)
	// After expiry, the ID should be treated as new.
	if d.See("expiry-id") {
		t.Error("after TTL expiry, ID should be treated as new")
	}
}

// TestDedupEvictsOldestOnFull verifies that when the table is at capacity,
// the oldest entry is evicted to make room for the new one.
func TestDedupEvictsOldestOnFull(t *testing.T) {
	maxSize := 5
	d := newDedupTable(maxSize, time.Hour)

	// Fill table to capacity.
	for i := 0; i < maxSize; i++ {
		id := fmt.Sprintf("msg-%d", i)
		d.See(id)
	}
	if d.Len() != maxSize {
		t.Fatalf("expected %d entries, got %d", maxSize, d.Len())
	}

	// Adding one more should evict the oldest (msg-0).
	d.See("msg-new")
	if d.Len() != maxSize {
		t.Fatalf("after eviction, expected %d entries, got %d", maxSize, d.Len())
	}

	// msg-0 should have been evicted — no longer a duplicate.
	if d.See("msg-0") {
		t.Error("msg-0 should have been evicted and treated as new")
	}

	// msg-new should still be a duplicate.
	if !d.See("msg-new") {
		t.Error("msg-new should still be a duplicate")
	}
}

// TestDedupLen verifies the Len method.
func TestDedupLen(t *testing.T) {
	d := newDedupTable(100, time.Hour)
	if d.Len() != 0 {
		t.Errorf("empty table should have Len 0, got %d", d.Len())
	}
	d.See("a")
	d.See("b")
	if d.Len() != 2 {
		t.Errorf("expected Len 2, got %d", d.Len())
	}
}

// TestDedupDefaultTable verifies that the default table uses the expected capacity.
func TestDedupDefaultTable(t *testing.T) {
	d := defaultDedupTable()
	if d.maxSize != 100_000 {
		t.Errorf("default maxSize = %d, want 100000", d.maxSize)
	}
	if d.ttl != time.Hour {
		t.Errorf("default TTL = %v, want 1h", d.ttl)
	}
}

// TestDedupPrunesExpiredBeforeEviction verifies that expired entries are pruned
// before falling back to oldest-entry eviction. This ensures that if the table
// is "full" with expired entries, a new entry gets added without evicting a live one.
func TestDedupPrunesExpiredBeforeEviction(t *testing.T) {
	maxSize := 3
	d := newDedupTable(maxSize, 10*time.Millisecond)

	// Fill table with short-TTL entries.
	d.See("old-1")
	d.See("old-2")
	d.See("old-3")

	// Wait for all to expire.
	time.Sleep(20 * time.Millisecond)

	// Now add a new entry — expired entries should be pruned, not evicted by order.
	// After this, the table should have exactly 1 live entry (new-1).
	d.See("new-1")

	if d.Len() != 1 {
		t.Errorf("after pruning expired + adding new, expected Len 1, got %d", d.Len())
	}

	// old entries should be gone (pruned, not evicted — same result for caller).
	if d.See("old-1") {
		t.Error("old-1 should have expired and been pruned, not still a duplicate")
	}
}

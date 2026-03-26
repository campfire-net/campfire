package http

import (
	"sync"
	"time"
)

// dedupEntry records a seen message ID with its expiry time.
type dedupEntry struct {
	expiresAt time.Time
	// insertOrder is used for eviction when the table is at capacity.
	// Lower value = older entry, should be evicted first.
	insertOrder uint64
}

// DedupTable is an in-memory LRU deduplication table keyed by message ID.
// It prevents re-forwarding of already-seen messages.
//
// Properties:
//   - Default capacity: 100,000 entries
//   - Default TTL: 1 hour
//   - On hit: reports duplicate, caller should drop silently
//   - On full: evicts the oldest entry (lowest insertOrder) before adding the new one
//   - Thread-safe: all methods are safe for concurrent use
type DedupTable struct {
	mu       sync.Mutex
	entries  map[string]*dedupEntry
	maxSize  int
	ttl      time.Duration
	counter  uint64 // monotonic insert order counter
}

// newDedupTable creates a new DedupTable with the given capacity and TTL.
func newDedupTable(maxSize int, ttl time.Duration) *DedupTable {
	return &DedupTable{
		entries: make(map[string]*dedupEntry, maxSize),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// defaultDedupTable creates a DedupTable with the spec-recommended defaults:
// 100,000 entries and 1 hour TTL.
func defaultDedupTable() *DedupTable {
	return newDedupTable(100_000, time.Hour)
}

// See reports whether the message ID has been seen before and records it if not.
// Returns true if the message was already seen (duplicate, caller should drop).
// Returns false if the message is new (caller may proceed).
//
// On a full table, the oldest entry is evicted to make room before the new ID is added.
// After TTL expiry, a previously-seen ID is treated as new (partition recovery).
func (d *DedupTable) See(id string) (duplicate bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	// Check if already seen and still within TTL.
	if e, ok := d.entries[id]; ok {
		if now.Before(e.expiresAt) {
			return true // duplicate
		}
		// TTL expired — treat as new, overwrite the entry below.
		delete(d.entries, id)
	}

	// Prune expired entries periodically (lazy eviction on write).
	// This keeps the map bounded without a separate goroutine.
	// Only prune when near capacity to avoid O(n) on every write.
	if len(d.entries) >= d.maxSize {
		d.pruneExpired(now)
	}

	// If still at capacity after pruning expired entries, evict the oldest entry.
	if len(d.entries) >= d.maxSize {
		d.evictOldest()
	}

	d.counter++
	d.entries[id] = &dedupEntry{
		expiresAt:   now.Add(d.ttl),
		insertOrder: d.counter,
	}
	return false // new message
}

// pruneExpired removes all entries whose TTL has elapsed.
// Must be called with d.mu held.
func (d *DedupTable) pruneExpired(now time.Time) {
	for k, e := range d.entries {
		if now.After(e.expiresAt) {
			delete(d.entries, k)
		}
	}
}

// evictOldest removes the entry with the lowest insertOrder (the oldest entry).
// Must be called with d.mu held. The table must be non-empty.
func (d *DedupTable) evictOldest() {
	var oldestKey string
	var oldestOrder uint64
	first := true
	for k, e := range d.entries {
		if first || e.insertOrder < oldestOrder {
			oldestKey = k
			oldestOrder = e.insertOrder
			first = false
		}
	}
	if oldestKey != "" {
		delete(d.entries, oldestKey)
	}
}

// Len returns the current number of entries in the dedup table.
// Useful for testing and monitoring.
func (d *DedupTable) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries)
}

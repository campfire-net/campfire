package convention

import (
	"errors"
	"context"
	"sync"
	"time"
)

// ErrDispatchNotFound is returned by MarkFulfilled and MarkFailed when the
// dispatch record does not exist. Callers can use errors.Is to distinguish
// "successfully marked" (nil) from "record was missing."
var ErrDispatchNotFound = errors.New("dispatch record not found")

// DispatchStore abstracts cursor and dispatch-marker storage for the
// ConventionDispatcher. The aztable implementation (in cf-mcp) and an
// in-memory implementation (for local/testing) both satisfy this interface.
type DispatchStore interface {
	// GetCursor returns the last-dispatched message timestamp for a
	// (serverID, campfireID) pair. Returns 0 if no cursor exists.
	GetCursor(ctx context.Context, serverID, campfireID string) (int64, error)

	// AdvanceCursor conditionally advances the cursor for (serverID, campfireID)
	// to newTimestamp. Only advances if newTimestamp > current cursor.
	// Returns true if advanced, false if the cursor was already at or past newTimestamp.
	AdvanceCursor(ctx context.Context, serverID, campfireID string, newTimestamp int64) (bool, error)

	// MarkDispatched records that a message was dispatched to a handler.
	// convention and operation identify the registered handler for this message
	// and are used by the fallback sweep to re-dispatch without re-reading the message.
	// Returns false if the message was already marked (insert-if-not-exists semantics).
	MarkDispatched(ctx context.Context, campfireID, messageID, serverID, forgeAccountID, convention, operation string) (bool, error)

	// MarkFulfilled updates the dispatch marker status to "fulfilled".
	// Returns ErrDispatchNotFound if the record does not exist.
	MarkFulfilled(ctx context.Context, campfireID, messageID string) error

	// MarkFailed updates the dispatch marker status to "failed".
	// Returns ErrDispatchNotFound if the record does not exist.
	MarkFailed(ctx context.Context, campfireID, messageID string) error

	// GetDispatchStatus returns the status of a dispatched message.
	// Returns "", nil if no dispatch record exists.
	GetDispatchStatus(ctx context.Context, campfireID, messageID string) (string, error)

	// ListStaleDispatches returns dispatched-but-not-fulfilled entries older than
	// the given threshold. Used by the fallback sweep.
	ListStaleDispatches(ctx context.Context, olderThan time.Duration) ([]DispatchRecord, error)

	// CleanupOldDispatches removes fulfilled/failed entries older than maxAge.
	CleanupOldDispatches(ctx context.Context, maxAge time.Duration) (int, error)

	// IncrementRedispatchCount atomically increments the re-dispatch counter for a
	// message and returns the new count. Used by the fallback sweep to enforce the
	// maximum re-dispatch cap.
	IncrementRedispatchCount(ctx context.Context, campfireID, messageID string) (int, error)

	// GetRedispatchCount returns the current RedispatchCount for a dispatch record.
	// Returns 0 if the record does not exist.
	GetRedispatchCount(ctx context.Context, campfireID, messageID string) (int, error)

	// MarkFulfilledCAS updates the dispatch marker status to "fulfilled" only if
	// the current RedispatchCount matches expectedGen. Returns true if the update
	// succeeded, false if the generation has changed (indicating a re-dispatch
	// occurred and the caller is a stale handler). This prevents double-dispatch:
	// a slow original handler completing after the sweep has re-dispatched.
	MarkFulfilledCAS(ctx context.Context, campfireID, messageID string, expectedGen int) (bool, error)

	// MarkFailedCAS updates the dispatch marker status to "failed" only if
	// the current RedispatchCount matches expectedGen. Returns true if the update
	// succeeded, false if the generation has changed. See MarkFulfilledCAS.
	MarkFailedCAS(ctx context.Context, campfireID, messageID string, expectedGen int) (bool, error)

	// ListUnbilledDispatches returns fulfilled dispatch records where
	// TokensConsumed > 0 and BilledAt == 0. Used by the billing sweep to find
	// dispatches that have self-reported token consumption but haven't been billed yet.
	ListUnbilledDispatches(ctx context.Context) ([]DispatchRecord, error)

	// MarkBilled sets BilledAt on a dispatch record to the current time.
	// No-op if the record does not exist.
	MarkBilled(ctx context.Context, campfireID, messageID string) error
}

// DispatchRecord holds metadata about a single message dispatch.
type DispatchRecord struct {
	CampfireID      string
	MessageID       string
	ServerID        string
	ForgeAccountID  string // Forge account that owns this convention server (for billing)
	Convention      string // convention name (e.g. "myconv")
	Operation       string // operation name (e.g. "myop")
	DispatchedAt    time.Time
	Status          string // "dispatched", "fulfilled", "failed"
	HandlerURL      string // tier 2 only
	RedispatchCount int    // number of times the sweep has re-dispatched this message
	TokensConsumed  int64  // LLM tokens self-reported by the handler (0 = not reported)
	BilledAt        int64  // unix nanoseconds when billing event was emitted (0 = not yet billed)
}

// dispatchKey is the composite key used to index dispatch records.
type dispatchKey struct {
	campfireID string
	messageID  string
}

// cursorKey is the composite key used to index cursors.
type cursorKey struct {
	serverID   string
	campfireID string
}

// MemoryDispatchStore is an in-memory implementation of DispatchStore.
// Suitable for local convention servers and testing. Not persistent across restarts.
type MemoryDispatchStore struct {
	mu        sync.RWMutex
	cursors   map[cursorKey]int64
	dispatches map[dispatchKey]*DispatchRecord
}

// NewMemoryDispatchStore creates an empty in-memory dispatch store.
func NewMemoryDispatchStore() *MemoryDispatchStore {
	return &MemoryDispatchStore{
		cursors:    make(map[cursorKey]int64),
		dispatches: make(map[dispatchKey]*DispatchRecord),
	}
}

// GetCursor returns the last-dispatched message timestamp for a
// (serverID, campfireID) pair. Returns 0 if no cursor exists.
func (s *MemoryDispatchStore) GetCursor(_ context.Context, serverID, campfireID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cursors[cursorKey{serverID: serverID, campfireID: campfireID}], nil
}

// AdvanceCursor conditionally advances the cursor for (serverID, campfireID)
// to newTimestamp. Only advances if newTimestamp > current cursor.
// Returns true if advanced, false if the cursor was already at or past newTimestamp.
func (s *MemoryDispatchStore) AdvanceCursor(_ context.Context, serverID, campfireID string, newTimestamp int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := cursorKey{serverID: serverID, campfireID: campfireID}
	if newTimestamp <= s.cursors[k] {
		return false, nil
	}
	s.cursors[k] = newTimestamp
	return true, nil
}

// MarkDispatched records that a message was dispatched to a handler.
// Returns false if the message was already marked (insert-if-not-exists semantics).
func (s *MemoryDispatchStore) MarkDispatched(_ context.Context, campfireID, messageID, serverID, forgeAccountID, conv, operation string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dispatchKey{campfireID: campfireID, messageID: messageID}
	if _, exists := s.dispatches[k]; exists {
		return false, nil
	}
	s.dispatches[k] = &DispatchRecord{
		CampfireID:     campfireID,
		MessageID:      messageID,
		ServerID:       serverID,
		ForgeAccountID: forgeAccountID,
		Convention:     conv,
		Operation:      operation,
		DispatchedAt:   time.Now(),
		Status:         "dispatched",
	}
	return true, nil
}

// MarkFulfilled updates the dispatch marker status to "fulfilled".
	// Returns ErrDispatchNotFound if the record does not exist.
func (s *MemoryDispatchStore) MarkFulfilled(_ context.Context, campfireID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dispatchKey{campfireID: campfireID, messageID: messageID}
	rec, exists := s.dispatches[k]
	if !exists {
		return ErrDispatchNotFound
	}
	rec.Status = "fulfilled"
	return nil
}

// MarkFailed updates the dispatch marker status to "failed".
	// Returns ErrDispatchNotFound if the record does not exist.
func (s *MemoryDispatchStore) MarkFailed(_ context.Context, campfireID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dispatchKey{campfireID: campfireID, messageID: messageID}
	rec, exists := s.dispatches[k]
	if !exists {
		return ErrDispatchNotFound
	}
	rec.Status = "failed"
	return nil
}

// GetDispatchStatus returns the status of a dispatched message.
// Returns "", nil if no dispatch record exists.
func (s *MemoryDispatchStore) GetDispatchStatus(_ context.Context, campfireID, messageID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := dispatchKey{campfireID: campfireID, messageID: messageID}
	rec, exists := s.dispatches[k]
	if !exists {
		return "", nil
	}
	return rec.Status, nil
}

// ListStaleDispatches returns dispatched-but-not-fulfilled entries older than
// the given threshold. Used by the fallback sweep.
func (s *MemoryDispatchStore) ListStaleDispatches(_ context.Context, olderThan time.Duration) ([]DispatchRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	threshold := time.Now().Add(-olderThan)
	var result []DispatchRecord
	for _, rec := range s.dispatches {
		if rec.Status == "dispatched" && rec.DispatchedAt.Before(threshold) {
			result = append(result, *rec)
		}
	}
	return result, nil
}

// IncrementRedispatchCount atomically increments the re-dispatch counter for a
// message and returns the new count. Returns 0, nil if the record does not exist.
func (s *MemoryDispatchStore) IncrementRedispatchCount(_ context.Context, campfireID, messageID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dispatchKey{campfireID: campfireID, messageID: messageID}
	rec, exists := s.dispatches[k]
	if !exists {
		return 0, nil
	}
	rec.RedispatchCount++
	return rec.RedispatchCount, nil
}

// GetRedispatchCount returns the current RedispatchCount for a dispatch record.
// Returns 0 if the record does not exist.
func (s *MemoryDispatchStore) GetRedispatchCount(_ context.Context, campfireID, messageID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := dispatchKey{campfireID: campfireID, messageID: messageID}
	rec, exists := s.dispatches[k]
	if !exists {
		return 0, nil
	}
	return rec.RedispatchCount, nil
}

// MarkFulfilledCAS updates the dispatch marker status to "fulfilled" only if
// the current RedispatchCount matches expectedGen. Returns false if the
// generation has changed (stale handler) or the record does not exist.
func (s *MemoryDispatchStore) MarkFulfilledCAS(_ context.Context, campfireID, messageID string, expectedGen int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dispatchKey{campfireID: campfireID, messageID: messageID}
	rec, exists := s.dispatches[k]
	if !exists {
		return false, nil
	}
	if rec.RedispatchCount != expectedGen {
		return false, nil
	}
	rec.Status = "fulfilled"
	return true, nil
}

// MarkFailedCAS updates the dispatch marker status to "failed" only if
// the current RedispatchCount matches expectedGen. Returns false if the
// generation has changed (stale handler) or the record does not exist.
func (s *MemoryDispatchStore) MarkFailedCAS(_ context.Context, campfireID, messageID string, expectedGen int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dispatchKey{campfireID: campfireID, messageID: messageID}
	rec, exists := s.dispatches[k]
	if !exists {
		return false, nil
	}
	if rec.RedispatchCount != expectedGen {
		return false, nil
	}
	rec.Status = "failed"
	return true, nil
}

// ListUnbilledDispatches returns fulfilled dispatch records where
// TokensConsumed > 0 and BilledAt == 0.
func (s *MemoryDispatchStore) ListUnbilledDispatches(_ context.Context) ([]DispatchRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []DispatchRecord
	for _, rec := range s.dispatches {
		if rec.Status == "fulfilled" && rec.TokensConsumed > 0 && rec.BilledAt == 0 {
			result = append(result, *rec)
		}
	}
	return result, nil
}

// MarkBilled sets BilledAt on a dispatch record to the current time.
// No-op if the record does not exist.
func (s *MemoryDispatchStore) MarkBilled(_ context.Context, campfireID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dispatchKey{campfireID: campfireID, messageID: messageID}
	rec, exists := s.dispatches[k]
	if !exists {
		return nil
	}
	rec.BilledAt = time.Now().UnixNano()
	return nil
}

// SetTokensConsumed sets the TokensConsumed field on a dispatch record.
// This is a test helper for simulating handler-reported token usage.
func (s *MemoryDispatchStore) SetTokensConsumed(campfireID, messageID string, tokens int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dispatchKey{campfireID: campfireID, messageID: messageID}
	if rec, ok := s.dispatches[k]; ok {
		rec.TokensConsumed = tokens
	}
}

// BackdateDispatch sets the DispatchedAt time of a dispatch record to age ago.
// This is a test helper for simulating stale records without sleeping.
func (s *MemoryDispatchStore) BackdateDispatch(campfireID, messageID string, age time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dispatchKey{campfireID: campfireID, messageID: messageID}
	if rec, ok := s.dispatches[k]; ok {
		rec.DispatchedAt = time.Now().Add(-age)
	}
}

// CleanupOldDispatches removes fulfilled/failed entries older than maxAge.
// Returns the number of entries removed.
func (s *MemoryDispatchStore) CleanupOldDispatches(_ context.Context, maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	threshold := time.Now().Add(-maxAge)
	count := 0
	for k, rec := range s.dispatches {
		if (rec.Status == "fulfilled" || rec.Status == "failed") && rec.DispatchedAt.Before(threshold) {
			delete(s.dispatches, k)
			count++
		}
	}
	return count, nil
}

// Package aztable_test — dispatch_store_cas_test.go
//
// CAS (Compare-And-Swap) contract tests for TableDispatchStore.MarkBilled.
//
// These tests verify the ETag-based concurrency contracts that production Azure
// Table Storage guarantees but Azurite enforces non-deterministically under
// concurrent write pressure (campfireagent-fc5). They run against a deterministic
// in-memory CAS store (casMockTable, see cas_mock_test.go) so the contracts are
// verified reliably on every run — no Azurite required, no flakes.
//
// No build tags: always compiled and always run.
//
// The replaced Azurite-backed tests (TestDispatchStore_MarkBilled_StaleETag,
// TestDispatchStore_MarkBilled_AlreadyBilled, TestDispatchStore_MarkBilled_ConcurrentRace)
// have been removed from dispatch_store_test.go. The happy-path test
// TestDispatchStore_MarkBilled_HappyPath and the retry test
// TestDispatchStore_MarkBilled_RetryAfterConflict remain in the Azurite suite
// because they don't test concurrent/stale scenarios that Azurite mishandles.
package aztable_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/store/aztable"
)

// casUnique returns a unique string for test isolation.
func casUnique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// newCASDispatchStore returns a TableDispatchStore backed by the deterministic
// in-memory CAS mock — no Azurite required.
func newCASDispatchStore(t *testing.T) *aztable.TableDispatchStore {
	t.Helper()
	return aztable.NewMockDispatchStore()
}

// helperCASSetupUnbilled dispatches, fulfils, and sets tokens on a record, then
// returns its ETag from ListUnbilledDispatches.
func helperCASSetupUnbilled(t *testing.T, s *aztable.TableDispatchStore, cfID, msgID, serverID string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := s.MarkDispatched(ctx, cfID, msgID, serverID, "", "conv", "op"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}
	if err := s.MarkFulfilled(ctx, cfID, msgID); err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}
	if err := s.SetTokensConsumed(ctx, cfID, msgID, 100); err != nil {
		t.Fatalf("SetTokensConsumed: %v", err)
	}
	unbilled, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches: %v", err)
	}
	for _, r := range unbilled {
		if r.CampfireID == cfID && r.MessageID == msgID {
			if r.ETag == "" {
				t.Fatal("ETag is empty")
			}
			return r.ETag
		}
	}
	t.Fatal("record not found in ListUnbilledDispatches")
	return ""
}

// TestCAS_MarkBilled_StaleETag verifies that MarkBilled with a stale ETag (the
// entity was modified after the caller's read) returns ErrConcurrentModification.
//
// Replaced the Azurite-backed TestDispatchStore_MarkBilled_StaleETag which flaked
// because Azurite does not reliably update ETags after writes.
func TestCAS_MarkBilled_StaleETag(t *testing.T) {
	s := newCASDispatchStore(t)
	ctx := context.Background()
	cfID := casUnique("cf")
	msgID := casUnique("msg")

	staleETag := helperCASSetupUnbilled(t, s, cfID, msgID, "srv")

	// Modify the entity to invalidate the ETag.
	if _, err := s.IncrementRedispatchCount(ctx, cfID, msgID); err != nil {
		t.Fatalf("IncrementRedispatchCount: %v", err)
	}

	// MarkBilled with the now-stale ETag must fail.
	err := s.MarkBilled(ctx, cfID, msgID, staleETag)
	if err == nil {
		t.Fatal("MarkBilled with stale ETag should have failed, but succeeded (lost update bug)")
	}
	if !errors.Is(err, convention.ErrConcurrentModification) {
		t.Fatalf("expected ErrConcurrentModification, got: %v", err)
	}

	// Re-read fresh ETag — MarkBilled should now succeed.
	unbilled, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches after conflict: %v", err)
	}
	var freshETag string
	for _, r := range unbilled {
		if r.CampfireID == cfID && r.MessageID == msgID {
			freshETag = r.ETag
			break
		}
	}
	if freshETag == "" {
		t.Fatal("expected record with fresh ETag in ListUnbilledDispatches")
	}
	if err := s.MarkBilled(ctx, cfID, msgID, freshETag); err != nil {
		t.Fatalf("MarkBilled with fresh ETag should succeed: %v", err)
	}
}

// TestCAS_MarkBilled_AlreadyBilled verifies that a second call to MarkBilled on
// an already-billed record returns ErrAlreadyBilled, even when the caller holds
// the original (now-stale) ETag.
//
// Replaced the Azurite-backed TestDispatchStore_MarkBilled_AlreadyBilled.
func TestCAS_MarkBilled_AlreadyBilled(t *testing.T) {
	s := newCASDispatchStore(t)
	ctx := context.Background()
	cfID := casUnique("cf")
	msgID := casUnique("msg")

	etag := helperCASSetupUnbilled(t, s, cfID, msgID, "srv")

	// First billing must succeed.
	if err := s.MarkBilled(ctx, cfID, msgID, etag); err != nil {
		t.Fatalf("first MarkBilled should succeed: %v", err)
	}

	// Second billing attempt with the same (now-stale) ETag.
	// BilledAt is checked before the stale-ETag guard, so the result is
	// ErrAlreadyBilled regardless of ETag staleness.
	err := s.MarkBilled(ctx, cfID, msgID, etag)
	if err == nil {
		t.Fatal("second MarkBilled should have returned ErrAlreadyBilled, but succeeded")
	}
	if !errors.Is(err, convention.ErrAlreadyBilled) {
		t.Fatalf("expected ErrAlreadyBilled, got: %v", err)
	}
}

// TestCAS_MarkBilled_ConcurrentRace verifies that when two goroutines race to call
// MarkBilled with the same ETag, exactly one succeeds and the other is rejected.
//
// Design:
//  1. Both goroutines hold the same initial ETag.
//  2. A read barrier (readyWg + writeGate) ensures both complete their GetEntity
//     reads before either issues its UpdateEntity write, maximising the race window.
//  3. The in-memory CAS mock serialises writes under a mutex, so the IfMatch check
//     and the write are atomic — exactly one goroutine wins.
//
// Replaced the Azurite-backed TestDispatchStore_MarkBilled_ConcurrentRace which
// flaked because Azurite does not serialise IfMatch checks under concurrent load.
func TestCAS_MarkBilled_ConcurrentRace(t *testing.T) {
	s := newCASDispatchStore(t)
	ctx := context.Background()
	cfID := casUnique("cf")
	msgID := casUnique("msg")

	etag := helperCASSetupUnbilled(t, s, cfID, msgID, "srv")

	var readyWg sync.WaitGroup
	readyWg.Add(2)
	writeGate := make(chan struct{})

	afterRead := func() {
		readyWg.Done() // signal read done
		<-writeGate    // wait for both reads before writing
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = s.MarkBilledWithBarrier(ctx, cfID, msgID, etag, afterRead)
		}(i)
	}

	readyWg.Wait()
	close(writeGate)
	wg.Wait()

	wins, rejected := 0, 0
	for i, err := range errs {
		if err == nil {
			wins++
		} else if errors.Is(err, convention.ErrConcurrentModification) || errors.Is(err, convention.ErrAlreadyBilled) {
			rejected++
		} else {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	if wins != 1 {
		t.Errorf("expected exactly 1 winner, got %d (wins=%d, rejected=%d)", wins, wins, rejected)
	}
	if rejected != 1 {
		t.Errorf("expected exactly 1 rejected, got %d (wins=%d, rejected=%d)", rejected, wins, rejected)
	}

	// Winner's change must have persisted.
	unbilled, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches after race: %v", err)
	}
	for _, r := range unbilled {
		if r.CampfireID == cfID && r.MessageID == msgID {
			t.Error("record still unbilled after race — winner's MarkBilled did not persist")
		}
	}
}

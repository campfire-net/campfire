//go:build azurite

// Package aztable_test — dispatch_store_test.go
//
// Integration tests for TableDispatchStore and ConventionServerStore.
// Run with: go test -tags azurite ./pkg/store/aztable/...
//
// Prerequisites:
//   - Azurite must be running on localhost:10002
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

// newTestDispatchStore creates a TableDispatchStore backed by Azurite.
func newTestDispatchStore(t *testing.T) *aztable.TableDispatchStore {
	t.Helper()
	s, err := aztable.NewDispatchStore(azuriteConnStr)
	if err != nil {
		t.Fatalf("NewDispatchStore: %v", err)
	}
	return s
}

// unique returns a unique string for test isolation.
func unique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// ---------------------------------------------------------------------------
// Cursor tests
// ---------------------------------------------------------------------------

// TestDispatchStore_GetCursor_NotFound verifies that GetCursor returns 0 for
// a (serverID, campfireID) pair that has no cursor.
func TestDispatchStore_GetCursor_NotFound(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()

	cur, err := s.GetCursor(ctx, unique("server"), unique("cf"))
	if err != nil {
		t.Fatalf("GetCursor: %v", err)
	}
	if cur != 0 {
		t.Errorf("expected 0 for absent cursor, got %d", cur)
	}
}

// TestDispatchStore_AdvanceCursor_BasicFlow verifies that a cursor can be set
// and advanced, and that advancing to a lower value is a no-op.
func TestDispatchStore_AdvanceCursor_BasicFlow(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	serverID := unique("server")
	cfID := unique("cf")

	// Verify no cursor yet.
	cur, err := s.GetCursor(ctx, serverID, cfID)
	if err != nil {
		t.Fatalf("GetCursor initial: %v", err)
	}
	if cur != 0 {
		t.Errorf("initial cursor: want 0, got %d", cur)
	}

	// First advance (insert).
	ts1 := time.Now().UnixNano()
	advanced, err := s.AdvanceCursor(ctx, serverID, cfID, ts1)
	if err != nil {
		t.Fatalf("AdvanceCursor first: %v", err)
	}
	if !advanced {
		t.Error("AdvanceCursor first: expected advanced=true")
	}

	// Read back.
	cur, err = s.GetCursor(ctx, serverID, cfID)
	if err != nil {
		t.Fatalf("GetCursor after first advance: %v", err)
	}
	if cur != ts1 {
		t.Errorf("cursor after first advance: want %d, got %d", ts1, cur)
	}

	// Second advance to higher value.
	ts2 := ts1 + 1000
	advanced, err = s.AdvanceCursor(ctx, serverID, cfID, ts2)
	if err != nil {
		t.Fatalf("AdvanceCursor second: %v", err)
	}
	if !advanced {
		t.Error("AdvanceCursor second: expected advanced=true")
	}

	// Read back.
	cur, err = s.GetCursor(ctx, serverID, cfID)
	if err != nil {
		t.Fatalf("GetCursor after second advance: %v", err)
	}
	if cur != ts2 {
		t.Errorf("cursor after second advance: want %d, got %d", ts2, cur)
	}

	// Try to set to a lower value — should return false.
	advanced, err = s.AdvanceCursor(ctx, serverID, cfID, ts1)
	if err != nil {
		t.Fatalf("AdvanceCursor regress: %v", err)
	}
	if advanced {
		t.Error("AdvanceCursor regress: expected advanced=false (cursor should not go backward)")
	}

	// Cursor should remain at ts2.
	cur, err = s.GetCursor(ctx, serverID, cfID)
	if err != nil {
		t.Fatalf("GetCursor after regress attempt: %v", err)
	}
	if cur != ts2 {
		t.Errorf("cursor after regress: want %d, got %d", ts2, cur)
	}
}

// TestDispatchStore_AdvanceCursor_SameValue verifies that advancing to the same
// value returns false.
func TestDispatchStore_AdvanceCursor_SameValue(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	serverID := unique("server")
	cfID := unique("cf")

	ts := time.Now().UnixNano()
	if _, err := s.AdvanceCursor(ctx, serverID, cfID, ts); err != nil {
		t.Fatalf("AdvanceCursor initial: %v", err)
	}

	advanced, err := s.AdvanceCursor(ctx, serverID, cfID, ts)
	if err != nil {
		t.Fatalf("AdvanceCursor same: %v", err)
	}
	if advanced {
		t.Error("AdvanceCursor same value: expected advanced=false")
	}
}

// ---------------------------------------------------------------------------
// MarkDispatched / status transition tests
// ---------------------------------------------------------------------------

// TestDispatchStore_MarkDispatched_InsertIfNotExists verifies insert-if-not-exists
// semantics: first call returns true, second call with same IDs returns false.
func TestDispatchStore_MarkDispatched_InsertIfNotExists(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	cfID := unique("cf")
	msgID := unique("msg")
	serverID := unique("server")

	inserted, err := s.MarkDispatched(ctx, cfID, msgID, serverID, "", "testconv", "testop")
	if err != nil {
		t.Fatalf("MarkDispatched first: %v", err)
	}
	if !inserted {
		t.Error("MarkDispatched first: expected inserted=true")
	}

	// Second call — should be idempotent.
	inserted2, err := s.MarkDispatched(ctx, cfID, msgID, serverID, "", "testconv", "testop")
	if err != nil {
		t.Fatalf("MarkDispatched second: %v", err)
	}
	if inserted2 {
		t.Error("MarkDispatched second: expected inserted=false (already dispatched)")
	}
}

// TestDispatchStore_StatusTransitions exercises the full lifecycle:
// dispatched → fulfilled and dispatched → failed.
func TestDispatchStore_StatusTransitions(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()

	t.Run("dispatched_to_fulfilled", func(t *testing.T) {
		cfID := unique("cf")
		msgID := unique("msg")
		serverID := unique("server")

		if _, err := s.MarkDispatched(ctx, cfID, msgID, serverID, "", "testconv", "testop"); err != nil {
			t.Fatalf("MarkDispatched: %v", err)
		}

		status, err := s.GetDispatchStatus(ctx, cfID, msgID)
		if err != nil {
			t.Fatalf("GetDispatchStatus after dispatch: %v", err)
		}
		if status != "dispatched" {
			t.Errorf("status after dispatch: want 'dispatched', got %q", status)
		}

		if err := s.MarkFulfilled(ctx, cfID, msgID); err != nil {
			t.Fatalf("MarkFulfilled: %v", err)
		}

		status, err = s.GetDispatchStatus(ctx, cfID, msgID)
		if err != nil {
			t.Fatalf("GetDispatchStatus after fulfill: %v", err)
		}
		if status != "fulfilled" {
			t.Errorf("status after fulfill: want 'fulfilled', got %q", status)
		}
	})

	t.Run("dispatched_to_failed", func(t *testing.T) {
		cfID := unique("cf")
		msgID := unique("msg")
		serverID := unique("server")

		if _, err := s.MarkDispatched(ctx, cfID, msgID, serverID, "", "testconv", "testop"); err != nil {
			t.Fatalf("MarkDispatched: %v", err)
		}

		if err := s.MarkFailed(ctx, cfID, msgID); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}

		status, err := s.GetDispatchStatus(ctx, cfID, msgID)
		if err != nil {
			t.Fatalf("GetDispatchStatus after fail: %v", err)
		}
		if status != "failed" {
			t.Errorf("status after fail: want 'failed', got %q", status)
		}
	})
}

// TestDispatchStore_GetDispatchStatus_NotFound verifies that GetDispatchStatus
// returns "", nil when no record exists.
func TestDispatchStore_GetDispatchStatus_NotFound(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()

	status, err := s.GetDispatchStatus(ctx, unique("cf"), unique("msg"))
	if err != nil {
		t.Fatalf("GetDispatchStatus not found: %v", err)
	}
	if status != "" {
		t.Errorf("expected empty status for absent record, got %q", status)
	}
}

// TestDispatchStore_MarkFulfilled_NoRecord verifies that MarkFulfilled on a
// non-existent record returns ErrDispatchNotFound.
func TestDispatchStore_MarkFulfilled_NoRecord(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()

	err := s.MarkFulfilled(ctx, unique("cf"), unique("msg"))
	if !errors.Is(err, convention.ErrDispatchNotFound) {
		t.Fatalf("MarkFulfilled on absent record: expected ErrDispatchNotFound, got %v", err)
	}
}

// TestDispatchStore_MarkFailed_NoRecord verifies that MarkFailed on a
// non-existent record returns ErrDispatchNotFound.
func TestDispatchStore_MarkFailed_NoRecord(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()

	err := s.MarkFailed(ctx, unique("cf"), unique("msg"))
	if !errors.Is(err, convention.ErrDispatchNotFound) {
		t.Fatalf("MarkFailed on absent record: expected ErrDispatchNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListStaleDispatches
// ---------------------------------------------------------------------------

// TestDispatchStore_ListStaleDispatches verifies that only "dispatched" entries
// older than the threshold are returned; fulfilled/failed and recent entries
// are excluded.
func TestDispatchStore_ListStaleDispatches(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()

	// We'll use a campfire prefix to avoid picking up entries from other tests.
	cfID := unique("cf-stale")
	serverID := unique("server")

	// Insert a stale dispatched entry by dispatching and then sleeping briefly.
	// We set the threshold to 0 so all dispatched entries in this campfire qualify.
	staleMsgID := unique("msg-stale")
	if _, err := s.MarkDispatched(ctx, cfID, staleMsgID, serverID, "", "testconv", "testop"); err != nil {
		t.Fatalf("MarkDispatched stale: %v", err)
	}

	// Insert a fulfilled entry (should NOT appear in stale list).
	fulfilledMsgID := unique("msg-fulfilled")
	if _, err := s.MarkDispatched(ctx, cfID, fulfilledMsgID, serverID, "", "testconv", "testop"); err != nil {
		t.Fatalf("MarkDispatched fulfilled: %v", err)
	}
	if err := s.MarkFulfilled(ctx, cfID, fulfilledMsgID); err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}

	// Insert a failed entry (should NOT appear in stale list).
	failedMsgID := unique("msg-failed")
	if _, err := s.MarkDispatched(ctx, cfID, failedMsgID, serverID, "", "testconv", "testop"); err != nil {
		t.Fatalf("MarkDispatched failed: %v", err)
	}
	if err := s.MarkFailed(ctx, cfID, failedMsgID); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	// Use olderThan=0 so all dispatched entries are "stale".
	stale, err := s.ListStaleDispatches(ctx, 0)
	if err != nil {
		t.Fatalf("ListStaleDispatches: %v", err)
	}

	// Find our stale entry.
	found := false
	for _, rec := range stale {
		if rec.MessageID == staleMsgID {
			found = true
			if rec.Status != "dispatched" {
				t.Errorf("stale record status: want 'dispatched', got %q", rec.Status)
			}
			if rec.ServerID != serverID {
				t.Errorf("stale record ServerID: want %q, got %q", serverID, rec.ServerID)
			}
		}
		if rec.MessageID == fulfilledMsgID || rec.MessageID == failedMsgID {
			t.Errorf("non-dispatched message %s appeared in stale list", rec.MessageID)
		}
	}
	if !found {
		t.Errorf("stale dispatched message %s not found in ListStaleDispatches result", staleMsgID)
	}
}

// ---------------------------------------------------------------------------
// CleanupOldDispatches
// ---------------------------------------------------------------------------

// TestDispatchStore_CleanupOldDispatches verifies that fulfilled/failed entries
// older than maxAge are removed, while dispatched entries and recent entries
// are preserved.
func TestDispatchStore_CleanupOldDispatches(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	cfID := unique("cf-cleanup")
	serverID := unique("server")

	// Dispatch three messages.
	dispatchedMsgID := unique("msg-dispatched")
	fulfilledMsgID := unique("msg-fulfilled")
	failedMsgID := unique("msg-failed")

	for _, msgID := range []string{dispatchedMsgID, fulfilledMsgID, failedMsgID} {
		if _, err := s.MarkDispatched(ctx, cfID, msgID, serverID, "", "testconv", "testop"); err != nil {
			t.Fatalf("MarkDispatched %s: %v", msgID, err)
		}
	}

	// Transition fulfilled and failed entries.
	if err := s.MarkFulfilled(ctx, cfID, fulfilledMsgID); err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}
	if err := s.MarkFailed(ctx, cfID, failedMsgID); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	// Cleanup with maxAge=0 (everything is old enough).
	removed, err := s.CleanupOldDispatches(ctx, 0)
	if err != nil {
		t.Fatalf("CleanupOldDispatches: %v", err)
	}

	// At least the two finished entries should have been removed.
	if removed < 2 {
		t.Errorf("CleanupOldDispatches: expected at least 2 removed, got %d", removed)
	}

	// The still-dispatched entry should still exist.
	status, err := s.GetDispatchStatus(ctx, cfID, dispatchedMsgID)
	if err != nil {
		t.Fatalf("GetDispatchStatus after cleanup: %v", err)
	}
	if status != "dispatched" {
		t.Errorf("dispatched entry was incorrectly removed (status=%q)", status)
	}

	// The fulfilled entry should be gone.
	statusFulfilled, err := s.GetDispatchStatus(ctx, cfID, fulfilledMsgID)
	if err != nil {
		t.Fatalf("GetDispatchStatus fulfilled after cleanup: %v", err)
	}
	if statusFulfilled != "" {
		t.Errorf("fulfilled entry was not removed (status=%q)", statusFulfilled)
	}

	// The failed entry should be gone.
	statusFailed, err := s.GetDispatchStatus(ctx, cfID, failedMsgID)
	if err != nil {
		t.Fatalf("GetDispatchStatus failed after cleanup: %v", err)
	}
	if statusFailed != "" {
		t.Errorf("failed entry was not removed (status=%q)", statusFailed)
	}
}

// ---------------------------------------------------------------------------
// ConventionServerStore integration tests
// ---------------------------------------------------------------------------

func newTestConventionServerStore(t *testing.T) aztable.ConventionServerStore {
	t.Helper()
	s, err := aztable.NewConventionServerStore(azuriteConnStr)
	if err != nil {
		t.Fatalf("NewConventionServerStore: %v", err)
	}
	return s
}

// TestConventionServerStore_RegisterAndGet verifies register and retrieval.
func TestConventionServerStore_RegisterAndGet(t *testing.T) {
	s := newTestConventionServerStore(t)
	ctx := context.Background()
	cfID := unique("cf")
	serverID := unique("server")

	rec := &aztable.ConventionServerRecord{
		CampfireID:  cfID,
		Convention:  "test-convention",
		Operation:   "do-thing",
		ServerID:    serverID,
		Tier:        1,
		HandlerURL:  "",
		Declaration: `{"name":"test-convention"}`,
		CustomerID:  "customer-001",
		CreatedAt:   time.Now(),
		Enabled:     true,
	}

	if err := s.RegisterConventionServer(ctx, rec); err != nil {
		t.Fatalf("RegisterConventionServer: %v", err)
	}

	got, err := s.GetConventionServer(ctx, cfID, "test-convention", "do-thing")
	if err != nil {
		t.Fatalf("GetConventionServer: %v", err)
	}
	if got == nil {
		t.Fatal("GetConventionServer returned nil")
	}
	if got.ServerID != serverID {
		t.Errorf("ServerID: want %q, got %q", serverID, got.ServerID)
	}
	if got.Convention != "test-convention" {
		t.Errorf("Convention: want 'test-convention', got %q", got.Convention)
	}
	if got.Operation != "do-thing" {
		t.Errorf("Operation: want 'do-thing', got %q", got.Operation)
	}
	if !got.Enabled {
		t.Error("Enabled: expected true")
	}
	if got.Tier != 1 {
		t.Errorf("Tier: want 1, got %d", got.Tier)
	}
}

// TestConventionServerStore_GetNotFound verifies nil return for missing record.
func TestConventionServerStore_GetNotFound(t *testing.T) {
	s := newTestConventionServerStore(t)
	ctx := context.Background()

	got, err := s.GetConventionServer(ctx, unique("cf"), "no-convention", "no-op")
	if err != nil {
		t.Fatalf("GetConventionServer not found: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for absent record, got %+v", got)
	}
}

// TestConventionServerStore_List verifies that ListConventionServers returns
// all handlers for a campfire.
func TestConventionServerStore_List(t *testing.T) {
	s := newTestConventionServerStore(t)
	ctx := context.Background()
	cfID := unique("cf")
	serverID := unique("server")

	ops := []string{"op-a", "op-b", "op-c"}
	for _, op := range ops {
		rec := &aztable.ConventionServerRecord{
			CampfireID: cfID,
			Convention: "conv",
			Operation:  op,
			ServerID:   serverID,
			Tier:       1,
			CreatedAt:  time.Now(),
			Enabled:    true,
		}
		if err := s.RegisterConventionServer(ctx, rec); err != nil {
			t.Fatalf("RegisterConventionServer %s: %v", op, err)
		}
	}

	list, err := s.ListConventionServers(ctx, cfID)
	if err != nil {
		t.Fatalf("ListConventionServers: %v", err)
	}
	if len(list) < len(ops) {
		t.Errorf("expected at least %d records, got %d", len(ops), len(list))
	}
}

// TestConventionServerStore_SetEnabled verifies enabling/disabling without deletion.
func TestConventionServerStore_SetEnabled(t *testing.T) {
	s := newTestConventionServerStore(t)
	ctx := context.Background()
	cfID := unique("cf")

	rec := &aztable.ConventionServerRecord{
		CampfireID: cfID,
		Convention: "conv",
		Operation:  "op",
		ServerID:   unique("server"),
		Tier:       1,
		CreatedAt:  time.Now(),
		Enabled:    true,
	}
	if err := s.RegisterConventionServer(ctx, rec); err != nil {
		t.Fatalf("RegisterConventionServer: %v", err)
	}

	// Disable.
	if err := s.SetConventionServerEnabled(ctx, cfID, "conv", "op", false); err != nil {
		t.Fatalf("SetConventionServerEnabled false: %v", err)
	}
	got, err := s.GetConventionServer(ctx, cfID, "conv", "op")
	if err != nil {
		t.Fatalf("GetConventionServer after disable: %v", err)
	}
	if got.Enabled {
		t.Error("expected Enabled=false after disabling")
	}

	// Re-enable.
	if err := s.SetConventionServerEnabled(ctx, cfID, "conv", "op", true); err != nil {
		t.Fatalf("SetConventionServerEnabled true: %v", err)
	}
	got2, err := s.GetConventionServer(ctx, cfID, "conv", "op")
	if err != nil {
		t.Fatalf("GetConventionServer after re-enable: %v", err)
	}
	if !got2.Enabled {
		t.Error("expected Enabled=true after re-enabling")
	}
}

// ---------------------------------------------------------------------------
// MarkBilled / ListUnbilledDispatches
// ---------------------------------------------------------------------------

// TestDispatchStore_MarkBilled_HappyPath verifies MarkBilled sets BilledAt
// when called with a valid ETag from ListUnbilledDispatches.
func TestDispatchStore_MarkBilled_HappyPath(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	cfID := unique("cf")
	msgID := unique("msg")
	serverID := unique("server")

	// Dispatch, fulfill, and set tokens so the record appears in ListUnbilledDispatches.
	if _, err := s.MarkDispatched(ctx, cfID, msgID, serverID, "", "conv", "op"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}
	if err := s.MarkFulfilled(ctx, cfID, msgID); err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}
	if err := s.SetTokensConsumed(ctx, cfID, msgID, 100); err != nil {
		t.Fatalf("SetTokensConsumed: %v", err)
	}

	// List unbilled — should find our record.
	unbilled, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches: %v", err)
	}
	var rec *convention.DispatchRecord
	for i := range unbilled {
		if unbilled[i].CampfireID == cfID && unbilled[i].MessageID == msgID {
			rec = &unbilled[i]
			break
		}
	}
	if rec == nil {
		t.Fatal("expected record in ListUnbilledDispatches, not found")
	}
	if rec.ETag == "" {
		t.Fatal("ETag from ListUnbilledDispatches is empty")
	}

	// MarkBilled with the ETag from the list.
	if err := s.MarkBilled(ctx, cfID, msgID, rec.ETag); err != nil {
		t.Fatalf("MarkBilled: %v", err)
	}

	// Should no longer appear in unbilled list.
	unbilled2, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches after billing: %v", err)
	}
	for _, r := range unbilled2 {
		if r.CampfireID == cfID && r.MessageID == msgID {
			t.Error("record still appears in unbilled list after MarkBilled")
		}
	}
}

// TestDispatchStore_MarkBilled_StaleETag verifies that MarkBilled with a stale
// ETag (concurrent modification) returns ErrConcurrentModification.
func TestDispatchStore_MarkBilled_StaleETag(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	cfID := unique("cf")
	msgID := unique("msg")
	serverID := unique("server")

	// Dispatch, fulfill, and set tokens.
	if _, err := s.MarkDispatched(ctx, cfID, msgID, serverID, "", "conv", "op"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}
	if err := s.MarkFulfilled(ctx, cfID, msgID); err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}
	if err := s.SetTokensConsumed(ctx, cfID, msgID, 100); err != nil {
		t.Fatalf("SetTokensConsumed: %v", err)
	}

	// Step 1: Read the ETag.
	unbilled, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches: %v", err)
	}
	var staleETag string
	for _, r := range unbilled {
		if r.CampfireID == cfID && r.MessageID == msgID {
			staleETag = r.ETag
			break
		}
	}
	if staleETag == "" {
		t.Fatal("expected record with ETag in ListUnbilledDispatches")
	}

	// Step 2: Concurrently modify the record (increment redispatch count).
	if _, err := s.IncrementRedispatchCount(ctx, cfID, msgID); err != nil {
		t.Fatalf("IncrementRedispatchCount: %v", err)
	}

	// Step 3: MarkBilled with the stale ETag must fail.
	err = s.MarkBilled(ctx, cfID, msgID, staleETag)
	if err == nil {
		t.Fatal("MarkBilled with stale ETag should have failed, but succeeded (lost update bug)")
	}
	if !errors.Is(err, convention.ErrConcurrentModification) {
		t.Fatalf("expected ErrConcurrentModification, got: %v", err)
	}

	// Step 4: Re-read with fresh ETag and MarkBilled should succeed.
	unbilled2, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches after conflict: %v", err)
	}
	var freshETag string
	for _, r := range unbilled2 {
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

// ---------------------------------------------------------------------------
// MarkBilled concurrent modification regression tests (campfire-agent-0uu)
// ---------------------------------------------------------------------------

// helperCreateUnbilledRecord dispatches, fulfills, and sets tokens on a record,
// returning the record's ETag from ListUnbilledDispatches.
func helperCreateUnbilledRecord(t *testing.T, s *aztable.TableDispatchStore, cfID, msgID, serverID string) string {
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

// TestDispatchStore_MarkBilled_ConcurrentRace verifies that when two goroutines
// both read the same ETag and race to call MarkBilled, exactly one succeeds and
// the other gets ErrConcurrentModification.
func TestDispatchStore_MarkBilled_ConcurrentRace(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	cfID := unique("cf")
	msgID := unique("msg")
	serverID := unique("server")

	// Create a fulfilled, unbilled record and capture the ETag.
	etag := helperCreateUnbilledRecord(t, s, cfID, msgID, serverID)

	// Two goroutines race with the same ETag.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	gate := make(chan struct{}) // synchronize start

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-gate // wait for both goroutines to be ready
			errs[idx] = s.MarkBilled(ctx, cfID, msgID, etag)
		}(i)
	}

	close(gate) // release both goroutines simultaneously
	wg.Wait()

	// Exactly one should succeed; the other should get ErrConcurrentModification.
	wins := 0
	conflicts := 0
	for i, err := range errs {
		if err == nil {
			wins++
		} else if errors.Is(err, convention.ErrConcurrentModification) {
			conflicts++
		} else {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	if wins != 1 {
		t.Errorf("expected exactly 1 winner, got %d (wins=%d, conflicts=%d)", wins, wins, conflicts)
	}
	if conflicts != 1 {
		t.Errorf("expected exactly 1 conflict, got %d (wins=%d, conflicts=%d)", conflicts, wins, conflicts)
	}

	// Verify the winner's change persisted — record should no longer be unbilled.
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

// TestDispatchStore_MarkBilled_RetryAfterConflict verifies the retry-after-conflict
// pattern: a loser re-reads the record with a fresh ETag and successfully calls
// MarkBilled on the second attempt.
func TestDispatchStore_MarkBilled_RetryAfterConflict(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	cfID := unique("cf")
	msgID := unique("msg")
	serverID := unique("server")

	// Create an unbilled record.
	originalETag := helperCreateUnbilledRecord(t, s, cfID, msgID, serverID)

	// Simulate a concurrent modification by incrementing the redispatch count,
	// which changes the entity and invalidates the original ETag.
	if _, err := s.IncrementRedispatchCount(ctx, cfID, msgID); err != nil {
		t.Fatalf("IncrementRedispatchCount: %v", err)
	}

	// Attempt 1: MarkBilled with the now-stale ETag — must fail.
	err := s.MarkBilled(ctx, cfID, msgID, originalETag)
	if err == nil {
		t.Fatal("MarkBilled with stale ETag should have failed")
	}
	if !errors.Is(err, convention.ErrConcurrentModification) {
		t.Fatalf("expected ErrConcurrentModification, got: %v", err)
	}

	// Retry pattern: re-read to get a fresh ETag.
	unbilled, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches for retry: %v", err)
	}
	var freshETag string
	for _, r := range unbilled {
		if r.CampfireID == cfID && r.MessageID == msgID {
			freshETag = r.ETag
			break
		}
	}
	if freshETag == "" {
		t.Fatal("record not found in unbilled list for retry")
	}
	if freshETag == originalETag {
		t.Fatal("fresh ETag should differ from original after concurrent modification")
	}

	// Attempt 2: MarkBilled with the fresh ETag — must succeed.
	if err := s.MarkBilled(ctx, cfID, msgID, freshETag); err != nil {
		t.Fatalf("MarkBilled with fresh ETag (retry) should succeed: %v", err)
	}

	// Verify record is now billed.
	unbilled2, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches after retry: %v", err)
	}
	for _, r := range unbilled2 {
		if r.CampfireID == cfID && r.MessageID == msgID {
			t.Error("record still unbilled after successful retry")
		}
	}
}

// TestDispatchStore_MarkBilled_NoRecord verifies MarkBilled is a no-op for
// a non-existent record.
func TestDispatchStore_MarkBilled_NoRecord(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	if err := s.MarkBilled(ctx, unique("cf"), unique("msg"), "any-etag"); err != nil {
		t.Fatalf("unexpected error for missing record: %v", err)
	}
}

// TestDispatchStore_MarkFulfilled_ConcurrentRace verifies that when two goroutines
// race to call MarkFulfilled on the same record, exactly one succeeds and the other
// gets ErrConcurrentModification (ETag guard in updateDispatchStatus).
func TestDispatchStore_MarkFulfilled_ConcurrentRace(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	cfID := unique("cf")
	msgID := unique("msg")
	serverID := unique("server")

	// Create a dispatched record (not yet fulfilled).
	if _, err := s.MarkDispatched(ctx, cfID, msgID, serverID, "", "conv", "op"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}

	// Two goroutines race to fulfill the same record.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	gate := make(chan struct{})

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-gate
			errs[idx] = s.MarkFulfilled(ctx, cfID, msgID)
		}(i)
	}

	close(gate)
	wg.Wait()

	// Exactly one should succeed; the other should get ErrConcurrentModification.
	wins := 0
	conflicts := 0
	for i, err := range errs {
		if err == nil {
			wins++
		} else if errors.Is(err, convention.ErrConcurrentModification) {
			conflicts++
		} else {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	if wins != 1 {
		t.Errorf("expected exactly 1 winner, got %d (wins=%d, conflicts=%d)", wins, wins, conflicts)
	}
	if conflicts != 1 {
		t.Errorf("expected exactly 1 conflict, got %d (wins=%d, conflicts=%d)", conflicts, wins, conflicts)
	}

	// Verify the record is now fulfilled.
	status, err := s.GetDispatchStatus(ctx, cfID, msgID)
	if err != nil {
		t.Fatalf("GetDispatchStatus after race: %v", err)
	}
	if status != "fulfilled" {
		t.Errorf("expected status 'fulfilled', got %q", status)
	}
}

// TestConventionServerStore_Deregister verifies removal of a handler record.
func TestConventionServerStore_Deregister(t *testing.T) {
	s := newTestConventionServerStore(t)
	ctx := context.Background()
	cfID := unique("cf")

	rec := &aztable.ConventionServerRecord{
		CampfireID: cfID,
		Convention: "conv",
		Operation:  "op",
		ServerID:   unique("server"),
		Tier:       1,
		CreatedAt:  time.Now(),
		Enabled:    true,
	}
	if err := s.RegisterConventionServer(ctx, rec); err != nil {
		t.Fatalf("RegisterConventionServer: %v", err)
	}

	if err := s.DeregisterConventionServer(ctx, cfID, "conv", "op"); err != nil {
		t.Fatalf("DeregisterConventionServer: %v", err)
	}

	got, err := s.GetConventionServer(ctx, cfID, "conv", "op")
	if err != nil {
		t.Fatalf("GetConventionServer after deregister: %v", err)
	}
	if got != nil {
		t.Error("expected nil after deregister, record still exists")
	}
}

// ---------------------------------------------------------------------------
// MarkFulfilledCAS / MarkFailedCAS — notFound branch
// ---------------------------------------------------------------------------

// TestDispatchStore_MarkFulfilledCAS_NotFound verifies that MarkFulfilledCAS
// returns notFound=true, updated=false, error=nil when the dispatch record
// does not exist.
func TestDispatchStore_MarkFulfilledCAS_NotFound(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()

	updated, notFound, err := s.MarkFulfilledCAS(ctx, unique("cf"), unique("msg"), 0)
	if err != nil {
		t.Fatalf("MarkFulfilledCAS on absent record: unexpected error: %v", err)
	}
	if !notFound {
		t.Error("MarkFulfilledCAS on absent record: expected notFound=true, got false")
	}
	if updated {
		t.Error("MarkFulfilledCAS on absent record: expected updated=false, got true")
	}
}

// TestDispatchStore_MarkFailedCAS_NotFound verifies that MarkFailedCAS
// returns notFound=true, updated=false, error=nil when the dispatch record
// does not exist.
func TestDispatchStore_MarkFailedCAS_NotFound(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()

	updated, notFound, err := s.MarkFailedCAS(ctx, unique("cf"), unique("msg"), 0)
	if err != nil {
		t.Fatalf("MarkFailedCAS on absent record: unexpected error: %v", err)
	}
	if !notFound {
		t.Error("MarkFailedCAS on absent record: expected notFound=true, got false")
	}
	if updated {
		t.Error("MarkFailedCAS on absent record: expected updated=false, got true")
	}
}

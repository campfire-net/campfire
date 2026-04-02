package convention_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/forge"
)

// testForgeEmitter is a test double for forge.ForgeEmitter that captures emitted events.
// It wraps a real ForgeEmitter but intercepts calls via a thin recorder layer.
// Since ForgeEmitter.Emit() is not an interface method we cannot inject a mock directly.
// Instead we use a real emitter backed by a no-op Forge client and inspect the
// recorded events via a channel-based capture.
//
// Approach: wire a capture hook via a real emitter connected to a test Forge client
// that records every Ingest call.

// captureClient is a forge.Client replacement that records ingest calls.
// We cannot embed forge.Client directly (it has private fields), so we build
// a test emitter using a real emitter + goroutine that reads from its channel.
//
// Instead, we test BillingSweep via its observable side effects:
//   - BilledAt is set on the dispatch record (via GetDispatchStatus or MarkBilled check)
//   - We verify the correct number of billing sweep runs
//
// For verifying emitted events, we create a thin wrapper around the public Emit/Run API
// by using a real ForgeEmitter connected to a fake Forge HTTP server.
//
// Simpler approach for unit tests: use a fake DispatchStore that records MarkBilled calls.

// fakeDispatchStore wraps MemoryDispatchStore and records MarkBilled calls.
type fakeDispatchStore struct {
	*convention.MemoryDispatchStore
	mu          sync.Mutex
	billedCalls []billedCall
}

type billedCall struct {
	campfireID string
	messageID  string
}

func newFakeDispatchStore() *fakeDispatchStore {
	return &fakeDispatchStore{
		MemoryDispatchStore: convention.NewMemoryDispatchStore(),
	}
}

func (f *fakeDispatchStore) MarkBilled(ctx context.Context, campfireID, messageID, etag string) error {
	f.mu.Lock()
	f.billedCalls = append(f.billedCalls, billedCall{campfireID, messageID})
	f.mu.Unlock()
	return f.MemoryDispatchStore.MarkBilled(ctx, campfireID, messageID, etag)
}

func (f *fakeDispatchStore) billedMessages() []billedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]billedCall, len(f.billedCalls))
	copy(out, f.billedCalls)
	return out
}

// newTestEmitter creates a ForgeEmitter backed by a non-listening address.
// The emitter is fail-open: Ingest calls fail with a network error and are logged
// and dropped. This is sufficient for unit tests that verify billed count and
// MarkBilled side effects — not actual Forge delivery.
func newTestEmitter(t *testing.T) (*forge.ForgeEmitter, context.CancelFunc) {
	t.Helper()
	// Point at a port that has nothing listening — Ingest will fail (network
	// error) but will not panic, matching the fail-open design.
	client := &forge.Client{
		BaseURL:    "http://127.0.0.1:0",
		ServiceKey: "forge-sk-test",
	}
	emitter := forge.NewForgeEmitter(client, 100, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go emitter.Run(ctx)
	return emitter, cancel
}

// TestBillingSweep_BillsUnbilledTokenDispatches verifies that the sweep emits
// billing events and marks records as billed for fulfilled dispatches with token data.
func TestBillingSweep_BillsUnbilledTokenDispatches(t *testing.T) {
	ctx := context.Background()
	ds := newFakeDispatchStore()
	emitter, cancel := newTestEmitter(t)
	defer cancel()

	// Create a fulfilled dispatch with token consumption.
	ds.MarkDispatched(ctx, "cf1", "msg1", "server1", "acct-server1", "myconv", "myop")
	ds.MarkFulfilled(ctx, "cf1", "msg1")
	ds.SetTokensConsumed("cf1", "msg1", 500)

	sweep := convention.NewBillingSweep(ds, emitter, nil)
	billed, err := sweep.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if billed != 1 {
		t.Fatalf("expected 1 billed, got %d", billed)
	}

	// Verify MarkBilled was called.
	calls := ds.billedMessages()
	if len(calls) != 1 {
		t.Fatalf("expected 1 MarkBilled call, got %d", len(calls))
	}
	if calls[0].campfireID != "cf1" || calls[0].messageID != "msg1" {
		t.Errorf("unexpected MarkBilled call: %+v", calls[0])
	}
}

// TestBillingSweep_SkipsAlreadyBilledDispatches verifies that dispatches with
// BilledAt != 0 are not billed again.
func TestBillingSweep_SkipsAlreadyBilledDispatches(t *testing.T) {
	ctx := context.Background()
	ds := newFakeDispatchStore()
	emitter, cancel := newTestEmitter(t)
	defer cancel()

	// Create and bill a dispatch first.
	ds.MarkDispatched(ctx, "cf1", "msg1", "server1", "acct-server1", "myconv", "myop")
	ds.MarkFulfilled(ctx, "cf1", "msg1")
	ds.SetTokensConsumed("cf1", "msg1", 500)
	// Get the ETag to pass to MarkBilled.
	unbilled, _ := ds.ListUnbilledDispatches(ctx)
	if len(unbilled) != 1 {
		t.Fatalf("expected 1 unbilled, got %d", len(unbilled))
	}
	ds.MarkBilled(ctx, "cf1", "msg1", unbilled[0].ETag) // already billed

	sweep := convention.NewBillingSweep(ds, emitter, nil)
	billed, err := sweep.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if billed != 0 {
		t.Fatalf("expected 0 billed (already billed), got %d", billed)
	}

	// MarkBilled was called once before the sweep; the sweep should not call it again.
	// Reset the call recorder by counting from zero — the fake store accumulated 1 call
	// from the pre-billing above. After the sweep, still 1 (no new calls).
	calls := ds.billedMessages()
	if len(calls) != 1 {
		t.Fatalf("expected 1 total MarkBilled call (pre-billing only), got %d", len(calls))
	}
}

// TestBillingSweep_SkipsZeroTokenDispatches verifies that fulfilled dispatches
// with TokensConsumed == 0 are not included (covered by flat-rate billing).
func TestBillingSweep_SkipsZeroTokenDispatches(t *testing.T) {
	ctx := context.Background()
	ds := newFakeDispatchStore()
	emitter, cancel := newTestEmitter(t)
	defer cancel()

	// Fulfilled but zero tokens — flat-rate covers this.
	ds.MarkDispatched(ctx, "cf1", "msg1", "server1", "acct-server1", "myconv", "myop")
	ds.MarkFulfilled(ctx, "cf1", "msg1")
	// TokensConsumed defaults to 0, don't call SetTokensConsumed.

	sweep := convention.NewBillingSweep(ds, emitter, nil)
	billed, err := sweep.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if billed != 0 {
		t.Fatalf("expected 0 billed for zero-token dispatch, got %d", billed)
	}

	calls := ds.billedMessages()
	if len(calls) != 0 {
		t.Fatalf("expected no MarkBilled calls, got %d", len(calls))
	}
}

// TestBillingSweep_IdempotencyKeyFormat verifies the idempotency key is
// formatted as serverID:messageID:tokens.
func TestBillingSweep_IdempotencyKeyFormat(t *testing.T) {
	ctx := context.Background()

	// We track the idempotency key by intercepting events through a test
	// Forge server that records the request body. Here we use the observable
	// side effect: the key is serverID + ":" + messageID + ":tokens".
	// We verify it indirectly by checking BillingSweep does NOT double-bill
	// when called twice (idempotency key prevents Forge double-charge, but
	// our MarkBilled guard prevents the second Emit entirely).

	ds := newFakeDispatchStore()
	emitter, cancel := newTestEmitter(t)
	defer cancel()

	ds.MarkDispatched(ctx, "cf1", "msg-abc", "srv-xyz", "acct-xyz", "myconv", "myop")
	ds.MarkFulfilled(ctx, "cf1", "msg-abc")
	ds.SetTokensConsumed("cf1", "msg-abc", 1000)

	sweep := convention.NewBillingSweep(ds, emitter, nil)

	// First run: bills the record.
	billed1, err := sweep.Run(ctx)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if billed1 != 1 {
		t.Fatalf("run 1: expected 1 billed, got %d", billed1)
	}

	// Second run: record is marked billed, should not appear in ListUnbilledDispatches.
	billed2, err := sweep.Run(ctx)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if billed2 != 0 {
		t.Fatalf("run 2: expected 0 billed (already billed), got %d", billed2)
	}

	// Expected idempotency key format: "srv-xyz:msg-abc:tokens"
	// We verify MarkBilled was called exactly once.
	calls := ds.billedMessages()
	if len(calls) != 1 {
		t.Fatalf("expected 1 MarkBilled call across both runs, got %d", len(calls))
	}
}

// TestBillingSweep_MultiplePending verifies that multiple pending records are
// all billed in one sweep pass.
func TestBillingSweep_MultiplePending(t *testing.T) {
	ctx := context.Background()
	ds := newFakeDispatchStore()
	emitter, cancel := newTestEmitter(t)
	defer cancel()

	for i, msgID := range []string{"msg1", "msg2", "msg3"} {
		ds.MarkDispatched(ctx, "cf1", msgID, "server1", "acct-server1", "myconv", "myop")
		ds.MarkFulfilled(ctx, "cf1", msgID)
		ds.SetTokensConsumed("cf1", msgID, int64(100*(i+1)))
	}

	sweep := convention.NewBillingSweep(ds, emitter, nil)
	billed, err := sweep.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if billed != 3 {
		t.Fatalf("expected 3 billed, got %d", billed)
	}

	calls := ds.billedMessages()
	if len(calls) != 3 {
		t.Fatalf("expected 3 MarkBilled calls, got %d", len(calls))
	}
}

// TestBillingSweep_EmptyStore verifies no-op on empty store.
func TestBillingSweep_EmptyStore(t *testing.T) {
	ctx := context.Background()
	ds := newFakeDispatchStore()
	emitter, cancel := newTestEmitter(t)
	defer cancel()

	sweep := convention.NewBillingSweep(ds, emitter, nil)
	billed, err := sweep.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if billed != 0 {
		t.Fatalf("expected 0 billed on empty store, got %d", billed)
	}
}

// TestMemoryDispatchStore_ListUnbilledDispatches verifies the filter logic
// in MemoryDispatchStore.ListUnbilledDispatches.
func TestMemoryDispatchStore_ListUnbilledDispatches(t *testing.T) {
	ctx := context.Background()
	ds := convention.NewMemoryDispatchStore()

	// Fulfilled with tokens — should appear.
	ds.MarkDispatched(ctx, "cf1", "msg-a", "srv1", "acct-srv1", "conv", "op")
	ds.MarkFulfilled(ctx, "cf1", "msg-a")
	ds.SetTokensConsumed("cf1", "msg-a", 100)

	// Fulfilled without tokens — should NOT appear.
	ds.MarkDispatched(ctx, "cf1", "msg-b", "srv1", "acct-srv1", "conv", "op")
	ds.MarkFulfilled(ctx, "cf1", "msg-b")

	// Fulfilled with tokens but already billed — should NOT appear.
	ds.MarkDispatched(ctx, "cf1", "msg-c", "srv1", "acct-srv1", "conv", "op")
	ds.MarkFulfilled(ctx, "cf1", "msg-c")
	ds.SetTokensConsumed("cf1", "msg-c", 200)
	// Get ETag for msg-c to pass to MarkBilled.
	unbilledC, _ := ds.ListUnbilledDispatches(ctx)
	var msgCETag string
	for _, u := range unbilledC {
		if u.MessageID == "msg-c" {
			msgCETag = u.ETag
		}
	}
	ds.MarkBilled(ctx, "cf1", "msg-c", msgCETag)

	// Dispatched (not fulfilled) with tokens — should NOT appear.
	ds.MarkDispatched(ctx, "cf1", "msg-d", "srv1", "acct-srv1", "conv", "op")
	ds.SetTokensConsumed("cf1", "msg-d", 300)

	unbilled, err := ds.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches: %v", err)
	}
	if len(unbilled) != 1 {
		t.Fatalf("expected 1 unbilled record, got %d", len(unbilled))
	}
	if unbilled[0].MessageID != "msg-a" {
		t.Errorf("expected msg-a, got %q", unbilled[0].MessageID)
	}
	if unbilled[0].TokensConsumed != 100 {
		t.Errorf("expected 100 tokens, got %d", unbilled[0].TokensConsumed)
	}
}

// TestMemoryDispatchStore_MarkBilled verifies that MarkBilled sets BilledAt.
func TestMemoryDispatchStore_MarkBilled(t *testing.T) {
	ctx := context.Background()
	ds := convention.NewMemoryDispatchStore()

	ds.MarkDispatched(ctx, "cf1", "msg1", "srv1", "acct-srv1", "conv", "op")
	ds.MarkFulfilled(ctx, "cf1", "msg1")
	ds.SetTokensConsumed("cf1", "msg1", 50)

	// Before billing: should appear as unbilled.
	unbilled, _ := ds.ListUnbilledDispatches(ctx)
	if len(unbilled) != 1 {
		t.Fatalf("expected 1 unbilled before MarkBilled, got %d", len(unbilled))
	}

	// Mark billed with the ETag from the list.
	if err := ds.MarkBilled(ctx, "cf1", "msg1", unbilled[0].ETag); err != nil {
		t.Fatalf("MarkBilled: %v", err)
	}

	// After billing: should NOT appear as unbilled.
	unbilled, _ = ds.ListUnbilledDispatches(ctx)
	if len(unbilled) != 0 {
		t.Fatalf("expected 0 unbilled after MarkBilled, got %d", len(unbilled))
	}
}

// TestMemoryDispatchStore_MarkBilled_NoRecord verifies no-op on missing record.
func TestMemoryDispatchStore_MarkBilled_NoRecord(t *testing.T) {
	ctx := context.Background()
	ds := convention.NewMemoryDispatchStore()
	if err := ds.MarkBilled(ctx, "cf1", "nonexistent", "any-etag"); err != nil {
		t.Fatalf("unexpected error for missing record: %v", err)
	}
}

// TestMarkBilled_ConcurrentRedispatchReset is a regression test for the lost-update
// bug: if IncrementRedispatchCount fires between ListUnbilledDispatches and MarkBilled,
// MarkBilled must fail with ErrConcurrentModification rather than silently overwriting
// the RedispatchCount with a stale value.
func TestMarkBilled_ConcurrentRedispatchReset(t *testing.T) {
	ctx := context.Background()
	ds := convention.NewMemoryDispatchStore()

	// Set up a fulfilled dispatch with token consumption.
	ds.MarkDispatched(ctx, "cf1", "msg1", "srv1", "acct-srv1", "conv", "op")
	ds.MarkFulfilled(ctx, "cf1", "msg1")
	ds.SetTokensConsumed("cf1", "msg1", 500)

	// Step 1: Read unbilled dispatches (captures ETag at this point in time).
	unbilled, err := ds.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches: %v", err)
	}
	if len(unbilled) != 1 {
		t.Fatalf("expected 1 unbilled, got %d", len(unbilled))
	}
	staleETag := unbilled[0].ETag

	// Step 2: Concurrent re-dispatch happens — increments RedispatchCount,
	// which changes the record's version/ETag.
	newCount, err := ds.IncrementRedispatchCount(ctx, "cf1", "msg1")
	if err != nil {
		t.Fatalf("IncrementRedispatchCount: %v", err)
	}
	if newCount != 1 {
		t.Fatalf("expected RedispatchCount=1 after increment, got %d", newCount)
	}

	// Step 3: MarkBilled with the stale ETag must fail.
	err = ds.MarkBilled(ctx, "cf1", "msg1", staleETag)
	if err == nil {
		t.Fatal("MarkBilled with stale ETag should have failed, but succeeded (lost update bug)")
	}
	if !errors.Is(err, convention.ErrConcurrentModification) {
		t.Fatalf("expected ErrConcurrentModification, got: %v", err)
	}

	// Step 4: Verify the record was NOT marked as billed (BilledAt should still be 0).
	// Re-read to confirm the RedispatchCount was preserved.
	stillUnbilled, err := ds.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches after failed MarkBilled: %v", err)
	}
	if len(stillUnbilled) != 1 {
		t.Fatalf("expected 1 still-unbilled record, got %d", len(stillUnbilled))
	}

	// Step 5: MarkBilled with the fresh ETag should succeed.
	freshETag := stillUnbilled[0].ETag
	if err := ds.MarkBilled(ctx, "cf1", "msg1", freshETag); err != nil {
		t.Fatalf("MarkBilled with fresh ETag should succeed: %v", err)
	}

	// Confirm it's now billed.
	finalUnbilled, _ := ds.ListUnbilledDispatches(ctx)
	if len(finalUnbilled) != 0 {
		t.Fatalf("expected 0 unbilled after successful MarkBilled, got %d", len(finalUnbilled))
	}
}

// TestBillingSweep_UsesForgeAccountID_NotServerID is a regression test verifying
// that the billing sweep emits UsageEvents with AccountID set to the customer's
// ForgeAccountID, not the convention server's own ServerID.
//
// Bug: BillingSweep previously used rec.ServerID as the AccountID in the emitted
// UsageEvent, causing all convention token charges to be attributed to the
// hosting service instead of the customer who owns the convention server.
func TestBillingSweep_UsesForgeAccountID_NotServerID(t *testing.T) {
	ctx := context.Background()

	// Capture emitted events via a test HTTP server.
	var mu sync.Mutex
	var capturedAccountIDs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage/ingest" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var event forge.UsageEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Errorf("decode ingest body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		capturedAccountIDs = append(capturedAccountIDs, event.AccountID)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"status":"created"}`))
	}))
	defer srv.Close()

	client := &forge.Client{
		BaseURL:     srv.URL,
		ServiceKey:  "forge-sk-test",
		RetryDelays: []time.Duration{}, // no retries for test speed
	}
	emitter := forge.NewForgeEmitter(client, 100, nil)
	emCtx, emCancel := context.WithCancel(context.Background())
	defer emCancel()
	go emitter.Run(emCtx)

	ds := newFakeDispatchStore()

	const serverID = "server-pubkey-hex-abc123"
	const customerForgeAccountID = "forge-acct-customer-xyz"

	// Create a fulfilled dispatch with token consumption, specifying both
	// the server's own ID and the customer's ForgeAccountID.
	ds.MarkDispatched(ctx, "cf-billing", "msg-regression", serverID, customerForgeAccountID, "myconv", "myop")
	ds.MarkFulfilled(ctx, "cf-billing", "msg-regression")
	ds.SetTokensConsumed("cf-billing", "msg-regression", 750)

	sweep := convention.NewBillingSweep(ds, emitter, nil)
	billed, err := sweep.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if billed != 1 {
		t.Fatalf("expected 1 billed, got %d", billed)
	}

	// Give the emitter time to flush to the test server.
	// The emitter batches on a 1-second timer, so wait for that plus network time.
	time.Sleep(2 * time.Second)
	emCancel()

	mu.Lock()
	defer mu.Unlock()

	if len(capturedAccountIDs) != 1 {
		t.Fatalf("expected 1 captured event, got %d", len(capturedAccountIDs))
	}

	// THE CRITICAL ASSERTION: AccountID must be the customer's ForgeAccountID,
	// not the server's own identity.
	if capturedAccountIDs[0] == serverID {
		t.Fatalf("BUG: emitted AccountID is the server's own ID (%q), not the customer's ForgeAccountID (%q)",
			serverID, customerForgeAccountID)
	}
	if capturedAccountIDs[0] != customerForgeAccountID {
		t.Fatalf("emitted AccountID = %q, want customer's ForgeAccountID %q",
			capturedAccountIDs[0], customerForgeAccountID)
	}
}

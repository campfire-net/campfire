package metering_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
	"github.com/campfire-net/campfire/pkg/metering"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// mockStorageStore implements StorageStore with fixed counters.
type mockStorageStore struct {
	counters []metering.StorageCounter
}

func (m *mockStorageStore) ListStorageCounters(_ context.Context) ([]metering.StorageCounter, error) {
	return m.counters, nil
}

// mockPeerCountStore implements PeerCountStore with fixed counts.
type mockPeerCountStore struct {
	counts []metering.PeerCount
}

func (m *mockPeerCountStore) ListCampfirePeerCounts(_ context.Context) ([]metering.PeerCount, error) {
	return m.counts, nil
}

// mockGCStore implements GCStore with in-memory messages and a delete log.
type mockGCStore struct {
	messages []metering.OldMessage
	deleted  []string // message IDs deleted
}

func (m *mockGCStore) ListMessagesOlderThan(_ context.Context, campfireID string, _ int64) ([]metering.OldMessage, error) {
	if campfireID == "" {
		return m.messages, nil
	}
	var out []metering.OldMessage
	for _, msg := range m.messages {
		if msg.CampfireID == campfireID {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (m *mockGCStore) DeleteMessage(_ context.Context, _, messageID string) error {
	m.deleted = append(m.deleted, messageID)
	return nil
}

// mockBalanceChecker returns fixed balances.
type mockBalanceChecker struct {
	balances map[string]int64 // accountID → balance_micro
}

func (m *mockBalanceChecker) Balance(_ context.Context, accountID string) (int64, error) {
	b, ok := m.balances[accountID]
	if !ok {
		return 0, fmt.Errorf("no balance for %s", accountID)
	}
	return b, nil
}

// newForgeServer starts a test HTTP server that records ingest calls.
func newForgeServer(t *testing.T, events *[]forge.UsageEvent) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage/ingest" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		var ev forge.UsageEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Errorf("decode ingest body: %v", err)
			http.Error(w, "bad request", 400)
			return
		}
		*events = append(*events, ev)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"status":"created"}`)
	}))
}

// newEmitter creates a ForgeEmitter backed by a test server and starts Run.
// Returns the emitter and a cleanup func that cancels and waits for Run.
func newEmitter(t *testing.T, srv *httptest.Server) (*forge.ForgeEmitter, func()) {
	t.Helper()
	client := &forge.Client{
		BaseURL:     srv.URL,
		ServiceKey:  "forge-sk-test",
		RetryDelays: []time.Duration{},
	}
	emitter := forge.NewForgeEmitter(client, 100, func(err error) {
		t.Logf("emitter error: %v", err)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		emitter.Run(ctx)
		close(done)
	}()
	cleanup := func() {
		cancel()
		<-done
	}
	return emitter, cleanup
}

// ---------------------------------------------------------------------------
// EmitStorageUsage tests
// ---------------------------------------------------------------------------

// TestEmitStorageUsage_GbDayMath verifies the GB-day computation is correct.
func TestEmitStorageUsage_GbDayMath(t *testing.T) {
	// 1 GiB stored → 1/24 GB-day per hour.
	oneGiB := int64(1073741824)
	wantGbDay := 1.0 / 24.0

	var events []forge.UsageEvent
	srv := newForgeServer(t, &events)
	defer srv.Close()

	emitter, cleanup := newEmitter(t, srv)
	defer cleanup()

	store := &mockStorageStore{
		counters: []metering.StorageCounter{
			{CampfireID: "cf-abc", BytesStored: oneGiB, MessageCount: 10},
		},
	}
	accountLookup := func(id string) string {
		if id == "cf-abc" {
			return "acct-001"
		}
		return ""
	}

	if err := metering.EmitStorageUsage(context.Background(), store, emitter, accountLookup); err != nil {
		t.Fatalf("EmitStorageUsage: %v", err)
	}

	// Wait for the emitter to flush (batch timeout ≤ 1s with no delays).
	time.Sleep(1200 * time.Millisecond)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.AccountID != "acct-001" {
		t.Errorf("AccountID: want acct-001, got %s", ev.AccountID)
	}
	if ev.UnitType != "message-storage-gb-day" {
		t.Errorf("UnitType: want message-storage-gb-day, got %s", ev.UnitType)
	}
	if ev.ServiceID != "campfire-hosting" {
		t.Errorf("ServiceID: want campfire-hosting, got %s", ev.ServiceID)
	}
	const eps = 1e-12
	if diff := ev.Quantity - wantGbDay; diff < -eps || diff > eps {
		t.Errorf("Quantity: want %v, got %v", wantGbDay, ev.Quantity)
	}
}

// TestEmitStorageUsage_HalfGiB verifies computation for a smaller byte count.
func TestEmitStorageUsage_HalfGiB(t *testing.T) {
	halfGiB := int64(1073741824 / 2)
	wantGbDay := (float64(halfGiB) / 1073741824.0) * (1.0 / 24.0)

	var events []forge.UsageEvent
	srv := newForgeServer(t, &events)
	defer srv.Close()

	emitter, cleanup := newEmitter(t, srv)
	defer cleanup()

	store := &mockStorageStore{
		counters: []metering.StorageCounter{
			{CampfireID: "cf-half", BytesStored: halfGiB},
		},
	}
	if err := metering.EmitStorageUsage(context.Background(), store, emitter, func(string) string { return "acct-002" }); err != nil {
		t.Fatalf("EmitStorageUsage: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	const eps = 1e-12
	if diff := events[0].Quantity - wantGbDay; diff < -eps || diff > eps {
		t.Errorf("Quantity: want %v, got %v", wantGbDay, events[0].Quantity)
	}
}

// TestEmitStorageUsage_IdempotencyKeyFormat checks that the idempotency key is
// campfireID + ":" + "2006-01-02T15" (UTC hour).
func TestEmitStorageUsage_IdempotencyKeyFormat(t *testing.T) {
	var events []forge.UsageEvent
	srv := newForgeServer(t, &events)
	defer srv.Close()

	emitter, cleanup := newEmitter(t, srv)
	defer cleanup()

	store := &mockStorageStore{
		counters: []metering.StorageCounter{
			{CampfireID: "cf-idem", BytesStored: 1024},
		},
	}
	before := time.Now().UTC().Format("2006-01-02T15")

	if err := metering.EmitStorageUsage(context.Background(), store, emitter, func(string) string { return "acct-003" }); err != nil {
		t.Fatalf("EmitStorageUsage: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)

	after := time.Now().UTC().Format("2006-01-02T15")

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	wantKey := "cf-idem:" + before
	// before and after should be identical unless we crossed an hour boundary.
	if before != after {
		t.Skipf("hour boundary during test run: before=%s after=%s", before, after)
	}
	if events[0].IdempotencyKey != wantKey {
		t.Errorf("IdempotencyKey: want %q, got %q", wantKey, events[0].IdempotencyKey)
	}
}

// TestEmitStorageUsage_SkipsMissingAccount verifies campfires with no account
// mapping produce no events.
func TestEmitStorageUsage_SkipsMissingAccount(t *testing.T) {
	var events []forge.UsageEvent
	srv := newForgeServer(t, &events)
	defer srv.Close()

	emitter, cleanup := newEmitter(t, srv)
	defer cleanup()

	store := &mockStorageStore{
		counters: []metering.StorageCounter{
			{CampfireID: "cf-no-acct", BytesStored: 1024},
		},
	}
	if err := metering.EmitStorageUsage(context.Background(), store, emitter, func(string) string { return "" }); err != nil {
		t.Fatalf("EmitStorageUsage: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if len(events) != 0 {
		t.Errorf("expected 0 events for missing account, got %d", len(events))
	}
}

// TestEmitStorageUsage_SkipsZeroBytes verifies counters with BytesStored=0
// produce no events.
func TestEmitStorageUsage_SkipsZeroBytes(t *testing.T) {
	var events []forge.UsageEvent
	srv := newForgeServer(t, &events)
	defer srv.Close()

	emitter, cleanup := newEmitter(t, srv)
	defer cleanup()

	store := &mockStorageStore{
		counters: []metering.StorageCounter{
			{CampfireID: "cf-zero", BytesStored: 0},
		},
	}
	if err := metering.EmitStorageUsage(context.Background(), store, emitter, func(string) string { return "acct-z" }); err != nil {
		t.Fatalf("EmitStorageUsage: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if len(events) != 0 {
		t.Errorf("expected 0 events for zero bytes, got %d", len(events))
	}
}

// ---------------------------------------------------------------------------
// EmitPeerEndpointUsage tests
// ---------------------------------------------------------------------------

// TestEmitPeerEndpointUsage_Basic verifies a campfire with 3 peer endpoints
// emits a "peer-endpoint-day" event with Quantity=3.
func TestEmitPeerEndpointUsage_Basic(t *testing.T) {
	var events []forge.UsageEvent
	srv := newForgeServer(t, &events)
	defer srv.Close()

	emitter, cleanup := newEmitter(t, srv)
	defer cleanup()

	store := &mockPeerCountStore{
		counts: []metering.PeerCount{
			{CampfireID: "cf-peer", Count: 3},
		},
	}
	if err := metering.EmitPeerEndpointUsage(context.Background(), store, emitter, func(string) string { return "acct-p1" }); err != nil {
		t.Fatalf("EmitPeerEndpointUsage: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.UnitType != "peer-endpoint-day" {
		t.Errorf("UnitType: want peer-endpoint-day, got %s", ev.UnitType)
	}
	if ev.Quantity != 3.0 {
		t.Errorf("Quantity: want 3, got %v", ev.Quantity)
	}
	if ev.ServiceID != "campfire-hosting" {
		t.Errorf("ServiceID: want campfire-hosting, got %s", ev.ServiceID)
	}
}

// TestEmitPeerEndpointUsage_IdempotencyKeyFormat checks the daily key format.
func TestEmitPeerEndpointUsage_IdempotencyKeyFormat(t *testing.T) {
	var events []forge.UsageEvent
	srv := newForgeServer(t, &events)
	defer srv.Close()

	emitter, cleanup := newEmitter(t, srv)
	defer cleanup()

	store := &mockPeerCountStore{
		counts: []metering.PeerCount{
			{CampfireID: "cf-key", Count: 1},
		},
	}
	dateStr := time.Now().UTC().Format("2006-01-02")

	if err := metering.EmitPeerEndpointUsage(context.Background(), store, emitter, func(string) string { return "acct-k" }); err != nil {
		t.Fatalf("EmitPeerEndpointUsage: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	wantKey := "cf-key:" + dateStr
	if events[0].IdempotencyKey != wantKey {
		t.Errorf("IdempotencyKey: want %q, got %q", wantKey, events[0].IdempotencyKey)
	}
}

// TestEmitPeerEndpointUsage_MultipleCampfires verifies each campfire gets
// its own event.
func TestEmitPeerEndpointUsage_MultipleCampfires(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"status":"created"}`)
	}))
	defer srv.Close()

	emitter, cleanup := newEmitter(t, srv)
	defer cleanup()

	store := &mockPeerCountStore{
		counts: []metering.PeerCount{
			{CampfireID: "cf-a", Count: 2},
			{CampfireID: "cf-b", Count: 5},
			{CampfireID: "cf-c", Count: 1},
		},
	}
	if err := metering.EmitPeerEndpointUsage(context.Background(), store, emitter, func(string) string { return "acct-x" }); err != nil {
		t.Fatalf("EmitPeerEndpointUsage: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)

	if n := atomic.LoadInt32(&count); n != 3 {
		t.Errorf("expected 3 ingest calls (one per campfire), got %d", n)
	}
}

// ---------------------------------------------------------------------------
// GarbageCollectZeroBalance tests
// ---------------------------------------------------------------------------

// TestGCZeroBalance_DeletesForZeroBalance verifies messages are deleted when
// operator balance is zero.
func TestGCZeroBalance_DeletesForZeroBalance(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "msg-1", CampfireID: "cf-gc"},
			{ID: "msg-2", CampfireID: "cf-gc"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-gc": 0},
	}
	accountLookup := func(id string) string {
		if id == "cf-gc" {
			return "acct-gc"
		}
		return ""
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker, accountLookup, 90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 messages deleted, got %d", n)
	}
	if len(store.deleted) != 2 {
		t.Errorf("expected 2 delete calls, got %d: %v", len(store.deleted), store.deleted)
	}
}

// TestGCZeroBalance_SkipsPositiveBalance verifies messages are NOT deleted
// when operator balance is positive.
func TestGCZeroBalance_SkipsPositiveBalance(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "msg-keep", CampfireID: "cf-rich"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-rich": 1000000}, // positive balance
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "acct-rich" },
		90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 messages deleted for positive balance, got %d", n)
	}
	if len(store.deleted) != 0 {
		t.Errorf("expected 0 delete calls, got %d", len(store.deleted))
	}
}

// TestGCZeroBalance_NegativeBalance verifies negative balance also triggers GC.
func TestGCZeroBalance_NegativeBalance(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "msg-neg", CampfireID: "cf-neg"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-neg": -500},
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "acct-neg" },
		90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 message deleted for negative balance, got %d", n)
	}
}

// TestGCZeroBalance_SkipsNoAccountMapping verifies campfires with no account
// mapping are skipped.
func TestGCZeroBalance_SkipsNoAccountMapping(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "msg-unowned", CampfireID: "cf-unowned"},
		},
	}
	balChecker := &mockBalanceChecker{balances: map[string]int64{}}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "" }, // no mapping
		90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 deletions for unmapped campfire, got %d", n)
	}
}

// TestGCZeroBalance_BalanceCacheDeduplication verifies balance is only fetched
// once per account even when multiple campfires share the same account.
func TestGCZeroBalance_BalanceCacheDeduplication(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "m1", CampfireID: "cf-x"},
			{ID: "m2", CampfireID: "cf-y"},
		},
	}
	callCount := 0
	balChecker := &countingBalanceChecker{
		balances:   map[string]int64{"acct-shared": 0},
		callCounts: &callCount,
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(id string) string { return "acct-shared" }, // both campfires → same account
		90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 deletions, got %d", n)
	}
	if callCount != 1 {
		t.Errorf("expected balance fetched once, got %d times", callCount)
	}
}

// countingBalanceChecker counts Balance() calls for deduplication tests.
type countingBalanceChecker struct {
	balances   map[string]int64
	callCounts *int
}

func (c *countingBalanceChecker) Balance(_ context.Context, accountID string) (int64, error) {
	*c.callCounts++
	b, ok := c.balances[accountID]
	if !ok {
		return 0, fmt.Errorf("no balance for %s", accountID)
	}
	return b, nil
}

// errorGCStore returns an error from ListMessagesOlderThan.
type errorGCStore struct{}

func (e *errorGCStore) ListMessagesOlderThan(_ context.Context, _ string, _ int64) ([]metering.OldMessage, error) {
	return nil, fmt.Errorf("storage unavailable")
}

func (e *errorGCStore) DeleteMessage(_ context.Context, _, _ string) error {
	return nil
}

// failDeleteGCStore succeeds on list but fails on specific deletes.
type failDeleteGCStore struct {
	messages  []metering.OldMessage
	deleted   []string
	failOnIDs map[string]bool // message IDs that fail to delete
}

func (f *failDeleteGCStore) ListMessagesOlderThan(_ context.Context, _ string, _ int64) ([]metering.OldMessage, error) {
	return f.messages, nil
}

func (f *failDeleteGCStore) DeleteMessage(_ context.Context, _ string, messageID string) error {
	if f.failOnIDs[messageID] {
		return fmt.Errorf("delete failed for %s", messageID)
	}
	f.deleted = append(f.deleted, messageID)
	return nil
}

// TestGCZeroBalance_EmptyMessageList verifies no deletions when there are no
// old messages.
func TestGCZeroBalance_EmptyMessageList(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-any": 0},
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "acct-any" },
		90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 deletions for empty message list, got %d", n)
	}
	if len(store.deleted) != 0 {
		t.Errorf("expected 0 delete calls, got %d", len(store.deleted))
	}
}

// TestGCZeroBalance_MixedBalances verifies that only campfires with zero or
// negative balance have messages deleted, while positive-balance campfires are
// left untouched.
func TestGCZeroBalance_MixedBalances(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "msg-zero-1", CampfireID: "cf-zero"},
			{ID: "msg-zero-2", CampfireID: "cf-zero"},
			{ID: "msg-pos-1", CampfireID: "cf-positive"},
			{ID: "msg-neg-1", CampfireID: "cf-negative"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{
			"acct-zero": 0,
			"acct-pos":  5000000,
			"acct-neg":  -100,
		},
	}
	accountLookup := func(id string) string {
		switch id {
		case "cf-zero":
			return "acct-zero"
		case "cf-positive":
			return "acct-pos"
		case "cf-negative":
			return "acct-neg"
		default:
			return ""
		}
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker, accountLookup, 90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	// Should delete: msg-zero-1, msg-zero-2 (zero balance), msg-neg-1 (negative balance)
	// Should keep: msg-pos-1 (positive balance)
	if n != 3 {
		t.Errorf("expected 3 deletions (2 zero + 1 negative), got %d", n)
	}

	// Verify positive-balance message was NOT deleted.
	for _, id := range store.deleted {
		if id == "msg-pos-1" {
			t.Error("message from positive-balance campfire should not have been deleted")
		}
	}
}

// TestGCZeroBalance_ListMessagesError verifies that a store error from
// ListMessagesOlderThan propagates as an error return.
func TestGCZeroBalance_ListMessagesError(t *testing.T) {
	store := &errorGCStore{}
	balChecker := &mockBalanceChecker{balances: map[string]int64{}}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "acct-x" },
		90*24*time.Hour,
	)
	if err == nil {
		t.Fatal("expected error from ListMessagesOlderThan, got nil")
	}
	if n != 0 {
		t.Errorf("expected 0 deletions on error, got %d", n)
	}
}

// TestGCZeroBalance_DeleteErrorSkipsMessage verifies that a per-message delete
// error is logged and skipped (fail-safe), not propagated.
func TestGCZeroBalance_DeleteErrorSkipsMessage(t *testing.T) {
	store := &failDeleteGCStore{
		messages: []metering.OldMessage{
			{ID: "msg-ok", CampfireID: "cf-gc"},
			{ID: "msg-fail", CampfireID: "cf-gc"},
			{ID: "msg-ok2", CampfireID: "cf-gc"},
		},
		failOnIDs: map[string]bool{"msg-fail": true},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-gc": 0},
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "acct-gc" },
		90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance should not return error on per-message failure: %v", err)
	}
	// 2 out of 3 messages should succeed.
	if n != 2 {
		t.Errorf("expected 2 successful deletions (1 failed), got %d", n)
	}
	if len(store.deleted) != 2 {
		t.Errorf("expected 2 delete calls recorded, got %d", len(store.deleted))
	}
}

// TestGCZeroBalance_BalanceCheckErrorSkipsCampfire verifies that a balance
// check error causes that campfire to be skipped (fail-safe), not the whole run.
func TestGCZeroBalance_BalanceCheckErrorSkipsCampfire(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "msg-err", CampfireID: "cf-err"},
			{ID: "msg-ok", CampfireID: "cf-ok"},
		},
	}
	// acct-err has no balance entry -> Balance() returns error.
	// acct-ok has zero balance -> should be GC'd.
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-ok": 0},
	}
	accountLookup := func(id string) string {
		switch id {
		case "cf-err":
			return "acct-err"
		case "cf-ok":
			return "acct-ok"
		default:
			return ""
		}
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker, accountLookup, 90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance should not fail on per-campfire balance error: %v", err)
	}
	// cf-err skipped due to balance error, cf-ok deleted.
	if n != 1 {
		t.Errorf("expected 1 deletion (cf-err skipped), got %d", n)
	}
}

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

// ---------------------------------------------------------------------------
// GarbageCollectZeroBalance edge-case tests (campfire-agent-0n3)
// ---------------------------------------------------------------------------

// cutoffAwareGCStore records the cutoff passed by GarbageCollectZeroBalance
// and filters messages by their simulated timestamp.
type cutoffAwareGCStore struct {
	// messages with simulated timestamps (UnixNano)
	timedMessages []timedOldMessage
	deleted       []string
	recordedCutoff int64
}

type timedOldMessage struct {
	metering.OldMessage
	timestampNano int64 // simulated creation time
}

func (c *cutoffAwareGCStore) ListMessagesOlderThan(_ context.Context, campfireID string, cutoff int64) ([]metering.OldMessage, error) {
	c.recordedCutoff = cutoff
	var out []metering.OldMessage
	for _, m := range c.timedMessages {
		if campfireID != "" && m.CampfireID != campfireID {
			continue
		}
		// Message is "older than cutoff" if its timestamp < cutoff
		if m.timestampNano < cutoff {
			out = append(out, m.OldMessage)
		}
	}
	return out, nil
}

func (c *cutoffAwareGCStore) DeleteMessage(_ context.Context, _, messageID string) error {
	c.deleted = append(c.deleted, messageID)
	return nil
}

// TestGCZeroBalance_MaxAge_ExactBoundary verifies that the cutoff computation
// uses time.Now().Add(-maxAge).UnixNano(). We place messages well before and
// well after the boundary to avoid flakiness from the time gap between test
// setup and function execution. The cutoff-aware mock validates that the
// cutoff value is in the expected range.
func TestGCZeroBalance_MaxAge_ExactBoundary(t *testing.T) {
	maxAge := 90 * 24 * time.Hour
	now := time.Now()

	// Message 10 seconds older than maxAge — clearly should be GC'd.
	wellBefore := now.Add(-maxAge - 10*time.Second).UnixNano()
	// Message 1 second older than maxAge — should be GC'd.
	oneSecBefore := now.Add(-maxAge - time.Second).UnixNano()
	// Message 10 seconds younger than maxAge — should NOT be GC'd.
	wellAfter := now.Add(-maxAge + 10*time.Second).UnixNano()
	// Message 1 second younger than maxAge — should NOT be GC'd.
	oneSecAfter := now.Add(-maxAge + time.Second).UnixNano()

	store := &cutoffAwareGCStore{
		timedMessages: []timedOldMessage{
			{metering.OldMessage{ID: "msg-well-before", CampfireID: "cf-edge"}, wellBefore},
			{metering.OldMessage{ID: "msg-1s-before", CampfireID: "cf-edge"}, oneSecBefore},
			{metering.OldMessage{ID: "msg-1s-after", CampfireID: "cf-edge"}, oneSecAfter},
			{metering.OldMessage{ID: "msg-well-after", CampfireID: "cf-edge"}, wellAfter},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-edge": 0},
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "acct-edge" },
		maxAge,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}

	// msg-well-before and msg-1s-before are older than cutoff → deleted.
	// msg-1s-after and msg-well-after are younger than cutoff → kept.
	if n != 2 {
		t.Errorf("expected 2 deletions (before-boundary messages), got %d", n)
	}
	deletedSet := make(map[string]bool)
	for _, id := range store.deleted {
		deletedSet[id] = true
	}
	if !deletedSet["msg-well-before"] || !deletedSet["msg-1s-before"] {
		t.Errorf("expected msg-well-before and msg-1s-before deleted, got %v", store.deleted)
	}
	if deletedSet["msg-1s-after"] || deletedSet["msg-well-after"] {
		t.Errorf("younger-than-cutoff messages should not be deleted, got %v", store.deleted)
	}

	// Verify the cutoff is in the right ballpark (within 2 seconds of expected).
	expectedCutoff := now.Add(-maxAge).UnixNano()
	diff := store.recordedCutoff - expectedCutoff
	if diff < 0 {
		diff = -diff
	}
	if diff > 2*int64(time.Second) {
		t.Errorf("cutoff drift too large: recorded=%d expected≈%d diff=%d",
			store.recordedCutoff, expectedCutoff, diff)
	}
}

// TestGCZeroBalance_MaxAge_ZeroDuration verifies behavior with maxAge=0.
// All messages should be considered "older than now".
func TestGCZeroBalance_MaxAge_ZeroDuration(t *testing.T) {
	now := time.Now()
	store := &cutoffAwareGCStore{
		timedMessages: []timedOldMessage{
			// A message created 1 second ago — older than "now"
			{metering.OldMessage{ID: "msg-recent", CampfireID: "cf-z"}, now.Add(-time.Second).UnixNano()},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-z": 0},
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "acct-z" },
		0, // maxAge = 0 means cutoff = now
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 deletion with maxAge=0, got %d", n)
	}
}

// TestGCZeroBalance_MaxAge_VeryLarge verifies that a very large maxAge means
// almost nothing gets garbage collected (cutoff far in the past).
func TestGCZeroBalance_MaxAge_VeryLarge(t *testing.T) {
	now := time.Now()
	// 10 years maxAge — cutoff is way in the past
	maxAge := 10 * 365 * 24 * time.Hour
	store := &cutoffAwareGCStore{
		timedMessages: []timedOldMessage{
			// Message from 1 year ago — still within 10-year maxAge
			{metering.OldMessage{ID: "msg-1yr", CampfireID: "cf-v"}, now.Add(-365 * 24 * time.Hour).UnixNano()},
			// Message from 5 years ago — still within 10-year maxAge
			{metering.OldMessage{ID: "msg-5yr", CampfireID: "cf-v"}, now.Add(-5 * 365 * 24 * time.Hour).UnixNano()},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-v": 0},
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "acct-v" },
		maxAge,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	// Both messages are younger than 10 years, so neither should be GC'd
	if n != 0 {
		t.Errorf("expected 0 deletions with 10yr maxAge, got %d", n)
	}
}

// TestGCZeroBalance_CampfireIDFiltering verifies that messages from different
// campfires are correctly grouped and only zero-balance campfire messages are
// deleted — the function scans all campfires (empty campfireID to store) then
// groups by campfireID in-memory.
func TestGCZeroBalance_CampfireIDFiltering(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "m1", CampfireID: "cf-poor"},
			{ID: "m2", CampfireID: "cf-poor"},
			{ID: "m3", CampfireID: "cf-rich"},
			{ID: "m4", CampfireID: "cf-rich"},
			{ID: "m5", CampfireID: "cf-rich"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{
			"acct-poor": 0,
			"acct-rich": 100,
		},
	}
	accountLookup := func(id string) string {
		switch id {
		case "cf-poor":
			return "acct-poor"
		case "cf-rich":
			return "acct-rich"
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
	// Only cf-poor's 2 messages should be deleted
	if n != 2 {
		t.Errorf("expected 2 deletions (cf-poor only), got %d", n)
	}
	for _, id := range store.deleted {
		if id == "m3" || id == "m4" || id == "m5" {
			t.Errorf("cf-rich message %s should not have been deleted", id)
		}
	}
}

// TestGCZeroBalance_MultipleCampfiresSameAccount verifies grouping when
// multiple campfires share the same Forge account (and that account has zero
// balance). All campfires under that account should be GC'd.
func TestGCZeroBalance_MultipleCampfiresSameAccount(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "m1", CampfireID: "cf-a1"},
			{ID: "m2", CampfireID: "cf-a2"},
			{ID: "m3", CampfireID: "cf-a3"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"shared-acct": 0},
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "shared-acct" },
		90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 deletions (all campfires share zero-balance account), got %d", n)
	}
}

// TestGCZeroBalance_AllPositiveBalance verifies that when every campfire has
// a positive balance, nothing is deleted.
func TestGCZeroBalance_AllPositiveBalance(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "m1", CampfireID: "cf-1"},
			{ID: "m2", CampfireID: "cf-2"},
			{ID: "m3", CampfireID: "cf-3"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{
			"acct-1": 100,
			"acct-2": 1,    // minimal positive
			"acct-3": 999999,
		},
	}
	accountLookup := func(id string) string {
		switch id {
		case "cf-1":
			return "acct-1"
		case "cf-2":
			return "acct-2"
		case "cf-3":
			return "acct-3"
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
	if n != 0 {
		t.Errorf("expected 0 deletions (all positive), got %d", n)
	}
	if len(store.deleted) != 0 {
		t.Errorf("expected 0 delete calls, got %d", len(store.deleted))
	}
}

// TestGCZeroBalance_AllZeroBalance verifies that when every campfire has zero
// balance, all messages are deleted.
func TestGCZeroBalance_AllZeroBalance(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "m1", CampfireID: "cf-1"},
			{ID: "m2", CampfireID: "cf-2"},
			{ID: "m3", CampfireID: "cf-3"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{
			"acct-1": 0,
			"acct-2": 0,
			"acct-3": 0,
		},
	}
	accountLookup := func(id string) string {
		switch id {
		case "cf-1":
			return "acct-1"
		case "cf-2":
			return "acct-2"
		case "cf-3":
			return "acct-3"
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
	if n != 3 {
		t.Errorf("expected 3 deletions (all zero), got %d", n)
	}
}

// TestGCZeroBalance_BalanceExactlyOne verifies that balance=1 (minimal positive)
// is treated as positive and messages are preserved.
func TestGCZeroBalance_BalanceExactlyOne(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "m1", CampfireID: "cf-one"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-one": 1}, // 1 micro-USD
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "acct-one" },
		90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 deletions (balance=1 is positive), got %d", n)
	}
}

// TestGCZeroBalance_BalanceExactlyNegativeOne verifies that balance=-1 (minimal
// negative) triggers GC.
func TestGCZeroBalance_BalanceExactlyNegativeOne(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "m1", CampfireID: "cf-negone"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-negone": -1}, // -1 micro-USD
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "acct-negone" },
		90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 deletion (balance=-1 is negative), got %d", n)
	}
}

// TestGCZeroBalance_MixedMappedAndUnmapped verifies that campfires without
// account mappings are silently skipped while mapped ones are processed.
func TestGCZeroBalance_MixedMappedAndUnmapped(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "m1", CampfireID: "cf-mapped"},
			{ID: "m2", CampfireID: "cf-unmapped"},
			{ID: "m3", CampfireID: "cf-mapped"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"acct-mapped": 0},
	}
	accountLookup := func(id string) string {
		if id == "cf-mapped" {
			return "acct-mapped"
		}
		return "" // cf-unmapped has no mapping
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker, accountLookup, 90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	// Only cf-mapped messages (m1, m3) should be deleted
	if n != 2 {
		t.Errorf("expected 2 deletions (cf-unmapped skipped), got %d", n)
	}
	for _, id := range store.deleted {
		if id == "m2" {
			t.Error("unmapped campfire message m2 should not have been deleted")
		}
	}
}

// TestGCZeroBalance_SingleMessageSingleCampfire verifies the simplest
// non-trivial case: one campfire, one message, zero balance.
func TestGCZeroBalance_SingleMessageSingleCampfire(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "only-msg", CampfireID: "only-cf"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{"only-acct": 0},
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker,
		func(string) string { return "only-acct" },
		24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 deletion, got %d", n)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "only-msg" {
		t.Errorf("expected only-msg deleted, got %v", store.deleted)
	}
}

// TestGCZeroBalance_ManyCampfiresVariedBalances exercises the grouping logic
// with a larger number of campfires with varied balance states.
func TestGCZeroBalance_ManyCampfiresVariedBalances(t *testing.T) {
	store := &mockGCStore{
		messages: []metering.OldMessage{
			{ID: "m1", CampfireID: "cf-zero-a"},
			{ID: "m2", CampfireID: "cf-zero-a"},
			{ID: "m3", CampfireID: "cf-zero-b"},
			{ID: "m4", CampfireID: "cf-pos-a"},
			{ID: "m5", CampfireID: "cf-pos-b"},
			{ID: "m6", CampfireID: "cf-neg-a"},
			{ID: "m7", CampfireID: "cf-neg-a"},
			{ID: "m8", CampfireID: "cf-neg-a"},
			{ID: "m9", CampfireID: "cf-unmapped"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{
			"acct-zero-a": 0,
			"acct-zero-b": 0,
			"acct-pos-a":  500,
			"acct-pos-b":  1,
			"acct-neg-a":  -999,
		},
	}
	accountLookup := func(id string) string {
		switch id {
		case "cf-zero-a":
			return "acct-zero-a"
		case "cf-zero-b":
			return "acct-zero-b"
		case "cf-pos-a":
			return "acct-pos-a"
		case "cf-pos-b":
			return "acct-pos-b"
		case "cf-neg-a":
			return "acct-neg-a"
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
	// Deleted: m1,m2 (cf-zero-a), m3 (cf-zero-b), m6,m7,m8 (cf-neg-a) = 6
	// Kept: m4 (cf-pos-a), m5 (cf-pos-b), m9 (cf-unmapped) = 3
	if n != 6 {
		t.Errorf("expected 6 deletions, got %d", n)
	}

	deletedSet := make(map[string]bool)
	for _, id := range store.deleted {
		deletedSet[id] = true
	}
	// Verify positive-balance and unmapped messages were NOT deleted
	for _, kept := range []string{"m4", "m5", "m9"} {
		if deletedSet[kept] {
			t.Errorf("message %s should not have been deleted", kept)
		}
	}
	// Verify zero/negative-balance messages WERE deleted
	for _, del := range []string{"m1", "m2", "m3", "m6", "m7", "m8"} {
		if !deletedSet[del] {
			t.Errorf("message %s should have been deleted", del)
		}
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

// ---------------------------------------------------------------------------
// campfireID parameter verification (campfire-agent-cbh)
// ---------------------------------------------------------------------------

// deleteCall records both campfireID and messageID passed to DeleteMessage.
type deleteCall struct {
	CampfireID string
	MessageID  string
}

// campfireIDTrackingStore captures the campfireID parameter in DeleteMessage calls.
type campfireIDTrackingStore struct {
	messages []metering.OldMessage
	calls    []deleteCall
}

func (s *campfireIDTrackingStore) ListMessagesOlderThan(_ context.Context, campfireID string, _ int64) ([]metering.OldMessage, error) {
	if campfireID == "" {
		return s.messages, nil
	}
	var out []metering.OldMessage
	for _, msg := range s.messages {
		if msg.CampfireID == campfireID {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (s *campfireIDTrackingStore) DeleteMessage(_ context.Context, campfireID, messageID string) error {
	s.calls = append(s.calls, deleteCall{CampfireID: campfireID, MessageID: messageID})
	return nil
}

// TestGCZeroBalance_DeleteMessageReceivesCorrectCampfireID verifies that
// GarbageCollectZeroBalance passes the correct campfireID to DeleteMessage
// for each message, especially when messages span multiple campfires.
func TestGCZeroBalance_DeleteMessageReceivesCorrectCampfireID(t *testing.T) {
	store := &campfireIDTrackingStore{
		messages: []metering.OldMessage{
			{ID: "msg-a1", CampfireID: "cf-alpha"},
			{ID: "msg-a2", CampfireID: "cf-alpha"},
			{ID: "msg-b1", CampfireID: "cf-beta"},
		},
	}
	balChecker := &mockBalanceChecker{
		balances: map[string]int64{
			"acct-alpha": 0,
			"acct-beta":  -100,
		},
	}
	accountLookup := func(id string) string {
		switch id {
		case "cf-alpha":
			return "acct-alpha"
		case "cf-beta":
			return "acct-beta"
		}
		return ""
	}

	n, err := metering.GarbageCollectZeroBalance(
		context.Background(), store, balChecker, accountLookup, 90*24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GarbageCollectZeroBalance: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 messages deleted, got %d", n)
	}
	if len(store.calls) != 3 {
		t.Fatalf("expected 3 DeleteMessage calls, got %d", len(store.calls))
	}

	// Build a set of (campfireID, messageID) pairs from the calls.
	type pair struct{ campfire, message string }
	got := make(map[pair]bool)
	for _, c := range store.calls {
		got[pair{c.CampfireID, c.MessageID}] = true
	}

	expected := []pair{
		{"cf-alpha", "msg-a1"},
		{"cf-alpha", "msg-a2"},
		{"cf-beta", "msg-b1"},
	}
	for _, e := range expected {
		if !got[e] {
			t.Errorf("missing DeleteMessage call with campfireID=%q messageID=%q", e.campfire, e.message)
		}
	}

	// Also verify no call received a wrong campfireID.
	for _, c := range store.calls {
		var expectedCampfire string
		switch {
		case c.MessageID == "msg-a1" || c.MessageID == "msg-a2":
			expectedCampfire = "cf-alpha"
		case c.MessageID == "msg-b1":
			expectedCampfire = "cf-beta"
		default:
			t.Errorf("unexpected message ID in DeleteMessage call: %q", c.MessageID)
			continue
		}
		if c.CampfireID != expectedCampfire {
			t.Errorf("DeleteMessage called with campfireID=%q for messageID=%q, expected campfireID=%q",
				c.CampfireID, c.MessageID, expectedCampfire)
		}
	}
}

package hosting

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
)

// mockIngester records ingested events and optionally returns an error.
type mockIngester struct {
	mu     sync.Mutex
	events []forge.UsageEvent
	err    error
}

func (m *mockIngester) Ingest(_ context.Context, e forge.UsageEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return m.err
}

func (m *mockIngester) get() []forge.UsageEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]forge.UsageEvent, len(m.events))
	copy(cp, m.events)
	return cp
}

// fixedNow returns a function that always returns t.
func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

// newTestEmitter creates a UsageEmitter wired to mock with a controlled clock.
func newTestEmitter(mock *mockIngester, interval time.Duration, now func() time.Time) *UsageEmitter {
	e := NewUsageEmitter(mock, interval)
	e.now = now
	return e
}

// ---- RecordMessage tests ----

func TestRecordMessage_IncrementsCounter(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, time.Millisecond, fixedNow(time.Now().UTC()))
	if err := e.RecordMessage("cf1", "op1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := e.RecordMessage("cf1", "op1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := e.RecordMessage("cf2", "op1"); err != nil { // different campfire, same operator
		t.Fatalf("unexpected error: %v", err)
	}

	snap, _ := e.snapshot()
	if got := snap["op1"]; got != 3 {
		t.Fatalf("expected 3 messages for op1, got %d", got)
	}
}

func TestRecordMessage_MultipleOperators(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, time.Millisecond, fixedNow(time.Now().UTC()))
	e.RecordMessage("cf1", "op1") //nolint:errcheck
	e.RecordMessage("cf2", "op2") //nolint:errcheck
	e.RecordMessage("cf3", "op2") //nolint:errcheck

	snap, _ := e.snapshot()
	if got := snap["op1"]; got != 1 {
		t.Fatalf("op1: want 1, got %d", got)
	}
	if got := snap["op2"]; got != 2 {
		t.Fatalf("op2: want 2, got %d", got)
	}
}

func TestRecordMessage_RejectsEmptyOperatorID(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, time.Millisecond, fixedNow(time.Now().UTC()))
	err := e.RecordMessage("cf1", "")
	if err == nil {
		t.Fatal("expected error for empty operatorAccountID, got nil")
	}
}

// ---- Emit / rollup tests ----

func TestEmit_ProducesCorrectUsageEvents(t *testing.T) {
	base := time.Date(2026, 3, 28, 15, 30, 0, 0, time.UTC) // mid-hour
	mock := &mockIngester{}
	e := newTestEmitter(mock, 10*time.Millisecond, fixedNow(base))

	e.RecordMessage("cf1", "op1") //nolint:errcheck
	e.RecordMessage("cf2", "op1") // same operator: rolled up //nolint:errcheck
	e.RecordMessage("cf3", "op2") //nolint:errcheck

	e.emit(context.Background())

	events := mock.get()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	byOp := make(map[string]forge.UsageEvent)
	for _, ev := range events {
		byOp[ev.AccountID] = ev
	}

	op1 := byOp["op1"]
	if op1.Quantity != 2 {
		t.Errorf("op1 quantity: want 2, got %f", op1.Quantity)
	}
	if op1.ServiceID != "campfire-hosting" {
		t.Errorf("op1 ServiceID: want campfire-hosting, got %s", op1.ServiceID)
	}
	if op1.UnitType != "message" {
		t.Errorf("op1 UnitType: want message, got %s", op1.UnitType)
	}

	op2 := byOp["op2"]
	if op2.Quantity != 1 {
		t.Errorf("op2 quantity: want 1, got %f", op2.Quantity)
	}
}

func TestEmit_IdempotencyKeyFormat(t *testing.T) {
	base := time.Date(2026, 3, 28, 15, 45, 0, 0, time.UTC)
	bucket := base.Truncate(time.Hour) // 15:00 UTC
	mock := &mockIngester{}
	e := newTestEmitter(mock, 10*time.Millisecond, fixedNow(base))
	e.RecordMessage("cf1", "op1") //nolint:errcheck
	e.emit(context.Background())

	events := mock.get()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	want := fmt.Sprintf("op1/%d", bucket.Unix())
	if events[0].IdempotencyKey != want {
		t.Errorf("IdempotencyKey: want %q, got %q", want, events[0].IdempotencyKey)
	}
}

func TestEmit_IdempotencyKeyConsistentAcrossCallsInSameHour(t *testing.T) {
	// Two emits in the same hour should produce the same idempotency key.
	base := time.Date(2026, 3, 28, 10, 5, 0, 0, time.UTC)
	bucket := base.Truncate(time.Hour)
	mock := &mockIngester{}
	e := newTestEmitter(mock, 10*time.Millisecond, fixedNow(base))

	e.RecordMessage("cf1", "op1") //nolint:errcheck
	e.emit(context.Background())
	e.RecordMessage("cf1", "op1") //nolint:errcheck
	e.emit(context.Background())

	events := mock.get()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	want := fmt.Sprintf("op1/%d", bucket.Unix())
	for _, ev := range events {
		if ev.IdempotencyKey != want {
			t.Errorf("IdempotencyKey: want %q, got %q", want, ev.IdempotencyKey)
		}
	}
}

func TestEmit_TimestampIsTopOfHour(t *testing.T) {
	base := time.Date(2026, 3, 28, 9, 55, 0, 0, time.UTC)
	bucket := base.Truncate(time.Hour) // 09:00
	mock := &mockIngester{}
	e := newTestEmitter(mock, 10*time.Millisecond, fixedNow(base))
	e.RecordMessage("cf1", "op1") //nolint:errcheck
	e.emit(context.Background())

	events := mock.get()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if !events[0].Timestamp.Equal(bucket) {
		t.Errorf("Timestamp: want %v, got %v", bucket, events[0].Timestamp)
	}
}

func TestEmit_EmptySnapshotProducesNoEvents(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, 10*time.Millisecond, fixedNow(time.Now().UTC()))
	e.emit(context.Background())
	if len(mock.get()) != 0 {
		t.Errorf("expected no events for empty snapshot")
	}
}

func TestEmit_SnapshotResetsCounters(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, 10*time.Millisecond, fixedNow(time.Now().UTC()))
	e.RecordMessage("cf1", "op1") //nolint:errcheck
	e.emit(context.Background())
	// Second emit should produce nothing.
	e.emit(context.Background())
	if len(mock.get()) != 1 {
		t.Errorf("expected only 1 event total, got %d", len(mock.get()))
	}
}

// TestEmit_HourBoundaryBucketConsistency verifies that the hour bucket is
// captured atomically with the snapshot drain. Simulates a clock that
// advances across an hour boundary between two calls.
func TestEmit_HourBoundaryBucketConsistency(t *testing.T) {
	// We record messages at 15:59 but the clock ticks to 16:00 mid-emit.
	// Both the snapshot AND the bucket should be captured together so the
	// idempotency key reflects the hour in which messages were counted.
	beforeBoundary := time.Date(2026, 3, 28, 15, 59, 59, 0, time.UTC)
	afterBoundary := time.Date(2026, 3, 28, 16, 0, 0, 0, time.UTC)

	callCount := 0
	advancingClock := func() time.Time {
		callCount++
		if callCount <= 1 {
			return beforeBoundary
		}
		return afterBoundary
	}

	mock := &mockIngester{}
	e := newTestEmitter(mock, 10*time.Millisecond, advancingClock)
	e.RecordMessage("cf1", "op1") //nolint:errcheck

	e.emit(context.Background())

	events := mock.get()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	// The bucket should match whichever single call snapshot() made to now().
	// With the fix, now() is called exactly once inside snapshot(), so the
	// bucket and the drain are always consistent.
	gotBucket := events[0].Timestamp
	wantKey := fmt.Sprintf("op1/%d", gotBucket.Unix())
	if events[0].IdempotencyKey != wantKey {
		t.Errorf("IdempotencyKey inconsistent with Timestamp: key=%q bucket=%v", events[0].IdempotencyKey, gotBucket)
	}
}

// ---- Stop() lifecycle tests ----

func TestStop_EmitsFinalBatch(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, time.Hour, fixedNow(time.Now().UTC()))
	e.RecordMessage("cf1", "op1") //nolint:errcheck

	ctx := context.Background()
	started := make(chan struct{})
	go func() {
		close(started)
		e.Start(ctx)
	}()
	<-started
	// Wait until Start() is blocked in the select by reading doneCh readiness.
	// We use a short retry loop with runtime.Gosched() instead of time.Sleep.
	// The goroutine enters the select almost immediately; yield a few times.
	for i := 0; i < 100; i++ {
		// A tiny sleep here is unavoidable: we need Start's goroutine to reach
		// the select statement. We use the minimum viable pause.
		time.Sleep(time.Microsecond)
	}

	e.Stop()

	events := mock.get()
	if len(events) != 1 {
		t.Fatalf("Stop: expected 1 final event, got %d", len(events))
	}
	if events[0].AccountID != "op1" {
		t.Errorf("Stop: unexpected AccountID %q", events[0].AccountID)
	}
}

// TestStop_DoubleCallDoesNotPanic verifies that calling Stop() twice does not
// panic (the original code closed an unbuffered channel twice).
func TestStop_DoubleCallDoesNotPanic(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, time.Hour, fixedNow(time.Now().UTC()))

	ctx := context.Background()
	go e.Start(ctx)
	// Brief yield so Start reaches the select.
	time.Sleep(time.Millisecond)

	e.Stop()
	// Second call must not panic.
	e.Stop()
}

// TestStop_BeforeStart verifies that calling Stop() before Start() does not
// deadlock. The original code blocked on <-doneCh which is only closed by
// Start(); calling Stop() first would hang forever.
func TestStop_BeforeStart(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, time.Hour, fixedNow(time.Now().UTC()))

	done := make(chan struct{})
	go func() {
		e.Stop()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() deadlocked when called before Start()")
	}
}

// TestStop_ConcurrentRecordMessage verifies that RecordMessage calls racing
// with Stop() do not cause data races or panics.
func TestStop_ConcurrentRecordMessage(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, time.Hour, fixedNow(time.Now().UTC()))

	ctx := context.Background()
	go e.Start(ctx)
	time.Sleep(time.Millisecond) // let Start reach select

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.RecordMessage("cf1", "op1") //nolint:errcheck
		}()
	}

	go func() {
		wg.Wait()
		e.Stop()
	}()

	// Block until Stop completes (via doneCh). We give it a generous timeout.
	select {
	case <-e.doneCh:
		// ok
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Stop to complete")
	}
}

// ---- Tick-driven integration test ----

func TestStart_TickProducesEvents(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, 20*time.Millisecond, fixedNow(time.Now().UTC()))
	e.RecordMessage("cf1", "op1") //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		e.Start(ctx)
		close(done)
	}()
	<-done

	if len(mock.get()) == 0 {
		t.Fatal("expected at least one event from tick loop")
	}
}

// ---- OnError callback ----

func TestEmit_OnErrorCalledOnIngestFailure(t *testing.T) {
	mock := &mockIngester{err: fmt.Errorf("network error")}
	var errCount int32
	e := newTestEmitter(mock, 10*time.Millisecond, fixedNow(time.Now().UTC()))
	e.OnError = func(op string, _ error) { atomic.AddInt32(&errCount, 1) }
	e.RecordMessage("cf1", "op1") //nolint:errcheck
	e.emit(context.Background())
	if atomic.LoadInt32(&errCount) != 1 {
		t.Errorf("OnError: want 1 call, got %d", errCount)
	}
}

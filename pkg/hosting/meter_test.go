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
	e.RecordMessage("cf1", "op1")
	e.RecordMessage("cf1", "op1")
	e.RecordMessage("cf2", "op1") // different campfire, same operator

	snap := e.snapshot()
	if got := snap["op1"]; got != 3 {
		t.Fatalf("expected 3 messages for op1, got %d", got)
	}
}

func TestRecordMessage_MultipleOperators(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, time.Millisecond, fixedNow(time.Now().UTC()))
	e.RecordMessage("cf1", "op1")
	e.RecordMessage("cf2", "op2")
	e.RecordMessage("cf3", "op2")

	snap := e.snapshot()
	if got := snap["op1"]; got != 1 {
		t.Fatalf("op1: want 1, got %d", got)
	}
	if got := snap["op2"]; got != 2 {
		t.Fatalf("op2: want 2, got %d", got)
	}
}

// ---- Emit / rollup tests ----

func TestEmit_ProducesCorrectUsageEvents(t *testing.T) {
	base := time.Date(2026, 3, 28, 15, 30, 0, 0, time.UTC) // mid-hour
	mock := &mockIngester{}
	e := newTestEmitter(mock, 10*time.Millisecond, fixedNow(base))

	e.RecordMessage("cf1", "op1")
	e.RecordMessage("cf2", "op1") // same operator: rolled up
	e.RecordMessage("cf3", "op2")

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
	e.RecordMessage("cf1", "op1")
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

	e.RecordMessage("cf1", "op1")
	e.emit(context.Background())
	e.RecordMessage("cf1", "op1")
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
	e.RecordMessage("cf1", "op1")
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
	e.RecordMessage("cf1", "op1")
	e.emit(context.Background())
	// Second emit should produce nothing.
	e.emit(context.Background())
	if len(mock.get()) != 1 {
		t.Errorf("expected only 1 event total, got %d", len(mock.get()))
	}
}

// ---- Stop() emits final batch ----

func TestStop_EmitsFinalBatch(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, time.Hour, fixedNow(time.Now().UTC()))
	e.RecordMessage("cf1", "op1")

	ctx := context.Background()
	go e.Start(ctx)

	// Give Start() a moment to be waiting in select.
	time.Sleep(5 * time.Millisecond)
	e.Stop()

	events := mock.get()
	if len(events) != 1 {
		t.Fatalf("Stop: expected 1 final event, got %d", len(events))
	}
	if events[0].AccountID != "op1" {
		t.Errorf("Stop: unexpected AccountID %q", events[0].AccountID)
	}
}

// ---- Tick-driven integration test ----

func TestStart_TickProducesEvents(t *testing.T) {
	mock := &mockIngester{}
	e := newTestEmitter(mock, 20*time.Millisecond, fixedNow(time.Now().UTC()))
	e.RecordMessage("cf1", "op1")

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
	e.RecordMessage("cf1", "op1")
	e.emit(context.Background())
	if atomic.LoadInt32(&errCount) != 1 {
		t.Errorf("OnError: want 1 call, got %d", errCount)
	}
}

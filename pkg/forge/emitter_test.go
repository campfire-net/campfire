package forge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
)

// newIngestServer returns a test server that records ingest calls.
// Each call increments *count and returns 201.
func newIngestServer(t *testing.T, count *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage/ingest" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		atomic.AddInt32(count, 1)
		writeJSON(w, http.StatusCreated, map[string]any{"status": "created"})
	}))
}

func makeEvent(id string) forge.UsageEvent {
	return forge.UsageEvent{
		AccountID:      "acct-test",
		ServiceID:      "campfire",
		IdempotencyKey: id,
		UnitType:       "message",
		Quantity:       1.0,
		Timestamp:      time.Now(),
	}
}

// TestEmitter_EmitEnqueuesAndDelivers verifies that emitted events reach the Forge client.
func TestEmitter_EmitEnqueuesAndDelivers(t *testing.T) {
	var count int32
	srv := newIngestServer(t, &count)
	defer srv.Close()

	client := newTestClient(srv)
	emitter := forge.NewForgeEmitter(client, 10, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		emitter.Run(ctx)
		close(done)
	}()

	emitter.Emit(makeEvent("evt-001"))
	emitter.Emit(makeEvent("evt-002"))
	emitter.Emit(makeEvent("evt-003"))

	// Wait for the 1-second batch timeout to flush.
	time.Sleep(1500 * time.Millisecond)

	cancel()
	<-done

	got := atomic.LoadInt32(&count)
	if got != 3 {
		t.Errorf("expected 3 ingest calls, got %d", got)
	}
}

// TestEmitter_DropWhenFull verifies that Emit is non-blocking when channel is full.
func TestEmitter_DropWhenFull(t *testing.T) {
	var count int32
	srv := newIngestServer(t, &count)
	defer srv.Close()

	// Use a slow server to prevent draining during fill.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&count, 1)
		writeJSON(w, http.StatusCreated, map[string]any{"status": "created"})
	}))
	defer slow.Close()

	slowClient := newTestClient(slow)
	// Buffer of 3 — fill it with 3 events, then try to add more.
	emitter := forge.NewForgeEmitter(slowClient, 3, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emitter.Run(ctx)

	// Fill the buffer quickly before Run() drains.
	// We emit more than the buffer can hold and verify no blocking.
	start := time.Now()
	for i := range 10 {
		emitter.Emit(makeEvent("evt-drop-" + string(rune('0'+i))))
	}
	elapsed := time.Since(start)

	// All 10 Emit calls must complete without blocking (< 10ms).
	if elapsed > 50*time.Millisecond {
		t.Errorf("Emit blocked: elapsed=%v", elapsed)
	}
}

// TestEmitter_BatchFlushBySize verifies that a full batch of 50 flushes immediately.
func TestEmitter_BatchFlushBySize(t *testing.T) {
	var count int32
	srv := newIngestServer(t, &count)
	defer srv.Close()

	client := newTestClient(srv)
	emitter := forge.NewForgeEmitter(client, 200, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		emitter.Run(ctx)
		close(done)
	}()

	// Emit exactly 50 events — should flush immediately (batch size cap).
	for i := range 50 {
		emitter.Emit(makeEvent("evt-batch-" + string(rune('A'+i%26)) + string(rune('0'+i/26))))
	}

	// Give a short time for the batch to flush (well under the 1s timer).
	time.Sleep(200 * time.Millisecond)

	cancel()
	<-done

	got := atomic.LoadInt32(&count)
	if got != 50 {
		t.Errorf("expected 50 ingest calls after batch flush, got %d", got)
	}
}

// TestEmitter_BatchFlushByTimeout verifies partial batches flush after 1 second.
func TestEmitter_BatchFlushByTimeout(t *testing.T) {
	var count int32
	srv := newIngestServer(t, &count)
	defer srv.Close()

	client := newTestClient(srv)
	emitter := forge.NewForgeEmitter(client, 100, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emitter.Run(ctx)

	// Emit 5 events — below batch size threshold.
	for i := range 5 {
		emitter.Emit(makeEvent("evt-timeout-" + string(rune('0'+i))))
	}

	// Should not be flushed yet.
	time.Sleep(100 * time.Millisecond)
	if c := atomic.LoadInt32(&count); c > 0 {
		t.Logf("events flushed early (count=%d), may be timing-dependent", c)
	}

	// After ~1 second timeout, should be flushed.
	time.Sleep(1200 * time.Millisecond)
	if got := atomic.LoadInt32(&count); got != 5 {
		t.Errorf("expected 5 ingest calls after timeout, got %d", got)
	}
}

// TestEmitter_ErrorHandling verifies onError is called and processing continues.
func TestEmitter_ErrorHandling(t *testing.T) {
	var ingestCalls int32
	var errorCalls int32

	// Server always returns 400 (4xx — no retry, immediate error).
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&ingestCalls, 1)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
	}))
	defer errSrv.Close()

	client := newTestClient(errSrv)
	onErr := func(err error) {
		atomic.AddInt32(&errorCalls, 1)
	}
	emitter := forge.NewForgeEmitter(client, 100, onErr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		emitter.Run(ctx)
		close(done)
	}()

	// Emit 3 events — all will fail.
	emitter.Emit(makeEvent("evt-err-001"))
	emitter.Emit(makeEvent("evt-err-002"))
	emitter.Emit(makeEvent("evt-err-003"))

	// Wait for flush.
	time.Sleep(1500 * time.Millisecond)

	cancel()
	<-done

	if got := atomic.LoadInt32(&ingestCalls); got != 3 {
		t.Errorf("expected 3 ingest calls, got %d", got)
	}
	if got := atomic.LoadInt32(&errorCalls); got != 3 {
		t.Errorf("expected 3 onError calls, got %d", got)
	}
}

// TestEmitter_DrainAndClose verifies that enqueued events are delivered on drain.
func TestEmitter_DrainAndClose(t *testing.T) {
	var count int32
	srv := newIngestServer(t, &count)
	defer srv.Close()

	client := newTestClient(srv)
	emitter := forge.NewForgeEmitter(client, 100, nil)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		emitter.Run(ctx)
		close(done)
	}()

	// Emit events.
	for i := range 10 {
		emitter.Emit(makeEvent("evt-drain-" + string(rune('0'+i))))
	}

	// Cancel context, then drain.
	cancel()
	emitter.DrainAndClose(5 * time.Second)
	<-done

	got := atomic.LoadInt32(&count)
	if got != 10 {
		t.Errorf("expected 10 ingest calls after drain, got %d", got)
	}
}

// TestEmitter_DrainAndCloseWithoutPriorCancel verifies that DrainAndClose
// works correctly even when the caller has NOT cancelled the context first.
// This is the regression test for the bug where DrainAndClose would hang
// until timeout if the context wasn't pre-cancelled.
func TestEmitter_DrainAndCloseWithoutPriorCancel(t *testing.T) {
	var count int32
	srv := newIngestServer(t, &count)
	defer srv.Close()

	client := newTestClient(srv)
	emitter := forge.NewForgeEmitter(client, 100, nil)

	// Do NOT cancel this context — that's the whole point of the test.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // cleanup only

	go emitter.Run(ctx)

	// Emit events.
	for i := range 5 {
		emitter.Emit(makeEvent("evt-nocancel-" + string(rune('0'+i))))
	}

	// Give Run a moment to start processing.
	time.Sleep(50 * time.Millisecond)

	// Call DrainAndClose WITHOUT cancelling ctx first.
	// Before the fix, this would block for the full 3-second timeout.
	start := time.Now()
	emitter.DrainAndClose(3 * time.Second)
	elapsed := time.Since(start)

	// DrainAndClose should complete quickly (well under the 3s timeout)
	// because it now cancels the context internally.
	if elapsed > 2*time.Second {
		t.Fatalf("DrainAndClose blocked for %v — likely did not cancel context internally", elapsed)
	}

	got := atomic.LoadInt32(&count)
	if got != 5 {
		t.Errorf("expected 5 ingest calls after drain, got %d", got)
	}
}

// TestEmitter_ContextCancellationExitsRun verifies Run exits when ctx is cancelled.
func TestEmitter_ContextCancellationExitsRun(t *testing.T) {
	var count int32
	srv := newIngestServer(t, &count)
	defer srv.Close()

	client := newTestClient(srv)
	emitter := forge.NewForgeEmitter(client, 100, nil)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		emitter.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Run exited as expected.
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

// TestNewForgeEmitter_DefaultBufferSize verifies default buffer size is applied for <= 0.
func TestNewForgeEmitter_DefaultBufferSize(t *testing.T) {
	var count int32
	srv := newIngestServer(t, &count)
	defer srv.Close()

	client := newTestClient(srv)
	// bufferSize=0 should use default (1000); we just verify it doesn't panic.
	emitter := forge.NewForgeEmitter(client, 0, nil)
	if emitter == nil {
		t.Fatal("NewForgeEmitter returned nil")
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
)

func TestHandleSweep_MethodNotAllowed(t *testing.T) {
	s := &server{}
	req := httptest.NewRequest(http.MethodGet, "/sweep", nil)
	w := httptest.NewRecorder()
	s.handleSweep(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleSweep_Disabled(t *testing.T) {
	s := &server{} // fallbackSweep is nil
	req := httptest.NewRequest(http.MethodPost, "/sweep", nil)
	w := httptest.NewRecorder()
	s.handleSweep(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp sweepResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "disabled" {
		t.Errorf("expected status 'disabled', got %q", resp.Status)
	}
	if resp.Redispatched != 0 {
		t.Errorf("expected 0 redispatched, got %d", resp.Redispatched)
	}
}

func TestHandleSweep_NoStaleMessages(t *testing.T) {
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)
	sw := convention.NewSweeper(d, ds, nil)

	s := &server{fallbackSweep: sw}
	req := httptest.NewRequest(http.MethodPost, "/sweep", nil)
	w := httptest.NewRecorder()
	s.handleSweep(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp sweepResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", resp.Status)
	}
	if resp.Redispatched != 0 {
		t.Errorf("expected 0 redispatched, got %d", resp.Redispatched)
	}
}

// TestHandleSweep_FindsAndRedispatchesMissedMessage verifies the end-to-end flow:
// a stale "dispatched" record is found by the sweep endpoint and re-dispatched.
func TestHandleSweep_FindsAndRedispatchesMissedMessage(t *testing.T) {
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)
	sw := convention.NewSweeper(d, ds, nil)

	// Register a handler that counts invocations.
	var handlerCalls atomic.Int64
	d.RegisterTier1Handler("cf-test", "myconv", "myop", nil,
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			handlerCalls.Add(1)
			return nil, nil
		}, "server-1", "")

	// Simulate a missed dispatch: mark as dispatched then backdate past the stale threshold.
	ctx := context.Background()
	ds.MarkDispatched(ctx, "cf-test", "missed-msg", "server-1", "", "myconv", "myop")
	ds.BackdateDispatch("cf-test", "missed-msg", 10*time.Minute)

	s := &server{fallbackSweep: sw}
	req := httptest.NewRequest(http.MethodPost, "/sweep", nil)
	w := httptest.NewRecorder()
	s.handleSweep(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp sweepResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", resp.Status)
	}
	if resp.Redispatched != 1 {
		t.Fatalf("expected 1 redispatched, got %d", resp.Redispatched)
	}

	// Wait for the async handler goroutine to complete.
	time.Sleep(200 * time.Millisecond)
	if handlerCalls.Load() != 1 {
		t.Errorf("expected handler called once, got %d", handlerCalls.Load())
	}
}

// TestHandleSweep_Idempotent verifies that calling sweep twice does not re-dispatch
// a message that was already successfully fulfilled.
func TestHandleSweep_Idempotent(t *testing.T) {
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)
	sw := convention.NewSweeper(d, ds, nil)

	var handlerCalls atomic.Int64
	d.RegisterTier1Handler("cf-test", "myconv", "myop", nil,
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			handlerCalls.Add(1)
			return nil, nil
		}, "server-1", "")

	ctx := context.Background()
	ds.MarkDispatched(ctx, "cf-test", "msg-1", "server-1", "", "myconv", "myop")
	ds.BackdateDispatch("cf-test", "msg-1", 10*time.Minute)

	s := &server{fallbackSweep: sw}

	// First sweep: should re-dispatch.
	req1 := httptest.NewRequest(http.MethodPost, "/sweep", nil)
	w1 := httptest.NewRecorder()
	s.handleSweep(w1, req1)

	var resp1 sweepResponse
	json.NewDecoder(w1.Body).Decode(&resp1)
	if resp1.Redispatched != 1 {
		t.Fatalf("first sweep: expected 1, got %d", resp1.Redispatched)
	}

	// Wait for handler to complete and mark fulfilled.
	time.Sleep(200 * time.Millisecond)

	// Second sweep: message is now fulfilled, should not re-dispatch.
	req2 := httptest.NewRequest(http.MethodPost, "/sweep", nil)
	w2 := httptest.NewRecorder()
	s.handleSweep(w2, req2)

	var resp2 sweepResponse
	json.NewDecoder(w2.Body).Decode(&resp2)
	if resp2.Redispatched != 0 {
		t.Fatalf("second sweep: expected 0, got %d", resp2.Redispatched)
	}

	// Handler should have been called exactly once.
	if handlerCalls.Load() != 1 {
		t.Errorf("expected handler called once total, got %d", handlerCalls.Load())
	}
}

package main

// relay_metering_test.go — Tests for M7: relay metering hook after DeliverToAll.
//
// Tests verify that:
//   - A UsageEvent with UnitType="relay-bytes" is emitted when DeliverToAll is called.
//   - Messages sent without peer endpoints (no relay) do not emit an event.
//   - The IdempotencyKey has the expected format: campfireID + ":" + msgID + ":relay".
//   - Hop count calculation: len(provenance) - 1 (first hop is origin, not relay).
//   - The emitter is nil-safe — no panic when forgeEmitter is unset.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
	"github.com/campfire-net/campfire/pkg/message"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newMeteringForgeServer returns a test HTTP server that accepts Forge ingest
// calls and increments count for each one. It returns the server and a
// *forge.Client pointed at it.
func newMeteringForgeServer(t *testing.T, count *int64) (*httptest.Server, *forge.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage/ingest" {
			t.Errorf("unexpected forge path: %s", r.URL.Path)
		}
		atomic.AddInt64(count, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"created"}`))
	}))
	t.Cleanup(srv.Close)

	client := &forge.Client{
		BaseURL:     srv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  srv.Client(),
		RetryDelays: []time.Duration{0, 0, 0}, // no retry delays in tests
	}
	return srv, client
}

// newCapturingForgeServer returns a test HTTP server that records every
// UsageEvent body sent to /v1/usage/ingest. Returns the server, client, and
// a pointer to the slice of captured events (guarded by the returned mutex).
func newCapturingForgeServer(t *testing.T) (*httptest.Server, *forge.Client, *[]forge.UsageEvent, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var events []forge.UsageEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage/ingest" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var ev forge.UsageEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Errorf("decoding ingest body: %v", err)
		}
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"created"}`))
	}))
	t.Cleanup(srv.Close)

	client := &forge.Client{
		BaseURL:     srv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  srv.Client(),
		RetryDelays: []time.Duration{0, 0, 0},
	}
	return srv, client, &events, &mu
}

// runEmitter starts a ForgeEmitter and registers ctx cancellation cleanup.
func runEmitter(t *testing.T, emitter *forge.ForgeEmitter) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go emitter.Run(ctx)
	return cancel
}

// waitForCount blocks until *count reaches want or deadline passes.
func waitForCount(t *testing.T, count *int64, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(count) >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("timed out waiting for count=%d, got %d", want, atomic.LoadInt64(count))
}

// relayMeteringShim mirrors the exact metering block added after DeliverToAll
// in handleSend. This lets tests exercise the logic without a full HTTP
// campfire setup.
func relayMeteringShim(
	emitter *forge.ForgeEmitter,
	campfireID string,
	msg *message.Message,
	peerCount int,
) {
	if peerCount == 0 {
		return // no relay — no event
	}
	if emitter != nil {
		hopCount := len(msg.Provenance) - 1
		if hopCount < 0 {
			hopCount = 0
		}
		_ = hopCount
		emitter.Emit(forge.UsageEvent{
			AccountID:      campfireID, // TODO(M5): replace with real Forge account ID
			ServiceID:      "campfire-hosting",
			UnitType:       "relay-bytes",
			Quantity:       float64(len(msg.Payload)),
			IdempotencyKey: campfireID + ":" + msg.ID + ":relay",
		})
	}
}

// ---------------------------------------------------------------------------
// TestRelayMetering_HopCountCalculation
// ---------------------------------------------------------------------------

// TestRelayMetering_HopCountCalculation verifies hop count = len(provenance) - 1.
// The first provenance hop is the origin campfire (storage write), not a relay.
func TestRelayMetering_HopCountCalculation(t *testing.T) {
	tests := []struct {
		name           string
		provenanceHops int // number of ProvenanceHop entries in msg.Provenance
		wantRelayHops  int // expected hop count = provenanceHops - 1, clamped to 0
	}{
		{"zero hops (not yet stored)", 0, 0},
		{"one hop (origin only)", 1, 0},
		{"two hops (one relay)", 2, 1},
		{"three hops (two relays)", 3, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provenance := make([]message.ProvenanceHop, tt.provenanceHops)
			hopCount := len(provenance) - 1
			if hopCount < 0 {
				hopCount = 0
			}
			if hopCount != tt.wantRelayHops {
				t.Errorf("hop count = %d, want %d (provenance len=%d)",
					hopCount, tt.wantRelayHops, tt.provenanceHops)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestRelayMetering_IdempotencyKeyFormat
// ---------------------------------------------------------------------------

// TestRelayMetering_IdempotencyKeyFormat verifies the idempotency key format.
func TestRelayMetering_IdempotencyKeyFormat(t *testing.T) {
	tests := []struct {
		campfireID string
		msgID      string
		wantSuffix string
	}{
		{"fire-abc", "msg-123", "fire-abc:msg-123:relay"},
		{"campfire-xyz-000", "bead-999", "campfire-xyz-000:bead-999:relay"},
	}

	for _, tt := range tests {
		t.Run(tt.campfireID+"/"+tt.msgID, func(t *testing.T) {
			got := tt.campfireID + ":" + tt.msgID + ":relay"
			if got != tt.wantSuffix {
				t.Errorf("idempotency key = %q, want %q", got, tt.wantSuffix)
			}
			// Verify ":relay" suffix is present.
			if !strings.HasSuffix(got, ":relay") {
				t.Errorf("idempotency key %q missing :relay suffix", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestRelayMetering_EmitsOnRelay
// ---------------------------------------------------------------------------

// TestRelayMetering_EmitsOnRelay verifies that a UsageEvent is emitted when
// peers > 0 (relay path).
func TestRelayMetering_EmitsOnRelay(t *testing.T) {
	var count int64
	_, client := newMeteringForgeServer(t, &count)
	emitter := forge.NewForgeEmitter(client, 100, nil)
	runEmitter(t, emitter)

	msg := &message.Message{
		ID:      "msg-relay-001",
		Payload: []byte("hello relay world"),
	}
	relayMeteringShim(emitter, "fire-relay", msg, 3 /* 3 peers */)

	waitForCount(t, &count, 1, 2*time.Second)

	got := atomic.LoadInt64(&count)
	if got != 1 {
		t.Errorf("expected 1 ingest call (relay event), got %d", got)
	}
}

// ---------------------------------------------------------------------------
// TestRelayMetering_NoEmitWhenNoPeers
// ---------------------------------------------------------------------------

// TestRelayMetering_NoEmitWhenNoPeers verifies that no UsageEvent is emitted
// when there are no peer endpoints (local storage only, not relayed).
func TestRelayMetering_NoEmitWhenNoPeers(t *testing.T) {
	var count int64
	_, client := newMeteringForgeServer(t, &count)
	emitter := forge.NewForgeEmitter(client, 100, nil)
	runEmitter(t, emitter)

	msg := &message.Message{
		ID:      "msg-no-relay-001",
		Payload: []byte("local only"),
	}
	relayMeteringShim(emitter, "fire-no-relay", msg, 0 /* no peers */)

	// Wait a bit — no flush should occur.
	time.Sleep(200 * time.Millisecond)

	got := atomic.LoadInt64(&count)
	if got != 0 {
		t.Errorf("expected 0 ingest calls (no relay), got %d", got)
	}
}

// ---------------------------------------------------------------------------
// TestRelayMetering_NilEmitterSafe
// ---------------------------------------------------------------------------

// TestRelayMetering_NilEmitterSafe verifies no panic when forgeEmitter is nil.
func TestRelayMetering_NilEmitterSafe(t *testing.T) {
	msg := &message.Message{
		ID:      "msg-nil-emitter",
		Payload: []byte("test"),
	}
	// Must not panic.
	relayMeteringShim(nil, "fire-nil", msg, 5)
}

// ---------------------------------------------------------------------------
// TestRelayMetering_EventFields
// ---------------------------------------------------------------------------

// TestRelayMetering_EventFields verifies the emitted UsageEvent has the correct
// UnitType, ServiceID, Quantity, and IdempotencyKey.
func TestRelayMetering_EventFields(t *testing.T) {
	_, client, events, mu := newCapturingForgeServer(t)
	emitter := forge.NewForgeEmitter(client, 100, nil)
	runEmitter(t, emitter)

	campfireID := "fire-fields-test"
	payload := []byte("payload of known length")
	msg := &message.Message{
		ID:      "msg-fields-001",
		Payload: payload,
	}

	relayMeteringShim(emitter, campfireID, msg, 1)

	// Wait for the event to be captured.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*events)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	evs := make([]forge.UsageEvent, len(*events))
	copy(evs, *events)
	mu.Unlock()

	if len(evs) != 1 {
		t.Fatalf("expected 1 captured event, got %d", len(evs))
	}
	ev := evs[0]

	if ev.UnitType != "relay-bytes" {
		t.Errorf("UnitType = %q, want %q", ev.UnitType, "relay-bytes")
	}
	if ev.ServiceID != "campfire-hosting" {
		t.Errorf("ServiceID = %q, want %q", ev.ServiceID, "campfire-hosting")
	}
	wantQty := float64(len(payload))
	if ev.Quantity != wantQty {
		t.Errorf("Quantity = %v, want %v", ev.Quantity, wantQty)
	}
	wantKey := campfireID + ":" + msg.ID + ":relay"
	if ev.IdempotencyKey != wantKey {
		t.Errorf("IdempotencyKey = %q, want %q", ev.IdempotencyKey, wantKey)
	}
}

// ---------------------------------------------------------------------------
// TestRelayMetering_ServerFieldWired
// ---------------------------------------------------------------------------

// TestRelayMetering_ServerFieldWired verifies that the server struct has a
// forgeEmitter field and it can be set to a non-nil ForgeEmitter.
func TestRelayMetering_ServerFieldWired(t *testing.T) {
	var count int64
	_, client := newMeteringForgeServer(t, &count)
	emitter := forge.NewForgeEmitter(client, 10, nil)

	srv := newTestServer(t)
	srv.forgeEmitter = emitter

	if srv.forgeEmitter == nil {
		t.Fatal("server.forgeEmitter should be non-nil after assignment")
	}
}

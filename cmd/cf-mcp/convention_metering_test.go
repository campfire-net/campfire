// convention_metering_test.go — Tests for M8: MeteringHook on ConventionDispatcher.
//
// Tests verify that:
//   - Tier 2 events emit a UsageEvent via the ForgeEmitter.
//   - Tier 1 events are skipped (no emission).
//   - IdempotencyKey format is "<serverID>:<messageID>".
//   - The hook is wired on the ConventionDispatcher via wireConventionMetering.
//   - wireConventionMetering is a no-op when emitter is nil.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/forge"
)

// ---------------------------------------------------------------------------
// TestConventionMetering_Tier2EmitsUsageEvent
// ---------------------------------------------------------------------------

// TestConventionMetering_Tier2EmitsUsageEvent verifies that a Tier 2 dispatch
// event causes a UsageEvent to be emitted to Forge.
func TestConventionMetering_Tier2EmitsUsageEvent(t *testing.T) {
	_, client, events, mu := newCapturingForgeServer(t)
	emitter := forge.NewForgeEmitter(client, 100, nil)
	runEmitter(t, emitter)

	hook := buildConventionMeteringHook(emitter)

	event := convention.ConventionMeterEvent{
		CampfireID:     "fire-abc",
		Convention:     "myconv",
		Operation:      "myop",
		Tier:           2,
		ServerID:       "server-xyz",
		ForgeAccountID: "acct-123",
		MessageID:      "msg-001",
		Status:         "dispatched",
	}

	hook(context.Background(), event)

	// Wait for the event to be delivered.
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
		t.Fatalf("expected 1 usage event, got %d", len(evs))
	}
	ev := evs[0]

	if ev.UnitType != "convention-op-tier2" {
		t.Errorf("UnitType = %q, want %q", ev.UnitType, "convention-op-tier2")
	}
	if ev.ServiceID != "campfire-hosting" {
		t.Errorf("ServiceID = %q, want %q", ev.ServiceID, "campfire-hosting")
	}
	if ev.AccountID != "acct-123" {
		t.Errorf("AccountID = %q, want %q", ev.AccountID, "acct-123")
	}
	wantKey := "server-xyz:msg-001"
	if ev.IdempotencyKey != wantKey {
		t.Errorf("IdempotencyKey = %q, want %q", ev.IdempotencyKey, wantKey)
	}
	if ev.Quantity != 1 {
		t.Errorf("Quantity = %v, want 1", ev.Quantity)
	}
}

// ---------------------------------------------------------------------------
// TestConventionMetering_Tier1Skipped
// ---------------------------------------------------------------------------

// TestConventionMetering_Tier1Skipped verifies that Tier 1 events are not
// emitted (they are free — no billing noise).
func TestConventionMetering_Tier1Skipped(t *testing.T) {
	_, client, capturedEvents, capturedMu := newCapturingForgeServer(t)
	emitter := forge.NewForgeEmitter(client, 100, nil)
	runEmitter(t, emitter)

	hook := buildConventionMeteringHook(emitter)

	event := convention.ConventionMeterEvent{
		CampfireID:     "fire-abc",
		Convention:     "myconv",
		Operation:      "myop",
		Tier:           1, // Tier 1 = free, must not emit
		ServerID:       "server-xyz",
		ForgeAccountID: "acct-123",
		MessageID:      "msg-002",
		Status:         "fulfilled",
	}

	hook(context.Background(), event)

	// Wait a bit — no event should be emitted.
	time.Sleep(200 * time.Millisecond)

	capturedMu.Lock()
	n := len(*capturedEvents)
	capturedMu.Unlock()

	if n != 0 {
		t.Errorf("expected 0 usage events for Tier 1, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// TestConventionMetering_IdempotencyKeyFormat
// ---------------------------------------------------------------------------

// TestConventionMetering_IdempotencyKeyFormat verifies the idempotency key is
// "<serverID>:<messageID>".
func TestConventionMetering_IdempotencyKeyFormat(t *testing.T) {
	tests := []struct {
		serverID  string
		messageID string
		wantKey   string
	}{
		{"server-abc", "msg-001", "server-abc:msg-001"},
		{"srv-xyz-000", "bead-999", "srv-xyz-000:bead-999"},
		{"my-server", "my-message-id", "my-server:my-message-id"},
	}

	for _, tt := range tests {
		t.Run(tt.serverID+"/"+tt.messageID, func(t *testing.T) {
			got := tt.serverID + ":" + tt.messageID
			if got != tt.wantKey {
				t.Errorf("idempotency key = %q, want %q", got, tt.wantKey)
			}
		})
	}

	// Also verify the hook produces the correct format via a round-trip test.
	_, client, events, mu := newCapturingForgeServer(t)
	emitter := forge.NewForgeEmitter(client, 100, nil)
	runEmitter(t, emitter)

	hook := buildConventionMeteringHook(emitter)
	hook(context.Background(), convention.ConventionMeterEvent{
		Tier:      2,
		ServerID:  "my-server",
		MessageID: "my-message-id",
		// ForgeAccountID is required for the hook to emit.
		ForgeAccountID: "acct-roundtrip",
	})

	// 5s deadline: ForgeEmitter batches for up to 1s before flushing. CI
	// scheduling jitter can push the flush to ~1.5s; 5s gives ample margin.
	deadline := time.Now().Add(5 * time.Second)
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
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if evs[0].IdempotencyKey != "my-server:my-message-id" {
		t.Errorf("IdempotencyKey = %q, want %q", evs[0].IdempotencyKey, "my-server:my-message-id")
	}
}

// ---------------------------------------------------------------------------
// TestConventionMetering_HookWiredOnDispatcher
// ---------------------------------------------------------------------------

// TestConventionMetering_HookWiredOnDispatcher verifies that wireConventionMetering
// sets MeteringHook on the ConventionDispatcher.
func TestConventionMetering_HookWiredOnDispatcher(t *testing.T) {
	var count int64
	_, client := newMeteringForgeServer(t, &count)
	emitter := forge.NewForgeEmitter(client, 100, nil)

	srv := newTestServer(t)
	srv.wireConventionMetering(emitter)

	if srv.conventionDispatcher == nil {
		t.Fatal("conventionDispatcher should be non-nil after wireConventionMetering")
	}
	if srv.conventionDispatcher.MeteringHook == nil {
		t.Fatal("MeteringHook should be non-nil on the ConventionDispatcher")
	}
}

// ---------------------------------------------------------------------------
// TestConventionMetering_NilEmitterNoOp
// ---------------------------------------------------------------------------

// TestConventionMetering_NilEmitterNoOp verifies that wireConventionMetering is
// a no-op when the ForgeEmitter is nil (development / stdio mode).
func TestConventionMetering_NilEmitterNoOp(t *testing.T) {
	srv := newTestServer(t)
	srv.wireConventionMetering(nil)

	if srv.conventionDispatcher != nil {
		t.Error("conventionDispatcher should be nil when wireConventionMetering is called with nil emitter")
	}
}

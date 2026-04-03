// convention_billing_sweep_test.go — Tests for BillingSweep wiring.
//
// Tests verify that:
//   - wireBillingSweep sets billingSweep on the server when emitter and
//     conventionDispatchStore are both non-nil.
//   - wireBillingSweep is a no-op when emitter is nil.
//   - wireBillingSweep is a no-op when conventionDispatchStore is nil (i.e.
//     wireConventionMetering was not called first).
//   - wireConventionMetering sets conventionDispatchStore (the shared store).
//   - The billing sweep correctly bills an unbilled dispatch record via Run.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/forge"
)

// ---------------------------------------------------------------------------
// TestBillingSweepWiring_WiredWhenEmitterAndStorePresent
// ---------------------------------------------------------------------------

// TestBillingSweepWiring_WiredWhenEmitterAndStorePresent verifies that calling
// wireConventionMetering followed by wireBillingSweep results in a non-nil
// billingSweep on the server.
func TestBillingSweepWiring_WiredWhenEmitterAndStorePresent(t *testing.T) {
	var count int64
	_, client := newMeteringForgeServer(t, &count)
	emitter := forge.NewForgeEmitter(client, 100, nil)

	srv := newTestServer(t)
	srv.wireConventionMetering(emitter)
	srv.wireBillingSweep(emitter)

	if srv.billingSweep == nil {
		t.Fatal("billingSweep should be non-nil after wireConventionMetering + wireBillingSweep")
	}
}

// ---------------------------------------------------------------------------
// TestBillingSweepWiring_NilEmitterNoOp
// ---------------------------------------------------------------------------

// TestBillingSweepWiring_NilEmitterNoOp verifies that wireBillingSweep is a
// no-op when the ForgeEmitter is nil.
func TestBillingSweepWiring_NilEmitterNoOp(t *testing.T) {
	var count int64
	_, client := newMeteringForgeServer(t, &count)
	emitter := forge.NewForgeEmitter(client, 100, nil)

	srv := newTestServer(t)
	srv.wireConventionMetering(emitter) // wires store
	srv.wireBillingSweep(nil)           // nil emitter — should no-op

	if srv.billingSweep != nil {
		t.Error("billingSweep should be nil when wireBillingSweep is called with nil emitter")
	}
}

// ---------------------------------------------------------------------------
// TestBillingSweepWiring_NilStoreNoOp
// ---------------------------------------------------------------------------

// TestBillingSweepWiring_NilStoreNoOp verifies that wireBillingSweep is a
// no-op when conventionDispatchStore is nil (wireConventionMetering not called).
func TestBillingSweepWiring_NilStoreNoOp(t *testing.T) {
	var count int64
	_, client := newMeteringForgeServer(t, &count)
	emitter := forge.NewForgeEmitter(client, 100, nil)

	srv := newTestServer(t)
	// Do NOT call wireConventionMetering — store remains nil.
	srv.wireBillingSweep(emitter)

	if srv.billingSweep != nil {
		t.Error("billingSweep should be nil when conventionDispatchStore is nil")
	}
}

// ---------------------------------------------------------------------------
// TestWireConventionMetering_SetsDispatchStore
// ---------------------------------------------------------------------------

// TestWireConventionMetering_SetsDispatchStore verifies that wireConventionMetering
// sets conventionDispatchStore on the server (the shared store for BillingSweep).
func TestWireConventionMetering_SetsDispatchStore(t *testing.T) {
	var count int64
	_, client := newMeteringForgeServer(t, &count)
	emitter := forge.NewForgeEmitter(client, 100, nil)

	srv := newTestServer(t)
	srv.wireConventionMetering(emitter)

	if srv.conventionDispatchStore == nil {
		t.Fatal("conventionDispatchStore should be non-nil after wireConventionMetering")
	}
}

// ---------------------------------------------------------------------------
// TestBillingSweepRun_BillsUnbilledRecord
// ---------------------------------------------------------------------------

// TestBillingSweepRun_BillsUnbilledRecord is an integration test verifying that,
// after wiring, the billing sweep emits a UsageEvent for an unbilled dispatch
// record (TokensConsumed > 0, BilledAt == 0).
//
// This test exercises the full wiring path: wireConventionMetering →
// wireBillingSweep → BillingSweep.Run(). The dispatch record is injected
// directly via the shared DispatchStore (type-asserted to MemoryDispatchStore
// to access SetTokensConsumed).
func TestBillingSweepRun_BillsUnbilledRecord(t *testing.T) {
	_, client, events, mu := newCapturingForgeServer(t)
	emitter := forge.NewForgeEmitter(client, 100, nil)
	runEmitter(t, emitter)

	srv := newTestServer(t)
	srv.wireConventionMetering(emitter)
	srv.wireBillingSweep(emitter)

	if srv.billingSweep == nil {
		t.Fatal("billingSweep not wired")
	}

	// Obtain the MemoryDispatchStore for direct record injection.
	mds, ok := srv.conventionDispatchStore.(*convention.MemoryDispatchStore)
	if !ok {
		t.Fatal("conventionDispatchStore is not *convention.MemoryDispatchStore")
	}

	ctx := context.Background()

	// Inject a dispatched → fulfilled record with token consumption.
	if _, err := mds.MarkDispatched(ctx, "cf-test", "msg-001", "server-xyz", "acct-test", "myconv", "myop"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}
	if err := mds.MarkFulfilled(ctx, "cf-test", "msg-001"); err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}
	mds.SetTokensConsumed(ctx, "cf-test", "msg-001", 42)

	// Run one billing sweep pass.
	billed, err := srv.billingSweep.Run(ctx)
	if err != nil {
		t.Fatalf("BillingSweep.Run: %v", err)
	}
	if billed != 1 {
		t.Fatalf("expected 1 billed record, got %d", billed)
	}

	// Wait for the ForgeEmitter to deliver the event.
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
		t.Fatalf("expected 1 usage event from billing sweep, got %d", len(evs))
	}
	ev := evs[0]
	if ev.UnitType != "convention-op-tier2-tokens" {
		t.Errorf("UnitType = %q, want %q", ev.UnitType, "convention-op-tier2-tokens")
	}
	if ev.AccountID != "acct-test" {
		t.Errorf("AccountID = %q, want %q", ev.AccountID, "acct-test")
	}
	if ev.Quantity != 42 {
		t.Errorf("Quantity = %v, want 42", ev.Quantity)
	}
	wantKey := "server-xyz:msg-001:tokens"
	if ev.IdempotencyKey != wantKey {
		t.Errorf("IdempotencyKey = %q, want %q", ev.IdempotencyKey, wantKey)
	}
}

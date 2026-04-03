// convention_metering.go — M8: MeteringHook wired on ConventionDispatcher.
//
// This file wires the convention metering hook into the ConventionDispatcher so
// that Tier 2 convention operation dispatches emit UsageEvents to Forge via the
// ForgeEmitter. Tier 1 operations are free — no event is emitted.
//
// Architecture:
//   - The hook is set on server.conventionDispatcher at startup (when forgeEmitter is non-nil).
//   - The dispatcher is created with a MemoryDispatchStore (in-process deduplication).
//   - Tier 2 hook fires after dispatch; idempotency key = serverID + ":" + messageID.
//
// Operator key sessions (forge-tk-):
//   - When a session authenticated via forge-tk- dispatches a convention operation,
//     metering is attributed to the operator's Forge account rather than the
//     convention server's account.
//   - The session's Forge account ID is threaded via context using sessionForgeAccountKey.
//   - Callers (handleSend) must use WithSessionForgeAccount to inject the account.
package main

import (
	"context"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/forge"
)

// sessionForgeAccountKey is the context key used to thread the operator's Forge
// account ID through convention dispatch for forge-tk- authenticated sessions.
// Set via WithSessionForgeAccount; read by buildConventionMeteringHook.
type sessionForgeAccountKey struct{}

// WithSessionForgeAccount returns a derived context that carries the operator's
// Forge account ID. When present, the convention metering hook bills this account
// instead of the convention server's account (event.ForgeAccountID).
func WithSessionForgeAccount(ctx context.Context, accountID string) context.Context {
	if accountID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionForgeAccountKey{}, accountID)
}

// sessionForgeAccountFromContext extracts the operator account ID injected by
// WithSessionForgeAccount. Returns "" when not set (normal session path).
func sessionForgeAccountFromContext(ctx context.Context) string {
	v, _ := ctx.Value(sessionForgeAccountKey{}).(string)
	return v
}

// buildConventionMeteringHook constructs a MeteringHook that emits a UsageEvent
// to the given ForgeEmitter for each Tier 2 convention operation dispatch.
// Tier 1 operations are free — the hook returns immediately without emitting.
//
// IdempotencyKey format: "<serverID>:<messageID>"
// UnitType: "convention-op-tier2"
// ServiceID: "campfire-hosting"
//
// For forge-tk- sessions the context carries a session-level Forge account ID
// (via WithSessionForgeAccount). When present, that account is billed directly
// rather than the convention server's account (event.ForgeAccountID).
//
// The emitter is fail-open (async, buffered). This function never blocks.
func buildConventionMeteringHook(emitter *forge.ForgeEmitter) convention.MeteringHook {
	return func(ctx context.Context, event convention.ConventionMeterEvent) {
		if event.Tier == 1 {
			// Tier 1 ops are free — no billing event.
			return
		}
		// For operator key sessions (forge-tk-), bill the operator's Forge account
		// directly. Falls back to the convention server's account for normal sessions.
		accountID := event.ForgeAccountID
		if sessionAcct := sessionForgeAccountFromContext(ctx); sessionAcct != "" {
			accountID = sessionAcct
		}
		if accountID == "" {
			// No account to bill — skip emission rather than sending a malformed
			// event with an empty AccountID that Forge would drop or misattribute.
			return
		}
		emitter.Emit(forge.UsageEvent{
			AccountID:      accountID,
			ServiceID:      "campfire-hosting",
			UnitType:       "convention-op-tier2",
			Quantity:       1,
			IdempotencyKey: event.ServerID + ":" + event.MessageID,
			Timestamp:      time.Now(),
		})
	}
}

// wireConventionMetering creates a ConventionDispatcher with a MeteringHook
// backed by the given ForgeEmitter and sets it on the server.
// If emitter is nil, no dispatcher is wired and convention metering is disabled.
//
// Also saves the DispatchStore on s.conventionDispatchStore so wireBillingSweep
// can share the same store (dispatch records must be visible to both).
//
// Call at server startup after the ForgeEmitter is initialized.
func (s *server) wireConventionMetering(emitter *forge.ForgeEmitter) {
	if emitter == nil {
		return
	}
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)
	d.MeteringHook = buildConventionMeteringHook(emitter)
	s.conventionDispatcher = d
	s.conventionDispatchStore = ds
	s.fallbackSweep = convention.NewSweeper(d, ds, nil)
}

// wireBillingSweep creates a BillingSweep from the shared conventionDispatchStore
// and sets it on the server. The sweep runs periodically via startSweepLoop.
//
// Must be called after wireConventionMetering (requires s.conventionDispatchStore).
// If the dispatch store or emitter is nil, the sweep is not wired.
func (s *server) wireBillingSweep(emitter *forge.ForgeEmitter) {
	if s.conventionDispatchStore == nil || emitter == nil {
		return
	}
	s.billingSweep = convention.NewBillingSweep(s.conventionDispatchStore, emitter, nil)
}

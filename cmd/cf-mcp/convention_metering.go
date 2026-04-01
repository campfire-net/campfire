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
package main

import (
	"context"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/forge"
)

// buildConventionMeteringHook constructs a MeteringHook that emits a UsageEvent
// to the given ForgeEmitter for each Tier 2 convention operation dispatch.
// Tier 1 operations are free — the hook returns immediately without emitting.
//
// IdempotencyKey format: "<serverID>:<messageID>"
// UnitType: "convention-op-tier2"
// ServiceID: "campfire-hosting"
//
// The emitter is fail-open (async, buffered). This function never blocks.
func buildConventionMeteringHook(emitter *forge.ForgeEmitter) convention.MeteringHook {
	return func(ctx context.Context, event convention.ConventionMeterEvent) {
		if event.Tier == 1 {
			// Tier 1 ops are free — no billing event.
			return
		}
		emitter.Emit(forge.UsageEvent{
			AccountID:      event.ForgeAccountID, // convention server's account pays
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
// Call at server startup after the ForgeEmitter is initialized.
func (s *server) wireConventionMetering(emitter *forge.ForgeEmitter) {
	if emitter == nil {
		return
	}
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)
	d.MeteringHook = buildConventionMeteringHook(emitter)
	s.conventionDispatcher = d
}

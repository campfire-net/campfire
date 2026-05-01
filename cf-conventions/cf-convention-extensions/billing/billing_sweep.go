// Package billing provides L3 applied-convention billing sweep for campfire
// convention dispatches. This is a Stage 2 placeholder; Stage 3 will add
// the full integration with Forge billing.
//
// Moved from pkg/convention/ in campfireagent-4d7.
package billing

import (
	"context"
	"fmt"
	"log"
	"time"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/pkg/forge"
)

const conventionServiceID = "campfire-hosting"

// BillingSweep processes fulfilled convention dispatches that have self-reported
// token consumption (TokensConsumed > 0) but haven't been billed yet (BilledAt == 0).
// It emits UsageEvents to Forge and marks records as billed to prevent double-billing.
//
// The billing sweep piggybacks on the same periodic timer as the fallback sweep.
// Run it after Sweeper.Run() in the same timer Function.
type BillingSweep struct {
	store   convention.DispatchStore
	emitter *forge.ForgeEmitter
	logger  *log.Logger
}

// NewBillingSweep creates a BillingSweep. store and emitter must be non-nil.
// If logger is nil, the default logger is used.
func NewBillingSweep(store convention.DispatchStore, emitter *forge.ForgeEmitter, logger *log.Logger) *BillingSweep {
	if logger == nil {
		logger = log.Default()
	}
	return &BillingSweep{
		store:   store,
		emitter: emitter,
		logger:  logger,
	}
}

// Run executes one billing sweep pass.
//
// For each fulfilled dispatch record where TokensConsumed > 0 and BilledAt == 0:
//   - Emits a UsageEvent{UnitType: "convention-op-tier2-tokens", Quantity: TokensConsumed}
//   - Marks the record as billed via MarkBilled
//
// Returns the number of records billed and any terminal error. Per-record errors
// are logged but do not abort the sweep (fail-open: revenue leakage, not data loss).
func (bs *BillingSweep) Run(ctx context.Context) (billed int, err error) {
	records, err := bs.store.ListUnbilledDispatches(ctx)
	if err != nil {
		return 0, fmt.Errorf("convention billing sweep: ListUnbilledDispatches: %w", err)
	}

	for _, rec := range records {
		idempotencyKey := rec.ServerID + ":" + rec.MessageID + ":tokens"

		event := forge.UsageEvent{
			AccountID:      rec.ForgeAccountID,
			ServiceID:      conventionServiceID,
			UnitType:       "convention-op-tier2-tokens",
			Quantity:       float64(rec.TokensConsumed),
			IdempotencyKey: idempotencyKey,
			Timestamp:      time.Now(),
		}
		bs.emitter.Emit(event)

		if markErr := bs.store.MarkBilled(ctx, rec.CampfireID, rec.MessageID, rec.ETag); markErr != nil {
			bs.logger.Printf("convention billing sweep: MarkBilled(%s/%s): %v",
				rec.CampfireID, rec.MessageID, markErr)
			// Continue — the idempotency key prevents double-billing on next sweep.
			continue
		}

		bs.logger.Printf("convention billing sweep: billed %d tokens for %s/%s (key=%s)",
			rec.TokensConsumed, rec.CampfireID, rec.MessageID, idempotencyKey)
		billed++
	}

	return billed, nil
}

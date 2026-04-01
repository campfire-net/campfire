package convention

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

const (
	// MaxRedispatches is the maximum number of times the sweep will re-dispatch
	// a single message. Prevents infinite loops from crash-looping handlers.
	MaxRedispatches = 3

	// SweepStaleThreshold is the minimum age of a "dispatched" record before the
	// sweep considers it orphaned and eligible for re-dispatch.
	SweepStaleThreshold = 5 * time.Minute

	// SweepCleanupMaxAge is the age at which fulfilled/failed dispatch records are
	// garbage-collected by the sweep.
	SweepCleanupMaxAge = 24 * time.Hour
)

// Sweeper runs the fallback sweep for orphaned convention dispatches. In steady
// state it finds nothing. When the primary dispatch path fails (crash, timeout),
// the sweep catches and re-dispatches missed messages up to MaxRedispatches times.
//
// Usage:
//
//	sw := convention.NewSweeper(dispatcher, store, logger)
//	redispatched, err := sw.Run(ctx)
type Sweeper struct {
	dispatcher *ConventionDispatcher
	store      DispatchStore
	logger     *log.Logger
}

// NewSweeper creates a Sweeper backed by the given dispatcher and store.
// If logger is nil, the default logger is used.
func NewSweeper(dispatcher *ConventionDispatcher, store DispatchStore, logger *log.Logger) *Sweeper {
	if logger == nil {
		logger = log.Default()
	}
	return &Sweeper{
		dispatcher: dispatcher,
		store:      store,
		logger:     logger,
	}
}

// Run executes one sweep pass using the default SweepStaleThreshold and
// SweepCleanupMaxAge constants.
//
// Returns the number of re-dispatches initiated and any terminal error. Per-message
// re-dispatch errors are logged but do not abort the sweep.
func (sw *Sweeper) Run(ctx context.Context) (int, error) {
	return sw.RunWithThreshold(ctx, SweepStaleThreshold)
}

// RunWithThreshold executes one sweep pass with a custom stale threshold. It finds
// stale dispatch records (dispatched but not fulfilled for longer than staleThreshold),
// re-dispatches them up to MaxRedispatches times, and garbage-collects old
// fulfilled/failed records using SweepCleanupMaxAge.
//
// Returns the number of re-dispatches initiated and any terminal error.
func (sw *Sweeper) RunWithThreshold(ctx context.Context, staleThreshold time.Duration) (int, error) {
	stale, err := sw.store.ListStaleDispatches(ctx, staleThreshold)
	if err != nil {
		return 0, err
	}

	redispatched := 0
	for _, rec := range stale {
		// Increment and check the re-dispatch cap before attempting re-dispatch.
		newCount, err := sw.store.IncrementRedispatchCount(ctx, rec.CampfireID, rec.MessageID)
		if err != nil {
			sw.logger.Printf("convention sweep: IncrementRedispatchCount(%s/%s): %v", rec.CampfireID, rec.MessageID, err)
			continue
		}
		if newCount > MaxRedispatches {
			// Cap exceeded — mark as failed so it stops appearing as stale
			// and will be cleaned up by CleanupOldDispatches.
			sw.logger.Printf("convention sweep: message %s/%s exceeded max re-dispatches (%d), marking failed",
				rec.CampfireID, rec.MessageID, MaxRedispatches)
			if markErr := sw.store.MarkFailed(ctx, rec.CampfireID, rec.MessageID); markErr != nil {
				sw.logger.Printf("convention sweep: MarkFailed(%s/%s): %v", rec.CampfireID, rec.MessageID, markErr)
			}
			continue
		}

		sw.logger.Printf("convention sweep: re-dispatching message %s/%s (attempt %d/%d)",
			rec.CampfireID, rec.MessageID, newCount, MaxRedispatches)

		if sw.redispatch(ctx, rec) {
			redispatched++
		}
	}

	// Garbage-collect old fulfilled/failed records.
	removed, err := sw.store.CleanupOldDispatches(ctx, SweepCleanupMaxAge)
	if err != nil {
		sw.logger.Printf("convention sweep: CleanupOldDispatches: %v", err)
		return redispatched, err
	}
	if removed > 0 {
		sw.logger.Printf("convention sweep: cleaned up %d old dispatch records", removed)
	}

	return redispatched, nil
}

// redispatch re-dispatches a stale orphaned message by looking up the registered
// handler and calling the handler goroutine directly, bypassing the MarkDispatched
// deduplication check. The RedispatchCount in the store tracks re-dispatch attempts.
//
// Returns true if re-dispatch was initiated (handler found and goroutine spawned).
func (sw *Sweeper) redispatch(ctx context.Context, rec DispatchRecord) bool {
	// Look up the registered handler for this (campfireID, convention, operation).
	sw.dispatcher.mu.RLock()
	entry, ok := sw.dispatcher.registry[conventionKey{
		CampfireID: rec.CampfireID,
		Convention: rec.Convention,
		Operation:  rec.Operation,
	}]
	sw.dispatcher.mu.RUnlock()
	if !ok {
		sw.logger.Printf("convention sweep: no handler for %s/%s/%s, skipping",
			rec.CampfireID, rec.Convention, rec.Operation)
		return false
	}

	// Reconstruct a minimal MessageRecord from stored dispatch metadata.
	// Convention and Operation are stored in DispatchRecord at MarkDispatched time.
	payload, err := json.Marshal(conventionOpPayload{
		Convention: rec.Convention,
		Operation:  rec.Operation,
	})
	if err != nil {
		sw.logger.Printf("convention sweep: marshal payload for %s/%s: %v", rec.CampfireID, rec.MessageID, err)
		return false
	}

	msg := &store.MessageRecord{
		ID:         rec.MessageID,
		CampfireID: rec.CampfireID,
		Sender:     rec.ServerID,
		Payload:    payload,
		Tags:       []string{rec.Convention + ":" + rec.Operation},
		Timestamp:  rec.DispatchedAt.UnixNano(),
	}

	op := conventionOpPayload{
		Convention: rec.Convention,
		Operation:  rec.Operation,
	}

	// Call invokeHandler() directly (package-internal), bypassing MarkDispatched
	// deduplication. The sweep tracks re-dispatch attempts via RedispatchCount in
	// the store — the existing dispatch record is preserved with its counter intact.
	//
	// invokeHandler is non-blocking when called via go — the next sweep pass
	// will observe the updated status if the handler completes successfully.
	go sw.dispatcher.invokeHandler(ctx, rec.CampfireID, msg, op, entry)
	return true
}

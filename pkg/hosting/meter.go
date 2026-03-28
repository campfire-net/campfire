package hosting

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
)

// Ingester abstracts forge.Client.Ingest for testing.
type Ingester interface {
	Ingest(ctx context.Context, event forge.UsageEvent) error
}

// UsageEmitter batches per-campfire message counts and emits hourly
// per-operator UsageEvents to Forge's ingest endpoint.
//
// Usage:
//
//	e := hosting.NewUsageEmitter(forgeClient, time.Hour)
//	e.Register("campfire-id", "operator-account-id")
//	go e.Start(ctx)
//	// ... on each message:
//	e.RecordMessage("campfire-id", "operator-account-id")
//	// ... on shutdown:
//	e.Stop()
//
// UsageEmitter is safe for concurrent use.
type UsageEmitter struct {
	ingester Ingester
	interval time.Duration
	now      func() time.Time

	mu     sync.Mutex
	counts map[string]int64 // operatorAccountID → running message count

	stopCh chan struct{}
	doneCh chan struct{}

	// OnError is called when Ingest fails for an operator. Optional.
	OnError func(operatorAccountID string, err error)
}

// NewUsageEmitter creates a UsageEmitter. interval is typically time.Hour.
// Providing a shorter interval (e.g. in tests) overrides wall-clock alignment.
func NewUsageEmitter(ingester Ingester, interval time.Duration) *UsageEmitter {
	return &UsageEmitter{
		ingester: ingester,
		interval: interval,
		now:      func() time.Time { return time.Now().UTC() },
		counts:   make(map[string]int64),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// RecordMessage increments the message count for operatorAccountID by 1.
// campfireID is accepted for API symmetry but the rollup is per-operator;
// all campfires belonging to the same operator are aggregated together.
func (e *UsageEmitter) RecordMessage(campfireID, operatorAccountID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.counts[operatorAccountID]++
}

// Start runs the emission loop, firing once per interval. It blocks until
// ctx is cancelled or Stop is called.
func (e *UsageEmitter) Start(ctx context.Context) {
	defer close(e.doneCh)
	for {
		wait := e.nextTick()
		select {
		case <-ctx.Done():
			e.emit(ctx)
			return
		case <-e.stopCh:
			e.emit(ctx)
			return
		case <-time.After(wait):
			e.emit(ctx)
		}
	}
}

// Stop signals the emission loop to flush any pending counts and exit.
// It blocks until the final batch has been sent.
func (e *UsageEmitter) Stop() {
	close(e.stopCh)
	<-e.doneCh
}

// nextTick returns how long to wait before the next emission.
// When interval equals time.Hour (production), it aligns to the top of the
// next wall-clock hour. Otherwise (tests) it returns the raw interval.
func (e *UsageEmitter) nextTick() time.Duration {
	if e.interval == time.Hour {
		now := e.now()
		next := now.Truncate(time.Hour).Add(time.Hour)
		return next.Sub(now)
	}
	return e.interval
}

// hourBucket returns the Unix timestamp of the top of the most recently
// completed hour (i.e. the start of the current hour window).
func (e *UsageEmitter) hourBucket() time.Time {
	return e.now().Truncate(time.Hour)
}

// snapshot atomically drains and returns the current per-operator counts.
func (e *UsageEmitter) snapshot() map[string]int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	snap := make(map[string]int64, len(e.counts))
	for op, n := range e.counts {
		if n > 0 {
			snap[op] = n
		}
	}
	e.counts = make(map[string]int64)
	return snap
}

// emit sends one UsageEvent per operator for the current hour bucket.
func (e *UsageEmitter) emit(ctx context.Context) {
	snap := e.snapshot()
	if len(snap) == 0 {
		return
	}
	bucket := e.hourBucket()
	for operatorID, count := range snap {
		event := forge.UsageEvent{
			ServiceID:      "campfire-hosting",
			AccountID:      operatorID,
			UnitType:       "message",
			Quantity:       float64(count),
			IdempotencyKey: fmt.Sprintf("%s/%d", operatorID, bucket.Unix()),
			Timestamp:      bucket,
		}
		if err := e.ingester.Ingest(ctx, event); err != nil {
			if e.OnError != nil {
				e.OnError(operatorID, err)
			}
		}
	}
}

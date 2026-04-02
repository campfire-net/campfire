package forge

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	defaultBufferSize = 1000
	batchSize         = 50
	batchTimeout      = time.Second
)

// ForgeEmitter accepts UsageEvents on a buffered channel and POSTs them to
// Forge in batches via a background goroutine. The emitter is fail-open: if
// Forge is unreachable, events are logged and dropped (revenue leakage, not
// data loss — the message is already stored).
type ForgeEmitter struct {
	client  *Client        // Forge client for Ingest calls
	events  chan UsageEvent // buffered, capacity defaultBufferSize
	done    chan struct{}   // closed when Run() exits
	onError func(error)    // optional error callback (for logging)

	mu     sync.Mutex         // guards cancel
	cancel context.CancelFunc // set by Run; called by DrainAndClose to stop the run loop
}

// NewForgeEmitter creates an emitter. client must be non-nil.
// bufferSize controls the channel capacity (default 1000 if <= 0).
// onError is called on Forge Ingest errors; may be nil.
func NewForgeEmitter(client *Client, bufferSize int, onError func(error)) *ForgeEmitter {
	if bufferSize <= 0 {
		bufferSize = defaultBufferSize
	}
	return &ForgeEmitter{
		client:  client,
		events:  make(chan UsageEvent, bufferSize),
		done:    make(chan struct{}),
		onError: onError,
	}
}

// Emit enqueues a UsageEvent for async delivery. Non-blocking.
// If the channel is full, the event is dropped and logged.
func (e *ForgeEmitter) Emit(event UsageEvent) {
	select {
	case e.events <- event:
	default:
		log.Printf("forge: emitter channel full, dropping event account=%s idempotency_key=%s",
			event.AccountID, event.IdempotencyKey)
	}
}

// Run processes events from the channel until ctx is cancelled.
// Batches: collects up to 50 events or waits 1 second, whichever comes first.
// POSTs each event individually (Forge ingest is single-event API).
// On error: calls onError if set, then continues (fail-open).
// Call this in a goroutine: go emitter.Run(ctx)
func (e *ForgeEmitter) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.cancel = cancel
	e.mu.Unlock()
	defer close(e.done)

	timer := time.NewTimer(batchTimeout)
	defer timer.Stop()

	batch := make([]UsageEvent, 0, batchSize)

	// flushCtx is the context to use for Ingest calls. Using a background
	// context for the drain path ensures events sent before shutdown are
	// delivered even after ctx is cancelled.
	flush := func(ingestCtx context.Context) {
		for _, ev := range batch {
			if err := e.client.Ingest(ingestCtx, ev); err != nil {
				if e.onError != nil {
					e.onError(err)
				}
			}
		}
		batch = batch[:0]
	}

	for {
		select {
		case ev := <-e.events:
			batch = append(batch, ev)
			if len(batch) >= batchSize {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				flush(ctx)
				timer.Reset(batchTimeout)
			}
		case <-timer.C:
			if len(batch) > 0 {
				flush(ctx)
			}
			timer.Reset(batchTimeout)
		case <-ctx.Done():
			// Drain remaining events best-effort using a fresh background context
			// so that in-flight and queued events can still be delivered after
			// the caller's context is cancelled.
			drainCtx := context.Background()
		drain:
			for {
				select {
				case ev := <-e.events:
					batch = append(batch, ev)
					if len(batch) >= batchSize {
						flush(drainCtx)
					}
				default:
					break drain
				}
			}
			if len(batch) > 0 {
				flush(drainCtx)
			}
			return
		}
	}
}

// DrainAndClose cancels the run-loop context (triggering a drain of any
// buffered events), then waits for Run() to exit. Safe to call regardless of
// whether the caller's context was already cancelled — DrainAndClose cancels
// it internally if needed.
//
// The provided timeout bounds how long this call blocks waiting for Run to exit.
func (e *ForgeEmitter) DrainAndClose(timeout time.Duration) {
	e.mu.Lock()
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	select {
	case <-e.done:
		// Run exited and drained buffered events.
	case <-time.After(timeout):
	}
}

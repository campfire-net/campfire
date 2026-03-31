package protocol

// bridge.go — protocol.Bridge pumps messages between two Clients with dedup.
//
// Covered bead: campfire-agent-utj
//
// Bridge is a standalone function (not a Client method) that subscribes to one
// or both Clients and forwards messages to the other side. Deduplication by
// message ID prevents loops in bidirectional mode.

import (
	"context"
	"sync"

	"github.com/campfire-net/campfire/pkg/campfire"
)

// BridgeOptions configures the bridge behavior.
type BridgeOptions struct {
	// Bidirectional enables two-way pumping (source→dest AND dest→source).
	Bidirectional bool

	// TagFilter restricts bridging to messages carrying at least one of these tags.
	// Empty means all messages are bridged.
	TagFilter []string

	// OnMessage is an optional callback invoked for each bridged message.
	// direction is "source→dest" or "dest→source".
	OnMessage func(msg *Message, direction string)
}

// dedupMap is a bounded set of seen message IDs. When it exceeds maxEntries,
// it clears and starts fresh (simple strategy, no LRU).
type dedupMap struct {
	mu         sync.Mutex
	seen       map[string]bool
	maxEntries int
}

func newDedupMap(max int) *dedupMap {
	return &dedupMap{
		seen:       make(map[string]bool),
		maxEntries: max,
	}
}

// add returns true if the ID was already seen (duplicate). Otherwise marks it seen.
func (d *dedupMap) add(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen[id] {
		return true
	}
	if len(d.seen) >= d.maxEntries {
		d.seen = make(map[string]bool)
	}
	d.seen[id] = true
	return false
}

// Bridge pumps messages from source to dest for the given campfireID.
// It subscribes to source, and for each new message, sends it to dest.
// If Bidirectional is true, also subscribes to dest and sends to source.
// Deduplicates by message ID to prevent loops in bidirectional mode.
// Returns when ctx is cancelled. Returns ctx.Err() on clean shutdown.
func Bridge(ctx context.Context, source, dest *Client, campfireID string, opts BridgeOptions) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	dedup := newDedupMap(10000)

	// pumpOne subscribes to `from` and sends each message to `to`.
	// direction is a label for the OnMessage callback.
	pumpOne := func(from, to *Client, direction string) error {
		sub := from.Subscribe(ctx, SubscribeRequest{
			CampfireID: campfireID,
			Tags:       opts.TagFilter,
		})

		for {
			select {
			case msg, ok := <-sub.Messages():
				if !ok {
					// Channel closed — check for subscription error.
					if err := sub.Err(); err != nil {
						return err
					}
					return ctx.Err()
				}
				if dedup.add(msg.ID) {
					continue // already seen — skip to prevent loops
				}

				// Forward the message with blind-relay role so IsBridged() returns true.
				// This marks the forwarded message as having passed through a bridge
				// transport, per Operator Provenance Convention v0.1 §4.2 (Level 2).
				sent, err := to.Send(SendRequest{
					CampfireID:   campfireID,
					Payload:      msg.Payload,
					Tags:         msg.Tags,
					RoleOverride: campfire.RoleBlindRelay,
				})
				if err != nil {
					return err
				}
				// Mark the sent message ID as seen too, so it won't loop back.
				dedup.add(sent.ID)

				if opts.OnMessage != nil {
					opts.OnMessage(&msg, direction)
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	if !opts.Bidirectional {
		return pumpOne(source, dest, "source→dest")
	}

	// Bidirectional: run two pumps, first error or ctx cancel stops both.
	errCh := make(chan error, 2)

	go func() {
		errCh <- pumpOne(source, dest, "source→dest")
	}()
	go func() {
		errCh <- pumpOne(dest, source, "dest→source")
	}()

	// Wait for the first error (or context cancellation propagated through a pump).
	err := <-errCh
	cancel() // stop the other pump
	<-errCh  // join second goroutine to prevent leak
	return err
}

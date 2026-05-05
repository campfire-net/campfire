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

	"github.com/campfire-net/campfire/cf-protocol/internal/campfire"
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

	// Forward enables pass-through mode. When true, the bridge writes the
	// original message envelope to the destination transport unchanged and
	// appends exactly one provenance hop signed by the bridging campfire with
	// Role = campfire.RoleBlindRelay. The message ID, Sender, Signature,
	// Payload, Tags, Antecedents, and existing Provenance are preserved —
	// original-author cryptographic attribution survives end-to-end.
	//
	// When false (the default), the bridge re-publishes: it calls dest.Send()
	// with the source message's payload and tags, producing a fresh message ID
	// and Sender attributed to the bridge agent. Original-author provenance
	// is NOT preserved in re-publish mode.
	//
	// Use Forward: true whenever the upstream and downstream sides share a
	// signature scheme — e.g. hosted-reader (Tier 3.5), multi-region
	// mirroring, or any cf→cf relay where original-author trust must reach
	// the receiver.
	Forward bool
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
	// senderKey, when non-empty, restricts forwarding to messages whose Sender
	// matches the given hex pubkey. This is used in bidirectional mode to prevent
	// a race where both pumps subscribe to the same campfire and compete to process
	// messages: without sender filtering, the pump that polls first wins, causing
	// the wrong direction label in OnMessage callbacks and spurious dedup skips.
	// In bidirectional mode each pump is bound to the originating side's key
	// (source pump: senderKey = source.PublicKeyHex(); dest pump: senderKey =
	// dest.PublicKeyHex()), making direction assignment deterministic regardless
	// of poll timing.
	pumpOne := func(from, to *Client, direction, senderKey string) error {
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

				// In bidirectional mode, only forward messages that originated
				// from the `from` side. Both pumps share the same campfire and
				// can see each other's messages; without this guard whichever
				// pump polls first claims a message, producing wrong direction
				// labels and starving the other pump.
				if senderKey != "" && msg.Sender != senderKey {
					continue
				}

				if dedup.add(msg.ID) {
					continue // already seen — skip to prevent loops
				}

				// Deliver the message to the destination.
				// In pass-through mode (opts.Forward): write the original envelope
				// unchanged, appending one blind-relay hop signed by the destination
				// campfire. Message ID, Sender, and Signature are preserved.
				// In re-publish mode (default): call Send() which produces a new
				// message attributed to the bridge agent.
				var sentID string
				if opts.Forward {
					forwarded, fwdErr := to.forwardMessage(campfireID, &msg)
					if fwdErr != nil {
						return fwdErr
					}
					sentID = forwarded.ID
				} else {
					sent, sendErr := to.Send(SendRequest{
						CampfireID:   campfireID,
						Payload:      msg.Payload,
						Tags:         msg.Tags,
						RoleOverride: campfire.RoleBlindRelay,
					})
					if sendErr != nil {
						return sendErr
					}
					sentID = sent.ID
				}
				// Mark the delivered message ID as seen so it won't loop back.
				dedup.add(sentID)

				if opts.OnMessage != nil {
					opts.OnMessage(&msg, direction)
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	if !opts.Bidirectional {
		return pumpOne(source, dest, "source→dest", "")
	}

	// Bidirectional: run two pumps, first error or ctx cancel stops both.
	// Each pump is bound to messages originating from its own side so they
	// don't race over the same message.
	srcKey := source.PublicKeyHex()
	dstKey := dest.PublicKeyHex()

	errCh := make(chan error, 2)

	go func() {
		errCh <- pumpOne(source, dest, "source→dest", srcKey)
	}()
	go func() {
		errCh <- pumpOne(dest, source, "dest→source", dstKey)
	}()

	// Wait for the first error (or context cancellation propagated through a pump).
	err := <-errCh
	cancel() // stop the other pump
	<-errCh  // join second goroutine to prevent leak
	return err
}

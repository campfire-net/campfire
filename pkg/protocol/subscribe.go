package protocol

import (
	"context"
	"sync"
	"time"
)

// SubscribeRequest specifies the parameters for a Subscribe operation.
type SubscribeRequest struct {
	// CampfireID is the campfire to subscribe to. Required.
	CampfireID string

	// AfterTimestamp returns only messages with timestamp > AfterTimestamp.
	// When 0, all existing messages are returned first, then new ones are streamed.
	AfterTimestamp int64

	// PollInterval is the time between store polls. Defaults to 500ms when zero.
	PollInterval time.Duration

	// Tags filters messages to those carrying at least one of the listed exact tags.
	// Nil or empty means no tag filtering.
	Tags []string

	// TagPrefixes filters messages to those carrying a tag starting with any prefix.
	TagPrefixes []string

	// ExcludeTags excludes messages carrying any of the listed exact tags.
	ExcludeTags []string

	// ExcludeTagPrefixes excludes messages carrying a tag starting with any prefix.
	ExcludeTagPrefixes []string
}

// Subscription is a live view of a campfire that delivers messages as they arrive.
// It is created by Client.Subscribe and must be cancelled via the context passed
// to Subscribe when the caller is done.
//
// Subscription is safe for concurrent reads from Messages() and Err().
type Subscription struct {
	msgs chan Message
	err  error
	mu   sync.Mutex
	done chan struct{}
}

// Messages returns the channel on which incoming messages are delivered.
// The channel is closed when the subscription ends (context cancelled or error).
func (sub *Subscription) Messages() <-chan Message {
	return sub.msgs
}

// Err returns the error that caused the subscription to end, if any.
// Returns nil if the subscription ended cleanly (context cancelled).
// Safe to call after Messages() is closed.
func (sub *Subscription) Err() error {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	return sub.err
}

// setErr records the terminal error (protected by mu).
func (sub *Subscription) setErr(err error) {
	sub.mu.Lock()
	sub.err = err
	sub.mu.Unlock()
}

// Subscribe starts a streaming subscription to a campfire. It returns a
// *Subscription immediately; the background goroutine polls via Client.Read()
// with proper cursor advancement (AfterTimestamp from ReadResult.MaxTimestamp).
//
// The Messages() channel delivers protocol.Message values as they arrive.
// When the context is cancelled, the channel is closed and the goroutine exits.
//
// If the underlying Read() encounters an error, the error is surfaced via
// Subscription.Err() and the channel is closed.
//
// Poll interval defaults to 500ms when not set in the request.
func (c *Client) Subscribe(ctx context.Context, req SubscribeRequest) *Subscription {
	interval := req.PollInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}

	sub := &Subscription{
		// Buffered channel: holds up to 64 messages to absorb bursts without blocking polls.
		msgs: make(chan Message, 64),
		done: make(chan struct{}),
	}

	go func() {
		defer close(sub.msgs)
		defer close(sub.done)

		cursor := req.AfterTimestamp

		for {
			// Check for cancellation before each poll.
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Sync explicitly so transport errors are visible to the subscription.
			// Read() swallows sync errors for resilience in one-shot reads; here
			// we need to surface them so the subscription can terminate cleanly
			// when the transport becomes permanently unavailable (e.g. dir deleted).
			if err := c.syncIfFilesystem(req.CampfireID); err != nil {
				sub.setErr(err)
				return
			}

			result, err := c.Read(ReadRequest{
				CampfireID:         req.CampfireID,
				AfterTimestamp:     cursor,
				Tags:               req.Tags,
				TagPrefixes:        req.TagPrefixes,
				ExcludeTags:        req.ExcludeTags,
				ExcludeTagPrefixes: req.ExcludeTagPrefixes,
				IncludeCompacted:   false,
				SkipSync:           true, // already synced above
			})
			if err != nil {
				sub.setErr(err)
				return
			}

			// Advance cursor regardless of whether messages were filtered out,
			// so filtered messages do not re-appear on the next poll.
			if result.MaxTimestamp > cursor {
				cursor = result.MaxTimestamp
			}

			// Deliver each message. Respect context cancellation mid-delivery.
			for _, msg := range result.Messages {
				select {
				case sub.msgs <- msg:
				case <-ctx.Done():
					return
				}
			}

			// Wait for the poll interval or context cancellation.
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()

	return sub
}

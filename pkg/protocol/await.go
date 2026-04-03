package protocol

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// fulfillmentLess reports whether a comes before b in the deterministic
// fulfillment ordering: earliest timestamp wins; ties broken by lexicographically
// smaller message ID. This ensures that when multiple messages fulfill the same
// future and their timestamps collide, Await always returns the same winner.
func fulfillmentLess(a, b store.MessageRecord) bool {
	if a.Timestamp != b.Timestamp {
		return a.Timestamp < b.Timestamp
	}
	return a.ID < b.ID
}

// ErrAwaitTimeout is returned by Await when the timeout expires before a
// fulfilling message is found.
var ErrAwaitTimeout = errors.New("await: timeout waiting for fulfillment")

// AwaitRequest specifies the parameters for an Await operation.
type AwaitRequest struct {
	// CampfireID is the campfire to poll. Required.
	CampfireID string

	// TargetMsgID is the message ID whose fulfillment we are waiting for.
	// The fulfilling message must carry the "fulfills" tag and have TargetMsgID
	// in its antecedents. Required.
	TargetMsgID string

	// Timeout is the maximum time to wait before returning ErrAwaitTimeout.
	// Zero means wait forever (subject to ctx cancellation).
	// Negative values are rejected with an error.
	Timeout time.Duration

	// PollInterval is the time between store polls. Defaults to 2 seconds when zero.
	PollInterval time.Duration
}

// Await blocks until a message that fulfills TargetMsgID is available in the
// campfire, or the timeout expires, or ctx is cancelled.
//
// A fulfilling message is one that:
//   - carries the "fulfills" tag, AND
//   - lists TargetMsgID in its antecedents.
//
// For filesystem-transport campfires, each poll begins with a sync from the
// transport directory so that messages written by other agents are visible
// without a separate sync step. This mirrors the behaviour of findFulfillment()
// in cmd/cf/cmd/await.go.
//
// Returns the fulfilling Message on success. Returns ErrAwaitTimeout if
// the deadline expires before a fulfillment is found. Returns ctx.Err() if
// the context is cancelled before a fulfillment is found. Returns a wrapped
// error for any store or sync failure encountered during the poll loop.
func (c *Client) Await(ctx context.Context, req AwaitRequest) (*Message, error) {
	if req.CampfireID == "" {
		return nil, fmt.Errorf("protocol.Client.Await: CampfireID is required")
	}
	if req.TargetMsgID == "" {
		return nil, fmt.Errorf("protocol.Client.Await: TargetMsgID is required")
	}
	// campfire-agent-kok: A negative timeout is a caller bug — return an error
	// immediately rather than silently waiting forever (which happened because
	// the req.Timeout > 0 guard below skipped timer creation for negative values).
	if req.Timeout < 0 {
		return nil, fmt.Errorf("protocol.Client.Await: Timeout must be >= 0 (got %v); use 0 to wait indefinitely", req.Timeout)
	}

	interval := req.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}

	// Set up deadline channel. A nil channel blocks forever, which is correct
	// for Timeout==0 (wait indefinitely, subject to ctx cancellation).
	var deadline <-chan time.Time
	if req.Timeout > 0 {
		timer := time.NewTimer(req.Timeout)
		defer timer.Stop()
		deadline = timer.C
	}

	// Initial sync-and-check before entering the poll loop.
	if err := c.syncIfFilesystem(req.CampfireID); err != nil {
		// campfire-agent-zyq: Non-fatal — the store may have messages from a
		// previous sync — but log it so operators can diagnose transport problems.
		fmt.Fprintf(os.Stderr, "protocol.Client.Await: initial sync error (campfire=%s): %v\n", req.CampfireID, err)
	}
	if rec, err := c.findFulfillment(req.CampfireID, req.TargetMsgID); err != nil {
		return nil, fmt.Errorf("protocol.Client.Await: initial fulfillment check: %w", err)
	} else if rec != nil {
		msg := MessageFromRecord(*rec)
		return &msg, nil
	}

	// Poll loop.
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, ErrAwaitTimeout
		case <-time.After(interval):
		}

		if err := c.syncIfFilesystem(req.CampfireID); err != nil {
			// campfire-agent-zyq: Non-fatal — keep polling — but log so operators
			// can see repeated transport failures without attaching a debugger.
			fmt.Fprintf(os.Stderr, "protocol.Client.Await: poll sync error (campfire=%s): %v\n", req.CampfireID, err)
		}
		if rec, err := c.findFulfillment(req.CampfireID, req.TargetMsgID); err != nil {
			return nil, fmt.Errorf("protocol.Client.Await: fulfillment check: %w", err)
		} else if rec != nil {
			msg := MessageFromRecord(*rec)
			return &msg, nil
		}
	}
}

// findFulfillment searches the store for a message that fulfills targetMsgID.
// It uses ListReferencingMessages to find candidates efficiently, then checks
// that the candidate carries the "fulfills" tag. Returns nil if none is found.
//
// When multiple messages fulfill the same future, the winner is selected
// deterministically: earliest timestamp wins; ties broken by lexicographically
// smaller message ID. (campfire-agent-mnh: non-deterministic winner when
// timestamps tie.)
func (c *Client) findFulfillment(campfireID, targetMsgID string) (*store.MessageRecord, error) {
	refs, err := c.store.ListReferencingMessages(targetMsgID)
	if err != nil {
		return nil, fmt.Errorf("listing referencing messages: %w", err)
	}
	var best *store.MessageRecord
	for i := range refs {
		if refs[i].CampfireID != campfireID {
			continue
		}
		hasFulfillsTag := false
		for _, tag := range refs[i].Tags {
			if tag == "fulfills" {
				hasFulfillsTag = true
				break
			}
		}
		if !hasFulfillsTag {
			continue
		}
		if best == nil || fulfillmentLess(refs[i], *best) {
			best = &refs[i]
		}
	}
	return best, nil
}

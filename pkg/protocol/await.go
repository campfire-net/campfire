package protocol

import (
	"errors"
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

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
	// Zero means wait forever.
	Timeout time.Duration

	// PollInterval is the time between store polls. Defaults to 2 seconds when zero.
	PollInterval time.Duration
}

// Await blocks until a message that fulfills TargetMsgID is available in the
// campfire, or the timeout expires.
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
// the deadline expires before a fulfillment is found. Returns a wrapped error
// for any store or sync failure encountered during the poll loop.
func (c *Client) Await(req AwaitRequest) (*Message, error) {
	if req.CampfireID == "" {
		return nil, fmt.Errorf("protocol.Client.Await: CampfireID is required")
	}
	if req.TargetMsgID == "" {
		return nil, fmt.Errorf("protocol.Client.Await: TargetMsgID is required")
	}

	interval := req.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}

	// Set up deadline channel. A nil channel blocks forever, which is correct
	// for Timeout==0 (wait indefinitely).
	var deadline <-chan time.Time
	if req.Timeout > 0 {
		timer := time.NewTimer(req.Timeout)
		defer timer.Stop()
		deadline = timer.C
	}

	// Initial sync-and-check before entering the poll loop.
	if err := c.syncIfFilesystem(req.CampfireID); err != nil {
		// Non-fatal: the store may have messages from a previous sync.
		_ = err
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
		case <-deadline:
			return nil, ErrAwaitTimeout
		case <-time.After(interval):
		}

		if err := c.syncIfFilesystem(req.CampfireID); err != nil {
			// Non-fatal: keep polling.
			_ = err
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
func (c *Client) findFulfillment(campfireID, targetMsgID string) (*store.MessageRecord, error) {
	refs, err := c.store.ListReferencingMessages(targetMsgID)
	if err != nil {
		return nil, fmt.Errorf("listing referencing messages: %w", err)
	}
	for i := range refs {
		if refs[i].CampfireID != campfireID {
			continue
		}
		for _, tag := range refs[i].Tags {
			if tag == "fulfills" {
				return &refs[i], nil
			}
		}
	}
	return nil, nil
}

// Package poller reads campfire messages on a timer and dispatches them to a handler.
package poller

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// MessageHandler is called for each new campfire message.
// Return nil to advance the cursor, non-nil to retry on next tick.
type MessageHandler func(msg store.MessageRecord) error

// Config configures a single campfire poller.
type Config struct {
	CampfireID         string
	PollInterval       time.Duration
	UrgentPollInterval time.Duration
	UrgentTags         []string
}

// Poller polls a campfire store for new messages and dispatches them.
type Poller struct {
	store   *store.Store
	cfg     Config
	handler MessageHandler
}

// New creates a poller for a single campfire.
func New(s *store.Store, cfg Config, handler MessageHandler) *Poller {
	return &Poller{store: s, cfg: cfg, handler: handler}
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) error {
	interval := p.cfg.PollInterval
	timer := time.NewTimer(0) // fire immediately on start
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			urgent, err := p.poll(ctx)
			if err != nil {
				log.Printf("poller %s: %v", p.cfg.CampfireID[:min(12, len(p.cfg.CampfireID))], err)
			}
			if urgent && p.cfg.UrgentPollInterval > 0 {
				interval = p.cfg.UrgentPollInterval
			} else {
				interval = p.cfg.PollInterval
			}
			timer.Reset(interval)
		}
	}
}

// poll reads new messages and dispatches them to the handler.
// Returns true if any message had urgent tags.
func (p *Poller) poll(ctx context.Context) (urgent bool, err error) {
	cursor, err := p.store.GetReadCursor(p.cfg.CampfireID)
	if err != nil {
		return false, fmt.Errorf("get cursor: %w", err)
	}

	msgs, err := p.store.ListMessages(p.cfg.CampfireID, cursor, store.MessageFilter{RespectCompaction: true})
	if err != nil {
		return false, fmt.Errorf("list messages: %w", err)
	}

	for _, msg := range msgs {
		if ctx.Err() != nil {
			return urgent, ctx.Err()
		}

		if err := p.handler(msg); err != nil {
			// Handler failed — do NOT advance cursor. Retry on next tick.
			return urgent, fmt.Errorf("handler for %s: %w", msg.ID, err)
		}

		// Handler succeeded — advance cursor AFTER confirmed processing.
		if err := p.store.SetReadCursor(p.cfg.CampfireID, msg.Timestamp); err != nil {
			return urgent, fmt.Errorf("set cursor: %w", err)
		}

		if p.hasUrgentTag(msg) {
			urgent = true
		}
	}

	return urgent, nil
}

func (p *Poller) hasUrgentTag(msg store.MessageRecord) bool {
	for _, tag := range msg.Tags {
		for _, ut := range p.cfg.UrgentTags {
			if tag == ut {
				return true
			}
		}
	}
	return false
}

// RunAll starts pollers for multiple campfires concurrently.
// Blocks until ctx is cancelled. Returns the first error from any poller.
func RunAll(ctx context.Context, s *store.Store, cfgs []Config, handler MessageHandler) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(cfgs))
	for _, cfg := range cfgs {
		p := New(s, cfg, handler)
		go func() {
			errCh <- p.Run(ctx)
		}()
	}

	// Wait for first error or context cancellation.
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

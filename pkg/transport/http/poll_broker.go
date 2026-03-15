package http

import (
	"fmt"
	"sync"
)

// PollBroker manages in-process fan-out for long-poll goroutines.
// Goroutines call Subscribe to receive a notification channel and a deregister
// function. handleDeliver calls Notify after persisting a message, which
// unblocks all waiting pollers for that campfire.
//
// No goroutines are created inside PollBroker; they live in handlePoll.
type PollBroker struct {
	mu             sync.Mutex
	subs           map[string][]chan struct{} // campfireID → notification channels
	limits         map[string]int            // campfireID → active poller count
	maxPerCampfire int                       // default 64
}

// Subscribe registers a poller for campfireID. It returns a receive-only
// channel that will be signalled by Notify, a deregister function the caller
// MUST invoke when the poll completes or is cancelled, and an error if the
// per-campfire limit is exceeded.
//
// The deregister function is safe to call more than once.
func (b *PollBroker) Subscribe(campfireID string) (<-chan struct{}, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.limits[campfireID] >= b.maxPerCampfire {
		return nil, nil, fmt.Errorf("poll_broker: too many active pollers for campfire %s (max %d)", campfireID, b.maxPerCampfire)
	}

	ch := make(chan struct{}, 1)
	b.subs[campfireID] = append(b.subs[campfireID], ch)
	b.limits[campfireID]++

	var once sync.Once
	dereg := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			subs := b.subs[campfireID]
			filtered := subs[:0]
			for _, s := range subs {
				if s != ch {
					filtered = append(filtered, s)
				}
			}
			b.subs[campfireID] = filtered
			b.limits[campfireID]--
		})
	}

	return ch, dereg, nil
}

// Notify sends a non-blocking signal to all pollers subscribed to campfireID.
// Pollers that are in the sync-then-block handoff and miss the signal will
// catch up via the initial ListMessages query on their next reconnect.
func (b *PollBroker) Notify(campfireID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs[campfireID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// NotifyEviction is a no-op stub. It exists so handleMembership can call it
// without requiring a file change when forced-eviction support is added later.
// Correctness without it: evicted members receive 403 on reconnect.
func (b *PollBroker) NotifyEviction(campfireID, memberPubKey string) {}

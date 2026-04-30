package http

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func newTestBroker() *PollBroker {
	return &PollBroker{
		subs:           make(map[string][]chan struct{}),
		limits:         make(map[string]int),
		maxPerCampfire: 4, // small limit for testing
	}
}

func TestPollBrokerSubscribeNotify(t *testing.T) {
	b := newTestBroker()

	ch1, dereg1, err := b.Subscribe("fire-a")
	if err != nil {
		t.Fatalf("Subscribe 1: %v", err)
	}
	defer dereg1()

	ch2, dereg2, err := b.Subscribe("fire-a")
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}
	defer dereg2()

	b.Notify("fire-a")

	timeout := time.After(10 * time.Millisecond)
	for i, ch := range []<-chan struct{}{ch1, ch2} {
		select {
		case <-ch:
			// got signal
		case <-timeout:
			t.Errorf("channel %d did not receive signal within 10ms", i+1)
		}
	}
}

func TestPollBrokerDeregister(t *testing.T) {
	b := newTestBroker()

	ch, dereg, err := b.Subscribe("fire-b")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	dereg()

	b.Notify("fire-b")

	select {
	case <-ch:
		t.Fatal("deregistered channel received a signal")
	case <-time.After(10 * time.Millisecond):
		// correct: nothing received
	}
}

func TestPollBrokerLimit(t *testing.T) {
	b := newTestBroker()

	var deregs []func()
	for i := 0; i < b.maxPerCampfire; i++ {
		_, dereg, err := b.Subscribe("fire-c")
		if err != nil {
			t.Fatalf("Subscribe %d failed unexpectedly: %v", i, err)
		}
		deregs = append(deregs, dereg)
	}

	// Next subscribe should fail.
	_, _, err := b.Subscribe("fire-c")
	if err == nil {
		t.Fatal("expected error when at limit, got nil")
	}

	// Deregister one, then subscribe should succeed.
	deregs[0]()

	_, dereg, err := b.Subscribe("fire-c")
	if err != nil {
		t.Fatalf("Subscribe after deregister failed: %v", err)
	}
	defer dereg()
}

// TestPollBrokerConcurrentRace exercises Subscribe, Notify, and deregister
// concurrently across multiple goroutines to catch data races. Run with -race.
func TestPollBrokerConcurrentRace(t *testing.T) {
	b := &PollBroker{
		subs:           make(map[string][]chan struct{}),
		limits:         make(map[string]int),
		maxPerCampfire: 64,
	}

	const campfires = 3
	const goroutinesPerCampfire = 8
	const notifyRounds = 20

	var wg sync.WaitGroup

	// Subscriber goroutines: subscribe, wait for one signal (or timeout), then deregister.
	for c := 0; c < campfires; c++ {
		fireID := fmt.Sprintf("race-fire-%d", c)
		for g := 0; g < goroutinesPerCampfire; g++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				ch, dereg, err := b.Subscribe(id)
				if err != nil {
					// Limit hit under concurrency is acceptable — just return.
					return
				}
				defer dereg()
				// Call deregister a second time to verify sync.Once safety.
				defer dereg()
				select {
				case <-ch:
				case <-time.After(200 * time.Millisecond):
				}
			}(fireID)
		}
	}

	// Notifier goroutine: repeatedly notify all campfires.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for r := 0; r < notifyRounds; r++ {
			for c := 0; c < campfires; c++ {
				b.Notify(fmt.Sprintf("race-fire-%d", c))
			}
		}
	}()

	wg.Wait()
}

func TestPollBrokerNotifyNoSubscribers(t *testing.T) {
	b := newTestBroker()
	// Notify on a campfire with no subscribers must not panic.
	b.Notify("nonexistent-fire")
}

func TestPollBrokerDeregisterIdempotent(t *testing.T) {
	b := newTestBroker()
	_, dereg, err := b.Subscribe("fire-idem")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// Calling deregister multiple times must not panic or corrupt state.
	dereg()
	dereg()
	dereg()
}

func TestPollBrokerMultiCampfire(t *testing.T) {
	b := newTestBroker()

	chA, deregA, err := b.Subscribe("fire-a")
	if err != nil {
		t.Fatalf("Subscribe fire-a: %v", err)
	}
	defer deregA()

	chB, deregB, err := b.Subscribe("fire-b")
	if err != nil {
		t.Fatalf("Subscribe fire-b: %v", err)
	}
	defer deregB()

	b.Notify("fire-a")

	// campfireA channel should fire.
	select {
	case <-chA:
		// correct
	case <-time.After(10 * time.Millisecond):
		t.Fatal("campfireA channel did not receive signal within 10ms")
	}

	// campfireB channel should NOT fire.
	select {
	case <-chB:
		t.Fatal("campfireB channel received unexpected signal")
	case <-time.After(10 * time.Millisecond):
		// correct
	}
}

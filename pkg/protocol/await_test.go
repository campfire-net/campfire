package protocol_test

import (
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// TestClientAwait_FulfillmentAlreadyPresent verifies that Await returns
// immediately when a fulfilling message already exists in the store.
func TestClientAwait_FulfillmentAlreadyPresent(t *testing.T) {
	campfireID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write a "future" message directly to the store.
	futureMsg := writeTransportMessage(t, tr, campfireID, "escalation: need ruling", []string{"future"})

	// Write a fulfillment via the transport (another agent posting).
	// The fulfillment carries the "fulfills" tag and sets futureMsg.ID as an antecedent.
	fulfillMsg := writeTransportMessageWithAntecedents(t, tr, campfireID, "ruling: go ahead", []string{"fulfills"}, []string{futureMsg.ID})

	// Sync both messages into the store.
	_, err := client.Read(protocol.ReadRequest{CampfireID: campfireID, IncludeCompacted: true})
	if err != nil {
		t.Fatalf("syncing messages: %v", err)
	}

	// Await should return immediately.
	got, err := client.Await(protocol.AwaitRequest{
		CampfireID:  campfireID,
		TargetMsgID: futureMsg.ID,
		Timeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Await: unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("Await: expected fulfilling message, got nil")
	}
	if got.ID != fulfillMsg.ID {
		t.Errorf("Await: expected fulfillment message ID %q, got %q", fulfillMsg.ID, got.ID)
	}
}

// TestClientAwait_FulfillmentArrivesLater verifies that Await polls until a
// fulfilling message appears in the filesystem transport.
func TestClientAwait_FulfillmentArrivesLater(t *testing.T) {
	campfireID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write the future message.
	futureMsg := writeTransportMessage(t, tr, campfireID, "need ruling", []string{"future"})

	// Launch Await in a goroutine with a short poll interval.
	type result struct {
		msg *store.MessageRecord
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := client.Await(protocol.AwaitRequest{
			CampfireID:   campfireID,
			TargetMsgID:  futureMsg.ID,
			Timeout:      10 * time.Second,
			PollInterval: 100 * time.Millisecond,
		})
		ch <- result{msg, err}
	}()

	// After a short delay, write the fulfillment to the transport.
	// Await's next poll will sync it and find it.
	time.Sleep(200 * time.Millisecond)
	fulfillMsg := writeTransportMessageWithAntecedents(t, tr, campfireID, "ruling: proceed", []string{"fulfills"}, []string{futureMsg.ID})

	// Collect the result.
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Await: unexpected error: %v", r.err)
		}
		if r.msg == nil {
			t.Fatal("Await: expected fulfilling message, got nil")
		}
		if r.msg.ID != fulfillMsg.ID {
			t.Errorf("Await: expected fulfillment ID %q, got %q", fulfillMsg.ID, r.msg.ID)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Await: timed out waiting for goroutine result")
	}
}

// TestClientAwait_Timeout verifies that Await returns ErrAwaitTimeout when
// no fulfillment arrives before the deadline.
func TestClientAwait_Timeout(t *testing.T) {
	campfireID, tr, s := setupTestCampfire(t)
	defer s.Close()
	_ = tr // no fulfillment written

	client := protocol.New(s, nil)

	// Write a future message (no fulfillment will follow).
	futureMsg := writeTransportMessage(t, tr, campfireID, "unanswered question", []string{"future"})

	_, err := client.Await(protocol.AwaitRequest{
		CampfireID:   campfireID,
		TargetMsgID:  futureMsg.ID,
		Timeout:      300 * time.Millisecond,
		PollInterval: 100 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("Await: expected ErrAwaitTimeout, got nil")
	}
	if err != protocol.ErrAwaitTimeout {
		t.Errorf("Await: expected ErrAwaitTimeout, got %v", err)
	}
}

// TestClientAwait_RequiresCampfireID verifies that Await returns an error when
// CampfireID is empty.
func TestClientAwait_RequiresCampfireID(t *testing.T) {
	_, _, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	_, err := client.Await(protocol.AwaitRequest{TargetMsgID: "someid"})
	if err == nil {
		t.Error("Await: expected error for empty CampfireID, got nil")
	}
}

// TestClientAwait_RequiresTargetMsgID verifies that Await returns an error when
// TargetMsgID is empty.
func TestClientAwait_RequiresTargetMsgID(t *testing.T) {
	campfireID, _, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	_, err := client.Await(protocol.AwaitRequest{CampfireID: campfireID})
	if err == nil {
		t.Error("Await: expected error for empty TargetMsgID, got nil")
	}
}

// TestClientAwait_IgnoresMessagesWithoutFulfillsTag verifies that a message
// referencing the target in its antecedents but lacking the "fulfills" tag
// does NOT satisfy the Await condition.
func TestClientAwait_IgnoresMessagesWithoutFulfillsTag(t *testing.T) {
	campfireID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	futureMsg := writeTransportMessage(t, tr, campfireID, "pending question", []string{"future"})

	// Write a reply that references the future message but has "status" tag, not "fulfills".
	writeTransportMessageWithAntecedents(t, tr, campfireID, "working on it", []string{"status"}, []string{futureMsg.ID})

	// Await should time out — "status" tag is not "fulfills".
	_, err := client.Await(protocol.AwaitRequest{
		CampfireID:   campfireID,
		TargetMsgID:  futureMsg.ID,
		Timeout:      400 * time.Millisecond,
		PollInterval: 100 * time.Millisecond,
	})
	if err != protocol.ErrAwaitTimeout {
		t.Errorf("Await: expected ErrAwaitTimeout (no fulfills tag), got %v", err)
	}
}

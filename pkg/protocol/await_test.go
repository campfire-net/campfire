package protocol_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// TestClientAwait_FulfillmentAlreadyPresent verifies that Await returns
// immediately when a fulfilling message already exists in the store.
func TestClientAwait_FulfillmentAlreadyPresent(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write a "future" message directly to the store.
	futureMsg := writeTransportMessage(t, cfID, tr, campfireID, "escalation: need ruling", []string{"future"})

	// Write a fulfillment via the transport (another agent posting).
	// The fulfillment carries the "fulfills" tag and sets futureMsg.ID as an antecedent.
	fulfillMsg := writeTransportMessageWithAntecedents(t, cfID, tr, campfireID, "ruling: go ahead", []string{"fulfills"}, []string{futureMsg.ID})

	// Sync both messages into the store.
	_, err := client.Read(protocol.ReadRequest{CampfireID: campfireID, IncludeCompacted: true})
	if err != nil {
		t.Fatalf("syncing messages: %v", err)
	}

	// Await should return immediately.
	got, err := client.Await(context.Background(), protocol.AwaitRequest{
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
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write the future message.
	futureMsg := writeTransportMessage(t, cfID, tr, campfireID, "need ruling", []string{"future"})

	// Launch Await in a goroutine with a short poll interval.
	type result struct {
		msg *protocol.Message
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := client.Await(context.Background(), protocol.AwaitRequest{
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
	fulfillMsg := writeTransportMessageWithAntecedents(t, cfID, tr, campfireID, "ruling: proceed", []string{"fulfills"}, []string{futureMsg.ID})

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
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write a future message (no fulfillment will follow).
	futureMsg := writeTransportMessage(t, cfID, tr, campfireID, "unanswered question", []string{"future"})

	_, err := client.Await(context.Background(), protocol.AwaitRequest{
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
	_, _, _, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	_, err := client.Await(context.Background(), protocol.AwaitRequest{TargetMsgID: "someid"})
	if err == nil {
		t.Error("Await: expected error for empty CampfireID, got nil")
	}
}

// TestClientAwait_RequiresTargetMsgID verifies that Await returns an error when
// TargetMsgID is empty.
func TestClientAwait_RequiresTargetMsgID(t *testing.T) {
	campfireID, _, _, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	_, err := client.Await(context.Background(), protocol.AwaitRequest{CampfireID: campfireID})
	if err == nil {
		t.Error("Await: expected error for empty TargetMsgID, got nil")
	}
}

// TestClientAwait_IgnoresMessagesWithoutFulfillsTag verifies that a message
// referencing the target in its antecedents but lacking the "fulfills" tag
// does NOT satisfy the Await condition.
func TestClientAwait_IgnoresMessagesWithoutFulfillsTag(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	futureMsg := writeTransportMessage(t, cfID, tr, campfireID, "pending question", []string{"future"})

	// Write a reply that references the future message but has "status" tag, not "fulfills".
	writeTransportMessageWithAntecedents(t, cfID, tr, campfireID, "working on it", []string{"status"}, []string{futureMsg.ID})

	// Await should time out -- "status" tag is not "fulfills".
	_, err := client.Await(context.Background(), protocol.AwaitRequest{
		CampfireID:   campfireID,
		TargetMsgID:  futureMsg.ID,
		Timeout:      400 * time.Millisecond,
		PollInterval: 100 * time.Millisecond,
	})
	if err != protocol.ErrAwaitTimeout {
		t.Errorf("Await: expected ErrAwaitTimeout (no fulfills tag), got %v", err)
	}
}

// TestClientAwait_ContextCancellation verifies that Await returns ctx.Err()
// when the context is cancelled before a fulfillment arrives.
func TestClientAwait_ContextCancellation(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write a future message -- no fulfillment will arrive.
	futureMsg := writeTransportMessage(t, cfID, tr, campfireID, "unanswered question", []string{"future"})

	ctx, cancel := context.WithCancel(context.Background())

	type result struct {
		msg *protocol.Message
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := client.Await(ctx, protocol.AwaitRequest{
			CampfireID:   campfireID,
			TargetMsgID:  futureMsg.ID,
			Timeout:      0, // no timeout -- only ctx cancellation stops it
			PollInterval: 50 * time.Millisecond,
		})
		ch <- result{msg, err}
	}()

	// Cancel the context after a short delay.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case r := <-ch:
		if r.err == nil {
			t.Fatal("Await: expected error on context cancellation, got nil")
		}
		if r.err != context.Canceled {
			t.Errorf("Await: expected context.Canceled, got %v", r.err)
		}
		if r.msg != nil {
			t.Errorf("Await: expected nil message on cancellation, got %v", r.msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Await: did not return after context cancellation")
	}
}

// TestClientAwait_NegativeTimeout verifies that Await returns an error immediately
// when Timeout is negative rather than silently waiting forever (campfire-agent-kok).
func TestClientAwait_NegativeTimeout(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	futureMsg := writeTransportMessage(t, cfID, tr, campfireID, "unanswered question", []string{"future"})

	start := time.Now()
	_, err := client.Await(context.Background(), protocol.AwaitRequest{
		CampfireID:  campfireID,
		TargetMsgID: futureMsg.ID,
		Timeout:     -1 * time.Second,
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Await: expected error for negative timeout, got nil")
	}
	// Should return almost instantly -- not after any poll interval.
	if elapsed > 500*time.Millisecond {
		t.Errorf("Await: negative timeout should return immediately, took %v", elapsed)
	}
}

// TestClientAwait_MultipleFulfillmentsTiebreaker verifies that when multiple
// messages fulfill the same future and their timestamps are identical, Await
// returns the message with the lexicographically smallest ID -- making the
// selection deterministic rather than dependent on iteration order.
// (campfire-agent-mnh regression test.)
func TestClientAwait_MultipleFulfillmentsTiebreaker(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write the future message into the transport so it gets synced.
	futureMsg := writeTransportMessage(t, cfID, tr, campfireID, "pending question", []string{"future"})

	// Sync the future message into the local store.
	if _, err := client.Read(protocol.ReadRequest{CampfireID: campfireID, IncludeCompacted: true}); err != nil {
		t.Fatalf("syncing future message: %v", err)
	}

	// Insert three fulfilling records directly into the store with identical
	// timestamps. We control the IDs so we can predict the deterministic winner.
	// The winner should be the lexicographically smallest ID: "aaa..."
	const sharedTimestamp = int64(999_888_777_000)
	fulfillIDs := []string{
		"ccc0000000000000000000000000000000000000000000000000000000000000",
		"aaa0000000000000000000000000000000000000000000000000000000000000", // <- smallest ID, expected winner
		"bbb0000000000000000000000000000000000000000000000000000000000000",
	}
	for _, id := range fulfillIDs {
		rec := store.MessageRecord{
			ID:          id,
			CampfireID:  campfireID,
			Sender:      "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd",
			Payload:     []byte("fulfillment"),
			Tags:        []string{"fulfills"},
			Antecedents: []string{futureMsg.ID},
			Timestamp:   sharedTimestamp,
			Signature:   []byte("sig"),
			Provenance:  nil,
			ReceivedAt:  sharedTimestamp + 1,
		}
		if _, err := s.AddMessage(rec); err != nil {
			t.Fatalf("AddMessage(%s): %v", id, err)
		}
	}

	// Await should return immediately (fulfillments already present) and always
	// select the same winner regardless of iteration order.
	got, err := client.Await(context.Background(), protocol.AwaitRequest{
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
	wantID := "aaa0000000000000000000000000000000000000000000000000000000000000"
	if got.ID != wantID {
		t.Errorf("Await: expected deterministic winner %q, got %q", wantID, got.ID)
	}
}

// TestClientAwait_ContextAlreadyCancelled verifies that Await returns immediately
// when called with an already-cancelled context and no fulfillment is present.
func TestClientAwait_ContextAlreadyCancelled(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	futureMsg := writeTransportMessage(t, cfID, tr, campfireID, "question", []string{"future"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := client.Await(ctx, protocol.AwaitRequest{
		CampfireID:   campfireID,
		TargetMsgID:  futureMsg.ID,
		Timeout:      0,
		PollInterval: 50 * time.Millisecond,
	})
	// The initial check runs before entering the poll loop, so if no fulfillment
	// is present the first poll select should return ctx.Err() immediately.
	if err == nil {
		t.Fatal("Await: expected error for cancelled context, got nil")
	}
	if err != context.Canceled {
		t.Errorf("Await: expected context.Canceled, got %v", err)
	}
}

// addCompactionEvent inserts a campfire:compact message into the store that
// marks the given message IDs as superseded. This is a test helper that mirrors
// how cmd/cf compact builds compaction records.
func addCompactionEvent(t *testing.T, s store.Store, campfireID string, supersedes []string) {
	t.Helper()
	type compactionPayload struct {
		Supersedes     []string `json:"supersedes"`
		Summary        []byte   `json:"summary"`
		Retention      string   `json:"retention"`
		CheckpointHash string   `json:"checkpoint_hash"`
	}
	payload, err := json.Marshal(compactionPayload{
		Supersedes:     supersedes,
		Summary:        []byte("test compaction summary"),
		Retention:      "archive",
		CheckpointHash: "testhash",
	})
	if err != nil {
		t.Fatalf("marshalling compaction payload: %v", err)
	}
	compactRec := store.MessageRecord{
		ID:          "compact-" + supersedes[0],
		CampfireID:  campfireID,
		Sender:      "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd",
		Payload:     payload,
		Tags:        []string{"campfire:compact"},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
		Signature:   []byte("testsig"),
		Provenance:  nil,
		ReceivedAt:  time.Now().UnixNano(),
	}
	if _, err := s.AddMessage(compactRec); err != nil {
		t.Fatalf("adding compaction event: %v", err)
	}
}

// TestClientAwait_CompactedFulfillmentIsIgnored verifies that Await times out
// when the only fulfillment in the store has been superseded by a compaction
// event. This is the regression test for campfire-agent-xy0: findFulfillment
// previously returned compacted messages, diverging from CLI behaviour.
func TestClientAwait_CompactedFulfillmentIsIgnored(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write the future message to transport, then sync it into the store.
	futureMsg := writeTransportMessage(t, cfID, tr, campfireID, "pending question", []string{"future"})
	if _, err := client.Read(protocol.ReadRequest{CampfireID: campfireID, IncludeCompacted: true}); err != nil {
		t.Fatalf("syncing future message: %v", err)
	}

	// Insert a fulfillment directly into the store with a known ID.
	const fulfillID = "fulfill0000000000000000000000000000000000000000000000000000000001"
	fulfillRec := store.MessageRecord{
		ID:          fulfillID,
		CampfireID:  campfireID,
		Sender:      "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd",
		Payload:     []byte("ruling: proceed"),
		Tags:        []string{"fulfills"},
		Antecedents: []string{futureMsg.ID},
		Timestamp:   time.Now().UnixNano(),
		Signature:   []byte("sig"),
		Provenance:  nil,
		ReceivedAt:  time.Now().UnixNano(),
	}
	if _, err := s.AddMessage(fulfillRec); err != nil {
		t.Fatalf("AddMessage(fulfillment): %v", err)
	}

	// Add a compaction event that supersedes the fulfillment.
	addCompactionEvent(t, s, campfireID, []string{fulfillID})

	// Await should NOT return the compacted fulfillment -- it must time out.
	_, err := client.Await(context.Background(), protocol.AwaitRequest{
		CampfireID:   campfireID,
		TargetMsgID:  futureMsg.ID,
		Timeout:      400 * time.Millisecond,
		PollInterval: 100 * time.Millisecond,
	})
	if err != protocol.ErrAwaitTimeout {
		t.Errorf("Await: expected ErrAwaitTimeout (compacted fulfillment must be ignored), got %v", err)
	}
}

// TestClientAwait_NonCompactedFulfillmentWinsWhenEarlierWasCompacted verifies
// that Await returns the non-compacted fulfillment when an earlier fulfillment
// was superseded by a compaction event. The compaction must not block Await from
// finding valid (non-compacted) fulfillments. (campfire-agent-xy0)
func TestClientAwait_NonCompactedFulfillmentWinsWhenEarlierWasCompacted(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write and sync the future message.
	futureMsg := writeTransportMessage(t, cfID, tr, campfireID, "pending question", []string{"future"})
	if _, err := client.Read(protocol.ReadRequest{CampfireID: campfireID, IncludeCompacted: true}); err != nil {
		t.Fatalf("syncing future message: %v", err)
	}

	// Insert two fulfillments: one that will be compacted, one that won't.
	const compactedFulfillID = "compacted00000000000000000000000000000000000000000000000000000001"
	const liveFulfillID = "live000000000000000000000000000000000000000000000000000000000001"
	baseTS := time.Now().UnixNano()

	compactedRec := store.MessageRecord{
		ID:          compactedFulfillID,
		CampfireID:  campfireID,
		Sender:      "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd",
		Payload:     []byte("old ruling"),
		Tags:        []string{"fulfills"},
		Antecedents: []string{futureMsg.ID},
		Timestamp:   baseTS,
		Signature:   []byte("sig"),
		Provenance:  nil,
		ReceivedAt:  baseTS,
	}
	liveRec := store.MessageRecord{
		ID:          liveFulfillID,
		CampfireID:  campfireID,
		Sender:      "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd",
		Payload:     []byte("new ruling: proceed"),
		Tags:        []string{"fulfills"},
		Antecedents: []string{futureMsg.ID},
		Timestamp:   baseTS + 1,
		Signature:   []byte("sig"),
		Provenance:  nil,
		ReceivedAt:  baseTS + 1,
	}
	for _, rec := range []store.MessageRecord{compactedRec, liveRec} {
		if _, err := s.AddMessage(rec); err != nil {
			t.Fatalf("AddMessage(%s): %v", rec.ID, err)
		}
	}

	// Compact only the earlier fulfillment, leaving the live one intact.
	addCompactionEvent(t, s, campfireID, []string{compactedFulfillID})

	// Await must return the live (non-compacted) fulfillment.
	got, err := client.Await(context.Background(), protocol.AwaitRequest{
		CampfireID:  campfireID,
		TargetMsgID: futureMsg.ID,
		Timeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Await: unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("Await: expected non-compacted fulfillment, got nil")
	}
	if got.ID != liveFulfillID {
		t.Errorf("Await: expected live fulfillment %q, got %q", liveFulfillID, got.ID)
	}
}

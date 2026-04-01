//go:build azurite

// Package aztable_test — storage_counters_test.go
//
// Azurite integration tests for CampfireStorageCounters.
// Run with: go test -tags azurite ./pkg/store/aztable/...
//
// Prerequisites:
//   - Azurite must be running on localhost:10002
//   - Connection string: DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;...
package aztable_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/store/aztable"
)

// newTestTableStore returns a raw *aztable.TableStore for tests that need
// access to methods beyond the store.Store interface (e.g., GetStorageCounter).
func newTestTableStore(t *testing.T) *aztable.TableStore {
	t.Helper()
	ts, err := aztable.NewRawTableStore(azuriteConnStr)
	if err != nil {
		t.Fatalf("NewRawTableStore: %v", err)
	}
	t.Cleanup(func() { ts.Close() })
	return ts
}

// TestStorageCounter_IncrementOnAddMessage verifies that adding a message
// increments BytesStored by len(payload) and MessageCount by 1.
func TestStorageCounter_IncrementOnAddMessage(t *testing.T) {
	ts := newTestTableStore(t)
	cfID := fmt.Sprintf("cf-counter-test-%d", time.Now().UnixNano())
	payload := []byte("hello, campfire")

	msg := store.MessageRecord{
		ID:         fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		CampfireID: cfID,
		Sender:     "test-sender",
		Payload:    payload,
		Tags:       []string{"test"},
		Timestamp:  time.Now().UnixNano(),
		ReceivedAt: time.Now().UnixNano(),
	}

	inserted, err := ts.AddMessage(msg)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if !inserted {
		t.Fatal("expected message to be inserted")
	}

	ctx := context.Background()
	bytesStored, messageCount, err := ts.GetStorageCounter(ctx, cfID)
	if err != nil {
		t.Fatalf("GetStorageCounter: %v", err)
	}
	if bytesStored != int64(len(payload)) {
		t.Errorf("BytesStored: got %d, want %d", bytesStored, len(payload))
	}
	if messageCount != 1 {
		t.Errorf("MessageCount: got %d, want 1", messageCount)
	}
}

// TestStorageCounter_MultipleMessages verifies cumulative byte count across
// multiple AddMessage calls.
func TestStorageCounter_MultipleMessages(t *testing.T) {
	ts := newTestTableStore(t)
	cfID := fmt.Sprintf("cf-counter-multi-%d", time.Now().UnixNano())

	payloads := [][]byte{
		[]byte("first"),
		[]byte("second message"),
		[]byte("third message payload"),
	}
	var totalBytes int64
	now := time.Now().UnixNano()
	for i, payload := range payloads {
		totalBytes += int64(len(payload))
		msg := store.MessageRecord{
			ID:         fmt.Sprintf("msg-%d-%d", now, i),
			CampfireID: cfID,
			Sender:     "test-sender",
			Payload:    payload,
			Tags:       []string{"test"},
			Timestamp:  now + int64(i),
			ReceivedAt: now,
		}
		if _, err := ts.AddMessage(msg); err != nil {
			t.Fatalf("AddMessage[%d]: %v", i, err)
		}
	}

	ctx := context.Background()
	bytesStored, messageCount, err := ts.GetStorageCounter(ctx, cfID)
	if err != nil {
		t.Fatalf("GetStorageCounter: %v", err)
	}
	if bytesStored != totalBytes {
		t.Errorf("BytesStored: got %d, want %d", bytesStored, totalBytes)
	}
	if messageCount != int64(len(payloads)) {
		t.Errorf("MessageCount: got %d, want %d", messageCount, len(payloads))
	}
}

// TestStorageCounter_UnknownCampfire verifies that GetStorageCounter returns
// (0, 0, nil) for a campfire that has no messages.
func TestStorageCounter_UnknownCampfire(t *testing.T) {
	ts := newTestTableStore(t)
	cfID := fmt.Sprintf("cf-counter-unknown-%d", time.Now().UnixNano())

	ctx := context.Background()
	bytesStored, messageCount, err := ts.GetStorageCounter(ctx, cfID)
	if err != nil {
		t.Fatalf("GetStorageCounter: %v", err)
	}
	if bytesStored != 0 {
		t.Errorf("BytesStored: got %d, want 0", bytesStored)
	}
	if messageCount != 0 {
		t.Errorf("MessageCount: got %d, want 0", messageCount)
	}
}

// TestStorageCounter_ConcurrentAddMessage runs 10 goroutines each adding a
// distinct message and verifies the final byte and message counts are correct.
func TestStorageCounter_ConcurrentAddMessage(t *testing.T) {
	ts := newTestTableStore(t)
	cfID := fmt.Sprintf("cf-counter-concurrent-%d", time.Now().UnixNano())

	const goroutines = 10
	payload := []byte("concurrent payload")
	expectedBytes := int64(len(payload)) * goroutines

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	now := time.Now().UnixNano()

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			msg := store.MessageRecord{
				ID:         fmt.Sprintf("msg-concurrent-%d-%d", now, n),
				CampfireID: cfID,
				Sender:     "test-sender",
				Payload:    payload,
				Tags:       []string{"test"},
				Timestamp:  now + int64(n),
				ReceivedAt: now,
			}
			if _, err := ts.AddMessage(msg); err != nil {
				errCh <- fmt.Errorf("goroutine %d: AddMessage: %w", n, err)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent error: %v", err)
	}

	ctx := context.Background()
	bytesStored, messageCount, err := ts.GetStorageCounter(ctx, cfID)
	if err != nil {
		t.Fatalf("GetStorageCounter: %v", err)
	}
	if messageCount != goroutines {
		t.Errorf("MessageCount: got %d, want %d", messageCount, goroutines)
	}
	if bytesStored != expectedBytes {
		t.Errorf("BytesStored: got %d, want %d", bytesStored, expectedBytes)
	}
}

// TestStorageCounter_CompactionDecrement verifies that adding a compaction
// message (with bytes_superseded set) decrements the storage counter for
// superseded messages and increments for the compaction message payload itself.
func TestStorageCounter_CompactionDecrement(t *testing.T) {
	ts := newTestTableStore(t)
	cfID := fmt.Sprintf("cf-counter-compact-%d", time.Now().UnixNano())

	// Add two regular messages.
	payloads := [][]byte{[]byte("msg one payload"), []byte("msg two payload")}
	msgIDs := make([]string, len(payloads))
	now := time.Now().UnixNano()
	var totalBytes int64
	for i, p := range payloads {
		totalBytes += int64(len(p))
		msgIDs[i] = fmt.Sprintf("msg-compact-%d-%d", now, i)
		msg := store.MessageRecord{
			ID:         msgIDs[i],
			CampfireID: cfID,
			Sender:     "test-sender",
			Payload:    p,
			Tags:       []string{"test"},
			Timestamp:  now + int64(i),
			ReceivedAt: now,
		}
		if _, err := ts.AddMessage(msg); err != nil {
			t.Fatalf("AddMessage[%d]: %v", i, err)
		}
	}

	ctx := context.Background()
	bytesStored, _, err := ts.GetStorageCounter(ctx, cfID)
	if err != nil {
		t.Fatalf("GetStorageCounter before compact: %v", err)
	}
	if bytesStored != totalBytes {
		t.Fatalf("BytesStored before compact: got %d, want %d", bytesStored, totalBytes)
	}

	// Build a compaction message that supersedes all prior messages.
	type compactPayloadShape struct {
		Supersedes      []string `json:"supersedes"`
		BytesSuperseded int64    `json:"bytes_superseded"`
	}
	compactPayload := compactPayloadShape{
		Supersedes:      msgIDs,
		BytesSuperseded: totalBytes,
	}
	payloadBytes, err := json.Marshal(compactPayload)
	if err != nil {
		t.Fatalf("marshal compact payload: %v", err)
	}

	compactMsg := store.MessageRecord{
		ID:         fmt.Sprintf("msg-compact-event-%d", now),
		CampfireID: cfID,
		Sender:     "test-sender",
		Payload:    payloadBytes,
		Tags:       []string{"campfire:compact"},
		Timestamp:  now + 1000,
		ReceivedAt: now,
	}
	if _, err := ts.AddMessage(compactMsg); err != nil {
		t.Fatalf("AddMessage compact: %v", err)
	}

	// After compaction:
	// - BytesStored was decremented by totalBytes (superseded messages)
	// - BytesStored was incremented by len(payloadBytes) (the compaction message)
	expectedBytes := int64(len(payloadBytes))
	bytesStored2, _, err := ts.GetStorageCounter(ctx, cfID)
	if err != nil {
		t.Fatalf("GetStorageCounter after compact: %v", err)
	}
	if bytesStored2 != expectedBytes {
		t.Errorf("BytesStored after compact: got %d, want %d", bytesStored2, expectedBytes)
	}
}

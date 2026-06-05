package store

// Tests for AddMessagesBatch — the bulk fast path for filesystem-transport
// sync (campfireagent-6d3). The batch must mirror AddMessage's semantics:
// INSERT OR IGNORE dedup, downgrade prevention for encrypted campfires, and
// compaction-bytes validation — with per-message validation failures skipping
// the record rather than aborting the batch.

import (
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/internal/crypto"
)

func batchRecord(campfireID, id string, payload []byte) MessageRecord {
	return MessageRecord{
		ID:         id,
		CampfireID: campfireID,
		Sender:     "sender1",
		Payload:    payload,
		Tags:       []string{},
		Signature:  []byte("sig"),
		Timestamp:  time.Now().UnixNano(),
		ReceivedAt: time.Now().UnixNano(),
	}
}

func TestAddMessagesBatch_InsertAndDedup(t *testing.T) {
	s := openTestStore(t)
	campfireID := "cf-batch-dedup"
	addTestMembership(t, s, campfireID)
	sq, ok := s.(*SQLiteStore)
	if !ok {
		t.Fatalf("openTestStore returned %T, want *SQLiteStore", s)
	}

	var batch []MessageRecord
	for i := 0; i < 5; i++ {
		batch = append(batch, batchRecord(campfireID, fmt.Sprintf("msg-%03d", i), []byte("p")))
	}

	added, err := sq.AddMessagesBatch(batch)
	if err != nil {
		t.Fatalf("AddMessagesBatch: %v", err)
	}
	if added != 5 {
		t.Fatalf("added = %d, want 5", added)
	}

	// Re-inserting the same batch plus one new record: only the new one counts.
	batch = append(batch, batchRecord(campfireID, "msg-new", []byte("p")))
	added, err = sq.AddMessagesBatch(batch)
	if err != nil {
		t.Fatalf("AddMessagesBatch(rerun): %v", err)
	}
	if added != 1 {
		t.Fatalf("rerun added = %d, want 1 (INSERT OR IGNORE dedup)", added)
	}

	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil || len(msgs) != 6 {
		t.Fatalf("ListMessages: %d messages, err=%v; want 6", len(msgs), err)
	}

	// Empty batch is a no-op.
	added, err = sq.AddMessagesBatch(nil)
	if err != nil || added != 0 {
		t.Fatalf("empty batch: added=%d err=%v", added, err)
	}
}

func TestAddMessagesBatch_DowngradeSkipsRecord(t *testing.T) {
	s := openTestStore(t)
	campfireID := "cf-batch-encrypted"
	addTestMembership(t, s, campfireID)
	if err := s.SetMembershipEncrypted(campfireID, true); err != nil {
		t.Fatalf("SetMembershipEncrypted: %v", err)
	}
	sq := s.(*SQLiteStore)

	validEP := crypto.EncryptedPayload{Epoch: 1, Nonce: make([]byte, 12), Ciphertext: make([]byte, 32)}
	encPayload, err := crypto.MarshalEncryptedPayload(validEP)
	if err != nil {
		t.Fatalf("MarshalEncryptedPayload: %v", err)
	}

	batch := []MessageRecord{
		batchRecord(campfireID, "msg-plain", []byte("plaintext payload")), // must be skipped
		batchRecord(campfireID, "msg-enc", encPayload),                    // must be stored
	}
	added, err := sq.AddMessagesBatch(batch)
	if err != nil {
		t.Fatalf("AddMessagesBatch: %v (per-message validation failures must skip, not abort)", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1 (plaintext record skipped in encrypted campfire)", added)
	}
	if has, _ := s.HasMessage("msg-plain"); has {
		t.Fatal("plaintext message was stored in encrypted campfire (downgrade prevention bypassed)")
	}
	if has, _ := s.HasMessage("msg-enc"); !has {
		t.Fatal("encrypted message missing")
	}
}

// TestAddMessagesBatch_ParityWithAddMessage stores the same records through
// both paths into two stores and verifies identical query results — the batch
// is an optimization, never a semantic fork.
func TestAddMessagesBatch_ParityWithAddMessage(t *testing.T) {
	sBatch := openTestStore(t)
	sSingle := openTestStore(t)
	campfireID := "cf-parity"
	addTestMembership(t, sBatch, campfireID)
	addTestMembership(t, sSingle, campfireID)

	var records []MessageRecord
	for i := 0; i < 10; i++ {
		r := batchRecord(campfireID, fmt.Sprintf("msg-%03d", i), []byte(fmt.Sprintf("payload-%d", i)))
		r.Tags = []string{"tag-a", fmt.Sprintf("tag-%d", i%2)}
		r.Timestamp = int64(1000 + i)
		records = append(records, r)
	}

	added, err := sBatch.(*SQLiteStore).AddMessagesBatch(records)
	if err != nil || added != 10 {
		t.Fatalf("batch path: added=%d err=%v", added, err)
	}
	for _, r := range records {
		if _, err := sSingle.AddMessage(r); err != nil {
			t.Fatalf("single path: %v", err)
		}
	}

	for _, f := range []MessageFilter{{}, {Tags: []string{"tag-1"}}, {Tags: []string{"tag-a"}}} {
		got, err1 := sBatch.ListMessages(campfireID, 0, f)
		want, err2 := sSingle.ListMessages(campfireID, 0, f)
		if err1 != nil || err2 != nil {
			t.Fatalf("ListMessages: %v / %v", err1, err2)
		}
		if len(got) != len(want) {
			t.Fatalf("filter %+v: batch=%d single=%d", f, len(got), len(want))
		}
		for i := range got {
			if got[i].ID != want[i].ID || got[i].Timestamp != want[i].Timestamp ||
				string(got[i].Payload) != string(want[i].Payload) ||
				fmt.Sprint(got[i].Tags) != fmt.Sprint(want[i].Tags) {
				t.Fatalf("filter %+v: record %d differs: batch=%+v single=%+v", f, i, got[i], want[i])
			}
		}
	}
}

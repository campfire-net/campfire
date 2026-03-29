package cmd

import (
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
)

// TestCursorAdvancesOnPreFilterMessages verifies that cursor advancement
// uses pre-filter timestamps, so filtered-out messages don't reappear
// on the next read. Regression test for bug fixed in c25edc6.
func TestCursorAdvancesOnPreFilterMessages(t *testing.T) {
	dir := t.TempDir()

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	cfID := "cf-cursor-test"
	s.AddMembership(store.Membership{
		CampfireID: cfID, TransportDir: "/tmp", JoinProtocol: "open",
		Role: "member", JoinedAt: 1,
	})

	// Add messages: one with "blocker" tag at t=1000, one with "status" at t=2000.
	s.AddMessage(store.MessageRecord{
		ID: "msg-blocker", CampfireID: cfID, Sender: "aabbcc",
		Payload: []byte("blocked"), Tags: []string{"blocker"}, Antecedents: nil,
		Timestamp: 1000, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 1000,
	})
	s.AddMessage(store.MessageRecord{
		ID: "msg-status", CampfireID: cfID, Sender: "aabbcc",
		Payload: []byte("status update"), Tags: []string{"status"}, Antecedents: nil,
		Timestamp: 2000, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 2000,
	})

	// Simulate reading with --tag blocker filter.
	// Pre-filter: both messages (t=1000, t=2000). Cursor should advance to 2000.
	// Post-filter: only msg-blocker shown.
	allMsgs, err := s.ListMessages(cfID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	// Compute pre-filter cursor (should be 2000, not 1000).
	var preCursorTS int64
	for _, m := range allMsgs {
		if m.Timestamp > preCursorTS {
			preCursorTS = m.Timestamp
		}
	}

	// Apply filter.
	filtered := filterMessages(allMsgs, store.MessageFilter{Tags: []string{"blocker"}})
	if len(filtered) != 1 {
		t.Fatalf("expected 1 filtered message, got %d", len(filtered))
	}

	// Set cursor from pre-filter timestamp.
	s.SetReadCursor(cfID, preCursorTS)

	// Next read should return no messages (cursor is past both).
	nextMsgs, err := s.ListMessages(cfID, preCursorTS)
	if err != nil {
		t.Fatalf("ListMessages after cursor: %v", err)
	}
	if len(nextMsgs) != 0 {
		t.Errorf("expected 0 messages after cursor advance, got %d (filtered-out message reappeared)", len(nextMsgs))
	}
}

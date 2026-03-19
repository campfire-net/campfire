package store

import (
	"path/filepath"
	"sync"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenClose(t *testing.T) {
	s := testStore(t)
	if s == nil {
		t.Fatal("store should not be nil")
	}
}

func TestAddListMembership(t *testing.T) {
	s := testStore(t)

	m := Membership{
		CampfireID:   "abc123",
		TransportDir: "/tmp/campfire/abc123",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	}
	if err := s.AddMembership(m); err != nil {
		t.Fatalf("AddMembership() error: %v", err)
	}

	memberships, err := s.ListMemberships()
	if err != nil {
		t.Fatalf("ListMemberships() error: %v", err)
	}
	if len(memberships) != 1 {
		t.Fatalf("got %d memberships, want 1", len(memberships))
	}
	if memberships[0].CampfireID != "abc123" {
		t.Errorf("campfire_id = %s, want abc123", memberships[0].CampfireID)
	}
	if memberships[0].Role != "creator" {
		t.Errorf("role = %s, want creator", memberships[0].Role)
	}
}

func TestGetMembership(t *testing.T) {
	s := testStore(t)

	// Not found
	m, err := s.GetMembership("nonexistent")
	if err != nil {
		t.Fatalf("GetMembership() error: %v", err)
	}
	if m != nil {
		t.Error("should return nil for nonexistent membership")
	}

	// Found
	s.AddMembership(Membership{
		CampfireID:   "abc123",
		TransportDir: "/tmp/campfire/abc123",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	})
	m, err = s.GetMembership("abc123")
	if err != nil {
		t.Fatalf("GetMembership() error: %v", err)
	}
	if m == nil {
		t.Fatal("should return membership")
	}
	if m.TransportDir != "/tmp/campfire/abc123" {
		t.Errorf("transport_dir = %s, want /tmp/campfire/abc123", m.TransportDir)
	}
}

func TestMessageInstanceField(t *testing.T) {
	s := testStore(t)

	// Add a membership so FK is satisfied
	s.AddMembership(Membership{
		CampfireID:   "cf1",
		TransportDir: "/tmp/campfire/cf1",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	})

	// Insert message with instance
	rec := MessageRecord{
		ID:          "msg-inst-1",
		CampfireID:  "cf1",
		Sender:      "aabbcc",
		Payload:     []byte("hello"),
		Tags:        `["test"]`,
		Antecedents: `[]`,
		Timestamp:   1000,
		Signature:   []byte("sig"),
		Provenance:  `[]`,
		ReceivedAt:  2000,
		Instance:    "strategist",
	}
	inserted, err := s.AddMessage(rec)
	if err != nil {
		t.Fatalf("AddMessage() error: %v", err)
	}
	if !inserted {
		t.Fatal("message should have been inserted")
	}

	// Retrieve and verify instance is stored
	got, err := s.GetMessage("msg-inst-1")
	if err != nil {
		t.Fatalf("GetMessage() error: %v", err)
	}
	if got.Instance != "strategist" {
		t.Errorf("instance = %q, want %q", got.Instance, "strategist")
	}

	// List messages and verify instance
	msgs, err := s.ListMessages("cf1", 0)
	if err != nil {
		t.Fatalf("ListMessages() error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Instance != "strategist" {
		t.Errorf("listed instance = %q, want %q", msgs[0].Instance, "strategist")
	}
}

func TestMessageInstanceFieldBackwardCompat(t *testing.T) {
	s := testStore(t)

	s.AddMembership(Membership{
		CampfireID:   "cf1",
		TransportDir: "/tmp/campfire/cf1",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	})

	// Insert message without instance (empty string)
	rec := MessageRecord{
		ID:          "msg-no-inst",
		CampfireID:  "cf1",
		Sender:      "aabbcc",
		Payload:     []byte("hello"),
		Tags:        `["test"]`,
		Antecedents: `[]`,
		Timestamp:   1000,
		Signature:   []byte("sig"),
		Provenance:  `[]`,
		ReceivedAt:  2000,
	}
	_, err := s.AddMessage(rec)
	if err != nil {
		t.Fatalf("AddMessage() error: %v", err)
	}

	got, err := s.GetMessage("msg-no-inst")
	if err != nil {
		t.Fatalf("GetMessage() error: %v", err)
	}
	if got.Instance != "" {
		t.Errorf("instance = %q, want empty string", got.Instance)
	}
}

func TestMembershipDescription(t *testing.T) {
	s := testStore(t)

	// AddMembership with description, GetMembership returns it.
	if err := s.AddMembership(Membership{
		CampfireID:   "desc-test",
		TransportDir: "/tmp/campfire/desc-test",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     2000,
		Description:  "test campfire purpose",
	}); err != nil {
		t.Fatalf("AddMembership() error: %v", err)
	}

	m, err := s.GetMembership("desc-test")
	if err != nil {
		t.Fatalf("GetMembership() error: %v", err)
	}
	if m == nil {
		t.Fatal("should return membership")
	}
	if m.Description != "test campfire purpose" {
		t.Errorf("description = %q, want %q", m.Description, "test campfire purpose")
	}

	// ListMemberships also returns description.
	memberships, err := s.ListMemberships()
	if err != nil {
		t.Fatalf("ListMemberships() error: %v", err)
	}
	if len(memberships) != 1 {
		t.Fatalf("got %d memberships, want 1", len(memberships))
	}
	if memberships[0].Description != "test campfire purpose" {
		t.Errorf("listed description = %q, want %q", memberships[0].Description, "test campfire purpose")
	}
}

func TestMembershipDescriptionEmpty(t *testing.T) {
	s := testStore(t)

	// Backward compatible: membership without description defaults to empty string.
	if err := s.AddMembership(Membership{
		CampfireID:   "no-desc",
		TransportDir: "/tmp/campfire/no-desc",
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     3000,
	}); err != nil {
		t.Fatalf("AddMembership() error: %v", err)
	}

	m, err := s.GetMembership("no-desc")
	if err != nil {
		t.Fatalf("GetMembership() error: %v", err)
	}
	if m.Description != "" {
		t.Errorf("description = %q, want empty string", m.Description)
	}
}

func TestRemoveMembership(t *testing.T) {
	s := testStore(t)

	s.AddMembership(Membership{
		CampfireID:   "abc123",
		TransportDir: "/tmp/campfire/abc123",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	})

	if err := s.RemoveMembership("abc123"); err != nil {
		t.Fatalf("RemoveMembership() error: %v", err)
	}

	memberships, _ := s.ListMemberships()
	if len(memberships) != 0 {
		t.Errorf("got %d memberships after remove, want 0", len(memberships))
	}
}

func TestGetMessageByPrefix_ExactMatch(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	msg := MessageRecord{
		ID: "abc12345-6789-0000-0000-000000000000", CampfireID: "cf1",
		Sender: "sender1", Payload: []byte("hello"), Tags: "[]", Antecedents: "[]",
		Timestamp: 100, Signature: []byte("sig"), Provenance: "[]", ReceivedAt: 200,
	}
	s.AddMessage(msg)

	got, err := s.GetMessageByPrefix(msg.ID)
	if err != nil {
		t.Fatalf("GetMessageByPrefix() error: %v", err)
	}
	if got == nil {
		t.Fatal("expected message, got nil")
	}
	if got.ID != msg.ID {
		t.Errorf("ID = %s, want %s", got.ID, msg.ID)
	}
}

func TestGetMessageByPrefix_PrefixMatch(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	msg := MessageRecord{
		ID: "abc12345-6789-0000-0000-000000000000", CampfireID: "cf1",
		Sender: "sender1", Payload: []byte("hello"), Tags: "[]", Antecedents: "[]",
		Timestamp: 100, Signature: []byte("sig"), Provenance: "[]", ReceivedAt: 200,
	}
	s.AddMessage(msg)

	got, err := s.GetMessageByPrefix("abc123")
	if err != nil {
		t.Fatalf("GetMessageByPrefix() error: %v", err)
	}
	if got == nil {
		t.Fatal("expected message, got nil")
	}
	if got.ID != msg.ID {
		t.Errorf("ID = %s, want %s", got.ID, msg.ID)
	}
}

func TestGetMessageByPrefix_NotFound(t *testing.T) {
	s := testStore(t)

	got, err := s.GetMessageByPrefix("nonexistent")
	if err != nil {
		t.Fatalf("GetMessageByPrefix() error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got message with ID %s", got.ID)
	}
}

func TestGetMessageByPrefix_Ambiguous(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	for _, id := range []string{
		"abc12345-aaaa-0000-0000-000000000000",
		"abc12345-bbbb-0000-0000-000000000000",
	} {
		s.AddMessage(MessageRecord{
			ID: id, CampfireID: "cf1", Sender: "s", Payload: []byte("p"),
			Tags: "[]", Antecedents: "[]", Timestamp: 100, Signature: []byte("s"),
			Provenance: "[]", ReceivedAt: 200,
		})
	}

	_, err := s.GetMessageByPrefix("abc123")
	if err == nil {
		t.Fatal("expected error for ambiguous prefix, got nil")
	}
}

func TestGetMessageByPrefix_CrossCampfire(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})
	s.AddMembership(Membership{CampfireID: "cf2", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	msg := MessageRecord{
		ID: "xyz99999-0000-0000-0000-000000000000", CampfireID: "cf2",
		Sender: "sender2", Payload: []byte("from cf2"), Tags: "[]", Antecedents: "[]",
		Timestamp: 100, Signature: []byte("sig"), Provenance: "[]", ReceivedAt: 200,
	}
	s.AddMessage(msg)

	got, err := s.GetMessageByPrefix("xyz999")
	if err != nil {
		t.Fatalf("GetMessageByPrefix() error: %v", err)
	}
	if got == nil {
		t.Fatal("expected message, got nil")
	}
	if got.CampfireID != "cf2" {
		t.Errorf("CampfireID = %s, want cf2", got.CampfireID)
	}
}

// --- workspace-pik: ClaimPendingThresholdShare concurrent race ---

// TestClaimPendingThresholdShareConcurrent verifies that when two goroutines
// race to claim the single pending share for a campfire, exactly one succeeds
// (gets non-nil shareData) and the other gets nil or an error. The invariant
// is that a single stored share can only be delivered once.
func TestClaimPendingThresholdShareConcurrent(t *testing.T) {
	s := testStore(t)
	campfireID := "cf-concurrent-claim-test"

	// Store exactly one pending share.
	shareData := []byte("test-dkg-share-data")
	if err := s.StorePendingThresholdShare(campfireID, 1, shareData); err != nil {
		t.Fatalf("StorePendingThresholdShare: %v", err)
	}

	type result struct {
		pid  uint32
		data []byte
		err  error
	}

	results := make([]result, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			pid, data, err := s.ClaimPendingThresholdShare(campfireID)
			results[i] = result{pid: pid, data: data, err: err}
		}()
	}
	wg.Wait()

	// Count successes (non-nil data).
	successes := 0
	for _, r := range results {
		if r.err != nil {
			t.Logf("goroutine got error: %v", r.err)
			continue
		}
		if r.data != nil {
			successes++
		}
	}

	if successes != 1 {
		t.Errorf("expected exactly 1 goroutine to claim the share, got %d", successes)
	}
}

// TestClaimPendingThresholdShare_EmptyPool verifies that ClaimPendingThresholdShare
// returns nil without error when no shares are available.
func TestClaimPendingThresholdShare_EmptyPool(t *testing.T) {
	s := testStore(t)
	campfireID := "cf-empty-pool-test"

	pid, data, err := s.ClaimPendingThresholdShare(campfireID)
	if err != nil {
		t.Fatalf("ClaimPendingThresholdShare on empty pool: %v", err)
	}
	if data != nil {
		t.Errorf("expected nil data for empty pool, got pid=%d data=%v", pid, data)
	}
}

// TestClaimPendingThresholdShare_ExhaustsAll verifies that after all shares are
// claimed, subsequent calls return nil without error.
func TestClaimPendingThresholdShare_ExhaustsAll(t *testing.T) {
	s := testStore(t)
	campfireID := "cf-exhaust-test"

	// Store 2 shares.
	if err := s.StorePendingThresholdShare(campfireID, 1, []byte("share-1")); err != nil {
		t.Fatalf("StorePendingThresholdShare(1): %v", err)
	}
	if err := s.StorePendingThresholdShare(campfireID, 2, []byte("share-2")); err != nil {
		t.Fatalf("StorePendingThresholdShare(2): %v", err)
	}

	// Claim both shares.
	for i := 0; i < 2; i++ {
		pid, data, err := s.ClaimPendingThresholdShare(campfireID)
		if err != nil {
			t.Fatalf("claim %d: unexpected error: %v", i+1, err)
		}
		if data == nil {
			t.Fatalf("claim %d: expected non-nil data", i+1)
		}
		if pid == 0 {
			t.Fatalf("claim %d: expected non-zero participant_id", i+1)
		}
	}

	// Third claim should return nil (pool exhausted).
	pid, data, err := s.ClaimPendingThresholdShare(campfireID)
	if err != nil {
		t.Fatalf("claim on exhausted pool: unexpected error: %v", err)
	}
	if data != nil {
		t.Errorf("expected nil data after pool exhausted, got pid=%d data=%v", pid, data)
	}
}

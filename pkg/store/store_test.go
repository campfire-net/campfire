package store

import (
	"encoding/json"
	"path/filepath"
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

// helpers shared across ListMessages filter tests.
func setupFilterTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	s := testStore(t)
	cfID := "filter-cf"
	s.AddMembership(Membership{CampfireID: cfID, TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})
	msgs := []MessageRecord{
		{ID: "m1", CampfireID: cfID, Sender: "aabbccdd", Payload: []byte("p1"), Tags: `["status"]`, Antecedents: "[]", Timestamp: 1, Signature: []byte("s"), Provenance: "[]", ReceivedAt: 10},
		{ID: "m2", CampfireID: cfID, Sender: "aabbccdd", Payload: []byte("p2"), Tags: `["blocker"]`, Antecedents: "[]", Timestamp: 2, Signature: []byte("s"), Provenance: "[]", ReceivedAt: 20},
		{ID: "m3", CampfireID: cfID, Sender: "11223344", Payload: []byte("p3"), Tags: `["status","finding"]`, Antecedents: "[]", Timestamp: 3, Signature: []byte("s"), Provenance: "[]", ReceivedAt: 30},
		{ID: "m4", CampfireID: cfID, Sender: "11223344", Payload: []byte("p4"), Tags: `[]`, Antecedents: "[]", Timestamp: 4, Signature: []byte("s"), Provenance: "[]", ReceivedAt: 40},
	}
	for _, m := range msgs {
		if _, err := s.AddMessage(m); err != nil {
			t.Fatalf("AddMessage(%s): %v", m.ID, err)
		}
	}
	return s, cfID
}

func TestListMessages_NoFilter_ReturnsAll(t *testing.T) {
	s, cfID := setupFilterTestStore(t)
	msgs, err := s.ListMessages(cfID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Errorf("got %d messages, want 4", len(msgs))
	}
}

func TestListMessages_TagFilter_SingleTag(t *testing.T) {
	s, cfID := setupFilterTestStore(t)
	msgs, err := s.ListMessages(cfID, 0, MessageFilter{Tags: []string{"status"}})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	// m1 has "status", m3 has "status" and "finding"
	if len(msgs) != 2 {
		t.Errorf("got %d messages, want 2", len(msgs))
	}
	ids := map[string]bool{msgs[0].ID: true, msgs[1].ID: true}
	if !ids["m1"] || !ids["m3"] {
		t.Errorf("expected m1 and m3, got %v", ids)
	}
}

func TestListMessages_TagFilter_MultipleTagsOR(t *testing.T) {
	s, cfID := setupFilterTestStore(t)
	msgs, err := s.ListMessages(cfID, 0, MessageFilter{Tags: []string{"blocker", "finding"}})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	// m2 has "blocker", m3 has "finding"
	if len(msgs) != 2 {
		t.Errorf("got %d messages, want 2", len(msgs))
	}
	ids := map[string]bool{msgs[0].ID: true, msgs[1].ID: true}
	if !ids["m2"] || !ids["m3"] {
		t.Errorf("expected m2 and m3, got %v", ids)
	}
}

func TestListMessages_TagFilter_CaseInsensitive(t *testing.T) {
	s, cfID := setupFilterTestStore(t)
	msgs, err := s.ListMessages(cfID, 0, MessageFilter{Tags: []string{"STATUS"}})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("got %d messages (case-insensitive), want 2", len(msgs))
	}
}

func TestListMessages_TagFilter_NoMatch(t *testing.T) {
	s, cfID := setupFilterTestStore(t)
	msgs, err := s.ListMessages(cfID, 0, MessageFilter{Tags: []string{"nonexistent"}})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("got %d messages, want 0", len(msgs))
	}
}

func TestListMessages_SenderFilter_Prefix(t *testing.T) {
	s, cfID := setupFilterTestStore(t)
	msgs, err := s.ListMessages(cfID, 0, MessageFilter{Sender: "aabb"})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	// m1 and m2 have sender "aabbccdd"
	if len(msgs) != 2 {
		t.Errorf("got %d messages, want 2", len(msgs))
	}
	ids := map[string]bool{msgs[0].ID: true, msgs[1].ID: true}
	if !ids["m1"] || !ids["m2"] {
		t.Errorf("expected m1 and m2, got %v", ids)
	}
}

func TestListMessages_SenderFilter_CaseInsensitive(t *testing.T) {
	s, cfID := setupFilterTestStore(t)
	msgs, err := s.ListMessages(cfID, 0, MessageFilter{Sender: "AABB"})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("got %d messages (case-insensitive sender), want 2", len(msgs))
	}
}

func TestListMessages_BothFilters(t *testing.T) {
	s, cfID := setupFilterTestStore(t)
	// sender aabb + tag status → only m1
	msgs, err := s.ListMessages(cfID, 0, MessageFilter{Tags: []string{"status"}, Sender: "aabb"})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].ID != "m1" {
		t.Errorf("ID = %s, want m1", msgs[0].ID)
	}
}

func TestListMessages_EmptyFilter_ReturnsAll(t *testing.T) {
	s, cfID := setupFilterTestStore(t)
	msgs, err := s.ListMessages(cfID, 0, MessageFilter{})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Errorf("got %d messages with empty filter, want 4", len(msgs))
	}
}

// --- Compaction tests ---

// setupCompactionTestStore creates a store with a campfire and a few messages,
// then adds a compaction event that supersedes the first two.
// Returns (store, campfireID, supersededIDs, compactionEventID).
func setupCompactionTestStore(t *testing.T) (*Store, string, []string, string) {
	t.Helper()
	s := testStore(t)
	cfID := "compact-cf"
	s.AddMembership(Membership{CampfireID: cfID, TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	msgs := []MessageRecord{
		{ID: "c1", CampfireID: cfID, Sender: "aa", Payload: []byte("old1"), Tags: `["status"]`, Antecedents: "[]", Timestamp: 1, Signature: []byte("s"), Provenance: "[]", ReceivedAt: 10},
		{ID: "c2", CampfireID: cfID, Sender: "aa", Payload: []byte("old2"), Tags: `["status"]`, Antecedents: "[]", Timestamp: 2, Signature: []byte("s"), Provenance: "[]", ReceivedAt: 20},
		{ID: "c3", CampfireID: cfID, Sender: "bb", Payload: []byte("new1"), Tags: `["status"]`, Antecedents: "[]", Timestamp: 3, Signature: []byte("s"), Provenance: "[]", ReceivedAt: 30},
	}
	for _, m := range msgs {
		if _, err := s.AddMessage(m); err != nil {
			t.Fatalf("AddMessage(%s): %v", m.ID, err)
		}
	}

	// Build compaction payload superseding c1 and c2.
	superseded := []string{"c1", "c2"}
	payload := CompactionPayload{
		Supersedes:     superseded,
		Summary:        []byte("summary of c1 and c2"),
		Retention:      "archive",
		CheckpointHash: "deadbeef",
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling compaction payload: %v", err)
	}

	compactionMsg := MessageRecord{
		ID:          "compact-ev1",
		CampfireID:  cfID,
		Sender:      "aa",
		Payload:     payloadJSON,
		Tags:        `["campfire:compact"]`,
		Antecedents: `["c2"]`,
		Timestamp:   4,
		Signature:   []byte("s"),
		Provenance:  "[]",
		ReceivedAt:  40,
	}
	if _, err := s.AddMessage(compactionMsg); err != nil {
		t.Fatalf("AddMessage(compact-ev1): %v", err)
	}

	return s, cfID, superseded, "compact-ev1"
}

func TestListCompactionEvents_ReturnsCompactionMessages(t *testing.T) {
	s, cfID, _, evID := setupCompactionTestStore(t)
	events, err := s.ListCompactionEvents(cfID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d compaction events, want 1", len(events))
	}
	if events[0].ID != evID {
		t.Errorf("compaction event ID = %s, want %s", events[0].ID, evID)
	}
}

func TestListCompactionEvents_Empty(t *testing.T) {
	s, cfID, _, _ := setupCompactionTestStore(t)
	// Query a different campfire — should return nothing.
	s.AddMembership(Membership{CampfireID: "other-cf", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})
	events, err := s.ListCompactionEvents("other-cf")
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events for campfire with no compaction, want 0", len(events))
	}
	_ = cfID
}

func TestListMessages_RespectCompaction_ExcludesSuperseded(t *testing.T) {
	s, cfID, superseded, evID := setupCompactionTestStore(t)
	msgs, err := s.ListMessages(cfID, 0, MessageFilter{RespectCompaction: true})
	if err != nil {
		t.Fatalf("ListMessages(RespectCompaction): %v", err)
	}

	// Should have c3 + compact-ev1 (2 messages), not c1 or c2.
	ids := make(map[string]bool)
	for _, m := range msgs {
		ids[m.ID] = true
	}
	for _, id := range superseded {
		if ids[id] {
			t.Errorf("superseded message %s should not appear when RespectCompaction=true", id)
		}
	}
	if !ids["c3"] {
		t.Error("non-superseded message c3 should appear")
	}
	if !ids[evID] {
		t.Errorf("compaction event %s should always appear", evID)
	}
}

func TestListMessages_CompactionEventAlwaysVisible(t *testing.T) {
	s, cfID, _, evID := setupCompactionTestStore(t)

	// Default (no compaction filtering): compaction event visible.
	msgs, err := s.ListMessages(cfID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	ids := make(map[string]bool)
	for _, m := range msgs {
		ids[m.ID] = true
	}
	if !ids[evID] {
		t.Errorf("compaction event %s should be visible in default read", evID)
	}
}

func TestListMessages_NoRespectCompaction_ShowsAll(t *testing.T) {
	s, cfID, _, _ := setupCompactionTestStore(t)
	// Without RespectCompaction, all 4 messages (c1, c2, c3, compact-ev1) are returned.
	msgs, err := s.ListMessages(cfID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Errorf("got %d messages without compaction filter, want 4", len(msgs))
	}
}

func TestListMessages_RespectCompaction_MultipleEvents(t *testing.T) {
	s := testStore(t)
	cfID := "multi-compact-cf"
	s.AddMembership(Membership{CampfireID: cfID, TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	for i, id := range []string{"m1", "m2", "m3", "m4", "m5"} {
		s.AddMessage(MessageRecord{
			ID: id, CampfireID: cfID, Sender: "aa",
			Payload: []byte("p"), Tags: `["status"]`, Antecedents: "[]",
			Timestamp: int64(i + 1), Signature: []byte("s"), Provenance: "[]", ReceivedAt: int64(i + 10),
		})
	}

	// Two compaction events: ev1 supersedes m1+m2, ev2 supersedes m3.
	for _, ev := range []struct {
		id        string
		supersede []string
		ts        int64
	}{
		{"ev1", []string{"m1", "m2"}, 6},
		{"ev2", []string{"m3"}, 7},
	} {
		p, _ := json.Marshal(CompactionPayload{Supersedes: ev.supersede, Retention: "archive", CheckpointHash: "hash"})
		s.AddMessage(MessageRecord{
			ID: ev.id, CampfireID: cfID, Sender: "aa",
			Payload: p, Tags: `["campfire:compact"]`, Antecedents: "[]",
			Timestamp: ev.ts, Signature: []byte("s"), Provenance: "[]", ReceivedAt: ev.ts + 100,
		})
	}

	msgs, err := s.ListMessages(cfID, 0, MessageFilter{RespectCompaction: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	ids := make(map[string]bool)
	for _, m := range msgs {
		ids[m.ID] = true
	}
	// m4, m5, ev1, ev2 should appear. m1, m2, m3 should not.
	for _, bad := range []string{"m1", "m2", "m3"} {
		if ids[bad] {
			t.Errorf("message %s should be excluded by compaction", bad)
		}
	}
	for _, good := range []string{"m4", "m5", "ev1", "ev2"} {
		if !ids[good] {
			t.Errorf("message %s should be visible", good)
		}
	}
}

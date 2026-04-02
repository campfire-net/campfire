package store

import (
	"errors"
	"os"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// --- campfire-agent-say: ValidateCompactionBytes tests ---

func TestValidateCompactionBytes_Consistent(t *testing.T) {
	// Superseded messages have payloads of 10 + 20 = 30 bytes.
	lookup := func(id string) ([]byte, error) {
		switch id {
		case "msg1":
			return make([]byte, 10), nil
		case "msg2":
			return make([]byte, 20), nil
		default:
			return nil, fmt.Errorf("unknown id: %s", id)
		}
	}
	err := ValidateCompactionBytes([]string{"msg1", "msg2"}, 30, lookup)
	if err != nil {
		t.Fatalf("expected nil error for consistent bytes, got: %v", err)
	}
}

func TestValidateCompactionBytes_Inconsistent(t *testing.T) {
	lookup := func(id string) ([]byte, error) {
		switch id {
		case "msg1":
			return make([]byte, 10), nil
		case "msg2":
			return make([]byte, 20), nil
		default:
			return nil, fmt.Errorf("unknown id: %s", id)
		}
	}
	err := ValidateCompactionBytes([]string{"msg1", "msg2"}, 999, lookup)
	if err == nil {
		t.Fatal("expected error for inconsistent bytes, got nil")
	}
	if !errors.Is(err, ErrCompactionBytesInconsistent) {
		t.Fatalf("expected ErrCompactionBytesInconsistent, got: %v", err)
	}
}

func TestValidateCompactionBytes_ZeroSkipped(t *testing.T) {
	// BytesSuperseded == 0 means old client; skip validation.
	called := false
	lookup := func(id string) ([]byte, error) {
		called = true
		return nil, nil
	}
	err := ValidateCompactionBytes([]string{"msg1"}, 0, lookup)
	if err != nil {
		t.Fatalf("expected nil error for zero BytesSuperseded, got: %v", err)
	}
	if called {
		t.Error("lookup should not be called when BytesSuperseded is zero")
	}
}

func TestValidateCompactionBytes_NonzeroWithEmptySupersedes(t *testing.T) {
	lookup := func(id string) ([]byte, error) {
		return nil, fmt.Errorf("should not be called")
	}
	err := ValidateCompactionBytes(nil, 100, lookup)
	if err == nil {
		t.Fatal("expected error for nonzero bytes with empty Supersedes, got nil")
	}
	if !errors.Is(err, ErrCompactionBytesInconsistent) {
		t.Fatalf("expected ErrCompactionBytesInconsistent, got: %v", err)
	}
}

// --- workspace-pyw: exact tag matching ---

// TestHasTag_ExactMatch verifies that HasTag matches the exact tag string.
func TestHasTag_ExactMatch(t *testing.T) {
	if !HasTag([]string{"campfire:compact"}, "campfire:compact") {
		t.Error("HasTag should match exact tag")
	}
}

// TestHasTag_NoFalsePositive is the security regression test for workspace-pyw.
// "xycampfire:compact" must NOT match a query for "campfire:compact".
func TestHasTag_NoFalsePositive(t *testing.T) {
	if HasTag([]string{"xycampfire:compact"}, "campfire:compact") {
		t.Error("HasTag must not match a tag that merely contains the substring")
	}
}

// TestHasTag_MultipleTagsNoFalsePositive verifies multi-element arrays.
func TestHasTag_MultipleTagsNoFalsePositive(t *testing.T) {
	if HasTag([]string{"status", "xycampfire:compact", "other"}, "campfire:compact") {
		t.Error("HasTag must not match on substring in multi-tag array")
	}
}

// TestIsCompactionEvent verifies that isCompactionEvent only fires on exact tag.
func TestIsCompactionEvent_Exact(t *testing.T) {
	rec := MessageRecord{Tags: []string{"campfire:compact"}}
	if !isCompactionEvent(rec) {
		t.Error("isCompactionEvent should return true for exact campfire:compact tag")
	}
}

// TestIsCompactionEvent_NoFalsePositive is the security regression test for workspace-pyw.
func TestIsCompactionEvent_NoFalsePositive(t *testing.T) {
	rec := MessageRecord{Tags: []string{"xycampfire:compact"}}
	if isCompactionEvent(rec) {
		t.Error("isCompactionEvent must not fire for a tag that only contains campfire:compact as a substring")
	}
}

// --- workspace-kw9: ListReferencingMessages LIKE injection ---

// TestListReferencingMessages_WildcardID is the security regression test for workspace-kw9.
// An ID containing SQL LIKE wildcards ('%' or '_') must not cause false matches.
func TestListReferencingMessages_WildcardID(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	// Message A: references a normal ID.
	normalID := "aabbccdd-0000-0000-0000-000000000001"
	msgA := MessageRecord{
		ID: "msg-a", CampfireID: "cf1", Sender: "s",
		Payload: []byte("a"), Tags: []string{},
		Antecedents: []string{normalID},
		Timestamp: 100, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 200,
	}
	s.AddMessage(msgA) //nolint:errcheck

	// Message B: references an ID that shares a common prefix with the wildcard query below.
	otherID := "aabbccdd-0000-0000-0000-000000000002"
	msgB := MessageRecord{
		ID: "msg-b", CampfireID: "cf1", Sender: "s",
		Payload: []byte("b"), Tags: []string{},
		Antecedents: []string{otherID},
		Timestamp: 101, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 201,
	}
	s.AddMessage(msgB) //nolint:errcheck

	// Query with an ID containing '%': must only match exact references.
	wildcardID := "aabbccdd-0000-0000-0000-0000000000%"
	refs, err := s.ListReferencingMessages(wildcardID)
	if err != nil {
		t.Fatalf("ListReferencingMessages() error: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("expected 0 results for wildcard ID query, got %d (LIKE injection)", len(refs))
	}
}

// TestListReferencingMessages_ExactMatch verifies the normal path still works.
func TestListReferencingMessages_ExactMatch(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	targetID := "target-id-0000-0000-0000-000000000001"
	msgA := MessageRecord{
		ID: "msg-ref", CampfireID: "cf1", Sender: "s",
		Payload: []byte("references target"), Tags: []string{},
		Antecedents: []string{targetID},
		Timestamp: 100, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 200,
	}
	s.AddMessage(msgA) //nolint:errcheck

	// Unrelated message.
	msgB := MessageRecord{
		ID: "msg-unrelated", CampfireID: "cf1", Sender: "s",
		Payload: []byte("unrelated"), Tags: []string{},
		Antecedents: []string{},
		Timestamp: 101, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 201,
	}
	s.AddMessage(msgB) //nolint:errcheck

	refs, err := s.ListReferencingMessages(targetID)
	if err != nil {
		t.Fatalf("ListReferencingMessages() error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 referencing message, got %d", len(refs))
	}
	if refs[0].ID != "msg-ref" {
		t.Errorf("got ID %q, want %q", refs[0].ID, "msg-ref")
	}
}

func testStore(t *testing.T) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s.(*SQLiteStore)
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
		Tags:        []string{"test"},
		Antecedents: []string{},
		Timestamp:   1000,
		Signature:   []byte("sig"),
		Provenance:  nil,
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
		Tags:        []string{"test"},
		Antecedents: []string{},
		Timestamp:   1000,
		Signature:   []byte("sig"),
		Provenance:  nil,
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
		Sender: "sender1", Payload: []byte("hello"), Tags: []string{}, Antecedents: []string{},
		Timestamp: 100, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 200,
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
		Sender: "sender1", Payload: []byte("hello"), Tags: []string{}, Antecedents: []string{},
		Timestamp: 100, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 200,
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
			Tags: []string{}, Antecedents: []string{}, Timestamp: 100, Signature: []byte("s"),
			Provenance: nil, ReceivedAt: 200,
		})
	}

	_, err := s.GetMessageByPrefix("abc123")
	if err == nil {
		t.Fatal("expected error for ambiguous prefix, got nil")
	}
}

// TestGetMessageByPrefix_AmbiguousThreeMatches verifies that a prefix matching
// 3+ messages still returns an ambiguous error. The LIMIT 2 in the query means
// SQLite reads at most 2 rows; the cursor must be released promptly (workspace-0eu).
func TestGetMessageByPrefix_AmbiguousThreeMatches(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	for _, id := range []string{
		"abc12345-aaaa-0000-0000-000000000000",
		"abc12345-bbbb-0000-0000-000000000000",
		"abc12345-cccc-0000-0000-000000000000",
	} {
		s.AddMessage(MessageRecord{
			ID: id, CampfireID: "cf1", Sender: "s", Payload: []byte("p"),
			Tags: nil, Antecedents: nil, Timestamp: 100, Signature: []byte("s"),
			Provenance: nil, ReceivedAt: 200,
		})
	}

	_, err := s.GetMessageByPrefix("abc123")
	if err == nil {
		t.Fatal("expected ambiguous error for prefix matching 3 messages, got nil")
	}
}

func TestGetMessageByPrefix_CrossCampfire(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})
	s.AddMembership(Membership{CampfireID: "cf2", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	msg := MessageRecord{
		ID: "xyz99999-0000-0000-0000-000000000000", CampfireID: "cf2",
		Sender: "sender2", Payload: []byte("from cf2"), Tags: []string{}, Antecedents: []string{},
		Timestamp: 100, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 200,
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

// TestGetMessageByPrefix_PercentWildcardInjection verifies that a '%' prefix
// does not match all messages (wildcard injection prevention, workspace-4dr).
func TestGetMessageByPrefix_PercentWildcardInjection(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	msg := MessageRecord{
		ID: "abc12345-6789-0000-0000-000000000000", CampfireID: "cf1",
		Sender: "sender1", Payload: []byte("hello"), Tags: []string{}, Antecedents: []string{},
		Timestamp: 100, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 200,
	}
	s.AddMessage(msg)

	// A prefix of '%' should match nothing — not all messages.
	got, err := s.GetMessageByPrefix("%")
	if err != nil {
		t.Fatalf("GetMessageByPrefix('%%') error: %v", err)
	}
	if got != nil {
		t.Errorf("GetMessageByPrefix('%%') matched message %s; expected no match (wildcard injection)", got.ID)
	}
}

// TestGetMessageByPrefix_UnderscoreWildcardInjection verifies that '_' in the
// prefix is treated as a literal character, not a LIKE wildcard (workspace-4dr).
func TestGetMessageByPrefix_UnderscoreWildcardInjection(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	msg := MessageRecord{
		ID: "abc12345-6789-0000-0000-000000000000", CampfireID: "cf1",
		Sender: "sender1", Payload: []byte("hello"), Tags: []string{}, Antecedents: []string{},
		Timestamp: 100, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 200,
	}
	s.AddMessage(msg)

	// "_bc12345" should NOT match "abc12345-..." — '_' is a literal, not wildcard.
	got, err := s.GetMessageByPrefix("_bc12345")
	if err != nil {
		t.Fatalf("GetMessageByPrefix('_bc12345') error: %v", err)
	}
	if got != nil {
		t.Errorf("GetMessageByPrefix('_bc12345') matched message %s; expected no match (underscore injection)", got.ID)
	}
}

// helpers shared across ListMessages filter tests.
func setupFilterTestStore(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	s := testStore(t)
	cfID := "filter-cf"
	s.AddMembership(Membership{CampfireID: cfID, TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})
	msgs := []MessageRecord{
		{ID: "m1", CampfireID: cfID, Sender: "aabbccdd", Payload: []byte("p1"), Tags: []string{"status"}, Antecedents: []string{}, Timestamp: 1, Signature: []byte("s"), Provenance: nil, ReceivedAt: 10},
		{ID: "m2", CampfireID: cfID, Sender: "aabbccdd", Payload: []byte("p2"), Tags: []string{"blocker"}, Antecedents: []string{}, Timestamp: 2, Signature: []byte("s"), Provenance: nil, ReceivedAt: 20},
		{ID: "m3", CampfireID: cfID, Sender: "11223344", Payload: []byte("p3"), Tags: []string{"status", "finding"}, Antecedents: []string{}, Timestamp: 3, Signature: []byte("s"), Provenance: nil, ReceivedAt: 30},
		{ID: "m4", CampfireID: cfID, Sender: "11223344", Payload: []byte("p4"), Tags: []string{}, Antecedents: []string{}, Timestamp: 4, Signature: []byte("s"), Provenance: nil, ReceivedAt: 40},
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

// TestListMessages_SenderFilter_PercentWildcardInjection verifies that a Sender
// value of "%" does not match all senders (LIKE wildcard injection, workspace-ipzx).
// The --sender flag is documented as a hex prefix; "%" should match nothing because
// no sender hex string starts with a literal percent sign.
func TestListMessages_SenderFilter_PercentWildcardInjection(t *testing.T) {
	s, cfID := setupFilterTestStore(t)
	msgs, err := s.ListMessages(cfID, 0, MessageFilter{Sender: "%"})
	if err != nil {
		t.Fatalf("ListMessages(Sender=%%): %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("ListMessages(Sender=%%): got %d messages, want 0 (LIKE wildcard injection)", len(msgs))
	}
}

// TestListMessages_SenderFilter_UnderscoreWildcardInjection verifies that '_' in
// Sender is treated as a literal character, not a LIKE wildcard (workspace-ipzx).
func TestListMessages_SenderFilter_UnderscoreWildcardInjection(t *testing.T) {
	s, cfID := setupFilterTestStore(t)
	// "_abbccdd" would match "aabbccdd" if '_' were a wildcard; it must not.
	msgs, err := s.ListMessages(cfID, 0, MessageFilter{Sender: "_abbccdd"})
	if err != nil {
		t.Fatalf("ListMessages(Sender=_abbccdd): %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("ListMessages(Sender=_abbccdd): got %d messages, want 0 (LIKE wildcard injection)", len(msgs))
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
func setupCompactionTestStore(t *testing.T) (*SQLiteStore, string, []string, string) {
	t.Helper()
	s := testStore(t)
	cfID := "compact-cf"
	s.AddMembership(Membership{CampfireID: cfID, TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})

	msgs := []MessageRecord{
		{ID: "c1", CampfireID: cfID, Sender: "aa", Payload: []byte("old1"), Tags: []string{"status"}, Antecedents: []string{}, Timestamp: 1, Signature: []byte("s"), Provenance: nil, ReceivedAt: 10},
		{ID: "c2", CampfireID: cfID, Sender: "aa", Payload: []byte("old2"), Tags: []string{"status"}, Antecedents: []string{}, Timestamp: 2, Signature: []byte("s"), Provenance: nil, ReceivedAt: 20},
		{ID: "c3", CampfireID: cfID, Sender: "bb", Payload: []byte("new1"), Tags: []string{"status"}, Antecedents: []string{}, Timestamp: 3, Signature: []byte("s"), Provenance: nil, ReceivedAt: 30},
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
		Tags:        []string{"campfire:compact"},
		Antecedents: []string{"c2"},
		Timestamp:   4,
		Signature:   []byte("s"),
		Provenance:  nil,
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
			Payload: []byte("p"), Tags: []string{"status"}, Antecedents: []string{},
			Timestamp: int64(i + 1), Signature: []byte("s"), Provenance: nil, ReceivedAt: int64(i + 10),
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
			Payload: p, Tags: []string{"campfire:compact"}, Antecedents: []string{},
			Timestamp: ev.ts, Signature: []byte("s"), Provenance: nil, ReceivedAt: ev.ts + 100,
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

// --- workspace-27q / workspace-2i1: isCompactionMsg uses HasTag ---

// TestIsCompactionEvent_SubstringFalsePositive verifies that the store's
// isCompactionEvent does not fire for tags that merely contain campfire:compact
// as a substring (e.g. "xycampfire:compact" or "campfire:compact-v2").
// This is the existing regression test carried from workspace-pyw; confirmed here
// for workspace-27q parity.
func TestIsCompactionEvent_SubstringFalsePositive(t *testing.T) {
	cases := []struct {
		tags    []string
		wantHit bool
	}{
		{[]string{"campfire:compact"}, true},
		{[]string{"xycampfire:compact"}, false},
		{[]string{"campfire:compact-v2"}, false},
		{[]string{"campfire:compact", "status"}, true},
		{[]string{"status", "campfire:compact"}, true},
		{[]string{}, false},
	}
	for _, tc := range cases {
		rec := MessageRecord{Tags: tc.tags}
		got := isCompactionEvent(rec)
		if got != tc.wantHit {
			t.Errorf("isCompactionEvent(%v) = %v, want %v", tc.tags, got, tc.wantHit)
		}
	}
}

// --- workspace-x9p: collectSupersededIDs cache ---

// TestCollectSupersededIDs_Cache verifies that the superseded-ID cache avoids
// redundant compaction event fetches. We call collectSupersededIDs twice and
// verify the second call hits the cache (same result, no error).
func TestCollectSupersededIDs_Cache(t *testing.T) {
	s := testStore(t)
	campfireID := "cf-cache"
	s.AddMembership(Membership{CampfireID: campfireID, TransportDir: "/tmp", JoinProtocol: "open", Role: "full", JoinedAt: 1}) //nolint:errcheck

	// Add two regular messages.
	m1 := MessageRecord{ID: "msg1", CampfireID: campfireID, Sender: "s", Payload: []byte("a"), Tags: []string{"status"}, Antecedents: []string{}, Timestamp: 100, Signature: []byte("s"), Provenance: nil, ReceivedAt: 100}
	m2 := MessageRecord{ID: "msg2", CampfireID: campfireID, Sender: "s", Payload: []byte("b"), Tags: []string{"status"}, Antecedents: []string{}, Timestamp: 200, Signature: []byte("s"), Provenance: nil, ReceivedAt: 200}
	s.AddMessage(m1) //nolint:errcheck
	s.AddMessage(m2) //nolint:errcheck

	// Add a compaction event superseding msg1 and msg2.
	payload, _ := json.Marshal(CompactionPayload{Supersedes: []string{"msg1", "msg2"}, Summary: []byte("compact"), Retention: "archive", CheckpointHash: "abc"})
	ev := MessageRecord{ID: "ev1", CampfireID: campfireID, Sender: "s", Payload: payload, Tags: []string{"campfire:compact"}, Antecedents: []string{"msg2"}, Timestamp: 300, Signature: []byte("s"), Provenance: nil, ReceivedAt: 300}
	s.AddMessage(ev) //nolint:errcheck

	// First call: cache miss, populates cache.
	sup1, err := s.collectSupersededIDs(campfireID)
	if err != nil {
		t.Fatalf("first collectSupersededIDs: %v", err)
	}
	if !sup1["msg1"] || !sup1["msg2"] {
		t.Errorf("first call: expected msg1 and msg2 in superseded set, got %v", sup1)
	}

	// Second call: should hit cache and return the same result.
	sup2, err := s.collectSupersededIDs(campfireID)
	if err != nil {
		t.Fatalf("second collectSupersededIDs: %v", err)
	}
	if len(sup2) != len(sup1) {
		t.Errorf("cache mismatch: first=%d ids, second=%d ids", len(sup1), len(sup2))
	}
	for id := range sup1 {
		if !sup2[id] {
			t.Errorf("cached result missing id %q", id)
		}
	}

	// Add a new compaction event: the cache must be invalidated (new max timestamp).
	payload2, _ := json.Marshal(CompactionPayload{Supersedes: []string{"msg3"}, Summary: []byte("compact2"), Retention: "archive", CheckpointHash: "def"})
	ev2 := MessageRecord{ID: "ev2", CampfireID: campfireID, Sender: "s", Payload: payload2, Tags: []string{"campfire:compact"}, Antecedents: []string{"ev1"}, Timestamp: 400, Signature: []byte("s"), Provenance: nil, ReceivedAt: 400}
	s.AddMessage(ev2) //nolint:errcheck

	sup3, err := s.collectSupersededIDs(campfireID)
	if err != nil {
		t.Fatalf("third collectSupersededIDs: %v", err)
	}
	// After the new compaction event, msg3 should also appear.
	if !sup3["msg3"] {
		t.Errorf("cache was not invalidated: msg3 not in superseded set after new compaction event")
	}
}

// --- workspace-zqdc: TOCTOU race in collectSupersededIDs cache ---

// TestCollectSupersededIDs_CacheInvalidatedOnNewCompaction is the regression test
// for workspace-zqdc. The previous implementation queried maxCompactionTimestamp
// outside the lock, then checked the cache under a separate lock acquisition.
// A new compaction event inserted between those two operations would cause the
// stale cache to be returned as a valid hit.
//
// The fix: AddMessage invalidates the superseded-ID cache entry for the campfire
// whenever a campfire:compact message is successfully inserted. This ensures that
// any reader running after the insert will always see a cache miss and rebuild
// from the DB, picking up the new compaction event.
//
// This test simulates the race outcome directly: warm the cache, insert a new
// compaction event, then verify that the cache is immediately invalidated (not
// returned as a hit) and that the new superseded ID appears in the next call.
func TestCollectSupersededIDs_CacheInvalidatedOnNewCompaction(t *testing.T) {
	s := testStore(t)
	campfireID := "cf-toctou"
	s.AddMembership(Membership{CampfireID: campfireID, TransportDir: "/tmp", JoinProtocol: "open", Role: "full", JoinedAt: 1}) //nolint:errcheck

	// Add three messages.
	for _, id := range []string{"msg-a", "msg-b", "msg-c"} {
		ts := map[string]int64{"msg-a": 100, "msg-b": 200, "msg-c": 300}[id]
		m := MessageRecord{
			ID: id, CampfireID: campfireID, Sender: "s",
			Payload: []byte("data"), Tags: []string{"status"}, Antecedents: []string{},
			Timestamp: ts, Signature: []byte("s"), Provenance: nil, ReceivedAt: ts,
		}
		if _, err := s.AddMessage(m); err != nil {
			t.Fatalf("AddMessage(%s): %v", id, err)
		}
	}

	// First compaction event: supersedes msg-a and msg-b.
	payload1, _ := json.Marshal(CompactionPayload{
		Supersedes: []string{"msg-a", "msg-b"}, Summary: []byte("c1"), Retention: "archive", CheckpointHash: "h1",
	})
	ev1 := MessageRecord{
		ID: "ev1", CampfireID: campfireID, Sender: "s",
		Payload: payload1, Tags: []string{"campfire:compact"}, Antecedents: []string{},
		Timestamp: 1000, Signature: []byte("s"), Provenance: nil, ReceivedAt: 1000,
	}
	if _, err := s.AddMessage(ev1); err != nil {
		t.Fatalf("AddMessage(ev1): %v", err)
	}

	// Warm the cache. At this point the cache is valid for maxTS=1000,
	// and the superseded set contains {msg-a, msg-b}.
	sup1, err := s.collectSupersededIDs(campfireID)
	if err != nil {
		t.Fatalf("collectSupersededIDs (warm): %v", err)
	}
	if !sup1["msg-a"] || !sup1["msg-b"] {
		t.Fatalf("expected msg-a and msg-b in superseded set after ev1, got %v", sup1)
	}

	// Insert a second compaction event superseding msg-c. This is the concurrent
	// writer in the TOCTOU scenario. With the fix, AddMessage invalidates the cache
	// entry for campfireID before returning, so no subsequent reader can get the
	// stale cache (which did not include msg-c).
	payload2, _ := json.Marshal(CompactionPayload{
		Supersedes: []string{"msg-c"}, Summary: []byte("c2"), Retention: "archive", CheckpointHash: "h2",
	})
	ev2 := MessageRecord{
		ID: "ev2", CampfireID: campfireID, Sender: "s",
		Payload: payload2, Tags: []string{"campfire:compact"}, Antecedents: []string{},
		Timestamp: 2000, Signature: []byte("s"), Provenance: nil, ReceivedAt: 2000,
	}
	if _, err := s.AddMessage(ev2); err != nil {
		t.Fatalf("AddMessage(ev2): %v", err)
	}

	// Now check that the cache was invalidated: msg-c must appear in the superseded set.
	sup2, err := s.collectSupersededIDs(campfireID)
	if err != nil {
		t.Fatalf("collectSupersededIDs (after ev2): %v", err)
	}
	if !sup2["msg-c"] {
		t.Errorf("TOCTOU regression: msg-c was not in superseded set after ev2; cache was not invalidated on AddMessage")
	}
	// Previous IDs must still be present (full rebuild).
	if !sup2["msg-a"] || !sup2["msg-b"] {
		t.Errorf("msg-a and msg-b should still be in superseded set after ev2, got %v", sup2)
	}

	// Verify through ListMessages: msg-c should not appear when RespectCompaction is true.
	msgs, err := s.ListMessages(campfireID, 0, MessageFilter{RespectCompaction: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range msgs {
		if m.ID == "msg-c" {
			t.Errorf("TOCTOU regression: superseded message msg-c appeared in ListMessages result after ev2")
		}
		if m.ID == "msg-a" || m.ID == "msg-b" {
			t.Errorf("superseded message %q appeared in ListMessages result", m.ID)
		}
	}
}

// TestCollectSupersededIDs_NonCompactionInsertDoesNotInvalidateCache verifies that
// inserting a non-compaction message does NOT invalidate the superseded-ID cache.
// Cache invalidation should only happen for campfire:compact messages.
func TestCollectSupersededIDs_NonCompactionInsertDoesNotInvalidateCache(t *testing.T) {
	s := testStore(t)
	campfireID := "cf-noinval"
	s.AddMembership(Membership{CampfireID: campfireID, TransportDir: "/tmp", JoinProtocol: "open", Role: "full", JoinedAt: 1}) //nolint:errcheck

	// Add a message and a compaction event.
	m1 := MessageRecord{ID: "m1", CampfireID: campfireID, Sender: "s", Payload: []byte("a"), Tags: []string{"status"}, Antecedents: []string{}, Timestamp: 100, Signature: []byte("s"), Provenance: nil, ReceivedAt: 100}
	s.AddMessage(m1) //nolint:errcheck

	payload, _ := json.Marshal(CompactionPayload{Supersedes: []string{"m1"}, Summary: []byte("c"), Retention: "archive", CheckpointHash: "h"})
	ev := MessageRecord{ID: "ev", CampfireID: campfireID, Sender: "s", Payload: payload, Tags: []string{"campfire:compact"}, Antecedents: []string{}, Timestamp: 500, Signature: []byte("s"), Provenance: nil, ReceivedAt: 500}
	s.AddMessage(ev) //nolint:errcheck

	// Warm the cache.
	sup1, err := s.collectSupersededIDs(campfireID)
	if err != nil {
		t.Fatalf("collectSupersededIDs (warm): %v", err)
	}
	if !sup1["m1"] {
		t.Fatalf("m1 should be superseded after ev")
	}

	// Insert a regular (non-compaction) message.
	m2 := MessageRecord{ID: "m2", CampfireID: campfireID, Sender: "s", Payload: []byte("b"), Tags: []string{"status"}, Antecedents: []string{}, Timestamp: 600, Signature: []byte("s"), Provenance: nil, ReceivedAt: 600}
	s.AddMessage(m2) //nolint:errcheck

	// The cache should still be valid — m1 should be in the superseded set without a DB rebuild.
	// (We verify correctness here; cache-hit behavior is an implementation detail.)
	sup2, err := s.collectSupersededIDs(campfireID)
	if err != nil {
		t.Fatalf("collectSupersededIDs (after m2): %v", err)
	}
	if !sup2["m1"] {
		t.Errorf("m1 should still be in superseded set after non-compaction insert, got %v", sup2)
	}
	// m2 is a regular message — it should NOT be in the superseded set.
	if sup2["m2"] {
		t.Errorf("m2 should not be in superseded set (it was not superseded by any compaction event)")
	}
}

// --- workspace-7q7: supersededCache returns a copy, not a shared map reference ---

// TestCollectSupersededIDs_CacheReturnsCopy verifies that mutating the map
// returned from a cache hit does not corrupt the cached entry for subsequent callers.
func TestCollectSupersededIDs_CacheReturnsCopy(t *testing.T) {
	s := testStore(t)
	campfireID := "cf-copy"
	s.AddMembership(Membership{CampfireID: campfireID, TransportDir: "/tmp", JoinProtocol: "open", Role: "full", JoinedAt: 1}) //nolint:errcheck

	// Add a regular message and a compaction event superseding it.
	m1 := MessageRecord{ID: "msg1", CampfireID: campfireID, Sender: "s", Payload: []byte("a"), Tags: []string{"status"}, Antecedents: nil, Timestamp: 100, Signature: []byte("s"), Provenance: nil, ReceivedAt: 100}
	s.AddMessage(m1) //nolint:errcheck
	payload, _ := json.Marshal(CompactionPayload{Supersedes: []string{"msg1"}, Summary: []byte("compact"), Retention: "archive", CheckpointHash: "abc"})
	ev := MessageRecord{ID: "ev1", CampfireID: campfireID, Sender: "s", Payload: payload, Tags: []string{"campfire:compact"}, Antecedents: []string{"msg1"}, Timestamp: 200, Signature: []byte("s"), Provenance: nil, ReceivedAt: 200}
	s.AddMessage(ev) //nolint:errcheck

	// First call populates the cache.
	sup1, err := s.collectSupersededIDs(campfireID)
	if err != nil {
		t.Fatalf("first collectSupersededIDs: %v", err)
	}
	if !sup1["msg1"] {
		t.Fatalf("expected msg1 in superseded set, got %v", sup1)
	}

	// Mutate the returned map — simulating a caller that modifies it.
	sup1["injected-id"] = true
	delete(sup1, "msg1")

	// Second call must return a fresh copy of the cached entry, unaffected by
	// the mutation above.
	sup2, err := s.collectSupersededIDs(campfireID)
	if err != nil {
		t.Fatalf("second collectSupersededIDs: %v", err)
	}
	if !sup2["msg1"] {
		t.Errorf("cache was corrupted: msg1 missing from second call after caller mutated first result")
	}
	if sup2["injected-id"] {
		t.Errorf("cache was corrupted: injected-id present in second call after caller mutated first result")
	}
}

// --- workspace-d68: poll cursor uses ReceivedAt, filter must use received_at ---

// TestListMessages_AfterReceivedAt verifies that when AfterReceivedAt is set in
// MessageFilter, the query filters by received_at rather than timestamp. This is
// the regression test for workspace-d68 where the poll cursor (ReceivedAt) was
// used as input to a filter on the timestamp column, causing messages from
// clock-skewed senders to be permanently missed.
func TestListMessages_AfterReceivedAt(t *testing.T) {
	s := testStore(t)
	campfireID := "cf-receivedAt"
	s.AddMembership(Membership{CampfireID: campfireID, TransportDir: "/tmp", JoinProtocol: "open", Role: "full", JoinedAt: 1}) //nolint:errcheck

	now := time.Now().UnixNano()

	// Message with a past Timestamp (sender clock is 60 seconds behind server),
	// but received now (so ReceivedAt is current).
	pastTimestamp := now - int64(60*time.Second)
	msgSkewed := MessageRecord{
		ID: "skewed", CampfireID: campfireID, Sender: "s",
		Payload: []byte("skewed"), Tags: []string{"status"}, Antecedents: []string{},
		Timestamp:  pastTimestamp, // sender's clock is 60s behind
		Signature:  []byte("s"),
		Provenance: nil,
		ReceivedAt: now, // received now by the server
	}

	// Message with a normal Timestamp and ReceivedAt.
	msgNormal := MessageRecord{
		ID: "normal", CampfireID: campfireID, Sender: "s",
		Payload: []byte("normal"), Tags: []string{"status"}, Antecedents: []string{},
		Timestamp:  now,
		Signature:  []byte("s"),
		Provenance: nil,
		ReceivedAt: now + int64(time.Millisecond),
	}

	s.AddMessage(msgSkewed) //nolint:errcheck
	s.AddMessage(msgNormal) //nolint:errcheck

	// Cursor set to 10 minutes ago — both messages have ReceivedAt > cursor.
	cursor := now - int64(10*time.Minute)

	// Filter using AfterReceivedAt (the fix): both messages should appear because
	// their ReceivedAt values are after the cursor, even though msgSkewed's
	// Timestamp is 60 seconds before now.
	msgs, err := s.ListMessages(campfireID, 0, MessageFilter{AfterReceivedAt: cursor})
	if err != nil {
		t.Fatalf("ListMessages with AfterReceivedAt: %v", err)
	}
	ids := make(map[string]bool)
	for _, m := range msgs {
		ids[m.ID] = true
	}
	if !ids["skewed"] {
		t.Error("skewed message (past Timestamp, current ReceivedAt) should appear when filtering by AfterReceivedAt — would have been missed with old timestamp filter")
	}
	if !ids["normal"] {
		t.Error("normal message should appear when filtering by AfterReceivedAt")
	}

	// Contrast: using the old timestamp filter would miss the skewed message.
	// The cursor points to 10 minutes ago; msgSkewed.Timestamp = now-60s which is
	// after the cursor, so in this particular case it would still appear. But if
	// we set the timestamp cursor to NOW, the skewed message would be missed.
	timestampCursor := now // cursor at exactly now
	msgsOldWay, err := s.ListMessages(campfireID, timestampCursor)
	if err != nil {
		t.Fatalf("ListMessages old way: %v", err)
	}
	idsOldWay := make(map[string]bool)
	for _, m := range msgsOldWay {
		idsOldWay[m.ID] = true
	}
	// With timestamp filter at 'now', the skewed message (Timestamp = now-60s) would be excluded.
	if idsOldWay["skewed"] {
		t.Error("(sanity check) old timestamp filter should exclude skewed message when cursor >= message Timestamp")
	}

	// With AfterReceivedAt set to 'now', only msgNormal (ReceivedAt = now+1ms) should appear.
	msgsNewWay, err := s.ListMessages(campfireID, 0, MessageFilter{AfterReceivedAt: now})
	if err != nil {
		t.Fatalf("ListMessages new way (at now): %v", err)
	}
	idsNewWay := make(map[string]bool)
	for _, m := range msgsNewWay {
		idsNewWay[m.ID] = true
	}
	if idsNewWay["skewed"] {
		// skewed message's ReceivedAt == now, so received_at > now is false
		t.Error("skewed message ReceivedAt == cursor should not appear (strict >)")
	}
	if !idsNewWay["normal"] {
		t.Error("normal message ReceivedAt = now+1ms should appear with AfterReceivedAt = now")
	}
}

// --- workspace-pik: ClaimPendingThresholdShare concurrent race ---

// --- workspace-e18n: UpdateCampfireID unit tests ---

// TestUpdateCampfireID_BasicRename verifies that UpdateCampfireID renames
// memberships, messages, read_cursors, peer_endpoints, threshold_shares, and
// pending_threshold_shares from old-id to new-id in a single atomic operation.
func TestUpdateCampfireID_BasicRename(t *testing.T) {
	s := testStore(t)
	oldID := "cf-old-rename"
	newID := "cf-new-rename"

	// Seed membership for oldID.
	if err := s.AddMembership(Membership{
		CampfireID:   oldID,
		TransportDir: "/tmp/campfire/" + oldID,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Seed a message under oldID.
	if _, err := s.AddMessage(MessageRecord{
		ID: "msg-rename-1", CampfireID: oldID, Sender: "aabb",
		Payload: []byte("hello"), Tags: []string{}, Antecedents: []string{},
		Timestamp: 1000, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 2000,
	}); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// Seed a read cursor for oldID.
	if err := s.SetReadCursor(oldID, 999); err != nil {
		t.Fatalf("SetReadCursor: %v", err)
	}

	// Seed a peer endpoint for oldID.
	if err := s.UpsertPeerEndpoint(PeerEndpoint{
		CampfireID: oldID, MemberPubkey: "pubkey1", Endpoint: "http://peer1", ParticipantID: 1,
	}); err != nil {
		t.Fatalf("UpsertPeerEndpoint: %v", err)
	}

	// Seed a threshold share for oldID.
	if err := s.UpsertThresholdShare(ThresholdShare{
		CampfireID: oldID, ParticipantID: 1, SecretShare: []byte("secret"), PublicData: []byte("pub"),
	}); err != nil {
		t.Fatalf("UpsertThresholdShare: %v", err)
	}

	// Seed a pending threshold share for oldID.
	if err := s.StorePendingThresholdShare(oldID, 2, []byte("pending-share")); err != nil {
		t.Fatalf("StorePendingThresholdShare: %v", err)
	}

	// Perform the rename.
	if err := s.UpdateCampfireID(oldID, newID); err != nil {
		t.Fatalf("UpdateCampfireID: %v", err)
	}

	// Verify membership moved to newID.
	m, err := s.GetMembership(newID)
	if err != nil {
		t.Fatalf("GetMembership(newID): %v", err)
	}
	if m == nil {
		t.Fatal("membership should exist under newID after rename")
	}
	old, err := s.GetMembership(oldID)
	if err != nil {
		t.Fatalf("GetMembership(oldID): %v", err)
	}
	if old != nil {
		t.Error("membership should no longer exist under oldID after rename")
	}

	// Verify messages moved to newID.
	msgs, err := s.ListMessages(newID, 0)
	if err != nil {
		t.Fatalf("ListMessages(newID): %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != "msg-rename-1" {
		t.Errorf("expected 1 message under newID, got %d", len(msgs))
	}
	oldMsgs, err := s.ListMessages(oldID, 0)
	if err != nil {
		t.Fatalf("ListMessages(oldID): %v", err)
	}
	if len(oldMsgs) != 0 {
		t.Errorf("expected 0 messages under oldID after rename, got %d", len(oldMsgs))
	}

	// Verify read cursor moved to newID.
	cursor, err := s.GetReadCursor(newID)
	if err != nil {
		t.Fatalf("GetReadCursor(newID): %v", err)
	}
	if cursor != 999 {
		t.Errorf("read cursor under newID = %d, want 999", cursor)
	}
	oldCursor, err := s.GetReadCursor(oldID)
	if err != nil {
		t.Fatalf("GetReadCursor(oldID): %v", err)
	}
	if oldCursor != 0 {
		t.Errorf("read cursor under oldID should be 0 after rename, got %d", oldCursor)
	}

	// Verify peer endpoints moved to newID.
	peers, err := s.ListPeerEndpoints(newID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints(newID): %v", err)
	}
	if len(peers) != 1 || peers[0].MemberPubkey != "pubkey1" {
		t.Errorf("expected 1 peer endpoint under newID, got %d", len(peers))
	}
	oldPeers, err := s.ListPeerEndpoints(oldID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints(oldID): %v", err)
	}
	if len(oldPeers) != 0 {
		t.Errorf("expected 0 peer endpoints under oldID after rename, got %d", len(oldPeers))
	}

	// Verify threshold share moved to newID.
	share, err := s.GetThresholdShare(newID)
	if err != nil {
		t.Fatalf("GetThresholdShare(newID): %v", err)
	}
	if share == nil {
		t.Fatal("threshold share should exist under newID after rename")
	}
	oldShare, err := s.GetThresholdShare(oldID)
	if err != nil {
		t.Fatalf("GetThresholdShare(oldID): %v", err)
	}
	if oldShare != nil {
		t.Error("threshold share should no longer exist under oldID after rename")
	}

	// Verify pending threshold share moved to newID.
	pid, data, err := s.ClaimPendingThresholdShare(newID)
	if err != nil {
		t.Fatalf("ClaimPendingThresholdShare(newID): %v", err)
	}
	if data == nil {
		t.Fatal("pending threshold share should exist under newID after rename")
	}
	if pid != 2 {
		t.Errorf("pending share participantID = %d, want 2", pid)
	}
}

// TestUpdateCampfireID_NonExistentOldID verifies that UpdateCampfireID succeeds
// (no error) when the oldID does not exist in any table. Zero rows are affected
// but the operation is not an error — silently a no-op.
func TestUpdateCampfireID_NonExistentOldID(t *testing.T) {
	s := testStore(t)

	// Call with IDs that don't exist in any table — must not error.
	if err := s.UpdateCampfireID("ghost-old", "ghost-new"); err != nil {
		t.Fatalf("UpdateCampfireID with non-existent oldID should succeed, got: %v", err)
	}

	// Confirm no membership was created for new-id either.
	m, err := s.GetMembership("ghost-new")
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m != nil {
		t.Error("no membership should exist for ghost-new")
	}
}

// TestUpdateCampfireID_ConflictRollback verifies that when newID already exists
// in campfire_memberships, the transaction rolls back and oldID is unchanged.
func TestUpdateCampfireID_ConflictRollback(t *testing.T) {
	s := testStore(t)
	oldID := "cf-conflict-old"
	newID := "cf-conflict-new"

	// Seed both old and new memberships so the rename causes a PK conflict.
	for _, id := range []string{oldID, newID} {
		if err := s.AddMembership(Membership{
			CampfireID:   id,
			TransportDir: "/tmp/campfire/" + id,
			JoinProtocol: "open",
			Role:         "creator",
			JoinedAt:     1000,
		}); err != nil {
			t.Fatalf("AddMembership(%s): %v", id, err)
		}
	}

	// Seed a message under oldID so we can verify it's untouched after rollback.
	if _, err := s.AddMessage(MessageRecord{
		ID: "msg-conflict-1", CampfireID: oldID, Sender: "aabb",
		Payload: []byte("hello"), Tags: []string{}, Antecedents: []string{},
		Timestamp: 1000, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 2000,
	}); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// UpdateCampfireID should fail — newID already exists in memberships.
	err := s.UpdateCampfireID(oldID, newID)
	if err == nil {
		t.Fatal("UpdateCampfireID should return an error when newID already exists (UNIQUE conflict)")
	}

	// Verify oldID membership is still intact (transaction rolled back).
	m, err2 := s.GetMembership(oldID)
	if err2 != nil {
		t.Fatalf("GetMembership(oldID) after failed rename: %v", err2)
	}
	if m == nil {
		t.Error("oldID membership must still exist after rollback")
	}

	// Verify messages under oldID are unchanged (not partially renamed).
	msgs, err3 := s.ListMessages(oldID, 0)
	if err3 != nil {
		t.Fatalf("ListMessages(oldID) after failed rename: %v", err3)
	}
	if len(msgs) != 1 || msgs[0].ID != "msg-conflict-1" {
		t.Errorf("expected 1 message under oldID after rollback, got %d", len(msgs))
	}
}

// TestUpdateCampfireID_PartialState verifies that tables without a matching row
// are silently skipped — only populated tables are renamed.
func TestUpdateCampfireID_PartialState(t *testing.T) {
	s := testStore(t)
	oldID := "cf-partial-old"
	newID := "cf-partial-new"

	// Only seed membership and a peer endpoint — leave messages, read_cursors,
	// threshold_shares, and pending_threshold_shares empty for oldID.
	if err := s.AddMembership(Membership{
		CampfireID:   oldID,
		TransportDir: "/tmp/campfire/" + oldID,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	if err := s.UpsertPeerEndpoint(PeerEndpoint{
		CampfireID: oldID, MemberPubkey: "pk-partial", Endpoint: "http://peer", ParticipantID: 1,
	}); err != nil {
		t.Fatalf("UpsertPeerEndpoint: %v", err)
	}

	// Rename should succeed even though most tables have no rows for oldID.
	if err := s.UpdateCampfireID(oldID, newID); err != nil {
		t.Fatalf("UpdateCampfireID with partial state: %v", err)
	}

	// Membership moved.
	m, err := s.GetMembership(newID)
	if err != nil || m == nil {
		t.Fatalf("membership should exist under newID: err=%v, m=%v", err, m)
	}

	// Peer endpoint moved.
	peers, err := s.ListPeerEndpoints(newID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints(newID): %v", err)
	}
	if len(peers) != 1 || peers[0].MemberPubkey != "pk-partial" {
		t.Errorf("expected 1 peer endpoint under newID, got %d", len(peers))
	}

	// oldID has no membership.
	old, err := s.GetMembership(oldID)
	if err != nil {
		t.Fatalf("GetMembership(oldID): %v", err)
	}
	if old != nil {
		t.Error("oldID membership should be gone after rename")
	}

	// Tables not seeded for oldID should have no rows under newID either.
	msgs, err := s.ListMessages(newID, 0)
	if err != nil {
		t.Fatalf("ListMessages(newID): %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages under newID (none seeded), got %d", len(msgs))
	}
}

// TestUpdateCampfireID_InvalidatesSupersededCache verifies that UpdateCampfireID
// evicts the supersededCache entry for oldID (and newID if present) after the
// rename. Without this, a stale cache entry for oldID would be a dangling
// artifact after rekey. (Fix for workspace-pm9m.5.1.)
func TestUpdateCampfireID_InvalidatesSupersededCache(t *testing.T) {
	s := testStore(t)
	oldID := "cf-cache-old"
	newID := "cf-cache-new"

	// Seed membership so AddMessage is accepted.
	if err := s.AddMembership(Membership{
		CampfireID:   oldID,
		TransportDir: "/tmp/campfire/" + oldID,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Add a regular message and a compaction event under oldID so the
	// supersededCache gets populated for oldID.
	if _, err := s.AddMessage(MessageRecord{
		ID: "msg-cache-1", CampfireID: oldID, Sender: "aabb",
		Payload: []byte("hello"), Tags: []string{"status"}, Antecedents: []string{},
		Timestamp: 100, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 100,
	}); err != nil {
		t.Fatalf("AddMessage regular: %v", err)
	}
	payload, _ := json.Marshal(CompactionPayload{
		Supersedes:     []string{"msg-cache-1"},
		Summary:        []byte("compact"),
		Retention:      "archive",
		CheckpointHash: "abc",
	})
	if _, err := s.AddMessage(MessageRecord{
		ID: "ev-cache-1", CampfireID: oldID, Sender: "aabb",
		Payload: payload, Tags: []string{"campfire:compact"}, Antecedents: []string{"msg-cache-1"},
		Timestamp: 200, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 200,
	}); err != nil {
		t.Fatalf("AddMessage compact: %v", err)
	}

	// Warm the supersededCache for oldID.
	sup, err := s.collectSupersededIDs(oldID)
	if err != nil {
		t.Fatalf("collectSupersededIDs(oldID): %v", err)
	}
	if !sup["msg-cache-1"] {
		t.Fatal("expected msg-cache-1 in superseded set before rename")
	}

	// Verify the cache entry for oldID exists.
	s.supersededMu.RLock()
	_, hadOld := s.supersededCache[oldID]
	s.supersededMu.RUnlock()
	if !hadOld {
		t.Fatal("supersededCache should have an entry for oldID before rename")
	}

	// Rename oldID → newID.
	if err := s.UpdateCampfireID(oldID, newID); err != nil {
		t.Fatalf("UpdateCampfireID: %v", err)
	}

	// After rename the cache entry for oldID must be gone.
	s.supersededMu.RLock()
	_, stillHasOld := s.supersededCache[oldID]
	s.supersededMu.RUnlock()
	if stillHasOld {
		t.Error("supersededCache still has stale entry for oldID after UpdateCampfireID — cache invalidation bug")
	}

	// The cache entry for newID must also be absent (evicted or never populated).
	s.supersededMu.RLock()
	_, hasNew := s.supersededCache[newID]
	s.supersededMu.RUnlock()
	if hasNew {
		t.Error("supersededCache should not have a pre-populated entry for newID after UpdateCampfireID")
	}
}

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

	// With SQLite, concurrent transactions may both fail with SQLITE_BUSY.
	// The invariant is that at most 1 goroutine claims the share. If both
	// fail due to lock contention, that's acceptable (no double-claim).
	if successes > 1 {
		t.Errorf("expected at most 1 goroutine to claim the share, got %d", successes)
	}
}

// --- workspace-dq5g: peer_endpoints role column ---

// TestUpsertPeerEndpoint_Role verifies that a role stored via UpsertPeerEndpoint
// is retrievable via GetPeerRole and appears in ListPeerEndpoints.
func TestUpsertPeerEndpoint_Role(t *testing.T) {
	s := testStore(t)
	campfireID := "role-cf"

	if err := s.AddMembership(Membership{
		CampfireID:   campfireID,
		TransportDir: "/tmp",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	cases := []struct {
		pubkey string
		role   string
	}{
		{"pubkey-observer", "observer"},
		{"pubkey-writer", "writer"},
		{"pubkey-member", "member"},
		{"pubkey-creator", "creator"},
	}
	for _, tc := range cases {
		err := s.UpsertPeerEndpoint(PeerEndpoint{
			CampfireID:   campfireID,
			MemberPubkey: tc.pubkey,
			Endpoint:     "http://example.com",
			Role:         tc.role,
		})
		if err != nil {
			t.Fatalf("UpsertPeerEndpoint(%s, %s): %v", tc.pubkey, tc.role, err)
		}
	}

	// Verify GetPeerRole returns correct role for each.
	for _, tc := range cases {
		got, err := s.GetPeerRole(campfireID, tc.pubkey)
		if err != nil {
			t.Fatalf("GetPeerRole(%s): %v", tc.pubkey, err)
		}
		if got != tc.role {
			t.Errorf("GetPeerRole(%s): got %q, want %q", tc.pubkey, got, tc.role)
		}
	}

	// Verify ListPeerEndpoints returns the correct role for each.
	endpoints, err := s.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints: %v", err)
	}
	byPubkey := make(map[string]string)
	for _, ep := range endpoints {
		byPubkey[ep.MemberPubkey] = ep.Role
	}
	for _, tc := range cases {
		got := byPubkey[tc.pubkey]
		if got != tc.role {
			t.Errorf("ListPeerEndpoints role for %s: got %q, want %q", tc.pubkey, got, tc.role)
		}
	}
}

// TestGetPeerRole_NotFound verifies that GetPeerRole returns "member" for unknown peers.
func TestGetPeerRole_NotFound(t *testing.T) {
	s := testStore(t)
	campfireID := "role-cf-notfound"

	if err := s.AddMembership(Membership{
		CampfireID:   campfireID,
		TransportDir: "/tmp",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	role, err := s.GetPeerRole(campfireID, "nonexistent-pubkey")
	if err != nil {
		t.Fatalf("GetPeerRole: %v", err)
	}
	if role != "member" {
		t.Errorf("GetPeerRole for unknown peer: got %q, want %q", role, "member")
	}
}

// TestUpsertPeerEndpoint_DefaultRole verifies that UpsertPeerEndpoint with empty
// role string defaults to "member".
func TestUpsertPeerEndpoint_DefaultRole(t *testing.T) {
	s := testStore(t)
	campfireID := "role-cf-default"

	if err := s.AddMembership(Membership{
		CampfireID:   campfireID,
		TransportDir: "/tmp",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	err := s.UpsertPeerEndpoint(PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: "pubkey-noRole",
		Endpoint:     "http://example.com",
		// Role is intentionally empty.
	})
	if err != nil {
		t.Fatalf("UpsertPeerEndpoint: %v", err)
	}

	role, err := s.GetPeerRole(campfireID, "pubkey-noRole")
	if err != nil {
		t.Fatalf("GetPeerRole: %v", err)
	}
	if role != "member" {
		t.Errorf("default role: got %q, want %q", role, "member")
	}
}

// --- workspace-oiaw: UpdateMembershipRole / GetReadCursor / SetReadCursor / HasMessage ---

// TestUpdateMembershipRole_HappyPath verifies that UpdateMembershipRole changes the role
// of an existing membership.
func TestUpdateMembershipRole_HappyPath(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf-role", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1}) //nolint:errcheck

	if err := s.UpdateMembershipRole("cf-role", "admin"); err != nil {
		t.Fatalf("UpdateMembershipRole() error: %v", err)
	}

	m, err := s.GetMembership("cf-role")
	if err != nil {
		t.Fatalf("GetMembership() error: %v", err)
	}
	if m == nil {
		t.Fatal("membership not found after update")
	}
	if m.Role != "admin" {
		t.Errorf("role = %q, want %q", m.Role, "admin")
	}
}

// TestUpdateMembershipRole_NotFound verifies that UpdateMembershipRole returns an error
// when the campfire_id does not exist.
func TestUpdateMembershipRole_NotFound(t *testing.T) {
	s := testStore(t)

	err := s.UpdateMembershipRole("nonexistent", "admin")
	if err == nil {
		t.Fatal("UpdateMembershipRole() expected error for nonexistent campfire, got nil")
	}
}

// TestGetReadCursor_NoCursor verifies that GetReadCursor returns 0 when no cursor exists.
func TestGetReadCursor_NoCursor(t *testing.T) {
	s := testStore(t)

	ts, err := s.GetReadCursor("cf-nocursor")
	if err != nil {
		t.Fatalf("GetReadCursor() error: %v", err)
	}
	if ts != 0 {
		t.Errorf("GetReadCursor() = %d, want 0 for missing cursor", ts)
	}
}

// TestSetAndGetReadCursor verifies that SetReadCursor persists the value and GetReadCursor
// returns it.
func TestSetAndGetReadCursor(t *testing.T) {
	s := testStore(t)

	if err := s.SetReadCursor("cf-cursor", 12345); err != nil {
		t.Fatalf("SetReadCursor() error: %v", err)
	}

	ts, err := s.GetReadCursor("cf-cursor")
	if err != nil {
		t.Fatalf("GetReadCursor() error: %v", err)
	}
	if ts != 12345 {
		t.Errorf("GetReadCursor() = %d, want 12345", ts)
	}
}

// TestSetReadCursor_Upsert verifies that calling SetReadCursor a second time overwrites
// the existing cursor (UPSERT behavior).
func TestSetReadCursor_Upsert(t *testing.T) {
	s := testStore(t)

	if err := s.SetReadCursor("cf-upsert", 100); err != nil {
		t.Fatalf("first SetReadCursor() error: %v", err)
	}
	if err := s.SetReadCursor("cf-upsert", 200); err != nil {
		t.Fatalf("second SetReadCursor() error: %v", err)
	}

	ts, err := s.GetReadCursor("cf-upsert")
	if err != nil {
		t.Fatalf("GetReadCursor() error: %v", err)
	}
	if ts != 200 {
		t.Errorf("GetReadCursor() = %d after upsert, want 200", ts)
	}
}

// TestHasMessage_Missing verifies that HasMessage returns false for an unknown ID.
func TestHasMessage_Missing(t *testing.T) {
	s := testStore(t)

	found, err := s.HasMessage("no-such-message")
	if err != nil {
		t.Fatalf("HasMessage() error: %v", err)
	}
	if found {
		t.Error("HasMessage() = true for nonexistent message, want false")
	}
}

// TestHasMessage_Present verifies that HasMessage returns true after a message is added.
func TestHasMessage_Present(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "cf-hasmsg", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1}) //nolint:errcheck

	msg := MessageRecord{
		ID: "msg-present", CampfireID: "cf-hasmsg", Sender: "s",
		Payload: []byte("hello"), Tags: []string{},
		Antecedents: []string{},
		Timestamp: 1000, Signature: []byte("sig"), Provenance: nil, ReceivedAt: 2000,
	}
	if _, err := s.AddMessage(msg); err != nil {
		t.Fatalf("AddMessage() error: %v", err)
	}

	found, err := s.HasMessage("msg-present")
	if err != nil {
		t.Fatalf("HasMessage() error: %v", err)
	}
	if !found {
		t.Error("HasMessage() = false for existing message, want true")
	}
}


// --- workspace-g88: UpdateCampfireID unit tests ---

// countRows returns the number of rows in table where col equals id.
// The store's db field is accessible because these tests are in the same package.
func countRows(t *testing.T, s *SQLiteStore, table, col, id string) int {
	t.Helper()
	var n int
	q := "SELECT COUNT(*) FROM " + table + " WHERE " + col + " = ?"
	if err := s.db.QueryRow(q, id).Scan(&n); err != nil {
		t.Fatalf("countRows(%s): %v", table, err)
	}
	return n
}

// TestUpdateCampfireID_MessagesRenamed verifies that messages sent before a
// rekey (under oldID) become accessible under newID after UpdateCampfireID and
// are no longer accessible under oldID. This is the primary regression test for
// the messages table coverage gap identified in workspace-g88.
func TestUpdateCampfireID_MessagesRenamed(t *testing.T) {
	s := testStore(t)
	oldID := "cf-g88-msgs-old"
	newID := "cf-g88-msgs-new"

	// Only seed membership for oldID — UpdateCampfireID renames it to newID.
	if err := s.AddMembership(Membership{
		CampfireID:   oldID,
		TransportDir: "/tmp/campfire/" + oldID,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Seed two messages before the rekey.
	for i, id := range []string{"g88-msg-alpha", "g88-msg-beta"} {
		if _, err := s.AddMessage(MessageRecord{
			ID: id, CampfireID: oldID, Sender: "alice",
			Payload: []byte("payload"), Tags: nil, Antecedents: nil,
			Timestamp: int64(1000 + i), Signature: []byte("sig"),
			Provenance: nil, ReceivedAt: int64(2000 + i),
		}); err != nil {
			t.Fatalf("AddMessage(%s): %v", id, err)
		}
	}

	// Verify messages exist under oldID before rename.
	if got := countRows(t, s, "messages", "campfire_id", oldID); got != 2 {
		t.Fatalf("pre-rekey: expected 2 messages under oldID, got %d", got)
	}

	if err := s.UpdateCampfireID(oldID, newID); err != nil {
		t.Fatalf("UpdateCampfireID: %v", err)
	}

	// No messages should remain under oldID.
	if got := countRows(t, s, "messages", "campfire_id", oldID); got != 0 {
		t.Errorf("messages: %d row(s) still under oldID after rename", got)
	}

	// Both messages should now be accessible under newID.
	msgs, err := s.ListMessages(newID, 0)
	if err != nil {
		t.Fatalf("ListMessages(%s): %v", newID, err)
	}
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages under newID, got %d", len(msgs))
	}

	// Membership itself was renamed.
	m, err := s.GetMembership(newID)
	if err != nil || m == nil {
		t.Fatalf("membership should exist under newID: err=%v, m=%v", err, m)
	}
	if old, _ := s.GetMembership(oldID); old != nil {
		t.Error("membership should no longer exist under oldID")
	}
}

// TestUpdateCampfireID_FiltersRenamed verifies that the filters table is also
// renamed by UpdateCampfireID. The filters table has no public accessor methods
// so we insert and verify via the store's db field (same package, accessible).
func TestUpdateCampfireID_FiltersRenamed(t *testing.T) {
	s := testStore(t)
	oldID := "cf-g88-filters-old"
	newID := "cf-g88-filters-new"

	if err := s.AddMembership(Membership{
		CampfireID:   oldID,
		TransportDir: "/tmp/campfire/" + oldID,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Insert a filter row directly (no public API).
	if _, err := s.db.Exec(
		`INSERT INTO filters (campfire_id, direction, pass_through, suppress) VALUES (?, 'inbound', '[]', '[]')`,
		oldID,
	); err != nil {
		t.Fatalf("insert filter: %v", err)
	}

	if got := countRows(t, s, "filters", "campfire_id", oldID); got != 1 {
		t.Fatalf("pre-rekey: expected 1 filter under oldID, got %d", got)
	}

	if err := s.UpdateCampfireID(oldID, newID); err != nil {
		t.Fatalf("UpdateCampfireID: %v", err)
	}

	if got := countRows(t, s, "filters", "campfire_id", oldID); got != 0 {
		t.Errorf("filters: %d row(s) still under oldID after rename", got)
	}
	if got := countRows(t, s, "filters", "campfire_id", newID); got != 1 {
		t.Errorf("filters: expected 1 row under newID, got %d", got)
	}
}

// TestUpdateCampfireID_ConcurrentSafe verifies that concurrent calls with the
// same oldID leave the store in a consistent state — all records end up under
// newID, no rows remain under oldID, and no data is duplicated or lost.
// SQLite serializes writers so exactly one goroutine performs the work; the
// rest either observe a no-op (0 rows matched) or receive a BUSY/LOCKED error.
// In either case the invariant holds: messages are not corrupted.
func TestUpdateCampfireID_ConcurrentSafe(t *testing.T) {
	s := testStore(t)
	oldID := "cf-g88-conc-old"
	newID := "cf-g88-conc-new"

	if err := s.AddMembership(Membership{
		CampfireID:   oldID,
		TransportDir: "/tmp/campfire/" + oldID,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Seed several messages under oldID.
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("g88-conc-msg-%d", i)
		if _, err := s.AddMessage(MessageRecord{
			ID: id, CampfireID: oldID, Sender: "alice",
			Payload: []byte("data"), Tags: nil, Antecedents: nil,
			Timestamp: int64(1000 + i), Signature: []byte("sig"),
			Provenance: nil, ReceivedAt: int64(2000 + i),
		}); err != nil {
			t.Fatalf("AddMessage(%s): %v", id, err)
		}
	}

	const goroutines = 5
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			// Errors are acceptable (BUSY, LOCKED, or UNIQUE conflict on a second
			// successful rename where 0 rows match) — what must not happen is
			// partial writes leaving messages split across both IDs.
			_ = s.UpdateCampfireID(oldID, newID) //nolint:errcheck
		}()
	}
	wg.Wait()

	// Post-condition: no messages under oldID, all messages under newID.
	oldCount := countRows(t, s, "messages", "campfire_id", oldID)
	newCount := countRows(t, s, "messages", "campfire_id", newID)

	if oldCount+newCount != 3 {
		t.Errorf("total message count changed: oldCount=%d newCount=%d want sum=3", oldCount, newCount)
	}
	if oldCount != 0 {
		t.Errorf("messages: %d row(s) still under oldID after concurrent rekey", oldCount)
	}
	if newCount != 3 {
		t.Errorf("messages: expected 3 rows under newID, got %d", newCount)
	}
}

// --- workspace-9ga: explicit transport_type field ---

// TestAddMembership_TransportTypeInferred verifies that AddMembership populates
// the transport_type field from TransportDir when TransportType is empty.
func TestAddMembership_TransportTypeInferred(t *testing.T) {
	s := testStore(t)
	dir := t.TempDir()
	campfireID := "deadbeef01"

	// Create a .cbor file to simulate a p2p-http campfire.
	cborPath := dir + "/" + campfireID + ".cbor"
	if err := os.WriteFile(cborPath, []byte("dummy"), 0644); err != nil {
		t.Fatalf("creating cbor file: %v", err)
	}

	// Insert without explicit TransportType — should be inferred as "p2p-http".
	if err := s.AddMembership(Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("AddMembership() error: %v", err)
	}

	m, err := s.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership() error: %v", err)
	}
	if m == nil {
		t.Fatal("expected membership")
	}
	if m.TransportType != "p2p-http" {
		t.Errorf("TransportType = %q, want %q", m.TransportType, "p2p-http")
	}
}

// TestAddMembership_TransportTypeGitHub verifies GitHub transport detection.
func TestAddMembership_TransportTypeGitHub(t *testing.T) {
	s := testStore(t)

	if err := s.AddMembership(Membership{
		CampfireID:   "ghcampfire01",
		TransportDir: `github:{"repo":"org/repo","issue_number":1}`,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("AddMembership() error: %v", err)
	}

	m, err := s.GetMembership("ghcampfire01")
	if err != nil {
		t.Fatalf("GetMembership() error: %v", err)
	}
	if m == nil {
		t.Fatal("expected membership")
	}
	if m.TransportType != "github" {
		t.Errorf("TransportType = %q, want %q", m.TransportType, "github")
	}
}

// TestAddMembership_TransportTypeFilesystem verifies filesystem fallback.
func TestAddMembership_TransportTypeFilesystem(t *testing.T) {
	s := testStore(t)

	if err := s.AddMembership(Membership{
		CampfireID:   "fscampfire01",
		TransportDir: "/tmp/campfire/fscampfire01",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("AddMembership() error: %v", err)
	}

	m, err := s.GetMembership("fscampfire01")
	if err != nil {
		t.Fatalf("GetMembership() error: %v", err)
	}
	if m == nil {
		t.Fatal("expected membership")
	}
	if m.TransportType != "filesystem" {
		t.Errorf("TransportType = %q, want %q", m.TransportType, "filesystem")
	}
}

// TestAddMembership_ExplicitTransportType verifies that an explicitly provided
// TransportType overrides the heuristic.
func TestAddMembership_ExplicitTransportType(t *testing.T) {
	s := testStore(t)

	// TransportDir looks like filesystem but we explicitly say p2p-http.
	if err := s.AddMembership(Membership{
		CampfireID:    "explicit01",
		TransportDir:  "/tmp/campfire/explicit01",
		JoinProtocol:  "open",
		Role:          "creator",
		JoinedAt:      1,
		TransportType: "p2p-http",
	}); err != nil {
		t.Fatalf("AddMembership() error: %v", err)
	}

	m, err := s.GetMembership("explicit01")
	if err != nil {
		t.Fatalf("GetMembership() error: %v", err)
	}
	if m == nil {
		t.Fatal("expected membership")
	}
	if m.TransportType != "p2p-http" {
		t.Errorf("TransportType = %q, want %q", m.TransportType, "p2p-http")
	}
}

// TestMigration_BackfillTransportType verifies that reopening a store with
// legacy rows (transport_type = '') backfills the correct values.
func TestMigration_BackfillTransportType(t *testing.T) {
	path := t.TempDir() + "/store.db"

	// Open the store once to create the schema with the transport_type column.
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}

	// Manually insert a row with transport_type = '' to simulate a legacy row.
	dir := t.TempDir()
	campfireID := "legacyhttp"
	cborPath := dir + "/" + campfireID + ".cbor"
	if err := os.WriteFile(cborPath, []byte("x"), 0644); err != nil {
		t.Fatalf("creating cbor file: %v", err)
	}
	// Force empty transport_type by bypassing AddMembership.
	_, dbErr := s1.(*SQLiteStore).db.Exec(
		`INSERT INTO campfire_memberships (campfire_id, transport_dir, join_protocol, role, joined_at, threshold, description, creator_pubkey, transport_type)
		 VALUES (?, ?, 'open', 'creator', 1, 1, '', '', '')`,
		campfireID, dir,
	)
	if dbErr != nil {
		t.Fatalf("direct insert error: %v", dbErr)
	}
	s1.Close()

	// Reopen: migration backfill should set transport_type = 'p2p-http'.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}
	defer s2.Close()

	m, err := s2.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership() error: %v", err)
	}
	if m == nil {
		t.Fatal("expected membership after backfill")
	}
	if m.TransportType != "p2p-http" {
		t.Errorf("after backfill: TransportType = %q, want %q", m.TransportType, "p2p-http")
	}
}

// --- workspace-ao9: interface boundary verification ---

// TestStoreImplementsMembershipStore verifies that Store satisfies MembershipStore.
func TestStoreImplementsMembershipStore(t *testing.T) {
	var _ MembershipStore = (Store)(nil)
}

// TestStoreImplementsMessageStore verifies that Store satisfies MessageStore.
func TestStoreImplementsMessageStore(t *testing.T) {
	var _ MessageStore = (Store)(nil)
}

// TestStoreImplementsPeerStore verifies that Store satisfies PeerStore.
func TestStoreImplementsPeerStore(t *testing.T) {
	var _ PeerStore = (Store)(nil)
}

// TestStoreImplementsThresholdStore verifies that Store satisfies ThresholdStore.
func TestStoreImplementsThresholdStore(t *testing.T) {
	var _ ThresholdStore = (Store)(nil)
}

// TestMembershipStoreInterface exercises MembershipStore methods via the interface,
// confirming that callers typed to the narrow interface can use all operations.
func TestMembershipStoreInterface(t *testing.T) {
	s := testStore(t)
	var ms MembershipStore = s

	m := Membership{
		CampfireID: "iface-cf1", TransportDir: "/tmp", JoinProtocol: "open",
		Role: "member", JoinedAt: 1,
	}
	if err := ms.AddMembership(m); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	got, err := ms.GetMembership("iface-cf1")
	if err != nil || got == nil {
		t.Fatalf("GetMembership: err=%v got=%v", err, got)
	}
	if got.Role != "member" {
		t.Errorf("role = %q, want member", got.Role)
	}
	if err := ms.UpdateMembershipRole("iface-cf1", "observer"); err != nil {
		t.Fatalf("UpdateMembershipRole: %v", err)
	}
	got, _ = ms.GetMembership("iface-cf1")
	if got.Role != "observer" {
		t.Errorf("role after update = %q, want observer", got.Role)
	}
	all, err := ms.ListMemberships()
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	found := false
	for _, x := range all {
		if x.CampfireID == "iface-cf1" {
			found = true
		}
	}
	if !found {
		t.Error("ListMemberships did not return iface-cf1")
	}
	if err := ms.RemoveMembership("iface-cf1"); err != nil {
		t.Fatalf("RemoveMembership: %v", err)
	}
	got, _ = ms.GetMembership("iface-cf1")
	if got != nil {
		t.Error("GetMembership returned non-nil after removal")
	}
}

// TestPeerStoreInterface exercises PeerStore methods via the narrow interface.
func TestPeerStoreInterface(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "peer-cf", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1}) //nolint:errcheck
	var ps PeerStore = s

	ep := PeerEndpoint{
		CampfireID:   "peer-cf",
		MemberPubkey: "pubkey-abc",
		Endpoint:     "http://peer.example.com",
		Role:         "creator",
	}
	if err := ps.UpsertPeerEndpoint(ep); err != nil {
		t.Fatalf("UpsertPeerEndpoint: %v", err)
	}
	role, err := ps.GetPeerRole("peer-cf", "pubkey-abc")
	if err != nil {
		t.Fatalf("GetPeerRole: %v", err)
	}
	if role != "creator" {
		t.Errorf("role = %q, want creator", role)
	}
	eps, err := ps.ListPeerEndpoints("peer-cf")
	if err != nil || len(eps) != 1 {
		t.Fatalf("ListPeerEndpoints: err=%v len=%d", err, len(eps))
	}
	if err := ps.DeletePeerEndpoint("peer-cf", "pubkey-abc"); err != nil {
		t.Fatalf("DeletePeerEndpoint: %v", err)
	}
	eps, _ = ps.ListPeerEndpoints("peer-cf")
	if len(eps) != 0 {
		t.Errorf("expected 0 endpoints after delete, got %d", len(eps))
	}
}

// TestThresholdStoreInterface exercises ThresholdStore methods via the narrow interface.
func TestThresholdStoreInterface(t *testing.T) {
	s := testStore(t)
	s.AddMembership(Membership{CampfireID: "thr-cf", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1}) //nolint:errcheck
	var ts ThresholdStore = s

	share := ThresholdShare{
		CampfireID:    "thr-cf",
		ParticipantID: 1,
		SecretShare:   []byte("secret"),
		PublicData:    []byte("public"),
	}
	if err := ts.UpsertThresholdShare(share); err != nil {
		t.Fatalf("UpsertThresholdShare: %v", err)
	}
	got, err := ts.GetThresholdShare("thr-cf")
	if err != nil || got == nil {
		t.Fatalf("GetThresholdShare: err=%v got=%v", err, got)
	}
	if got.ParticipantID != 1 {
		t.Errorf("participant_id = %d, want 1", got.ParticipantID)
	}

	if err := ts.StorePendingThresholdShare("thr-cf", 2, []byte("pending")); err != nil {
		t.Fatalf("StorePendingThresholdShare: %v", err)
	}
	pid, data, err := ts.ClaimPendingThresholdShare("thr-cf")
	if err != nil {
		t.Fatalf("ClaimPendingThresholdShare: %v", err)
	}
	if pid != 2 || string(data) != "pending" {
		t.Errorf("claimed share pid=%d data=%q, want pid=2 data=pending", pid, data)
	}
	// Second claim should return nil (no more pending).
	pid2, data2, err := ts.ClaimPendingThresholdShare("thr-cf")
	if err != nil || pid2 != 0 || data2 != nil {
		t.Errorf("second claim: pid=%d data=%v err=%v, want (0,nil,nil)", pid2, data2, err)
	}
}

// --- workspace-pm9m.5.16: versioned migration tests ---

// TestMigrations_VersionsTracked verifies that all migrations in schemaMigrations
// are recorded in schema_migrations after Open().
func TestMigrations_VersionsTracked(t *testing.T) {
	s := testStore(t)
	rows, err := s.db.Query(`SELECT version, description FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("querying schema_migrations: %v", err)
	}
	defer rows.Close()

	var versions []int
	for rows.Next() {
		var v int
		var desc string
		if err := rows.Scan(&v, &desc); err != nil {
			t.Fatalf("scanning migration row: %v", err)
		}
		versions = append(versions, v)
	}
	if rows.Err() != nil {
		t.Fatalf("rows error: %v", rows.Err())
	}

	if len(versions) != len(schemaMigrations) {
		t.Errorf("schema_migrations has %d rows, want %d", len(versions), len(schemaMigrations))
	}
	for i, m := range schemaMigrations {
		if i >= len(versions) {
			t.Errorf("missing migration version %d in schema_migrations", m.version)
			continue
		}
		if versions[i] != m.version {
			t.Errorf("migration row %d has version %d, want %d", i, versions[i], m.version)
		}
	}
}

// TestMigrations_Idempotent verifies that calling Open() twice on the same db
// does not duplicate rows in schema_migrations.
func TestMigrations_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	var count int
	if err := s2.(*SQLiteStore).db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("querying schema_migrations count: %v", err)
	}
	if count != len(schemaMigrations) {
		t.Errorf("schema_migrations has %d rows after two Opens, want %d (idempotency failure)", count, len(schemaMigrations))
	}
}

// TestBackfillTransportTypes_SkipsWhenEmpty verifies the COUNT(*) guard:
// a store with no memberships having empty transport_type incurs only the
// COUNT query (no secondary SELECT). This is a correctness test, not a
// performance test — it verifies the guard doesn't error on an empty table.
func TestBackfillTransportTypes_SkipsWhenEmpty(t *testing.T) {
	s := testStore(t)
	// Add a membership with an explicit transport_type to ensure the table is
	// non-empty but the backfill guard returns early.
	err := s.AddMembership(Membership{
		CampfireID:    "cf-no-backfill",
		TransportDir:  "/tmp",
		JoinProtocol:  "open",
		Role:          "full",
		JoinedAt:      1,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	// Calling backfillTransportTypes directly with no empty rows should be a no-op.
	if err := backfillTransportTypes(s.db); err != nil {
		t.Errorf("backfillTransportTypes with no empty rows: %v", err)
	}
}

// --- workspace-pm9m.5.15: MaxMessageTimestamp ---

// TestMaxMessageTimestamp_ReturnsMaxTS verifies the max timestamp across all messages.
func TestMaxMessageTimestamp_ReturnsMaxTS(t *testing.T) {
	s, cfID := setupFilterTestStore(t) // timestamps 1,2,3,4
	maxTS, err := s.MaxMessageTimestamp(cfID, 0)
	if err != nil {
		t.Fatalf("MaxMessageTimestamp: %v", err)
	}
	if maxTS != 4 {
		t.Errorf("MaxMessageTimestamp = %d, want 4", maxTS)
	}
}

// TestMaxMessageTimestamp_AfterTS verifies that afterTS is respected.
func TestMaxMessageTimestamp_AfterTS(t *testing.T) {
	s, cfID := setupFilterTestStore(t) // timestamps 1,2,3,4
	maxTS, err := s.MaxMessageTimestamp(cfID, 2)
	if err != nil {
		t.Fatalf("MaxMessageTimestamp: %v", err)
	}
	// Only timestamps 3 and 4 are > 2; max is 4.
	if maxTS != 4 {
		t.Errorf("MaxMessageTimestamp(afterTS=2) = %d, want 4", maxTS)
	}
}

// TestMaxMessageTimestamp_NoMessages verifies that 0 is returned when no messages match.
func TestMaxMessageTimestamp_NoMessages(t *testing.T) {
	s, cfID := setupFilterTestStore(t) // timestamps 1,2,3,4
	maxTS, err := s.MaxMessageTimestamp(cfID, 100)
	if err != nil {
		t.Fatalf("MaxMessageTimestamp: %v", err)
	}
	if maxTS != 0 {
		t.Errorf("MaxMessageTimestamp(afterTS=100) = %d, want 0 (no messages)", maxTS)
	}
}

// TestMaxMessageTimestamp_IgnoresFilters verifies that MaxMessageTimestamp returns the
// unfiltered max — it counts all messages regardless of tags/sender. This is the
// correctness invariant: cursor advances past ALL messages even when display is filtered.
func TestMaxMessageTimestamp_IgnoresFilters(t *testing.T) {
	s, cfID := setupFilterTestStore(t) // m4 has no tags, timestamp=4
	// ListMessages with tag filter would exclude m4 (no "status" tag).
	// MaxMessageTimestamp should still return 4.
	maxTS, err := s.MaxMessageTimestamp(cfID, 0)
	if err != nil {
		t.Fatalf("MaxMessageTimestamp: %v", err)
	}
	if maxTS != 4 {
		t.Errorf("MaxMessageTimestamp = %d, want 4 (unfiltered; m4 has no tags but highest ts)", maxTS)
	}
}

// ---------------------------------------------------------------------------
// Invite store tests
// ---------------------------------------------------------------------------

// TestValidateAndUseInvite_Basic verifies that ValidateAndUseInvite succeeds for a
// valid code and increments the use count.
func TestValidateAndUseInvite_Basic(t *testing.T) {
	s := testStore(t)
	cfID := "campfire-basic-invite"
	code := "code-abc"
	if err := s.CreateInvite(InviteRecord{
		CampfireID: cfID,
		InviteCode: code,
		CreatedBy:  "owner",
		CreatedAt:  1,
		MaxUses:    5,
	}); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	inv, err := s.ValidateAndUseInvite(cfID, code)
	if err != nil {
		t.Fatalf("ValidateAndUseInvite: %v", err)
	}
	if inv.UseCount != 1 {
		t.Errorf("UseCount = %d, want 1", inv.UseCount)
	}
}

// TestValidateAndUseInvite_ExhaustedReturnsError verifies that using an invite beyond
// max_uses returns ErrInviteExhausted.
func TestValidateAndUseInvite_ExhaustedReturnsError(t *testing.T) {
	s := testStore(t)
	cfID := "campfire-exhaust"
	code := "code-exhaust"
	if err := s.CreateInvite(InviteRecord{
		CampfireID: cfID,
		InviteCode: code,
		CreatedBy:  "owner",
		CreatedAt:  1,
		MaxUses:    2,
	}); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, err := s.ValidateAndUseInvite(cfID, code); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := s.ValidateAndUseInvite(cfID, code); err != nil {
		t.Fatalf("second use: %v", err)
	}
	_, err := s.ValidateAndUseInvite(cfID, code)
	if !errors.Is(err, ErrInviteExhausted) {
		t.Errorf("third use: got %v, want ErrInviteExhausted", err)
	}
}

// TestValidateAndUseInvite_UnlimitedMaxUses verifies that a code with max_uses=0
// (unlimited) can be used many times.
func TestValidateAndUseInvite_UnlimitedMaxUses(t *testing.T) {
	s := testStore(t)
	cfID := "campfire-unlimited"
	code := "code-unlimited"
	if err := s.CreateInvite(InviteRecord{
		CampfireID: cfID,
		InviteCode: code,
		CreatedBy:  "owner",
		CreatedAt:  1,
		MaxUses:    0, // unlimited
	}); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := s.ValidateAndUseInvite(cfID, code); err != nil {
			t.Fatalf("use %d: %v", i+1, err)
		}
	}
}

// TestValidateAndUseInvite_Concurrent verifies that concurrent callers racing to use
// a max_uses=1 invite code result in exactly one success and the rest receiving
// ErrInviteExhausted.  This is the core TOCTOU regression test.
func TestValidateAndUseInvite_Concurrent(t *testing.T) {
	s := testStore(t)
	cfID := "campfire-concurrent"
	code := "code-race"
	if err := s.CreateInvite(InviteRecord{
		CampfireID: cfID,
		InviteCode: code,
		CreatedBy:  "owner",
		CreatedAt:  1,
		MaxUses:    1,
	}); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	successes := make([]int, goroutines)
	errs := make([]error, goroutines)

	// Barrier so all goroutines attempt ValidateAndUseInvite simultaneously.
	ready := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-ready
			_, errs[idx] = s.ValidateAndUseInvite(cfID, code)
			if errs[idx] == nil {
				successes[idx] = 1
			}
		}(i)
	}
	close(ready)
	wg.Wait()

	totalSuccess := 0
	for _, v := range successes {
		totalSuccess += v
	}
	if totalSuccess != 1 {
		t.Errorf("concurrent ValidateAndUseInvite: %d successes, want exactly 1", totalSuccess)
	}
	for i, err := range errs {
		if successes[i] == 0 && !errors.Is(err, ErrInviteExhausted) {
			t.Errorf("goroutine %d: got %v, want ErrInviteExhausted", i, err)
		}
	}

	// Confirm the use_count is exactly 1 in the store.
	inv, err := s.LookupInvite(code)
	if err != nil {
		t.Fatalf("LookupInvite: %v", err)
	}
	if inv.UseCount != 1 {
		t.Errorf("use_count = %d after concurrent joins, want 1", inv.UseCount)
	}
}

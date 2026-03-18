package store

import (
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

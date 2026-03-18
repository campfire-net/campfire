package cmd

import (
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
)

func TestRunPull_ExactID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	s.AddMembership(store.Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})
	s.AddMessage(store.MessageRecord{
		ID: "msg-abc123-0000-0000-0000-000000000000", CampfireID: "cf1",
		Sender: "sender1", Payload: []byte("hello world"), Tags: `["status"]`, Antecedents: "[]",
		Timestamp: 1000000000, Signature: []byte("sig"), Provenance: "[]", ReceivedAt: 2000000000,
	})

	// Set read cursor so we can verify pull doesn't advance it.
	s.SetReadCursor("cf1", 500000000)
	s.Close()

	err = runPull("msg-abc123-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("runPull() error: %v", err)
	}

	// Verify cursor was NOT advanced.
	s2, _ := store.Open(filepath.Join(dir, "store.db"))
	defer s2.Close()
	cursor, _ := s2.GetReadCursor("cf1")
	if cursor != 500000000 {
		t.Errorf("cursor = %d, want 500000000 (should not advance)", cursor)
	}
}

func TestRunPull_PrefixID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	s.AddMembership(store.Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})
	s.AddMessage(store.MessageRecord{
		ID: "msg-abc123-0000-0000-0000-000000000000", CampfireID: "cf1",
		Sender: "sender1", Payload: []byte("hello world"), Tags: `["status"]`, Antecedents: "[]",
		Timestamp: 1000000000, Signature: []byte("sig"), Provenance: "[]", ReceivedAt: 2000000000,
	})
	s.Close()

	err = runPull("msg-abc")
	if err != nil {
		t.Fatalf("runPull() with prefix error: %v", err)
	}
}

func TestRunPull_MultipleIDs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	s.AddMembership(store.Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})
	s.AddMessage(store.MessageRecord{
		ID: "msg-aaa-0000", CampfireID: "cf1",
		Sender: "s1", Payload: []byte("first"), Tags: "[]", Antecedents: "[]",
		Timestamp: 1000, Signature: []byte("sig"), Provenance: "[]", ReceivedAt: 2000,
	})
	s.AddMessage(store.MessageRecord{
		ID: "msg-bbb-0000", CampfireID: "cf1",
		Sender: "s1", Payload: []byte("second"), Tags: "[]", Antecedents: "[]",
		Timestamp: 2000, Signature: []byte("sig"), Provenance: "[]", ReceivedAt: 3000,
	})
	s.Close()

	err = runPull("msg-aaa-0000,msg-bbb-0000")
	if err != nil {
		t.Fatalf("runPull() with multiple IDs error: %v", err)
	}
}

func TestRunPull_NotFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	s.Close()

	err = runPull("msg-nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent message, got nil")
	}
	if got := err.Error(); got != "message not found: msg-nonexistent" {
		t.Errorf("error = %q, want %q", got, "message not found: msg-nonexistent")
	}
}

func TestRunPull_MutualExclusivity(t *testing.T) {
	// Test that the command rejects --pull with --all/--peek/--follow.
	// We test this by setting the flags and calling the RunE directly.
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)

	// Save and restore flag state.
	origPull := readPull
	origAll := readAll
	origPeek := readPeek
	origFollow := readFollow
	defer func() {
		readPull = origPull
		readAll = origAll
		readPeek = origPeek
		readFollow = origFollow
	}()

	tests := []struct {
		name   string
		all    bool
		peek   bool
		follow bool
	}{
		{"all", true, false, false},
		{"peek", false, true, false},
		{"follow", false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readPull = "some-id"
			readAll = tt.all
			readPeek = tt.peek
			readFollow = tt.follow

			err := readCmd.RunE(readCmd, nil)
			if err == nil {
				t.Fatal("expected error for mutually exclusive flags, got nil")
			}
		})
	}
}

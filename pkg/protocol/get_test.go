package protocol_test

// Tests for protocol.Client.Get and protocol.Client.GetByPrefix — campfire-agent-69l.
//
// All tests use a real SQLite store (store.Open), real client.Send to insert
// messages, and real Get/GetByPrefix to retrieve them. No mock stores.

import (
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// storeMessageRecord returns a minimal store.MessageRecord with the given id and campfireID.
// Used for tests that need to control message IDs (e.g. ambiguity testing).
func storeMessageRecord(id, campfireID string) store.MessageRecord {
	return store.MessageRecord{
		ID:          id,
		CampfireID:  campfireID,
		Sender:      "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd",
		Payload:     []byte("test payload"),
		Tags:        []string{},
		Antecedents: []string{},
		Timestamp:   1000000,
		Signature:   []byte("sig"),
		Provenance:  nil,
		ReceivedAt:  2000000,
	}
}

// TestClientGet_ReturnsMessage verifies that Get retrieves a message by full ID
// and that all fields match what was sent.
func TestClientGet_ReturnsMessage(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)
	client := protocol.New(s, agentID)

	sent, err := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     []byte("hello world"),
		Tags:        []string{"status"},
		Antecedents: []string{},
		Instance:    "tester",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, err := client.Get(sent.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil for existing message")
	}

	if got.ID != sent.ID {
		t.Errorf("ID mismatch: got %q, want %q", got.ID, sent.ID)
	}
	if string(got.Payload) != "hello world" {
		t.Errorf("Payload mismatch: got %q, want %q", got.Payload, "hello world")
	}
	if len(got.Tags) != 1 || got.Tags[0] != "status" {
		t.Errorf("Tags mismatch: got %v, want [status]", got.Tags)
	}
	if got.Instance != "tester" {
		t.Errorf("Instance mismatch: got %q, want %q", got.Instance, "tester")
	}
	if got.Sender == "" {
		t.Error("Sender is empty")
	}
	if got.Timestamp == 0 {
		t.Error("Timestamp is zero")
	}
}

// TestClientGet_Antecedents verifies that Get preserves antecedent message IDs.
func TestClientGet_Antecedents(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)
	client := protocol.New(s, agentID)

	first, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("first"),
	})
	if err != nil {
		t.Fatalf("Send first: %v", err)
	}

	reply, err := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     []byte("reply"),
		Antecedents: []string{first.ID},
	})
	if err != nil {
		t.Fatalf("Send reply: %v", err)
	}

	got, err := client.Get(reply.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if len(got.Antecedents) != 1 || got.Antecedents[0] != first.ID {
		t.Errorf("Antecedents mismatch: got %v, want [%s]", got.Antecedents, first.ID)
	}
}

// TestClientGet_NotFound verifies that Get returns nil, nil for a non-existent ID.
func TestClientGet_NotFound(t *testing.T) {
	_, s, _ := setupTestEnv(t)
	client := protocol.New(s, nil)

	got, err := client.Get("nonexistent-id-that-does-not-exist")
	if err != nil {
		t.Fatalf("Get: expected nil error for nonexistent ID, got %v", err)
	}
	if got != nil {
		t.Fatalf("Get: expected nil for nonexistent ID, got %+v", got)
	}
}

// TestClientGet_EmptyID verifies that Get returns an error when id is empty.
func TestClientGet_EmptyID(t *testing.T) {
	_, s, _ := setupTestEnv(t)
	client := protocol.New(s, nil)

	_, err := client.Get("")
	if err == nil {
		t.Fatal("Get: expected error for empty id, got nil")
	}
}

// TestClientGetByPrefix_ReturnsMessage verifies that GetByPrefix retrieves a
// message by a prefix of its ID and that all fields match what was sent.
func TestClientGetByPrefix_ReturnsMessage(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)
	client := protocol.New(s, agentID)

	sent, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("prefix test payload"),
		Tags:       []string{"finding"},
		Instance:   "implementer",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Use first 8 chars of the ID as the prefix.
	prefix := sent.ID[:8]
	got, err := client.GetByPrefix(prefix)
	if err != nil {
		t.Fatalf("GetByPrefix(%q): %v", prefix, err)
	}
	if got == nil {
		t.Fatal("GetByPrefix returned nil for existing message")
	}

	if got.ID != sent.ID {
		t.Errorf("ID mismatch: got %q, want %q", got.ID, sent.ID)
	}
	if string(got.Payload) != "prefix test payload" {
		t.Errorf("Payload mismatch: got %q, want %q", got.Payload, "prefix test payload")
	}
	if len(got.Tags) != 1 || got.Tags[0] != "finding" {
		t.Errorf("Tags mismatch: got %v, want [finding]", got.Tags)
	}
	if got.Instance != "implementer" {
		t.Errorf("Instance mismatch: got %q, want %q", got.Instance, "implementer")
	}
	if got.Sender == "" {
		t.Error("Sender is empty")
	}
	if got.Timestamp == 0 {
		t.Error("Timestamp is zero")
	}
}

// TestClientGetByPrefix_ExactMatch verifies that GetByPrefix works with the full ID.
func TestClientGetByPrefix_ExactMatch(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)
	client := protocol.New(s, agentID)

	sent, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("exact match"),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, err := client.GetByPrefix(sent.ID)
	if err != nil {
		t.Fatalf("GetByPrefix(full id): %v", err)
	}
	if got == nil {
		t.Fatal("GetByPrefix returned nil for exact match")
	}
	if got.ID != sent.ID {
		t.Errorf("ID mismatch: got %q, want %q", got.ID, sent.ID)
	}
}

// TestClientGetByPrefix_NotFound verifies that GetByPrefix returns nil, nil
// when no message matches the prefix.
func TestClientGetByPrefix_NotFound(t *testing.T) {
	_, s, _ := setupTestEnv(t)
	client := protocol.New(s, nil)

	got, err := client.GetByPrefix("zzzzzzzznonexistent")
	if err != nil {
		t.Fatalf("GetByPrefix: expected nil error for nonexistent prefix, got %v", err)
	}
	if got != nil {
		t.Fatalf("GetByPrefix: expected nil for nonexistent prefix, got %+v", got)
	}
}

// TestClientGetByPrefix_Ambiguous verifies that GetByPrefix returns an error
// when the prefix matches more than one message.
func TestClientGetByPrefix_Ambiguous(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)
	client := protocol.New(s, agentID)

	// Send two messages and manually insert a second with an ID sharing the same prefix.
	// The easiest way is to insert two records directly into the store since we need
	// to control the IDs for ambiguity testing.
	// We use store.AddMessage directly to set IDs with a shared prefix.
	const sharedPrefix = "aabbccdd"
	idA := sharedPrefix + "0000-0000-0000-000000000001"
	idB := sharedPrefix + "0000-0000-0000-000000000002"

	for _, id := range []string{idA, idB} {
		_, err := s.AddMessage(storeMessageRecord(id, campfireID))
		if err != nil {
			t.Fatalf("AddMessage(%s): %v", id, err)
		}
	}

	_, err := client.GetByPrefix(sharedPrefix)
	if err == nil {
		t.Fatal("GetByPrefix: expected ambiguity error, got nil")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("GetByPrefix: expected 'ambiguous' in error, got %q", err.Error())
	}
}

// TestClientGetByPrefix_EmptyPrefix verifies that GetByPrefix returns an error
// when the prefix is empty.
func TestClientGetByPrefix_EmptyPrefix(t *testing.T) {
	_, s, _ := setupTestEnv(t)
	client := protocol.New(s, nil)

	_, err := client.GetByPrefix("")
	if err == nil {
		t.Fatal("GetByPrefix: expected error for empty prefix, got nil")
	}
}

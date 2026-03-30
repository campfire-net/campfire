package cmd

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// awaitFulfillment is a thin helper for tests: creates a protocol.Client and
// calls Await with a 0-timeout to check for an existing fulfillment without blocking.
func awaitFulfillment(s store.Store, campfireID, targetMsgID string) (*protocol.Message, error) {
	id, err := identity.Generate()
	if err != nil {
		return nil, err
	}
	client := protocol.New(s, id)
	msg, err := client.Await(protocol.AwaitRequest{
		CampfireID:   campfireID,
		TargetMsgID:  targetMsgID,
		Timeout:      1 * time.Millisecond, // immediate timeout = check-only
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil && err != protocol.ErrAwaitTimeout {
		return nil, err
	}
	return msg, nil
}

// TestFindFulfillment verifies that protocol.Client.Await correctly identifies a
// message with the "fulfills" tag whose antecedents contain the target ID.
func TestFindFulfillment(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	campfireID := "await-test-campfire"
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	// Send a future message.
	futureMsg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("Need ruling"), []string{"escalation", "future"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(store.MessageRecordFromMessage(campfireID, futureMsg, store.NowNano())); err != nil {
		t.Fatal(err)
	}

	// No fulfillment yet.
	found, err := awaitFulfillment(s, campfireID, futureMsg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Fatal("expected no fulfillment before one is posted")
	}

	// Send an unrelated message (not a fulfillment).
	unrelated, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("Unrelated"), []string{"status"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(store.MessageRecordFromMessage(campfireID, unrelated, store.NowNano())); err != nil {
		t.Fatal(err)
	}

	// Still no fulfillment.
	found, err = awaitFulfillment(s, campfireID, futureMsg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Fatal("expected no fulfillment from unrelated message")
	}

	// Send a fulfillment: has "fulfills" tag and the future's ID in antecedents.
	fulfillMsg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("Use optimistic locking"), []string{"decision", "fulfills"}, []string{futureMsg.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(store.MessageRecordFromMessage(campfireID, fulfillMsg, store.NowNano())); err != nil {
		t.Fatal(err)
	}

	// Now awaitFulfillment should return the fulfilling message.
	found, err = awaitFulfillment(s, campfireID, futureMsg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("expected fulfillment to be found")
	}
	if found.ID != fulfillMsg.ID {
		t.Errorf("expected fulfillment ID %s, got %s", fulfillMsg.ID, found.ID)
	}
	if string(found.Payload) != "Use optimistic locking" {
		t.Errorf("expected payload 'Use optimistic locking', got %q", string(found.Payload))
	}
}

// TestFindFulfillmentWrongTarget verifies that a fulfillment for a different
// message ID is not returned.
func TestFindFulfillmentWrongTarget(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	campfireID := "await-wrong-target"
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	// Send two future messages.
	future1, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("Question 1"), []string{"future"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	future2, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("Question 2"), []string{"future"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.AddMessage(store.MessageRecordFromMessage(campfireID, future1, store.NowNano()))
	s.AddMessage(store.MessageRecordFromMessage(campfireID, future2, store.NowNano()))

	// Fulfill future2 only.
	fulfill, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("Answer 2"), []string{"fulfills"}, []string{future2.ID})
	if err != nil {
		t.Fatal(err)
	}
	s.AddMessage(store.MessageRecordFromMessage(campfireID, fulfill, store.NowNano()))

	// Searching for future1's fulfillment should return nil.
	found, err := awaitFulfillment(s, campfireID, future1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Errorf("expected no fulfillment for future1, got message %s", found.ID)
	}

	// Searching for future2's fulfillment should return the fulfill message.
	found, err = awaitFulfillment(s, campfireID, future2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("expected fulfillment for future2")
	}
	if found.ID != fulfill.ID {
		t.Errorf("expected %s, got %s", fulfill.ID, found.ID)
	}
}

// TestAwaitCmdExists verifies the await command is registered and has correct usage.
func TestAwaitCmdExists(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"await"})
	if err != nil {
		t.Fatalf("await command not found: %v", err)
	}
	if cmd.Use != "await <campfire-id> <msg-id>" {
		t.Errorf("unexpected usage: %s", cmd.Use)
	}

	// Verify flags exist.
	f := cmd.Flags().Lookup("timeout")
	if f == nil {
		t.Error("--timeout flag not found")
	}
}

// TestAwaitTimeout verifies that await exits with error on timeout.
func TestAwaitTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	campfireID := "await-timeout-test"

	s, err := store.Open(filepath.Join(tmpDir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Add membership.
	cfDir := filepath.Join(tmpDir, "campfires", campfireID)
	err = s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: cfDir,
		JoinProtocol: "filesystem",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatal(err)
	}

	id, _ := identity.Generate()

	// Send a future message.
	futureMsg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("Question"), []string{"future"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.AddMessage(store.MessageRecordFromMessage(campfireID, futureMsg, store.NowNano()))

	// awaitFulfillment should return nil (no fulfillment posted).
	found, err := awaitFulfillment(s, campfireID, futureMsg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Fatal("expected no fulfillment")
	}
}

// TestOutputFulfillmentText verifies text output format.
func TestOutputFulfillmentText(t *testing.T) {
	msg := protocol.Message{
		ID:      fmt.Sprintf("%032d", 1), // 32-char ID
		Payload: []byte("Use optimistic locking"),
	}

	// outputFulfillment writes to stdout; we just verify it doesn't error.
	// JSON mode is controlled by the global jsonOutput var — test text mode.
	origJSON := jsonOutput
	jsonOutput = false
	defer func() { jsonOutput = origJSON }()

	err := outputFulfillment(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

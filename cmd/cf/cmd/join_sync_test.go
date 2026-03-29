package cmd

// Tests for campfire-agent-j1o: cf join must sync messages (including
// convention declarations) immediately after writing the membership record.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupCampfireWithMessages creates an open-protocol filesystem campfire that
// already has messages written to the transport. Returns campfireID and transport
// base dir. The creator identity is also returned so callers can write additional
// messages.
func setupCampfireWithMessages(t *testing.T, msgCount int) (campfireID string, transportBaseDir string, creator *identity.Identity) {
	t.Helper()

	dir := t.TempDir()
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	creator, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}

	campfireID = cfID.PublicKeyHex()
	cfDir := filepath.Join(dir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating directory %s: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	// Write the creator as a member on disk.
	tr := fs.New(dir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: creator.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing creator member record: %v", err)
	}

	// Write messages to the transport. Each message needs at least one provenance
	// hop (signed by the campfire) so syncFromFilesystem accepts it.
	for i := 0; i < msgCount; i++ {
		payload := []byte(fmt.Sprintf(`{"text":"message %d"}`, i))
		msg, err := message.NewMessage(creator.PrivateKey, creator.PublicKey, payload, []string{"test:msg"}, nil)
		if err != nil {
			t.Fatalf("creating message %d: %v", i, err)
		}
		if err := msg.AddHop(cfID.PrivateKey, cfID.PublicKey, nil, 1, "open", []string{}, ""); err != nil {
			t.Fatalf("adding provenance hop to message %d: %v", i, err)
		}
		if err := tr.WriteMessage(campfireID, msg); err != nil {
			t.Fatalf("writing message %d to transport: %v", i, err)
		}
	}

	return campfireID, dir, creator
}

// TestJoinFilesystem_SyncsMessages verifies that joinFilesystem syncs messages
// from the transport into the local store immediately after writing membership,
// without requiring a separate cf read.
func TestJoinFilesystem_SyncsMessages(t *testing.T) {
	const msgCount = 3
	campfireID, transportBaseDir, _ := setupCampfireWithMessages(t, msgCount)

	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Join via filesystem — this should sync messages as part of the join.
	if err := joinFilesystem(campfireID, agentID, s); err != nil {
		t.Fatalf("joinFilesystem: %v", err)
	}

	// Verify membership was recorded.
	m, err := s.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("expected membership after join")
	}

	// Verify messages were synced into the store without a separate cf read.
	// +1 for the campfire:member-joined system message written by admitFSMemberIfNew.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	expectedMsgs := msgCount + 1 // +1 for campfire:member-joined system message
	if len(msgs) != expectedMsgs {
		t.Errorf("expected %d messages synced after join (including member-joined), got %d", expectedMsgs, len(msgs))
	}
}

// TestJoinFilesystem_SyncsConventionDeclarations verifies that convention
// declarations posted to a campfire are immediately available after joining,
// so that convention operations work without an intermediate cf read.
func TestJoinFilesystem_SyncsConventionDeclarations(t *testing.T) {
	campfireID, transportBaseDir, creator := setupCampfireWithMessages(t, 0)

	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	// Write a convention:operation declaration to the transport.
	declPayload := []byte(`{
		"convention": "test-sync-conv",
		"version": "0.1",
		"operation": "post",
		"description": "Test",
		"produces_tags": [{"tag": "test:post", "cardinality": "exactly_one"}],
		"args": [{"name": "text", "type": "string", "required": true, "max_length": 1000}],
		"signing": "member_key"
	}`)
	tr := fs.New(transportBaseDir)
	declMsg, err := message.NewMessage(creator.PrivateKey, creator.PublicKey, declPayload, []string{convention.ConventionOperationTag}, nil)
	if err != nil {
		t.Fatalf("creating declaration message: %v", err)
	}
	// Add a provenance hop so syncFromFilesystem accepts the message.
	// setupCampfireWithMessages returns campfireID (pubkey hex) but not the private key;
	// use the creator identity as a stand-in relay to satisfy the non-zero hop check.
	if err := declMsg.AddHop(creator.PrivateKey, creator.PublicKey, nil, 1, "open", []string{}, ""); err != nil {
		t.Fatalf("adding provenance hop to declaration message: %v", err)
	}
	if err := tr.WriteMessage(campfireID, declMsg); err != nil {
		t.Fatalf("writing declaration to transport: %v", err)
	}

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Join — declarations must be synced as part of join.
	if err := joinFilesystem(campfireID, agentID, s); err != nil {
		t.Fatalf("joinFilesystem: %v", err)
	}

	// Query convention:operation messages immediately — no cf read needed.
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{Tags: []string{convention.ConventionOperationTag}})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected convention declaration in store after join, got none — cf join did not sync messages")
	}
}

// TestJoinFilesystem_EmptyCampfire_NoError verifies that joining an empty campfire
// (no messages yet) still succeeds and syncs cleanly.
func TestJoinFilesystem_EmptyCampfire_NoError(t *testing.T) {
	campfireID, transportBaseDir, _ := setupCampfireWithMessages(t, 0)

	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	if err := joinFilesystem(campfireID, agentID, s); err != nil {
		t.Fatalf("joinFilesystem on empty campfire: %v", err)
	}

	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	// Only the campfire:member-joined system message is expected.
	if len(msgs) != 1 {
		t.Errorf("expected 1 message (member-joined) for empty campfire after join, got %d", len(msgs))
	}
}

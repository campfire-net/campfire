package cmd

// fs_transport_dir_test.go — verifies that protocol.Client.Send uses the
// TransportDir from the membership record instead of fs.DefaultBaseDir().
//
// This replaces the former sendFilesystem-based test that directly passed
// a transportDir argument. Now that all callers use protocol.Client.Send,
// the transport dir is always sourced from the membership record in the store.

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// TestFSTransportDirFromMembership verifies that protocol.Client.Send uses the
// TransportDir from the membership record to route the message — NOT the default
// base dir. The campfire is created in a custom temp directory that differs from
// CF_TRANSPORT_DIR, ensuring the membership TransportDir is the only path that works.
func TestFSTransportDirFromMembership(t *testing.T) {
	// Create a custom base dir — completely separate from DefaultBaseDir().
	customBaseDir := t.TempDir()

	// Generate an agent identity.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	// Create a campfire using the custom base dir.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}
	cf.AddMember(agentID.PublicKey)

	tr := fs.New(customBaseDir)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("init transport: %v", err)
	}

	campfireID := cf.PublicKeyHex()

	// Write the agent as a member in the transport dir.
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	// TransportDir as stored by create/join: the campfire-specific subdirectory.
	transportDir := tr.CampfireDir(campfireID)

	// Open a local store.
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Add membership with custom TransportDir (as create/join would do).
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transportDir,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Send a message via protocol.Client.Send — must use TransportDir from membership,
	// NOT DefaultBaseDir(). If it falls back to DefaultBaseDir() the message would
	// be written to the wrong directory and the sync step would find nothing.
	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("hello from transport-dir-test"),
	})
	if err != nil {
		t.Fatalf("protocol.Client.Send: %v", err)
	}
	if msg == nil {
		t.Fatal("protocol.Client.Send returned nil message")
	}

	// Sync messages from the filesystem transport into the store.
	// Must use customBaseDir, not DefaultBaseDir().
	syncFromFilesystem(campfireID, transportDir, s)

	// Verify the message is now in the store.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected at least one message after syncFromFilesystem, got 0")
	}

	found := false
	for _, m := range msgs {
		if m.ID == msg.ID {
			found = true
			payload := string(m.Payload)
			if payload != "hello from transport-dir-test" {
				t.Errorf("unexpected payload: %q", payload)
			}
		}
	}
	if !found {
		t.Errorf("sent message %s not found in store after sync; got %d messages", msg.ID[:8], len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: id=%s payload=%q", i, m.ID[:8], string(m.Payload))
		}
	}
}

// TestProtocolClientSendNotMember verifies that protocol.Client.Send returns an error
// when the agent is not a member of the campfire (no membership record in the store).
// This replaces TestFSTransportDirFallback which tested the old sendFilesystem fallback
// behavior when transportDir was empty.
func TestProtocolClientSendNotMember(t *testing.T) {
	// This test just verifies the error path — we don't need real filesystem data.
	fakeID := fmt.Sprintf("%064x", [32]byte{1, 2, 3})
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Should fail with "not a member" error since no membership is in the store.
	client := protocol.New(s, agentID)
	_, err = client.Send(protocol.SendRequest{
		CampfireID: fakeID,
		Payload:    []byte("test"),
	})
	if err == nil {
		t.Fatal("expected error for non-member campfire, got nil")
	}
}

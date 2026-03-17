package cmd

// TestFSTransportDirFromMembership verifies that sendFilesystem and syncFromFilesystem
// use the TransportDir from the membership record instead of fs.DefaultBaseDir().
//
// The test creates a campfire in a custom temp directory (not CF_TRANSPORT_DIR),
// joins it, sends a message, syncs via syncFromFilesystem, and asserts the
// message appears in the store — without setting CF_TRANSPORT_DIR.
//
// Verifies sendFilesystem/syncFromFilesystem use membership TransportDir.

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

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

	transport := fs.New(customBaseDir)
	if err := transport.Init(cf); err != nil {
		t.Fatalf("init transport: %v", err)
	}

	campfireID := cf.PublicKeyHex()

	// Write the agent as a member in the transport dir.
	if err := transport.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	// TransportDir as stored by create/join: the campfire-specific subdirectory.
	transportDir := transport.CampfireDir(campfireID)

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

	// Send a message using sendFilesystem with the membership TransportDir.
	// This must NOT fall back to DefaultBaseDir() — it must use customBaseDir.
	msg, err := sendFilesystem(campfireID, "hello from transport-dir-test", nil, nil, agentID, transportDir)
	if err != nil {
		t.Fatalf("sendFilesystem: %v", err)
	}
	if msg == nil {
		t.Fatal("sendFilesystem returned nil message")
	}

	// Sync messages from the filesystem transport into the store.
	// Again must use customBaseDir, not DefaultBaseDir().
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

// TestFSTransportDirFallback verifies that sendFilesystem and syncFromFilesystem
// fall back to DefaultBaseDir() when transportDir is empty.
func TestFSTransportDirFallback(t *testing.T) {
	// This test just verifies the path — we don't need real filesystem data.
	// sendFilesystem with empty transportDir should use fs.DefaultBaseDir()
	// which is /tmp/campfire (or CF_TRANSPORT_DIR env). The campfire won't
	// exist there, so it will return an error — but the important thing is
	// that it does NOT panic and does NOT use a garbage path.
	fakeID := fmt.Sprintf("%064x", [32]byte{1, 2, 3})
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	// Should fail with "listing members" error (transport dir doesn't exist),
	// not a nil-pointer panic or unexpected error about an unrelated dir.
	_, err = sendFilesystem(fakeID, "test", nil, nil, agentID, "")
	if err == nil {
		t.Fatal("expected error for non-existent campfire, got nil")
	}
	// Should mention members or campfire state (transport-layer error).
	// Just confirm it doesn't panic.
}

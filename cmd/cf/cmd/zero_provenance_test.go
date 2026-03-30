package cmd

// Regression test for campfire-agent-zi9: syncFromFilesystem must reject messages
// with zero provenance hops.
//
// Fix: sync.go added `len(fsMsg.Provenance) == 0` guard. This test ensures the
// guard stays in place — a zero-provenance message must never reach the store.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupSyncTestCampfire creates a minimal filesystem campfire for testing
// syncFromFilesystem directly. Returns campfireID, transportBaseDir, campfire
// identity, and the filesystem transport.
func setupSyncTestCampfire(t *testing.T) (campfireID string, cfID *identity.Identity, transportBaseDir string, tr *fs.Transport) {
	t.Helper()

	transportBaseDir = t.TempDir()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID = cfID.PublicKeyHex()

	cfDir := filepath.Join(transportBaseDir, campfireID)
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

	tr = fs.New(transportBaseDir)
	return campfireID, cfID, transportBaseDir, tr
}

// writeSyncZeroProvenanceMessage writes a validly-signed message with NO
// provenance hops to the transport — the attack vector for this regression.
func writeSyncZeroProvenanceMessage(t *testing.T, campfireID string, tr *fs.Transport, payload string, tags []string) *message.Message {
	t.Helper()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating sender identity: %v", err)
	}

	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, []string{})
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}

	// Deliberately do NOT call msg.AddHop() — zero provenance hops.
	if err := tr.WriteMessage(campfireID, msg); err != nil {
		t.Fatalf("writing zero-provenance message to transport: %v", err)
	}
	return msg
}

// writeSyncValidMessage writes a validly-signed message WITH a provenance hop
// to the transport (positive control).
func writeSyncValidMessage(t *testing.T, campfireID string, cfID *identity.Identity, tr *fs.Transport, payload string, tags []string) *message.Message {
	t.Helper()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating sender identity: %v", err)
	}

	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, []string{})
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}

	if err := msg.AddHop(cfID.PrivateKey, cfID.PublicKey, nil, 1, "open", []string{}, "full"); err != nil {
		t.Fatalf("adding provenance hop: %v", err)
	}

	if err := tr.WriteMessage(campfireID, msg); err != nil {
		t.Fatalf("writing valid message to transport: %v", err)
	}
	return msg
}

// TestSyncFromFilesystem_RejectsZeroProvenanceMessage verifies that
// syncFromFilesystem silently drops messages with no provenance hops, while
// messages with a valid hop are accepted (positive control).
func TestSyncFromFilesystem_RejectsZeroProvenanceMessage(t *testing.T) {
	campfireID, cfID, transportBaseDir, tr := setupSyncTestCampfire(t)

	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Write a zero-provenance message to the filesystem transport.
	zeroProvMsg := writeSyncZeroProvenanceMessage(t, campfireID, tr, "should be rejected", []string{"status"})

	// Write a valid message with a provenance hop (positive control).
	validMsg := writeSyncValidMessage(t, campfireID, cfID, tr, "should be accepted", []string{"status"})

	// Call syncFromFilesystem directly.
	syncFromFilesystem(campfireID, filepath.Join(transportBaseDir, campfireID), s)

	// Query the store to see what was stored.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	storedIDs := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		storedIDs[m.ID] = true
	}

	// Zero-provenance message must NOT be in the store.
	if storedIDs[zeroProvMsg.ID] {
		t.Errorf("zero-provenance message %q was accepted by syncFromFilesystem — guard missing or broken", zeroProvMsg.ID)
	}

	// Valid message WITH a provenance hop MUST be in the store (positive control).
	if !storedIDs[validMsg.ID] {
		t.Errorf("valid message %q (with provenance hop) was not found in store after sync — positive control failed", validMsg.ID)
	}
}

package protocol_test

// Regression test for campfire-agent-zi9: syncIfFilesystem must reject messages
// with zero provenance hops.
//
// Fix: read.go added `len(fsMsg.Provenance) == 0` guard. This test ensures the
// guard stays in place — a zero-provenance message must never reach the store.

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// writeZeroProvenanceMessage writes a validly-signed message with NO provenance
// hops directly to the filesystem transport. This is the attack vector being
// tested: an attacker writes a message that would pass the hop-verification loop
// trivially (zero iterations) if the zero-provenance guard were absent.
func writeZeroProvenanceMessage(t *testing.T, campfireID string, tr interface {
	WriteMessage(string, *message.Message) error
}, payload string, tags []string) *message.Message {
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

// TestSyncIfFilesystem_RejectsZeroProvenanceMessage verifies that syncIfFilesystem
// (triggered by protocol.Client.Read) silently drops messages with no provenance
// hops, while messages with a valid hop are accepted.
func TestSyncIfFilesystem_RejectsZeroProvenanceMessage(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write a zero-provenance message to the filesystem transport.
	zeroProvMsg := writeZeroProvenanceMessage(t, campfireID, tr, "should be rejected", []string{"status"})

	// Write a valid message with a provenance hop (positive control).
	validMsg := writeTransportMessage(t, cfID, tr, campfireID, "should be accepted", []string{"status"})

	// Trigger syncIfFilesystem by calling Read (SkipSync=false).
	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
		SkipSync:         false,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Build a set of stored message IDs for easy lookup.
	storedIDs := make(map[string]bool, len(result.Messages))
	for _, m := range result.Messages {
		storedIDs[m.ID] = true
	}

	// Zero-provenance message must NOT be in the store.
	if storedIDs[zeroProvMsg.ID] {
		t.Errorf("zero-provenance message %q was accepted by syncIfFilesystem — guard missing or broken", zeroProvMsg.ID)
	}

	// Valid message WITH a provenance hop MUST be in the store (positive control).
	if !storedIDs[validMsg.ID] {
		t.Errorf("valid message %q (with provenance hop) was not found in store after sync — positive control failed", validMsg.ID)
	}
}

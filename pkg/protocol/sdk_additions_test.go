package protocol_test

// Tests for SDK 0.12 additions (campfire-agent-liu):
//   1. CreateRequest.Description — stored in membership metadata
//   4. CreateResult.BeaconID — hex beacon ID equals CampfireID
//   5. IsBridged() on protocol.Message

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// TestCreateDescription verifies that CreateRequest.Description is stored in
// the membership record and retrievable after Create().
func TestCreateDescription(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	configDir := t.TempDir()

	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	const wantDesc = "my test campfire"

	result, err := client.Create(protocol.CreateRequest{
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
		Description: wantDesc,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify description is stored in membership.
	m, err := client.ClientStore().GetMembership(result.CampfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("membership record is nil after Create")
	}
	if m.Description != wantDesc {
		t.Errorf("membership Description = %q, want %q", m.Description, wantDesc)
	}
}

// TestCreateBeaconID verifies that CreateResult.BeaconID equals CampfireID
// and is non-empty.
func TestCreateBeaconID(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	configDir := t.TempDir()

	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	result, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if result.BeaconID == "" {
		t.Error("CreateResult.BeaconID is empty")
	}
	if result.BeaconID != result.CampfireID {
		t.Errorf("BeaconID %q != CampfireID %q", result.BeaconID, result.CampfireID)
	}
}

// writeBlindRelayTransportMessage writes a message with a blind-relay provenance
// hop directly to the filesystem transport.
func writeBlindRelayTransportMessage(t *testing.T, cfID *identity.Identity, tr *fs.Transport, campfireID string, payload string, tags []string) *message.Message {
	t.Helper()

	senderID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating sender identity: %v", err)
	}

	msg, err := message.NewMessage(senderID.PrivateKey, senderID.PublicKey, []byte(payload), tags, []string{})
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}

	// Add provenance hop with "blind-relay" role — this is what IsBridged() detects.
	if err := msg.AddHop(cfID.PrivateKey, cfID.PublicKey, []byte("testhash"), 1, "open", []string{}, "blind-relay"); err != nil {
		t.Fatalf("adding blind-relay provenance hop: %v", err)
	}

	if err := tr.WriteMessage(campfireID, msg); err != nil {
		t.Fatalf("writing message to transport: %v", err)
	}
	return msg
}

// TestMessageIsBridged verifies that IsBridged() returns true when a message
// was relayed through a blind-relay hop, and false for a normal full-member hop.
// Read() returns protocol.Message directly; IsBridged() is called on those values.
func TestMessageIsBridged(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write a regular (full-member hop) message.
	regularMsg := writeTransportMessage(t, cfID, tr, campfireID, "hello from full member", []string{"status"})

	// Write a blind-relay (bridged) message.
	bridgedMsg := writeBlindRelayTransportMessage(t, cfID, tr, campfireID, "hello from bridge", []string{"status"})

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
		SkipSync:         false,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Find our messages in the result (Read returns []protocol.Message directly).
	var gotRegular, gotBridged *protocol.Message
	for i := range result.Messages {
		switch result.Messages[i].ID {
		case regularMsg.ID:
			gotRegular = &result.Messages[i]
		case bridgedMsg.ID:
			gotBridged = &result.Messages[i]
		}
	}

	if gotRegular == nil {
		t.Fatal("regular message not found in Read results")
	}
	if gotBridged == nil {
		t.Fatal("bridged message not found in Read results")
	}

	if gotRegular.IsBridged() {
		t.Error("regular message (full-member hop) IsBridged() = true, want false")
	}
	if !gotBridged.IsBridged() {
		t.Error("bridged message (blind-relay hop) IsBridged() = false, want true")
	}
}

package provenance_test

// bridge_level_test.go — tests for LevelFromMessage provenance tiers.
//
// Covered bead: campfire-agent-0ca
//
// Tests verify:
//   - TestBridgeIngressLevel2: a message that traversed a blind-relay hop reports
//     LevelContactable (2) via LevelFromMessage.
//   - TestQuorumCallLevel3: a message sent by a known root key reports LevelPresent (3)
//     via LevelFromMessage.
//
// No mocks: tests use real campfire clients and real filesystem transport.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/provenance"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// blSetupEnv creates a minimal test environment: an identity, a SQLite store,
// and a temp transport directory.
func blSetupEnv(t *testing.T) (*identity.Identity, store.Store, string) {
	t.Helper()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	transportDir := t.TempDir()
	return id, s, transportDir
}

// blSetupCampfire creates a campfire using a separate identity as the campfire key
// (mirrors setupFilesystemCampfire in pkg/protocol/client_test.go).
func blSetupCampfire(t *testing.T, agentID *identity.Identity, s store.Store, transportDir string) string {
	t.Helper()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	cfDir := filepath.Join(transportDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s directory: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	tr := fs.New(transportDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("WriteMember: %v", err)
	}
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	return campfireID
}

// blAddMember adds a second identity as a member of an existing campfire.
func blAddMember(t *testing.T, id *identity.Identity, s store.Store, transportDir, campfireID string) {
	t.Helper()
	tr := fs.New(transportDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: id.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("WriteMember (add member): %v", err)
	}
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("AddMembership (add member): %v", err)
	}
}

// blHopsFromMessage converts a protocol.Message's Provenance chain to
// []provenance.MessageHop for use with LevelFromMessage.
func blHopsFromMessage(msg *protocol.Message) []provenance.MessageHop {
	hops := make([]provenance.MessageHop, len(msg.Provenance))
	for i, h := range msg.Provenance {
		hops[i] = provenance.MessageHop{Role: h.Role}
	}
	return hops
}

// TestBridgeIngressLevel2 verifies that a message forwarded through protocol.Bridge()
// has a provenance level of LevelContactable (2) when evaluated via LevelFromMessage.
//
// Bridge() sends with RoleOverride: "blind-relay", which means the forwarded message's
// provenance chain contains a blind-relay hop. LevelFromMessage detects this and returns
// LevelContactable.
func TestBridgeIngressLevel2(t *testing.T) {
	srcID, srcStore, transportDir := blSetupEnv(t)
	campfireID := blSetupCampfire(t, srcID, srcStore, transportDir)
	source := protocol.New(srcStore, srcID)

	destID, destStore, _ := blSetupEnv(t)
	blAddMember(t, destID, destStore, transportDir, campfireID)
	dest := protocol.New(destStore, destID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{}) //nolint:errcheck
	}()

	// Give bridge time to subscribe.
	time.Sleep(300 * time.Millisecond)

	// Send a message from source.
	_, err := source.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("level2 bridge test"),
		Tags:       []string{"test"},
	})
	if err != nil {
		t.Fatalf("source.Send: %v", err)
	}

	// Wait for the bridged message to appear on dest (re-sent by dest identity).
	var bridgedMsg *protocol.Message
	deadline := time.After(10 * time.Second)
	for {
		result, err := dest.Read(protocol.ReadRequest{CampfireID: campfireID})
		if err != nil {
			t.Fatalf("dest.Read: %v", err)
		}
		for i := range result.Messages {
			msg := result.Messages[i]
			if string(msg.Payload) == "level2 bridge test" && msg.Sender == destID.PublicKeyHex() {
				bridgedMsg = &msg
				break
			}
		}
		if bridgedMsg != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for bridged message")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	cancel()

	// Convert provenance hops and compute level.
	hops := blHopsFromMessage(bridgedMsg)
	level := provenance.LevelFromMessage(hops, bridgedMsg.Sender, nil)

	if level != provenance.LevelContactable {
		t.Errorf("LevelFromMessage for bridged message = %v (%d), want LevelContactable (%d)",
			level, int(level), int(provenance.LevelContactable))
	}
}

// TestQuorumCallLevel3 verifies that a message whose sender key is in the
// provided root-key set is elevated to LevelPresent (3) by LevelFromMessage.
//
// This represents a quorum-call resolution: when a message is signed by a known
// root (center campfire) key, it is granted the highest operator provenance level.
func TestQuorumCallLevel3(t *testing.T) {
	// Set up a sender identity that will act as the "root key".
	rootID, rootStore, transportDir := blSetupEnv(t)
	campfireID := blSetupCampfire(t, rootID, rootStore, transportDir)
	rootClient := protocol.New(rootStore, rootID)

	// Send a message from the root key.
	_, err := rootClient.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("root key signed message"),
		Tags:       []string{"test"},
	})
	if err != nil {
		t.Fatalf("rootClient.Send: %v", err)
	}

	result, err := rootClient.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("rootClient.Read: %v", err)
	}

	var rootMsg *protocol.Message
	for i := range result.Messages {
		msg := result.Messages[i]
		if string(msg.Payload) == "root key signed message" {
			rootMsg = &msg
			break
		}
	}
	if rootMsg == nil {
		t.Fatal("root key message not found in read result")
	}

	// Register the sender's key as a known root key (simulates quorum call resolution).
	rootKeys := map[string]bool{
		rootMsg.Sender: true,
	}

	hops := blHopsFromMessage(rootMsg)
	level := provenance.LevelFromMessage(hops, rootMsg.Sender, rootKeys)

	if level != provenance.LevelPresent {
		t.Errorf("LevelFromMessage for root-key message = %v (%d), want LevelPresent (%d)",
			level, int(level), int(provenance.LevelPresent))
	}
}

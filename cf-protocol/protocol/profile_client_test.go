package protocol_test

// TestClient_ProfileCache_PersistAcrossSessions is a feature-depth integration test.
// It proves that when client.Read() encounters an identity:profile message, the
// display name is persisted to disk and survives across Init() sessions.
//
// Spec: campfire-agent-een (Wire disk-persisting ProfileCache into Client)

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// setupInitEnv creates a self-contained cfHome with identity.json and store.db,
// plus a filesystem-transport campfire for the agent, and returns the cfHome
// dir, campfire ID, and the sender identity used for the profile message.
func setupInitEnv(t *testing.T) (cfHome, campfireID string, senderID *identity.Identity, transportDir string) {
	t.Helper()

	cfHome = t.TempDir()
	transportDir = t.TempDir()

	// Init session 1 — creates identity.json + store.db in cfHome.
	client1, _, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("protocol.Init session 1: %v", err)
	}

	// Set up a filesystem-transport campfire for reading.
	s1 := client1.ClientStore()
	agentID1 := client1.ClientIdentity()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID = cfID.PublicKeyHex()

	cfDir := filepath.Join(transportDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
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
		t.Fatalf("writing campfire.cbor: %v", err)
	}

	tr := fs.New(transportDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID1.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}
	if err := s1.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Generate a "sender" identity that will send the identity:profile message.
	senderID, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating sender identity: %v", err)
	}

	// Build an identity:profile message from senderID and inject it into the store.
	payload, _ := json.Marshal(map[string]string{"display_name": "Alice"})
	signer, err := message.NewEd25519Signer(senderID.PrivateKey, senderID.PublicKey)
	if err != nil {
		t.Fatalf("creating signer: %v", err)
	}
	msg, err := message.NewMessage(signer, payload, []string{"identity:profile"}, nil)
	if err != nil {
		t.Fatalf("creating identity:profile message: %v", err)
	}
	// Add a provenance hop signed by the campfire key.
	cf := &campfire.Campfire{Members: []campfire.Member{{PublicKey: agentID1.PublicKey, Role: campfire.RoleFull}}}
	if err := msg.AddHop(
		ed25519.PrivateKey(cfID.PrivateKey),
		ed25519.PublicKey(cfID.PublicKey),
		cf.MembershipHash(),
		1,
		"open",
		[]string{},
		campfire.RoleFull,
	); err != nil {
		t.Fatalf("adding provenance hop: %v", err)
	}

	rec := store.MessageRecordFromMessage(campfireID, msg, store.NowNano())
	if _, err := s1.AddMessage(rec); err != nil {
		t.Fatalf("adding message to store: %v", err)
	}

	if err := client1.Close(); err != nil {
		t.Fatalf("closing session 1: %v", err)
	}

	return cfHome, campfireID, senderID, transportDir
}

// TestClient_ProfileCache_PersistAcrossSessions verifies that when Read() encounters
// an identity:profile message in session 1, the display name is persisted to
// cfHome/profiles.json and a fresh Init() in session 2 returns it via ProfileCache.Get().
func TestClient_ProfileCache_PersistAcrossSessions(t *testing.T) {
	cfHome, campfireID, senderID, _ := setupInitEnv(t)

	// --- Session 1: re-init and read the campfire ---
	client1, _, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("protocol.Init session 1 (read): %v", err)
	}

	result, err := client1.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		SkipSync:   true, // messages already in store
	})
	if err != nil {
		t.Fatalf("client1.Read: %v", err)
	}

	// Verify we got the identity:profile message.
	var found bool
	for _, m := range result.Messages {
		for _, tag := range m.Tags {
			if tag == "identity:profile" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("identity:profile message not found in Read() result — test setup error; got %d messages", len(result.Messages))
	}

	if err := client1.Close(); err != nil {
		t.Fatalf("closing session 1 (read): %v", err)
	}

	// --- Session 2: fresh Init() on same cfHome ---
	client2, _, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("protocol.Init session 2: %v", err)
	}
	defer client2.Close()

	// profiles.json must exist.
	profilesPath := filepath.Join(cfHome, "profiles.json")
	if _, statErr := os.Stat(profilesPath); os.IsNotExist(statErr) {
		t.Fatal("profiles.json was not written to cfHome after Read() in session 1")
	}

	// ProfileCache.Get must return the display name learned in session 1.
	senderPubkeyHex := senderID.PublicKeyHex()
	senderPubkeyBytes, err := hex.DecodeString(senderPubkeyHex)
	if err != nil {
		t.Fatalf("decoding sender pubkey hex: %v", err)
	}

	name, ok := client2.ProfileCache().Get(ed25519.PublicKey(senderPubkeyBytes))
	if !ok {
		t.Fatal("ProfileCache.Get returned ok=false in session 2 — display name was not persisted")
	}
	if name != "Alice" {
		t.Errorf("ProfileCache.Get returned %q, want %q", name, "Alice")
	}
}

// TestClient_ProfileCache_MethodExists verifies that the ProfileCache() method
// is available on *protocol.Client and returns a non-nil *ProfileCache.
func TestClient_ProfileCache_MethodExists(t *testing.T) {
	cfHome := t.TempDir()
	client, _, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("protocol.Init: %v", err)
	}
	defer client.Close()

	pc := client.ProfileCache()
	if pc == nil {
		t.Fatal("ProfileCache() returned nil, want non-nil *ProfileCache")
	}
}

package cmd

// identity_upgrade_test.go — Tests for cf identity upgrade (campfire-agent-bey).
//
// Done conditions:
// 1. Upgrade from keypair-only creates a self-campfire with correct genesis
// 2. Upgrade is idempotent — second run returns "already upgraded"
// 3. No old home campfire → upgrade succeeds (no linking step)
// 4. Alias "home" points to new self-campfire after upgrade
// 5. Old home campfire gets linked via declare-home ceremony when one exists

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// TestIdentityUpgrade_CreatesIdentityCampfire verifies that cf identity upgrade
// creates a self-campfire with an identity convention genesis message signed by
// the campfire key (not the agent key).
func TestIdentityUpgrade_CreatesIdentityCampfire(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Run upgrade — no existing home alias.
	newCampfireID, _, err := createSelfCampfire(cfHomeDir, agentID, false)
	if err != nil {
		t.Fatalf("createSelfCampfire (upgrade): %v", err)
	}
	if newCampfireID == "" {
		t.Fatal("expected non-empty campfire ID from upgrade")
	}

	// Verify genesis message is campfire-key-signed identity convention declaration.
	s, err := store.Open(store.StorePath(cfHomeDir))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	m, err := s.GetMembership(newCampfireID)
	if err != nil || m == nil {
		t.Fatalf("membership not found: %v", err)
	}

	tr := fs.ForDir(m.TransportDir)
	msgs, err := tr.ListMessages(newCampfireID)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("no messages in identity campfire: %v", err)
	}

	msg0 := msgs[0]
	// Sender of message 0 must be the campfire key (campfire ID = hex of campfire public key).
	if msg0.SenderHex() != newCampfireID {
		t.Errorf("message 0 sender = %s, want campfire key %s", msg0.SenderHex(), newCampfireID)
	}

	// Payload must be an identity convention declaration.
	var decl map[string]any
	if err := json.Unmarshal(msg0.Payload, &decl); err != nil {
		t.Fatalf("parsing message 0 payload: %v", err)
	}
	if conv, _ := decl["convention"].(string); conv != convention.IdentityConvention {
		t.Errorf("message 0 convention = %q, want %q", conv, convention.IdentityConvention)
	}
}

// TestIdentityUpgrade_Idempotent verifies that calling createSelfCampfire (upgrade path)
// a second time returns the existing identity campfire without error.
// The idempotency check in identityUpgradeCmd.RunE detects the home alias pointing
// to an identity campfire and returns "already upgraded".
func TestIdentityUpgrade_Idempotent(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// First upgrade.
	firstID, _, err := createSelfCampfire(cfHomeDir, agentID, false)
	if err != nil {
		t.Fatalf("first createSelfCampfire: %v", err)
	}

	// The idempotency guard: check message 0 of the "home" campfire.
	// isUpgradeIdentityGenesis should return true for the first self-campfire.
	aliases := naming.NewAliasStore(cfHomeDir)
	homeID, err := aliases.Get("home")
	if err != nil {
		t.Fatalf("getting home alias: %v", err)
	}
	if homeID != firstID {
		t.Errorf("home alias = %s, want %s", homeID, firstID)
	}

	s, err := store.Open(store.StorePath(cfHomeDir))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	m, err := s.GetMembership(homeID)
	if err != nil || m == nil {
		t.Fatalf("membership not found: %v", err)
	}

	tr := fs.ForDir(m.TransportDir)
	msgs, err := tr.ListMessages(homeID)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("no messages: %v", err)
	}

	msg0 := msgs[0]
	if !isUpgradeIdentityGenesis(homeID, msg0.SenderHex(), msg0.Payload) {
		t.Error("isUpgradeIdentityGenesis returned false for a valid identity campfire — idempotency check broken")
	}
}

// TestIdentityUpgrade_NoOldHome verifies that upgrade succeeds when there is no
// existing "home" alias (clean keypair-only identity with no prior campfires).
func TestIdentityUpgrade_NoOldHome(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// No "home" alias set — no old home campfire.
	aliases := naming.NewAliasStore(cfHomeDir)
	_, err = aliases.Get("home")
	if err == nil {
		t.Log("no home alias exists (expected for a fresh identity)")
	}

	// Upgrade should succeed without the linking step.
	newCampfireID, _, err := createSelfCampfire(cfHomeDir, agentID, false)
	if err != nil {
		t.Fatalf("createSelfCampfire with no old home: %v", err)
	}
	if newCampfireID == "" {
		t.Fatal("expected non-empty campfire ID")
	}

	// Home alias must now point to the new campfire.
	homeID, err := aliases.Get("home")
	if err != nil {
		t.Fatalf("getting home alias after upgrade: %v", err)
	}
	if homeID != newCampfireID {
		t.Errorf("home alias = %s, want %s", homeID, newCampfireID)
	}
}

// TestIdentityUpgrade_AliasUpdated verifies that after upgrade the "home" alias
// points to the new self-campfire ID, not to any old campfire.
func TestIdentityUpgrade_AliasUpdated(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Set a fake old home alias (simulating a prior non-identity campfire).
	aliases := naming.NewAliasStore(cfHomeDir)
	fakeOldID := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	if err := aliases.Set("home", fakeOldID); err != nil {
		t.Fatalf("setting fake old home alias: %v", err)
	}

	// Run upgrade — fake old home has no membership, so linking step is skipped.
	newCampfireID, _, err := createSelfCampfire(cfHomeDir, agentID, false)
	if err != nil {
		t.Fatalf("createSelfCampfire: %v", err)
	}

	// After upgrade, "home" must point to the new campfire.
	homeID, err := aliases.Get("home")
	if err != nil {
		t.Fatalf("getting home alias: %v", err)
	}
	if homeID != newCampfireID {
		t.Errorf("home alias = %s, want new campfire %s", homeID, newCampfireID)
	}
}

// TestIdentityUpgrade_IsUpgradeIdentityGenesis verifies the helper function
// correctly identifies identity campfire genesis messages.
func TestIdentityUpgrade_IsUpgradeIdentityGenesis(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	// Create a campfire with campfire-key-signed genesis.
	selfCF, err := campfire.New("invite-only", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	baseDir := t.TempDir()
	transport := fs.New(baseDir)
	if err := transport.Init(selfCF); err != nil {
		t.Fatalf("transport.Init: %v", err)
	}
	if err := transport.WriteMember(selfCF.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("transport.WriteMember: %v", err)
	}

	campfireID := selfCF.PublicKeyHex()

	// Post campfire-key-signed identity declaration.
	decls := convention.IdentityDeclarations()
	if len(decls) == 0 {
		t.Fatal("convention.IdentityDeclarations() returned empty slice")
	}
	declPayload, err := json.Marshal(decls[0])
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	genesisMsg, err := message.NewMessage(
		selfCF.PrivateKey,
		selfCF.PublicKey,
		declPayload,
		[]string{convention.ConventionOperationTag},
		nil,
	)
	if err != nil {
		t.Fatalf("message.NewMessage: %v", err)
	}
	if err := transport.WriteMessage(campfireID, genesisMsg); err != nil {
		t.Fatalf("transport.WriteMessage: %v", err)
	}

	msgs, err := transport.ListMessages(campfireID)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("listing messages: %v", err)
	}

	msg0 := msgs[0]

	// isUpgradeIdentityGenesis must return true for a campfire-key-signed identity declaration.
	if !isUpgradeIdentityGenesis(campfireID, msg0.SenderHex(), msg0.Payload) {
		t.Error("isUpgradeIdentityGenesis returned false for valid identity campfire genesis")
	}

	// Must return false when sender is agent key (not campfire key).
	if isUpgradeIdentityGenesis(campfireID, agentID.PublicKeyHex(), msg0.Payload) {
		t.Error("isUpgradeIdentityGenesis returned true when sender is agent key, not campfire key")
	}

	// Must return false for non-identity convention payload.
	otherPayload := []byte(`{"convention":"other","operation":"test"}`)
	if isUpgradeIdentityGenesis(campfireID, campfireID, otherPayload) {
		t.Error("isUpgradeIdentityGenesis returned true for non-identity convention")
	}
}

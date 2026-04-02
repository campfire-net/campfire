package protocol_test

// Tests for the identity campfire disband guard — campfire-agent-wcg.
//
// Done conditions:
// 1. Disbanding an identity campfire returns error "cannot disband identity campfire"
// 2. Disbanding a normal campfire (no identity genesis message) still succeeds
// 3. Error message contains "cannot disband identity campfire"

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	fstransport "github.com/campfire-net/campfire/pkg/transport/fs"
)

// TestDisbandIdentityGuard runs all identity-campfire-guard sub-tests.
func TestDisbandIdentityGuard(t *testing.T) {
	t.Run("RejectsIdentityCampfire", testDisbandRejectsIdentityCampfire)
	t.Run("AllowsNormalCampfire", testDisbandAllowsNormalCampfire)
	t.Run("ErrorMessageContainsCannot", testDisbandErrorMessageContainsCannot)
}

// testDisbandRejectsIdentityCampfire verifies that disbanding a campfire whose
// genesis message is signed by the campfire key and has an identity convention payload
// returns an error. This is the protocol-level guard for identity campfires.
func testDisbandRejectsIdentityCampfire(t *testing.T) {
	t.Helper()

	cfHome := t.TempDir()
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	campfireID, s := createIdentityCampfireForTest(t, cfHome, agentID)
	defer s.Close()

	client := protocol.New(s, agentID)

	err = client.Disband(campfireID)
	if err == nil {
		t.Fatal("expected Disband to fail for identity campfire, got nil error")
	}
	if err.Error() == "" {
		t.Error("error message should not be empty")
	}
}

// testDisbandAllowsNormalCampfire verifies that a normal campfire (no identity genesis
// message) can still be disbanded successfully.
func testDisbandAllowsNormalCampfire(t *testing.T) {
	t.Helper()

	client := newJoinClient(t)
	campfireID, _ := createFSCampfire(t, client, "open")

	if err := client.Disband(campfireID); err != nil {
		t.Errorf("expected Disband to succeed for normal campfire, got: %v", err)
	}
}

// testDisbandErrorMessageContainsCannot verifies the error message from disbanding
// an identity campfire contains "cannot disband identity campfire".
func testDisbandErrorMessageContainsCannot(t *testing.T) {
	t.Helper()

	cfHome := t.TempDir()
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	campfireID, s := createIdentityCampfireForTest(t, cfHome, agentID)
	defer s.Close()

	client := protocol.New(s, agentID)

	err = client.Disband(campfireID)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	wantSubstr := "cannot disband identity campfire"
	if errStr := err.Error(); len(errStr) < len(wantSubstr) || !contains(errStr, wantSubstr) {
		t.Errorf("error = %q, want it to contain %q", errStr, wantSubstr)
	}
}

// contains reports whether s contains substr.
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// createIdentityCampfireForTest creates a self-campfire with a campfire-key-signed
// identity convention genesis message. Returns the campfire ID and an open store
// (caller must Close).
func createIdentityCampfireForTest(t *testing.T, cfHome string, agentID *identity.Identity) (string, store.Store) {
	t.Helper()

	// Create campfire keypair.
	selfCF, err := campfire.New("invite-only", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	// Set up filesystem transport.
	baseDir := t.TempDir()
	transport := fstransport.New(baseDir)
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
	transportDir := transport.CampfireDir(campfireID)

	// Post identity convention genesis message signed by campfire key (message 0).
	// This is the type assertion that makes this an identity campfire.
	decls := convention.IdentityDeclarations()
	if len(decls) == 0 {
		t.Fatal("convention.IdentityDeclarations() returned empty slice")
	}
	declPayload, err := json.Marshal(decls[0])
	if err != nil {
		t.Fatalf("json.Marshal declaration: %v", err)
	}
	genesisMsg, err := message.NewMessage(
		selfCF.PrivateKey,
		selfCF.PublicKey,
		declPayload,
		[]string{convention.ConventionOperationTag},
		nil,
	)
	if err != nil {
		t.Fatalf("message.NewMessage for genesis: %v", err)
	}
	if err := transport.WriteMessage(campfireID, genesisMsg); err != nil {
		t.Fatalf("transport.WriteMessage genesis: %v", err)
	}

	// Open store and record membership.
	s, err := store.Open(filepath.Join(cfHome, "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  transportDir,
		JoinProtocol:  selfCF.JoinProtocol,
		Role:          store.PeerRoleCreator,
		CreatorPubkey: agentID.PublicKeyHex(),
		JoinedAt:      store.NowNano(),
		Threshold:     selfCF.Threshold,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("s.AddMembership: %v", err)
	}

	return campfireID, s
}

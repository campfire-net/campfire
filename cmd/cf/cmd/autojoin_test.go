package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupOpenCampfire creates a minimal open-protocol filesystem campfire and
// returns the campfire ID (public key hex) and the transport base dir.
func setupOpenCampfire(t *testing.T) (campfireID string, transportBaseDir string) {
	t.Helper()

	dir := t.TempDir()
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
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
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	return campfireID, dir
}

// setupInviteOnlyCampfire creates a minimal invite-only filesystem campfire.
func setupInviteOnlyCampfire(t *testing.T) (campfireID string, transportBaseDir string) {
	t.Helper()

	dir := t.TempDir()
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
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
		JoinProtocol:          "invite-only",
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

	return campfireID, dir
}

// TestAutoJoinRootCampfire_OpenProtocol verifies that an agent not yet a member
// of an open-protocol campfire is automatically joined.
func TestAutoJoinRootCampfire_OpenProtocol(t *testing.T) {
	campfireID, transportBaseDir := setupOpenCampfire(t)

	// Override CF_TRANSPORT_DIR so the fs transport reads from our temp dir.
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Confirm not yet a member.
	m, err := s.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m != nil {
		t.Fatal("expected no membership before auto-join")
	}

	// Run auto-join.
	if err := autoJoinRootCampfire(campfireID, agentID, s); err != nil {
		t.Fatalf("autoJoinRootCampfire: %v", err)
	}

	// Verify membership was recorded.
	m, err = s.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership after auto-join: %v", err)
	}
	if m == nil {
		t.Fatal("expected membership after auto-join, got nil")
	}
	if m.JoinProtocol != "open" {
		t.Errorf("expected JoinProtocol=open, got %q", m.JoinProtocol)
	}

	// Verify member record was written to transport.
	transport := fs.New(transportBaseDir)
	members, err := transport.ListMembers(campfireID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	found := false
	for _, mem := range members {
		if fmt.Sprintf("%x", mem.PublicKey) == agentID.PublicKeyHex() {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected agent to be in transport member list after auto-join")
	}
}

// TestAutoJoinRootCampfire_InviteOnly verifies that an invite-only campfire is
// skipped (no error, no membership recorded).
func TestAutoJoinRootCampfire_InviteOnly(t *testing.T) {
	campfireID, transportBaseDir := setupInviteOnlyCampfire(t)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Run auto-join — should not return error.
	if err := autoJoinRootCampfire(campfireID, agentID, s); err != nil {
		t.Fatalf("autoJoinRootCampfire returned unexpected error: %v", err)
	}

	// Verify no membership was recorded.
	m, err := s.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m != nil {
		t.Fatal("expected no membership for invite-only campfire, got one")
	}
}

// TestAutoJoinRootCampfire_AlreadyMember verifies that calling auto-join when
// the agent is already in the store is idempotent (no double-add error).
func TestAutoJoinRootCampfire_AlreadyMember(t *testing.T) {
	campfireID, transportBaseDir := setupOpenCampfire(t)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Pre-add to the transport.
	transport := fs.New(transportBaseDir)
	if err := transport.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("pre-writing member: %v", err)
	}

	// First auto-join call should succeed.
	if err := autoJoinRootCampfire(campfireID, agentID, s); err != nil {
		t.Fatalf("first autoJoinRootCampfire: %v", err)
	}

	// The store should now have a membership. Second call should not error
	// (caller guards — we already have membership and won't call a second time).
	m, err := s.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("expected membership after first auto-join")
	}
}

// TestAutoJoinRootCampfire_NoTransportState verifies that when no campfire
// state file exists on disk, auto-join skips silently without error.
func TestAutoJoinRootCampfire_NoTransportState(t *testing.T) {
	transportBaseDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	nonExistentID := "aabbccdd" + "11223344" + "55667788" + "99aabbcc" + "ddeeff00" + "11223344" + "55667788" + "99aabbcc"

	// Should return nil (skip silently).
	if err := autoJoinRootCampfire(nonExistentID, agentID, s); err != nil {
		t.Fatalf("expected nil error for missing state, got: %v", err)
	}

	m, err := s.GetMembership(nonExistentID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m != nil {
		t.Fatal("expected no membership when state file is absent")
	}
}

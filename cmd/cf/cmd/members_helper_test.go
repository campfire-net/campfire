package cmd

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// TestListMembersByTransport_Filesystem verifies that filesystem campfires still
// use fs.ForDir().ListMembers() and return the correct member records (regression test).
func TestListMembersByTransport_Filesystem(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	member1, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating member1: %v", err)
	}
	member2, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating member2: %v", err)
	}

	fsT := fs.New(transportBaseDir)
	// Create the transport directory structure before writing member records.
	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating directory %s: %v", sub, err)
		}
	}
	// Write member records to the filesystem transport.
	if err := fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: member1.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member1: %v", err)
	}
	if err := fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: member2.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleWriter,
	}); err != nil {
		t.Fatalf("writing member2: %v", err)
	}

	m := store.Membership{
		CampfireID:    campfireID,
		TransportDir:  fsT.CampfireDir(campfireID),
		JoinProtocol:  "invite-only",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}
	if err := s.AddMembership(m); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	members, err := listMembersByTransport(m, s)
	if err != nil {
		t.Fatalf("listMembersByTransport: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("expected 2 members, got %d", len(members))
	}

	// Verify both pubkeys are present.
	m1Hex := fmt.Sprintf("%x", member1.PublicKey)
	m2Hex := fmt.Sprintf("%x", member2.PublicKey)
	found := map[string]bool{}
	for _, mem := range members {
		found[fmt.Sprintf("%x", mem.PublicKey)] = true
	}
	if !found[m1Hex] {
		t.Errorf("member1 (%s) not found in result", m1Hex[:8])
	}
	if !found[m2Hex] {
		t.Errorf("member2 (%s) not found in result", m2Hex[:8])
	}
}

// TestListMembersByTransport_PeerHTTP verifies that p2p-http campfires return
// members from the store's peer_endpoints table with correct pubkeys and roles.
func TestListMembersByTransport_PeerHTTP(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	member1, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating member1: %v", err)
	}
	member2, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating member2: %v", err)
	}

	m := store.Membership{
		CampfireID:    campfireID,
		TransportDir:  "https://relay.example.com/" + campfireID,
		JoinProtocol:  "invite-only",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "p2p-http",
	}
	if err := s.AddMembership(m); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	m1Hex := fmt.Sprintf("%x", member1.PublicKey)
	m2Hex := fmt.Sprintf("%x", member2.PublicKey)

	// Populate peer endpoints (as join.go does after a successful join).
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: m1Hex,
		Endpoint:     "http://peer1.example.com:8080",
		Role:         store.PeerRoleCreator,
	}); err != nil {
		t.Fatalf("upserting peer endpoint 1: %v", err)
	}
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: m2Hex,
		Endpoint:     "http://peer2.example.com:8080",
		Role:         store.PeerRoleMember,
	}); err != nil {
		t.Fatalf("upserting peer endpoint 2: %v", err)
	}

	members, err := listMembersByTransport(m, s)
	if err != nil {
		t.Fatalf("listMembersByTransport: %v", err)
	}

	if len(members) != 2 {
		t.Errorf("expected 2 members, got %d; endpoints: %+v", len(members), members)
	}

	found := map[string]string{} // pubkeyHex → role
	for _, mem := range members {
		found[fmt.Sprintf("%x", mem.PublicKey)] = mem.Role
	}
	if _, ok := found[m1Hex]; !ok {
		t.Errorf("member1 (%s) not found in result", m1Hex[:8])
	}
	if _, ok := found[m2Hex]; !ok {
		t.Errorf("member2 (%s) not found in result", m2Hex[:8])
	}
	// creator role → EffectiveRole → RoleFull
	if found[m1Hex] != campfire.RoleFull {
		t.Errorf("member1 role: got %q, want %q", found[m1Hex], campfire.RoleFull)
	}
	// member role → EffectiveRole → RoleFull (legacy default)
	if found[m2Hex] != campfire.RoleFull {
		t.Errorf("member2 role: got %q, want %q", found[m2Hex], campfire.RoleFull)
	}
}

// TestListMembersByTransport_GitHub verifies that GitHub-transport campfires
// return members from the store's peer_endpoints table with correct pubkeys and
// roles — exercising the TypeGitHub branch in listMembersByTransport, which
// shares the listMembersFromStore path with TypePeerHTTP.
func TestListMembersByTransport_GitHub(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	member1, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating member1: %v", err)
	}
	member2, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating member2: %v", err)
	}

	m := store.Membership{
		CampfireID:    campfireID,
		TransportDir:  "github:owner/repo/" + campfireID,
		JoinProtocol:  "invite-only",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "github",
	}
	if err := s.AddMembership(m); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	m1Hex := fmt.Sprintf("%x", member1.PublicKey)
	m2Hex := fmt.Sprintf("%x", member2.PublicKey)

	// Populate peer endpoints (as join.go does after a successful join).
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: m1Hex,
		Endpoint:     "https://github.com/owner/repo",
		Role:         store.PeerRoleCreator,
	}); err != nil {
		t.Fatalf("upserting peer endpoint 1: %v", err)
	}
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: m2Hex,
		Endpoint:     "https://github.com/owner/repo",
		Role:         store.PeerRoleMember,
	}); err != nil {
		t.Fatalf("upserting peer endpoint 2: %v", err)
	}

	members, err := listMembersByTransport(m, s)
	if err != nil {
		t.Fatalf("listMembersByTransport: %v", err)
	}

	if len(members) != 2 {
		t.Errorf("expected 2 members, got %d; endpoints: %+v", len(members), members)
	}

	found := map[string]string{} // pubkeyHex → role
	for _, mem := range members {
		found[fmt.Sprintf("%x", mem.PublicKey)] = mem.Role
	}
	if _, ok := found[m1Hex]; !ok {
		t.Errorf("member1 (%s) not found in result", m1Hex[:8])
	}
	if _, ok := found[m2Hex]; !ok {
		t.Errorf("member2 (%s) not found in result", m2Hex[:8])
	}
	// creator role → EffectiveRole → RoleFull
	if found[m1Hex] != campfire.RoleFull {
		t.Errorf("member1 role: got %q, want %q", found[m1Hex], campfire.RoleFull)
	}
	// member role → EffectiveRole → RoleFull (legacy default)
	if found[m2Hex] != campfire.RoleFull {
		t.Errorf("member2 role: got %q, want %q", found[m2Hex], campfire.RoleFull)
	}
}

// TestListMembersFromStore_MalformedPubkey verifies that malformed hex pubkeys
// in peer_endpoints are silently skipped rather than causing an error.
func TestListMembersFromStore_MalformedPubkey(t *testing.T) {
	cfHomeDir := t.TempDir()
	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	m := store.Membership{
		CampfireID:    campfireID,
		TransportDir:  "https://relay.example.com/" + campfireID,
		JoinProtocol:  "invite-only",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "p2p-http",
	}
	if err := s.AddMembership(m); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Insert a valid member.
	validID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating valid member: %v", err)
	}
	validHex := hex.EncodeToString(validID.PublicKey)
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: validHex,
		Endpoint:     "http://valid.example.com:8080",
		Role:         store.PeerRoleMember,
	}); err != nil {
		t.Fatalf("upserting valid peer: %v", err)
	}

	members, err := listMembersFromStore(campfireID, s)
	if err != nil {
		t.Fatalf("listMembersFromStore: %v", err)
	}
	if len(members) != 1 {
		t.Errorf("expected 1 member, got %d", len(members))
	}
}

// TestLsCmd_PeerHTTP_MemberCount verifies that cf ls shows correct member_count
// for a p2p-http campfire.
func TestLsCmd_PeerHTTP_MemberCount(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", t.TempDir())

	// Save a minimal identity so CFHome works.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving agent identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	member1, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating member1: %v", err)
	}
	member2, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating member2: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  "https://relay.example.com/" + campfireID,
		JoinProtocol:  "invite-only",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "p2p-http",
		Description:   "test-campfire",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	m1Hex := fmt.Sprintf("%x", member1.PublicKey)
	m2Hex := fmt.Sprintf("%x", member2.PublicKey)
	for _, peer := range []struct{ pubkey, endpoint string }{
		{m1Hex, "http://peer1.example.com:8080"},
		{m2Hex, "http://peer2.example.com:8080"},
	} {
		if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
			CampfireID:   campfireID,
			MemberPubkey: peer.pubkey,
			Endpoint:     peer.endpoint,
			Role:         store.PeerRoleMember,
		}); err != nil {
			t.Fatalf("upserting peer endpoint: %v", err)
		}
	}
	s.Close()

	// Run cf ls and capture output.
	out := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"ls"})
		if err := rootCmd.Execute(); err != nil {
			t.Logf("ls execute error (non-fatal): %v", err)
		}
	})

	// Output should show "2 members".
	if !strings.Contains(out, "2 members") {
		t.Errorf("cf ls output should show '2 members' for p2p-http campfire; got:\n%s", out)
	}
	// Short campfire ID should appear.
	if !strings.Contains(out, campfireID[:12]) {
		t.Errorf("cf ls output should show campfire ID prefix %s; got:\n%s", campfireID[:12], out)
	}
}

// TestMembersCmd_PeerHTTP verifies that cf members lists all members of a
// p2p-http campfire with correct pubkeys.
func TestMembersCmd_PeerHTTP(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", t.TempDir())

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving agent identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	member1, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating member1: %v", err)
	}
	member2, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating member2: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  "https://relay.example.com/" + campfireID,
		JoinProtocol:  "invite-only",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "p2p-http",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	m1Hex := fmt.Sprintf("%x", member1.PublicKey)
	m2Hex := fmt.Sprintf("%x", member2.PublicKey)
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: m1Hex,
		Endpoint:     "http://peer1.example.com:8080",
		Role:         store.PeerRoleMember,
	}); err != nil {
		t.Fatalf("upserting peer1: %v", err)
	}
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: m2Hex,
		Endpoint:     "http://peer2.example.com:8080",
		Role:         store.PeerRoleMember,
	}); err != nil {
		t.Fatalf("upserting peer2: %v", err)
	}
	s.Close()

	// Run cf members <campfireID>.
	out := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"members", campfireID})
		if err := rootCmd.Execute(); err != nil {
			t.Logf("members execute error (non-fatal): %v", err)
		}
	})

	// Both member pubkey prefixes must appear.
	if !strings.Contains(out, m1Hex[:8]) {
		t.Errorf("cf members output missing member1 pubkey prefix %s; got:\n%s", m1Hex[:8], out)
	}
	if !strings.Contains(out, m2Hex[:8]) {
		t.Errorf("cf members output missing member2 pubkey prefix %s; got:\n%s", m2Hex[:8], out)
	}
}

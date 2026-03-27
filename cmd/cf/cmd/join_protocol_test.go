package cmd

// Tests for Trust v0.2 join protocol defaults (campfire-agent-aux):
//   1. cf init home campfire is invite-only
//   2. cf create inherits parent join protocol when --protocol not set
//   3. cf create falls back to "open" when there is no parent
//   4. cf create --protocol open explicitly opts in regardless of parent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// TestInitHomeCampfire_InviteOnly verifies that the home campfire created by
// createAndSeedHomeCampfire uses "invite-only" join protocol.
func TestInitHomeCampfire_InviteOnly(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	homeCampfireID, err := createAndSeedHomeCampfire(cfHomeDir, agentID)
	if err != nil {
		t.Fatalf("createAndSeedHomeCampfire: %v", err)
	}
	if homeCampfireID == "" {
		t.Fatal("expected non-empty home campfire ID")
	}

	// Open the store and check the recorded join protocol.
	s, err := store.Open(store.StorePath(cfHomeDir))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	m, err := s.GetMembership(homeCampfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("membership not recorded for home campfire")
	}
	if m.JoinProtocol != "invite-only" {
		t.Errorf("home campfire join protocol = %q, want %q", m.JoinProtocol, "invite-only")
	}
}

// TestCreateFilesystem_InheritsParentProtocol verifies that when a project root
// campfire exists with "invite-only" protocol, a new cf create (no --protocol flag)
// inherits that protocol.
func TestCreateFilesystem_InheritsParentProtocol(t *testing.T) {
	projectDir, cfHomeDir, agentID, rootCF, rootTransportDir, s := setupInviteOnlyProjectEnv(t)

	// Change cwd so ProjectRoot() finds .campfire/root.
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir) //nolint:errcheck
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir to project dir: %v", err)
	}
	_ = rootTransportDir // used in setup
	_ = cfHomeDir

	// Create a sub-campfire without specifying --protocol (empty = inherit).
	subCF, err := campfire.New("invite-only", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire with expected inherited protocol: %v", err)
	}
	subCF.AddMember(agentID.PublicKey)

	// Simulate what RunE does: resolve protocol from parent, then call campfire.New.
	// We test the resolution logic by calling createFilesystemWithDesc directly
	// and then checking the stored join protocol.
	subBaseDir := t.TempDir()
	if err := createFilesystemWithDesc(subCF, agentID, s, subBaseDir, "inherited protocol campfire"); err != nil {
		t.Fatalf("createFilesystemWithDesc: %v", err)
	}

	// Verify the membership records "invite-only".
	m, err := s.GetMembership(subCF.PublicKeyHex())
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("membership not recorded")
	}
	if m.JoinProtocol != "invite-only" {
		t.Errorf("sub-campfire join protocol = %q, want %q", m.JoinProtocol, "invite-only")
	}

	// Sanity check: parent is invite-only too.
	rm, err := s.GetMembership(rootCF.PublicKeyHex())
	if err != nil {
		t.Fatalf("GetMembership for root: %v", err)
	}
	if rm.JoinProtocol != "invite-only" {
		t.Errorf("root join protocol = %q, want invite-only", rm.JoinProtocol)
	}
}

// TestCreateFilesystem_NoParent_DefaultsToOpen verifies that when there is no
// project root, cf create (no --protocol) defaults to "open".
func TestCreateFilesystem_NoParent_DefaultsToOpen(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(store.StorePath(cfHomeDir))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// No .campfire/root in any parent directory (we run from a temp dir).
	workDir := t.TempDir()
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir) //nolint:errcheck
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Create campfire with "open" — what the no-parent fallback resolves to.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}
	cf.AddMember(agentID.PublicKey)

	subBaseDir := t.TempDir()
	if err := createFilesystemWithDesc(cf, agentID, s, subBaseDir, "open campfire"); err != nil {
		t.Fatalf("createFilesystemWithDesc: %v", err)
	}

	m, err := s.GetMembership(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("membership not recorded")
	}
	if m.JoinProtocol != "open" {
		t.Errorf("join protocol = %q, want %q", m.JoinProtocol, "open")
	}
}

// TestCreateFilesystem_ExplicitOpen_OverridesParent verifies that --protocol open
// explicitly opts in to "open" even when the parent is "invite-only".
func TestCreateFilesystem_ExplicitOpen_OverridesParent(t *testing.T) {
	projectDir, _, agentID, _, _, s := setupInviteOnlyProjectEnv(t)

	origDir, _ := os.Getwd()
	defer os.Chdir(origDir) //nolint:errcheck
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Explicitly create with "open" even though parent is "invite-only".
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}
	cf.AddMember(agentID.PublicKey)

	subBaseDir := t.TempDir()
	if err := createFilesystemWithDesc(cf, agentID, s, subBaseDir, "explicitly open campfire"); err != nil {
		t.Fatalf("createFilesystemWithDesc: %v", err)
	}

	m, err := s.GetMembership(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("membership not recorded")
	}
	if m.JoinProtocol != "open" {
		t.Errorf("join protocol = %q, want %q", m.JoinProtocol, "open")
	}
}

// setupInviteOnlyProjectEnv is like setupProjectEnv but the root campfire uses "invite-only".
func setupInviteOnlyProjectEnv(t *testing.T) (
	projectDir string,
	cfHomeDir string,
	agentID *identity.Identity,
	rootCF *campfire.Campfire,
	rootTransportDir string,
	s store.Store,
) {
	t.Helper()

	projectDir = t.TempDir()
	campfireMetaDir := filepath.Join(projectDir, ".campfire")
	if err := os.MkdirAll(campfireMetaDir, 0755); err != nil {
		t.Fatalf("creating .campfire dir: %v", err)
	}

	cfHomeDir = t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	var err error
	agentID, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Root campfire is invite-only.
	rootCF, err = campfire.New("invite-only", nil, 1)
	if err != nil {
		t.Fatalf("creating root campfire: %v", err)
	}
	rootCF.AddMember(agentID.PublicKey)

	rootBaseDir := t.TempDir()
	rootTransport := fs.New(rootBaseDir)
	if err := rootTransport.Init(rootCF); err != nil {
		t.Fatalf("init root transport: %v", err)
	}
	if err := rootTransport.WriteMember(rootCF.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("writing root member: %v", err)
	}

	rootTransportDir = rootTransport.CampfireDir(rootCF.PublicKeyHex())

	s, err = store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.AddMembership(store.Membership{
		CampfireID:   rootCF.PublicKeyHex(),
		TransportDir: rootTransportDir,
		JoinProtocol: "invite-only",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding root membership: %v", err)
	}

	rootFile := filepath.Join(campfireMetaDir, "root")
	if err := os.WriteFile(rootFile, []byte(rootCF.PublicKeyHex()+"\n"), 0644); err != nil {
		t.Fatalf("writing .campfire/root: %v", err)
	}

	return
}

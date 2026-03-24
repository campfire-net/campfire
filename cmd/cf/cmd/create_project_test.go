package cmd

// TestCreateFilesystem_ProjectMode verifies that when a .campfire/root file
// exists in the project directory, cf create (filesystem transport):
//   1. Publishes the beacon to .campfire/beacons/ in the project dir.
//   2. Sends a campfire:sub-created message to the root campfire.
//
// workspace-57: project mode sub-campfire creation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupProjectEnv sets up a project directory with .campfire/root pointing to a
// root campfire, a CF_HOME with identity and store, and returns everything needed
// for project-mode create tests.
func setupProjectEnv(t *testing.T) (
	projectDir string,
	cfHomeDir string,
	agentID *identity.Identity,
	rootCF *campfire.Campfire,
	rootTransportDir string,
	s store.Store,
) {
	t.Helper()

	// Set up a project directory.
	projectDir = t.TempDir()
	campfireMetaDir := filepath.Join(projectDir, ".campfire")
	if err := os.MkdirAll(campfireMetaDir, 0755); err != nil {
		t.Fatalf("creating .campfire dir: %v", err)
	}

	// Set up agent identity in a temp CF_HOME.
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

	// Create a root campfire with filesystem transport.
	rootCF, err = campfire.New("open", nil, 1)
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

	// Open local store and record membership in root campfire.
	s, err = store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.AddMembership(store.Membership{
		CampfireID:   rootCF.PublicKeyHex(),
		TransportDir: rootTransportDir,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding root membership: %v", err)
	}

	// Write .campfire/root pointing to the root campfire.
	rootFile := filepath.Join(campfireMetaDir, "root")
	if err := os.WriteFile(rootFile, []byte(rootCF.PublicKeyHex()+"\n"), 0644); err != nil {
		t.Fatalf("writing .campfire/root: %v", err)
	}

	return
}

func TestCreateFilesystem_ProjectMode_BeaconPublished(t *testing.T) {
	projectDir, _, agentID, _, _, s := setupProjectEnv(t)

	// Change cwd to project dir so ProjectRoot() finds it.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir) //nolint:errcheck
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir to project dir: %v", err)
	}

	// Create the sub-campfire.
	subCF, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating sub-campfire: %v", err)
	}
	subCF.AddMember(agentID.PublicKey)

	subBaseDir := t.TempDir()
	if err := createFilesystemWithDesc(subCF, agentID, s, subBaseDir, "test sub-campfire"); err != nil {
		t.Fatalf("createFilesystem: %v", err)
	}

	// Verify beacon appears in .campfire/beacons/ in the project dir.
	beaconsDir := filepath.Join(projectDir, ".campfire", "beacons")
	entries, err := os.ReadDir(beaconsDir)
	if err != nil {
		t.Fatalf("reading .campfire/beacons/: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected beacon in .campfire/beacons/, found none")
	}

	// Verify it's the sub-campfire's beacon.
	expectedBeaconFile := subCF.PublicKeyHex() + ".beacon"
	found := false
	for _, e := range entries {
		if e.Name() == expectedBeaconFile {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected beacon file %q in .campfire/beacons/, got: %v", expectedBeaconFile, entries)
	}
}

func TestCreateFilesystem_ProjectMode_AnnouncementSent(t *testing.T) {
	projectDir, _, agentID, rootCF, rootTransportDir, s := setupProjectEnv(t)

	// Change cwd to project dir so ProjectRoot() finds it.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir) //nolint:errcheck
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir to project dir: %v", err)
	}

	// Create the sub-campfire.
	subCF, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating sub-campfire: %v", err)
	}
	subCF.AddMember(agentID.PublicKey)

	subBaseDir := t.TempDir()
	if err := createFilesystemWithDesc(subCF, agentID, s, subBaseDir, "test sub-campfire"); err != nil {
		t.Fatalf("createFilesystem: %v", err)
	}

	// Sync messages from root campfire transport dir into store.
	syncFromFilesystem(rootCF.PublicKeyHex(), rootTransportDir, s)

	// Verify announcement message in store.
	msgs, err := s.ListMessages(rootCF.PublicKeyHex(), 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected announcement message in root campfire, got 0")
	}

	// Check for campfire:sub-created tag and sub-campfire short ID in payload.
	subShortID := subCF.PublicKeyHex()[:12]
	found := false
	for _, m := range msgs {
		hasTag := false
		for _, t := range m.Tags {
			if t == "campfire:sub-created" {
				hasTag = true
				break
			}
		}
		if hasTag && strings.Contains(string(m.Payload), subShortID) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected campfire:sub-created message with sub-campfire ID in root campfire; got %d messages", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: id=%s tags=%s payload=%q", i, m.ID[:8], m.Tags, string(m.Payload))
		}
	}
}

// TestCreateFilesystem_NoProjectMode verifies that without .campfire/root,
// beacon is only published to the standard beacon dir (not a project dir).
func TestCreateFilesystem_NoProjectMode(t *testing.T) {
	// Use a temp dir as cwd that has no .campfire/root.
	cwd := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir) //nolint:errcheck
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_BEACON_DIR", filepath.Join(cfHomeDir, "beacons"))

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}
	cf.AddMember(agentID.PublicKey)

	subBaseDir := t.TempDir()
	if err := createFilesystemWithDesc(cf, agentID, s, subBaseDir, "standalone campfire"); err != nil {
		t.Fatalf("createFilesystem: %v", err)
	}

	// .campfire/beacons/ must NOT exist in cwd (no project mode).
	beaconsDir := filepath.Join(cwd, ".campfire", "beacons")
	if _, err := os.Stat(beaconsDir); !os.IsNotExist(err) {
		t.Errorf("expected .campfire/beacons/ to not exist in non-project cwd, but it does")
	}

	// Standard beacon dir must contain the beacon.
	standardBeaconsDir := filepath.Join(cfHomeDir, "beacons")
	entries, err := os.ReadDir(standardBeaconsDir)
	if err != nil {
		t.Fatalf("reading beacon dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected beacon in standard beacon dir")
	}

	// Verify the beacon can be parsed.
	beacons, err := beacon.Scan(standardBeaconsDir)
	if err != nil {
		t.Fatalf("scanning beacons: %v", err)
	}
	if len(beacons) == 0 {
		t.Fatal("no beacons found in standard beacon dir")
	}
}

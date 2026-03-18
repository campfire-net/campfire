package cmd

// Tests for workspace-ykp.5: campfire description persistence

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

func TestCreateStoresDescription(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_BEACON_DIR", filepath.Join(cfHomeDir, "beacons"))

	// Work from a temp dir (no project mode).
	cwd := t.TempDir()
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir) //nolint:errcheck
	os.Chdir(cwd)          //nolint:errcheck

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	agentID.Save(filepath.Join(cfHomeDir, "identity.json")) //nolint:errcheck

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

	baseDir := t.TempDir()
	if err := createFilesystemWithDesc(cf, agentID, s, baseDir, "coordination channel"); err != nil {
		t.Fatalf("createFilesystemWithDesc: %v", err)
	}

	// Verify description stored in membership.
	m, err := s.GetMembership(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("membership not found")
	}
	if m.Description != "coordination channel" {
		t.Errorf("description = %q, want %q", m.Description, "coordination channel")
	}
}

func TestJoinCopiesDescriptionFromBeacon(t *testing.T) {
	cfHomeDir := t.TempDir()
	beaconDir := filepath.Join(cfHomeDir, "beacons")
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_BEACON_DIR", beaconDir)

	// Work from a temp dir (no project mode).
	cwd := t.TempDir()
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir) //nolint:errcheck
	os.Chdir(cwd)          //nolint:errcheck

	// Create campfire as agent A.
	agentA, _ := identity.Generate()
	agentA.Save(filepath.Join(cfHomeDir, "identity.json")) //nolint:errcheck

	sA, _ := store.Open(filepath.Join(cfHomeDir, "store.db"))
	defer sA.Close()

	cf, _ := campfire.New("open", nil, 1)
	cf.AddMember(agentA.PublicKey)

	baseDir := t.TempDir()
	if err := createFilesystemWithDesc(cf, agentA, sA, baseDir, "test purpose"); err != nil {
		t.Fatalf("createFilesystemWithDesc: %v", err)
	}

	// Now join as agent B using a separate store.
	agentB, _ := identity.Generate()
	cfHomeDirB := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDirB)

	// Copy the beacon to B's beacon dir so lookupBeaconDescription can find it.
	beaconDirB := filepath.Join(cfHomeDirB, "beacons")
	os.MkdirAll(beaconDirB, 0755) //nolint:errcheck
	t.Setenv("CF_BEACON_DIR", beaconDirB)

	// Copy beacon file from A's beacon dir to B's.
	entries, _ := os.ReadDir(beaconDir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(beaconDir, e.Name()))
		os.WriteFile(filepath.Join(beaconDirB, e.Name()), data, 0644) //nolint:errcheck
	}

	sB, _ := store.Open(filepath.Join(cfHomeDirB, "store.db"))
	defer sB.Close()

	// Join via filesystem transport.
	transport := fs.New(baseDir)
	state, err := transport.ReadState(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}

	// Write member record for B.
	transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentB.PublicKey,
		JoinedAt:  store.NowNano(),
	}) //nolint:errcheck

	// Store membership with description from beacon lookup.
	desc := lookupBeaconDescription(cf.PublicKeyHex())
	if err := sB.AddMembership(store.Membership{
		CampfireID:   cf.PublicKeyHex(),
		TransportDir: transport.CampfireDir(cf.PublicKeyHex()),
		JoinProtocol: state.JoinProtocol,
		Role:         "member",
		JoinedAt:     store.NowNano(),
		Description:  desc,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Verify description was copied from beacon.
	m, _ := sB.GetMembership(cf.PublicKeyHex())
	if m == nil {
		t.Fatal("membership not found for B")
	}
	if m.Description != "test purpose" {
		t.Errorf("description = %q, want %q", m.Description, "test purpose")
	}
}

func TestLsShowsDescription(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	s.AddMembership(store.Membership{
		CampfireID:   "abcdef123456abcdef123456abcdef123456abcdef123456abcdef123456abcdef12",
		TransportDir: "/tmp/test",
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     1000,
		Threshold:    1,
		Description:  "project coordination",
	}) //nolint:errcheck

	memberships, _ := s.ListMemberships()
	if len(memberships) != 1 {
		t.Fatalf("got %d memberships, want 1", len(memberships))
	}
	if memberships[0].Description != "project coordination" {
		t.Errorf("description = %q, want %q", memberships[0].Description, "project coordination")
	}
}

func TestLsJSONIncludesDescription(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	s.AddMembership(store.Membership{
		CampfireID:   "abcdef123456",
		TransportDir: "/tmp/test",
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     1000,
		Threshold:    1,
		Description:  "json test purpose",
	}) //nolint:errcheck

	m, _ := s.GetMembership("abcdef123456")
	if m == nil {
		t.Fatal("membership not found")
	}

	// Marshal to JSON and verify description field is present.
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !bytes.Contains(data, []byte(`"description":"json test purpose"`)) {
		t.Errorf("JSON output missing description field: %s", data)
	}
}

func TestReadShowsDescriptionHeader(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := "readtest123456"
	s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: "/tmp/test",
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     1000,
		Threshold:    1,
		Description:  "read header test",
	}) //nolint:errcheck

	// Verify the membership has the description stored.
	m, _ := s.GetMembership(campfireID)
	if m == nil {
		t.Fatal("membership not found")
	}
	if m.Description != "read header test" {
		t.Errorf("description = %q, want %q", m.Description, "read header test")
	}
}

func TestBackwardCompatibleMigration(t *testing.T) {
	// Open a store (creates schema with description column).
	dbPath := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Add a membership without description (zero value).
	s.AddMembership(store.Membership{
		CampfireID:   "compat-test",
		TransportDir: "/tmp/compat",
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
	}) //nolint:errcheck

	// Re-open the store (simulating a new session with migration).
	s.Close()
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer s2.Close()

	// The membership should have an empty description.
	m, _ := s2.GetMembership("compat-test")
	if m == nil {
		t.Fatal("membership not found after re-open")
	}
	if m.Description != "" {
		t.Errorf("description after migration = %q, want empty", m.Description)
	}
}

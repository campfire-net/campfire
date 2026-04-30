package cmd

// Tests for cf create config-in-repo behavior (campfire-agent-y87, Change 8).
//
// Tests verify:
//  1. cf create in a git repo → .cf/config.toml contains the beacon (TestCreate_WritesBeaconToConfig)
//  2. cf create --no-config → no .cf/config.toml written (TestCreate_NoConfigFlag)
//  3. cf create outside git repo → no config written, no error (TestCreate_NotInGitRepo)
//
// All tests use real temp dirs, real stores, and real Ed25519 keys.
// No mocks. test-scope: targeted (cmd/cf/cmd/... only).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// setupCreateConfigEnv sets up the minimum environment for create config tests:
// a CF_HOME with identity, an empty store, and an empty beacon dir.
func setupCreateConfigEnv(t *testing.T) (cfHomeDir string) {
	t.Helper()

	cfHomeDir = t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_BEACON_DIR", filepath.Join(cfHomeDir, "beacons"))
	t.Setenv("CF_ROOT_REGISTRY", "")
	// Reset the package-level cfHome flag variable so CFHome() uses the env.
	origCFHome := cfHome
	cfHome = ""
	t.Cleanup(func() { cfHome = origCFHome })

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	return cfHomeDir
}

// TestCreate_WritesBeaconToConfig verifies that createFilesystemWithDescAndConfig
// writes the campfire beacon to .cf/config.toml under behavior.auto_join
// when called from inside a git repository directory.
func TestCreate_WritesBeaconToConfig(t *testing.T) {
	cfHomeDir := setupCreateConfigEnv(t)

	// Create a fake git repo directory (has a .git subdirectory).
	gitRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitRoot, ".git"), 0755); err != nil {
		t.Fatalf("creating fake .git dir: %v", err)
	}

	// Create a subdirectory inside the git root and chdir into it.
	subDir := filepath.Join(gitRoot, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) }) //nolint:errcheck
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("chdir %s: %v", subDir, err)
	}

	// Create a campfire and run the filesystem create with config writing.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}

	agentID, err := identity.Load(filepath.Join(cfHomeDir, "identity.json"))
	if err != nil {
		t.Fatalf("loading identity: %v", err)
	}
	cf.AddMember(agentID.PublicKey)

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	baseDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", baseDir)
	transport := fs.New(baseDir)
	if err := transport.Init(cf); err != nil {
		t.Fatalf("init transport: %v", err)
	}

	// Run create (noConfig=false → should write config).
	if err := createFilesystemWithDescAndConfig(cf, agentID, s, baseDir, "test campfire", false); err != nil {
		t.Fatalf("createFilesystemWithDescAndConfig: %v", err)
	}

	// Verify .cf/config.toml exists at the git root.
	configPath := filepath.Join(gitRoot, ".cf", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading .cf/config.toml: %v (file not written)", err)
	}

	// Parse and verify behavior.auto_join contains an entry.
	var raw struct {
		Behavior struct {
			AutoJoin []string `toml:"auto_join"`
		} `toml:"behavior"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		t.Fatalf("parsing .cf/config.toml: %v", err)
	}
	if len(raw.Behavior.AutoJoin) == 0 {
		t.Fatal("behavior.auto_join is empty — beacon was not written")
	}

	// The entry must start with "beacon:" (portable beacon string).
	entry := raw.Behavior.AutoJoin[0]
	if !strings.HasPrefix(entry, "beacon:") {
		t.Errorf("auto_join[0] = %q, want prefix \"beacon:\"", entry)
	}
}

// TestCreate_NoConfigFlag verifies that createFilesystemWithDescAndConfig with
// noConfig=true does not write .cf/config.toml even when inside a git repo.
func TestCreate_NoConfigFlag(t *testing.T) {
	cfHomeDir := setupCreateConfigEnv(t)

	// Create a fake git repo directory.
	gitRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitRoot, ".git"), 0755); err != nil {
		t.Fatalf("creating fake .git dir: %v", err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) }) //nolint:errcheck
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatalf("chdir %s: %v", gitRoot, err)
	}

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}

	agentID, err := identity.Load(filepath.Join(cfHomeDir, "identity.json"))
	if err != nil {
		t.Fatalf("loading identity: %v", err)
	}
	cf.AddMember(agentID.PublicKey)

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	baseDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", baseDir)
	transport := fs.New(baseDir)
	if err := transport.Init(cf); err != nil {
		t.Fatalf("init transport: %v", err)
	}

	// Run create with noConfig=true.
	if err := createFilesystemWithDescAndConfig(cf, agentID, s, baseDir, "no-config campfire", true); err != nil {
		t.Fatalf("createFilesystemWithDescAndConfig: %v", err)
	}

	// Verify .cf/config.toml was NOT written.
	configPath := filepath.Join(gitRoot, ".cf", "config.toml")
	if _, err := os.Stat(configPath); err == nil {
		t.Error(".cf/config.toml was written despite --no-config flag")
	}
}

// TestCreate_NotInGitRepo verifies that createFilesystemWithDescAndConfig
// silently skips config writing when not inside a git repository.
func TestCreate_NotInGitRepo(t *testing.T) {
	cfHomeDir := setupCreateConfigEnv(t)

	// Use a temp dir with NO .git directory.
	nonGitDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) }) //nolint:errcheck
	if err := os.Chdir(nonGitDir); err != nil {
		t.Fatalf("chdir %s: %v", nonGitDir, err)
	}

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}

	agentID, err := identity.Load(filepath.Join(cfHomeDir, "identity.json"))
	if err != nil {
		t.Fatalf("loading identity: %v", err)
	}
	cf.AddMember(agentID.PublicKey)

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	baseDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", baseDir)
	transport := fs.New(baseDir)
	if err := transport.Init(cf); err != nil {
		t.Fatalf("init transport: %v", err)
	}

	// Must not error.
	if err := createFilesystemWithDescAndConfig(cf, agentID, s, baseDir, "non-git campfire", false); err != nil {
		t.Fatalf("createFilesystemWithDescAndConfig: %v (expected no error outside git repo)", err)
	}

	// No .cf/config.toml should exist in the temp dir.
	configPath := filepath.Join(nonGitDir, ".cf", "config.toml")
	if _, err := os.Stat(configPath); err == nil {
		t.Error(".cf/config.toml was written despite not being in a git repo")
	}
}

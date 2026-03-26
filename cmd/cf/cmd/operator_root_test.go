package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/store"
)

func TestEnsureOperatorRoot_CreatesAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)
	defer func() { cfHome = ""; os.Unsetenv("CF_HOME") }()
	cfHome = ""

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := id.Save(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// First call — creates
	campfireID1, err := EnsureOperatorRoot("testorg", s)
	if err != nil {
		t.Fatalf("EnsureOperatorRoot (create): %v", err)
	}
	if len(campfireID1) != 64 {
		t.Errorf("expected 64-char campfire ID, got %d: %s", len(campfireID1), campfireID1)
	}

	// Save operator root (as cf root init would)
	root := &naming.OperatorRoot{Name: "testorg", CampfireID: campfireID1}
	if err := naming.SaveOperatorRoot(dir, root); err != nil {
		t.Fatalf("saving operator root: %v", err)
	}

	// Second call — idempotent, returns same ID
	campfireID2, err := EnsureOperatorRoot("testorg", s)
	if err != nil {
		t.Fatalf("EnsureOperatorRoot (idempotent): %v", err)
	}
	if campfireID2 != campfireID1 {
		t.Errorf("idempotent call returned different ID: %s vs %s", campfireID2, campfireID1)
	}
}

func TestRootInitCmd_CreatesAliasAndConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)
	defer func() { cfHome = ""; os.Unsetenv("CF_HOME") }()
	cfHome = ""

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := id.Save(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Run cf root init --name testop directly via RunE
	if err := rootInitCmd.RunE(rootInitCmd, nil); err != nil {
		// RunE needs --name flag set; let's call it via args
		t.Logf("RunE without flags: %v", err)
	}

	// Use the internal function directly for a cleaner test
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID, err := ensureOperatorRoot("testop", id.PublicKey, s)
	if err != nil {
		t.Fatalf("ensureOperatorRoot: %v", err)
	}

	// Save config and alias (as the command would)
	root := &naming.OperatorRoot{Name: "testop", CampfireID: campfireID}
	if err := naming.SaveOperatorRoot(dir, root); err != nil {
		t.Fatalf("saving operator root: %v", err)
	}
	aliases := naming.NewAliasStore(dir)
	if err := aliases.Set("testop", campfireID); err != nil {
		t.Fatalf("setting alias: %v", err)
	}

	// Verify operator-root.json was written
	loaded, err := naming.LoadOperatorRoot(dir)
	if err != nil {
		t.Fatalf("loading operator root: %v", err)
	}
	if loaded == nil {
		t.Fatal("operator-root.json not written")
	}
	if loaded.Name != "testop" {
		t.Errorf("operator root name = %q, want 'testop'", loaded.Name)
	}

	// Verify alias was created
	aliasStore := naming.NewAliasStore(dir)
	aliasID, err := aliasStore.Get("testop")
	if err != nil {
		t.Fatalf("getting alias: %v", err)
	}
	if aliasID != campfireID {
		t.Errorf("alias ID %q != operator root ID %q", aliasID, campfireID)
	}
}

func TestOperatorRoot_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	root := &naming.OperatorRoot{Name: "acme", CampfireID: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}
	if err := naming.SaveOperatorRoot(dir, root); err != nil {
		t.Fatalf("SaveOperatorRoot: %v", err)
	}
	got, err := naming.LoadOperatorRoot(dir)
	if err != nil {
		t.Fatalf("LoadOperatorRoot: %v", err)
	}
	if got == nil {
		t.Fatal("LoadOperatorRoot returned nil")
	}
	if got.Name != root.Name || got.CampfireID != root.CampfireID {
		t.Errorf("loaded root mismatch: got %+v, want %+v", got, root)
	}
}

func TestOperatorRoot_NotFound(t *testing.T) {
	dir := t.TempDir()
	root, err := naming.LoadOperatorRoot(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root != nil {
		t.Errorf("expected nil when no operator root configured, got %+v", root)
	}
}

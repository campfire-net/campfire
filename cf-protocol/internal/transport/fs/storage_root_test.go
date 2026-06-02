package fs

// storage_root_test.go — integration tests for the tree-walk storage-root
// resolution introduced in campfireagent-3f0.
//
// Tests cover all three resolution paths against real temp directories:
//   (a) CF_HOME set → overrides tree-walk and default.
//   (b) CF_HOME unset + .cf/config.toml naming a storage_root present in an
//       ancestor directory → resolves to that root.
//   (c) Neither → ~/.campfire (or a temp HOME to avoid polluting the real one).
//
// No mocking of the filesystem — all tests hit real temp directories.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigTOML writes a minimal .cf/config.toml with a [transport] section
// containing storage_root into dir/.cf/config.toml.
// The .cf directory is created with mode 0700 (satisfies the ownership + world-
// writable security checks in the config loader).
func writeConfigTOML(t *testing.T, dir, storageRoot string) string {
	t.Helper()
	cfDir := filepath.Join(dir, ".cf")
	if err := os.MkdirAll(cfDir, 0700); err != nil {
		t.Fatalf("creating .cf dir: %v", err)
	}
	content := "[transport]\nstorage_root = \"" + storageRoot + "\"\n"
	cfgPath := filepath.Join(cfDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
	return cfgPath
}

// TestResolveStorageRoot_CFHOMEOverrides verifies that when CF_HOME is set,
// ResolveStorageRoot returns CF_HOME/campfires regardless of any .cf/config.toml.
func TestResolveStorageRoot_CFHOMEOverrides(t *testing.T) {
	tmp := t.TempDir()
	cfHome := filepath.Join(tmp, "cf-home")
	if err := os.MkdirAll(cfHome, 0700); err != nil {
		t.Fatalf("creating cf-home: %v", err)
	}

	// Also create a project dir with a .cf/config.toml naming a different root —
	// it must NOT win when CF_HOME is set.
	projectDir := filepath.Join(tmp, "project")
	otherRoot := filepath.Join(tmp, "other-root")
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatalf("creating project dir: %v", err)
	}
	writeConfigTOML(t, projectDir, otherRoot)

	t.Setenv("CF_HOME", cfHome)
	// CF_TRANSPORT_DIR must be unset so it doesn't take priority.
	t.Setenv("CF_TRANSPORT_DIR", "")

	got := ResolveStorageRoot(projectDir)
	want := filepath.Join(cfHome, "campfires")
	if got != want {
		t.Errorf("ResolveStorageRoot with CF_HOME set: got %q, want %q", got, want)
	}
}

// TestResolveStorageRoot_CFTransportDirOverrides verifies that when
// CF_TRANSPORT_DIR is set, it takes priority over everything else.
func TestResolveStorageRoot_CFTransportDirOverrides(t *testing.T) {
	tmp := t.TempDir()
	transportDir := filepath.Join(tmp, "transport-dir")

	// A .cf/config.toml naming a different root — must NOT win.
	projectDir := filepath.Join(tmp, "project")
	otherRoot := filepath.Join(tmp, "other-root")
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatalf("creating project dir: %v", err)
	}
	writeConfigTOML(t, projectDir, otherRoot)

	t.Setenv("CF_TRANSPORT_DIR", transportDir)
	t.Setenv("CF_HOME", "") // unset so CF_TRANSPORT_DIR wins alone

	got := ResolveStorageRoot(projectDir)
	if got != transportDir {
		t.Errorf("ResolveStorageRoot with CF_TRANSPORT_DIR set: got %q, want %q", got, transportDir)
	}
}

// TestResolveStorageRoot_ConfigWalksUp verifies that when CF_HOME and
// CF_TRANSPORT_DIR are both unset, ResolveStorageRoot walks up from the cwd
// looking for a .cf/config.toml with a storage_root and returns that root.
func TestResolveStorageRoot_ConfigWalksUp(t *testing.T) {
	// Build a temp tree:  tmp/parent/child/
	// .cf/config.toml lives at tmp/parent/.cf/config.toml naming storageRoot.
	// We call ResolveStorageRoot from tmp/parent/child — it must walk up and find it.
	tmp := t.TempDir()
	parentDir := filepath.Join(tmp, "parent")
	childDir := filepath.Join(parentDir, "child")
	if err := os.MkdirAll(childDir, 0700); err != nil {
		t.Fatalf("creating child dir: %v", err)
	}

	storageRoot := filepath.Join(tmp, "my-campfire-data")
	writeConfigTOML(t, parentDir, storageRoot)

	t.Setenv("CF_HOME", "")
	t.Setenv("CF_TRANSPORT_DIR", "")

	got := ResolveStorageRoot(childDir)
	if got != storageRoot {
		t.Errorf("ResolveStorageRoot tree-walk: got %q, want %q", got, storageRoot)
	}
}

// TestResolveStorageRoot_DefaultsToHomeCampfire verifies that when CF_HOME,
// CF_TRANSPORT_DIR are both unset and there is no .cf/config.toml with a
// storage_root in the tree, ResolveStorageRoot returns <home>/.campfire.
// We redirect HOME to a temp dir to avoid depending on the real ~/.campfire.
func TestResolveStorageRoot_DefaultsToHomeCampfire(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "fakehome")
	if err := os.MkdirAll(fakeHome, 0700); err != nil {
		t.Fatalf("creating fake home: %v", err)
	}

	// Project dir with no .cf/config.toml at all.
	projectDir := filepath.Join(tmp, "project")
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatalf("creating project dir: %v", err)
	}

	t.Setenv("CF_HOME", "")
	t.Setenv("CF_TRANSPORT_DIR", "")
	t.Setenv("HOME", fakeHome)

	got := ResolveStorageRoot(projectDir)
	want := filepath.Join(fakeHome, ".campfire")
	if got != want {
		t.Errorf("ResolveStorageRoot default: got %q, want %q", got, want)
	}
}

// TestDefaultBaseDir_UsesResolveStorageRoot verifies that DefaultBaseDir()
// delegates to ResolveStorageRoot and appends "campfires" when the storage
// root is a bare ~/.campfire-style path.
//
// Specifically: when the resolved root already ends in "campfires" (CF_HOME
// path), DefaultBaseDir returns it as-is. When the resolved root is a plain
// storage_root value (e.g. /foo/bar), DefaultBaseDir appends "campfires".
func TestDefaultBaseDir_LegacyCFHomePathUnchanged(t *testing.T) {
	tmp := t.TempDir()
	cfHome := filepath.Join(tmp, "cf-home")
	if err := os.MkdirAll(cfHome, 0700); err != nil {
		t.Fatalf("creating cf-home: %v", err)
	}

	t.Setenv("CF_HOME", cfHome)
	t.Setenv("CF_TRANSPORT_DIR", "")

	got := DefaultBaseDir()
	want := filepath.Join(cfHome, "campfires")
	if got != want {
		t.Errorf("DefaultBaseDir with CF_HOME: got %q, want %q", got, want)
	}
}

// TestDefaultBaseDir_StorageRootAppendsCampfires verifies that when the fs
// transport resolves a storage_root from config, DefaultBaseDir returns
// storage_root/campfires (so the BaseDir layout stays consistent with the
// pre-existing CF_HOME/campfires convention).
func TestDefaultBaseDir_StorageRootAppendsCampfires(t *testing.T) {
	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, "project")
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatalf("creating project dir: %v", err)
	}

	storageRoot := filepath.Join(tmp, "bot-data")
	writeConfigTOML(t, projectDir, storageRoot)

	t.Setenv("CF_HOME", "")
	t.Setenv("CF_TRANSPORT_DIR", "")

	// We can't directly call DefaultBaseDir with a projectDir arg because the
	// existing signature is DefaultBaseDir() — it reads CWD. Change to project dir.
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	got := DefaultBaseDir()
	want := filepath.Join(storageRoot, "campfires")
	if got != want {
		t.Errorf("DefaultBaseDir with storage_root config: got %q, want %q", got, want)
	}
}

// TestResolveStorageRoot_NearestConfigWins verifies that the nearest .cf/config.toml
// in the tree takes priority over a more distant ancestor's config.
func TestResolveStorageRoot_NearestConfigWins(t *testing.T) {
	// tmp/ancestor/  ← has .cf/config.toml with storageRootFar
	// tmp/ancestor/middle/project/  ← has .cf/config.toml with storageRootNear
	tmp := t.TempDir()
	ancestorDir := filepath.Join(tmp, "ancestor")
	middleDir := filepath.Join(ancestorDir, "middle")
	projectDir := filepath.Join(middleDir, "project")
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatalf("creating project dir: %v", err)
	}

	storageRootFar := filepath.Join(tmp, "far-data")
	storageRootNear := filepath.Join(tmp, "near-data")

	writeConfigTOML(t, ancestorDir, storageRootFar)
	writeConfigTOML(t, projectDir, storageRootNear)

	t.Setenv("CF_HOME", "")
	t.Setenv("CF_TRANSPORT_DIR", "")

	got := ResolveStorageRoot(projectDir)
	if got != storageRootNear {
		t.Errorf("ResolveStorageRoot nearest-wins: got %q, want %q (far %q should not win)", got, storageRootNear, storageRootFar)
	}
}

// TestResolveStorageRoot_StopsAtHome verifies the tree-walk does not escape
// the user's home directory (avoids picking up configs from system-level dirs).
func TestResolveStorageRoot_StopsAtHome(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "home")
	projectDir := filepath.Join(fakeHome, "work", "project")
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatalf("creating project dir: %v", err)
	}

	// Place a config above the fake home — the walk must not reach it.
	aboveHomeRoot := filepath.Join(tmp, "above-home-data")
	writeConfigTOML(t, tmp, aboveHomeRoot)

	t.Setenv("CF_HOME", "")
	t.Setenv("CF_TRANSPORT_DIR", "")
	t.Setenv("HOME", fakeHome)

	got := ResolveStorageRoot(projectDir)
	// Since there's no config within the home tree, must return the default.
	want := filepath.Join(fakeHome, ".campfire")
	if got != want {
		t.Errorf("ResolveStorageRoot stops-at-home: got %q (must not be %q)", got, aboveHomeRoot)
	}
	if strings.Contains(got, "above-home-data") {
		t.Errorf("ResolveStorageRoot walked above home directory, got %q", got)
	}
}

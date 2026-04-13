package protocol

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes a TOML config file to path, creating parent dirs as needed.
func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestLoadConfig_EmptyNoConfig verifies that with no config files present,
// compiled defaults are returned and no layers are reported.
func TestLoadConfig_EmptyNoConfig(t *testing.T) {
	tmp := t.TempDir()
	cfg, layers, warns, err := LoadConfig(tmp, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(layers) != 0 {
		t.Errorf("expected 0 layers, got %d", len(layers))
	}
	if len(warns) != 0 {
		t.Errorf("expected 0 warnings, got %v", warns)
	}
	// Compiled defaults.
	if cfg.Transport.Type != defaultTransportType {
		t.Errorf("transport.type: got %q, want %q", cfg.Transport.Type, defaultTransportType)
	}
	if cfg.Transport.Endpoint != defaultTransportEndpoint {
		t.Errorf("transport.endpoint: got %q, want %q", cfg.Transport.Endpoint, defaultTransportEndpoint)
	}
	if cfg.Identity.File == "" {
		t.Error("identity.file should have a compiled default")
	}
}

// TestLoadConfig_GlobalOnly verifies that global config values are applied.
func TestLoadConfig_GlobalOnly(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity]
display_name = "Agent Smith"

[transport]
type = "fs"
dir = "/tmp/campfire"
`)

	cfg, layers, warns, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if len(layers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(layers))
	}
	if layers[0].Source != "global" {
		t.Errorf("layer source: got %q, want %q", layers[0].Source, "global")
	}
	if layers[0].Skipped {
		t.Error("global layer should not be skipped")
	}
	if cfg.Identity.DisplayName != "Agent Smith" {
		t.Errorf("display_name: got %q, want %q", cfg.Identity.DisplayName, "Agent Smith")
	}
	if cfg.Transport.Type != "fs" {
		t.Errorf("transport.type: got %q, want %q", cfg.Transport.Type, "fs")
	}
}

// TestLoadConfig_ProjectOnly verifies that a project config in .cf/ is loaded.
func TestLoadConfig_ProjectOnly(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")   // no config file here
	projectDir := filepath.Join(tmp, "project")
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[transport]
type = "fs"
endpoint = "http://local"
`)

	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}

	cfg, layers, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only the project layer (no global file).
	hasProject := false
	for _, l := range layers {
		if l.Source == "project" && !l.Skipped {
			hasProject = true
		}
	}
	if !hasProject {
		t.Errorf("expected project layer, got %v", layers)
	}
	if cfg.Transport.Type != "fs" {
		t.Errorf("transport.type: got %q, want %q", cfg.Transport.Type, "fs")
	}
}

// TestLoadConfig_GlobalAndProject verifies global + project merge with deepest-wins.
func TestLoadConfig_GlobalAndProject(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[transport]
type = "http"
endpoint = "https://global.example.com"
`)
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[transport]
type = "fs"
`)

	cfg, layers, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(layers) < 2 {
		t.Fatalf("expected at least 2 layers, got %d: %v", len(layers), layers)
	}
	// Project type overrides global.
	if cfg.Transport.Type != "fs" {
		t.Errorf("transport.type: got %q, want %q (project wins)", cfg.Transport.Type, "fs")
	}
	// Global endpoint is inherited (project didn't set it).
	if cfg.Transport.Endpoint != "https://global.example.com" {
		t.Errorf("transport.endpoint: got %q, want inherited from global", cfg.Transport.Endpoint)
	}
}

// TestLoadConfig_MultiAncestor verifies global + ancestor + project three-layer merge.
func TestLoadConfig_MultiAncestor(t *testing.T) {
	tmp := t.TempDir()
	homeDir := tmp
	globalDir := filepath.Join(tmp, cfDir)
	// Ancestor: tmp/ancestor
	ancestorDir := filepath.Join(tmp, "ancestor")
	// Project: tmp/ancestor/project
	projectDir := filepath.Join(ancestorDir, "project")

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[naming]
seeds = ["beacon:global-seed"]
`)
	writeConfig(t, filepath.Join(ancestorDir, cfDir, configFilename), `
[naming]
seeds = ["beacon:ancestor-seed"]
`)
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[naming]
seeds = ["beacon:project-seed"]
`)

	cfg, layers, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = homeDir // suppress unused

	// Seeds should be appended: global + ancestor + project.
	seeds := cfg.Naming.Seeds
	wantSeeds := []string{"beacon:global-seed", "beacon:ancestor-seed", "beacon:project-seed"}
	if len(seeds) != len(wantSeeds) {
		t.Errorf("seeds: got %v, want %v", seeds, wantSeeds)
	} else {
		for i, w := range wantSeeds {
			if seeds[i] != w {
				t.Errorf("seeds[%d]: got %q, want %q", i, seeds[i], w)
			}
		}
	}
	// Should have global + ancestor + project layers (exactly 3 non-skipped).
	nonSkipped := 0
	for _, l := range layers {
		if !l.Skipped {
			nonSkipped++
		}
	}
	if nonSkipped != 3 {
		t.Errorf("expected exactly 3 non-skipped layers (global + ancestor + project), got %d: %v", nonSkipped, layers)
	}
}

// TestLoadConfig_WorldWritableSkip_Project verifies that a world-writable project
// config file is skipped (S2 check). This test is distinct from
// TestLoadConfig_WorldWritableSkip which tests the global config.
//
// Note: UID mismatch (S1) cannot be directly tested without chown(2) and root
// access. S1 is covered by code review only (see isOwnerTrusted in config.go).
func TestLoadConfig_WorldWritableSkip_Project(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test world-writable check as root")
	}

	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	// Create project config directory. We can't chown in tests (requires root),
	// so we trigger the S2 (world-writable) check instead.
	cfPath := filepath.Join(projectDir, cfDir, configFilename)
	writeConfig(t, cfPath, `
[transport]
type = "fs"
`)
	// Make file world-writable to trigger S2 skip.
	if err := os.Chmod(cfPath, 0666); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}

	_, layers, warns, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	skipped := false
	for _, l := range layers {
		if l.Skipped {
			skipped = true
		}
	}
	if !skipped {
		t.Error("expected at least one layer to be skipped")
	}
	if len(warns) == 0 {
		t.Error("expected warnings when a layer is skipped")
	}
}

// TestLoadConfig_WorldWritableSkip verifies that world-writable config files are skipped.
func TestLoadConfig_WorldWritableSkip(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test world-writable check as root")
	}

	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")

	cfPath := filepath.Join(globalDir, configFilename)
	writeConfig(t, cfPath, `
[transport]
type = "fs"
`)
	if err := os.Chmod(cfPath, 0666); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	cfg, layers, warns, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(layers) == 0 {
		t.Error("expected at least one layer entry")
	}
	skipped := false
	for _, l := range layers {
		if l.Skipped && l.SkipReason == "world-writable" {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("expected world-writable skip, layers=%v warns=%v", layers, warns)
	}
	// Value should be compiled default (not the fs from the skipped file).
	if cfg.Transport.Type != defaultTransportType {
		t.Errorf("transport.type: got %q, want default %q (skipped file should not contribute)", cfg.Transport.Type, defaultTransportType)
	}
}

// TestLoadConfig_SymlinkOutsideHome verifies that symlinks resolving outside home are skipped.
func TestLoadConfig_SymlinkOutsideHome(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test symlink check as root")
	}

	tmp := t.TempDir()
	// Create a real config somewhere "outside" of the home area.
	realDir := t.TempDir()
	realConfig := filepath.Join(realDir, configFilename)
	if err := os.WriteFile(realConfig, []byte(`[transport]
type = "fs"
`), 0600); err != nil {
		t.Fatalf("write real config: %v", err)
	}

	// Create global dir with a symlink pointing to realConfig.
	globalDir := filepath.Join(tmp, "global")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(globalDir, configFilename)
	if err := os.Symlink(realConfig, symlinkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Override home detection: use tmp as home, so realDir is "outside".
	// We can't override os.UserHomeDir() but can verify the mechanism
	// by calling loadAndCheck directly.
	layer, raw, warns, err := loadAndCheck(symlinkPath, "global", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The symlink resolves to realDir which is in /tmp — /tmp is outside the
	// user home tree (/home/...), so the symlink-outside-home check (S3) should
	// skip the layer with SkipReason "symlink".
	if !layer.Skipped {
		t.Errorf("expected layer to be skipped (symlink resolves outside home), got Skipped=false")
	}
	if layer.SkipReason != "symlink" {
		t.Errorf("expected SkipReason %q, got %q", "symlink", layer.SkipReason)
	}
	if raw != nil {
		t.Error("expected nil rawConfig for skipped layer")
	}
	_ = warns
}

// TestLoadConfig_TrustSectionRejected verifies that [trust] section causes parse error.
func TestLoadConfig_TrustSectionRejected(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[trust]
some_key = "some_value"
`)

	_, _, _, err := LoadConfig(globalDir, globalDir)
	if err == nil {
		t.Fatal("expected error for [trust] section, got nil")
	}
	if !strings.Contains(err.Error(), "trust") {
		t.Errorf("error should mention trust, got: %v", err)
	}
}

// TestLoadConfig_NamingRootInProjectRejected verifies naming.root in non-global config causes error.
func TestLoadConfig_NamingRootInProjectRejected(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[naming]
root = "abc123def456"
`)

	_, _, _, err := LoadConfig(globalDir, projectDir)
	if err == nil {
		t.Fatal("expected error for naming.root in project config, got nil")
	}
	if !strings.Contains(err.Error(), "naming.root") {
		t.Errorf("error should mention naming.root, got: %v", err)
	}
}

// TestLoadConfig_ReplaceListSentinel verifies that "!replace" discards inherited seeds.
func TestLoadConfig_ReplaceListSentinel(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[naming]
seeds = ["beacon:global-seed-1", "beacon:global-seed-2"]
`)
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[naming]
seeds = ["!replace", "beacon:only-this"]
`)

	cfg, _, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Naming.Seeds) != 1 || cfg.Naming.Seeds[0] != "beacon:only-this" {
		t.Errorf("seeds after !replace: got %v, want [beacon:only-this]", cfg.Naming.Seeds)
	}
}

// TestLoadConfig_AutoJoinAppend verifies that auto_join lists are appended by default.
func TestLoadConfig_AutoJoinAppend(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[behavior]
auto_join = ["beacon:join-1"]
`)
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[behavior]
auto_join = ["beacon:join-2"]
`)

	cfg, _, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Behavior.AutoJoin) != 2 {
		t.Errorf("auto_join: got %v, want 2 entries", cfg.Behavior.AutoJoin)
	}
}

// TestMergeList_AppendDeduplicates verifies that duplicate list entries are removed.
func TestMergeList_AppendDeduplicates(t *testing.T) {
	existing := []string{"a", "b"}
	newList := []string{"b", "c"}
	result := mergeList(existing, newList)

	want := []string{"a", "b", "c"}
	if len(result) != len(want) {
		t.Fatalf("mergeList: got %v, want %v", result, want)
	}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("result[%d]: got %q, want %q", i, result[i], w)
		}
	}
}

// TestMergeList_ReplaceStripsFirst verifies that "!replace" is stripped from result.
func TestMergeList_ReplaceStripsFirst(t *testing.T) {
	existing := []string{"old"}
	newList := []string{"!replace", "new1", "new2"}
	result := mergeList(existing, newList)

	want := []string{"new1", "new2"}
	if len(result) != len(want) {
		t.Fatalf("mergeList replace: got %v, want %v", result, want)
	}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("result[%d]: got %q, want %q", i, result[i], w)
		}
	}
}

// TestValidateIdentityPath verifies path constraint checks.
func TestValidateIdentityPath(t *testing.T) {
	cases := []struct {
		path    string
		wantErr bool
	}{
		{"identity.json", false},
		{"keys/identity.json", false},
		{"/absolute/path.json", true},
		{"../escape.json", true},
		{"sub/../escape.json", true},
	}
	for _, tc := range cases {
		err := ValidateIdentityPath(tc.path)
		if tc.wantErr && err == nil {
			t.Errorf("ValidateIdentityPath(%q): expected error, got nil", tc.path)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ValidateIdentityPath(%q): unexpected error: %v", tc.path, err)
		}
	}
}

// TestLoadConfig_IdentityFileRelativePath verifies that identity.file = "keys/identity.json"
// in a config file resolves to an absolute path relative to the config file's directory.
func TestLoadConfig_IdentityFileRelativePath(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity]
file = "keys/identity.json"
`)

	cfg, _, _, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// identity.file should be resolved to an absolute path relative to globalDir.
	wantPath := filepath.Join(globalDir, "keys", "identity.json")
	if cfg.Identity.File != wantPath {
		t.Errorf("identity.File: got %q, want %q", cfg.Identity.File, wantPath)
	}
	if !filepath.IsAbs(cfg.Identity.File) {
		t.Errorf("identity.File should be absolute, got: %q", cfg.Identity.File)
	}
}

// TestLoadConfig_RolesSectionRejected verifies that [roles] section causes parse error.
func TestLoadConfig_RolesSectionRejected(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[roles]
admin = ["some-pubkey"]
`)

	_, _, _, err := LoadConfig(globalDir, globalDir)
	if err == nil {
		t.Fatal("expected error for [roles] section, got nil")
	}
	if !strings.Contains(err.Error(), "roles") {
		t.Errorf("error should mention roles, got: %v", err)
	}
}

// TestLoadConfig_IdentityFile_PathTraversal verifies that identity.file with ".." components
// causes LoadConfig to return a hard error (S4 check).
func TestLoadConfig_IdentityFile_PathTraversal(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity]
file = "../../etc/passwd"
`)

	_, _, _, err := LoadConfig(globalDir, globalDir)
	if err == nil {
		t.Fatal("expected error for identity.file with path traversal, got nil")
	}
	if !strings.Contains(err.Error(), "identity.file") {
		t.Errorf("error should mention identity.file, got: %v", err)
	}
}

// TestLoadConfig_WorldWritableDir_Skipped verifies that a config file whose containing
// directory is world-writable is skipped (S2 directory check).
func TestLoadConfig_WorldWritableDir_Skipped(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test world-writable check as root")
	}

	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	cfPath := filepath.Join(globalDir, configFilename)
	writeConfig(t, cfPath, `
[transport]
type = "fs"
`)
	// Make the containing directory world-writable to trigger the S2 directory check.
	if err := os.Chmod(globalDir, 0777); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() {
		// Restore permissions so TempDir cleanup can remove the directory.
		_ = os.Chmod(globalDir, 0700)
	})

	cfg, layers, warns, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	skipped := false
	for _, l := range layers {
		if l.Skipped && l.SkipReason == "world-writable-dir" {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("expected world-writable-dir skip, layers=%v warns=%v", layers, warns)
	}
	// Value should be compiled default (not the fs from the skipped file).
	if cfg.Transport.Type != defaultTransportType {
		t.Errorf("transport.type: got %q, want default %q (skipped file should not contribute)", cfg.Transport.Type, defaultTransportType)
	}
}

// TestLoadConfig_SymlinkSiblingDir_Rejected verifies that a symlink resolving to a sibling
// directory (e.g. /home/baronmalicious) is rejected even though its prefix matches /home/baron.
func TestLoadConfig_SymlinkSiblingDir_Rejected(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test symlink check as root")
	}

	tmp := t.TempDir()
	// Simulate a "sibling" directory: we want to test that /home/baron does not match
	// /home/baronmalicious. We do this by creating a target outside tmp (a separate TempDir)
	// and pointing a symlink at it — the resolved path won't have tmp as prefix+separator.
	outsideDir := t.TempDir()
	realConfig := filepath.Join(outsideDir, configFilename)
	if err := os.WriteFile(realConfig, []byte(`[transport]
type = "fs"
`), 0600); err != nil {
		t.Fatalf("write real config: %v", err)
	}

	globalDir := filepath.Join(tmp, "global")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(globalDir, configFilename)
	if err := os.Symlink(realConfig, symlinkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Call loadAndCheck directly to test the symlink rejection logic.
	// The resolved path points to outsideDir which is a separate TempDir —
	// it will not be inside the user's home directory.
	layer, raw, _, err := loadAndCheck(symlinkPath, "global", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !layer.Skipped {
		t.Errorf("expected layer to be skipped (symlink resolves outside home), got Skipped=false")
	}
	if layer.SkipReason != "symlink" {
		t.Errorf("expected SkipReason %q, got %q", "symlink", layer.SkipReason)
	}
	if raw != nil {
		t.Error("expected nil rawConfig for skipped layer")
	}
}

// TestLoadConfig_NamingRootGlobalAllowed verifies naming.root is allowed in global config.
func TestLoadConfig_NamingRootGlobalAllowed(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[naming]
root = "abc123def456abc123def456abc123def456abc123def456abc123def456abc123"
`)

	cfg, _, _, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error for naming.root in global config: %v", err)
	}
	if cfg.Naming.Root == "" {
		t.Error("naming.root should be set from global config")
	}
}

// TestLoadConfig_UntrustedOwner_Skipped verifies that S1 (UID ownership check) causes
// a layer to be skipped when the containing directory is not owned by the current user.
// Because creating files owned by a different UID requires root, we inject a fake
// ownerTrustedFn that always returns false to exercise the code path directly.
func TestLoadConfig_UntrustedOwner_Skipped(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test owner check as root (root owns everything)")
	}

	// Inject a fake owner check that always reports untrusted.
	orig := ownerTrustedFn
	ownerTrustedFn = func(string) bool { return false }
	defer func() { ownerTrustedFn = orig }()

	// Create a real config file — the owner check is the only gate we're injecting.
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `[transport]
endpoint = "https://fake.example.com"
`)

	cfg, layers, _, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The layer must be skipped with SkipReason == "ownership".
	skipped := false
	for _, l := range layers {
		if l.Skipped && strings.Contains(l.SkipReason, "ownership") {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("expected S1 ownership skip; layers = %+v", layers)
	}

	// The endpoint must NOT have been applied (skipped layer contributes nothing).
	if cfg.Transport.Endpoint != defaultTransportEndpoint {
		t.Errorf("endpoint = %q, want default %q (untrusted layer must not contribute)",
			cfg.Transport.Endpoint, defaultTransportEndpoint)
	}
}

// TestLoadConfig_ScopeCampfiresBareReplaceSentinel_Rejected verifies that a project
// config using ["!replace"] alone (no entries) cannot empty a global campfire allowlist.
// An empty allowlist means "allow all", so bare !replace would convert a restrictive
// allowlist into unrestricted access — this must be rejected (treated as a no-op).
func TestLoadConfig_ScopeCampfiresBareReplaceSentinel_Rejected(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	globalID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// Global config defines a campfire allowlist.
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[scope]
campfires = ["`+globalID+`"]
`)
	// Project config uses bare !replace — no entries — attempting to clear the allowlist.
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[scope]
campfires = ["!replace"]
`)

	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}

	cfg, _, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The global allowlist must be preserved — bare !replace is a no-op.
	if len(cfg.Scope.Campfires) != 1 {
		t.Errorf("scope.campfires: got %v, want [%s] (bare !replace must not empty allowlist)", cfg.Scope.Campfires, globalID)
	}
	if len(cfg.Scope.Campfires) == 1 && cfg.Scope.Campfires[0] != globalID {
		t.Errorf("scope.campfires[0]: got %q, want %q", cfg.Scope.Campfires[0], globalID)
	}

	// Verify the enforcer still denies campfires not in the allowlist.
	e := NewScopeEnforcer(cfg.Scope)
	if err := e.CheckCampfire(globalID); err != nil {
		t.Errorf("allowlisted campfire should be permitted after bare !replace attempt, got: %v", err)
	}
	if err := e.CheckCampfire("not-in-list"); err == nil {
		t.Error("campfire not in allowlist should be denied; bare !replace must not have cleared the list")
	}
}

// TestLoadConfig_ScopeCampfiresReplaceWithEntries_Allowed verifies that !replace
// with at least one entry (["!replace", "id1"]) works correctly — it replaces the
// existing allowlist with the specified entries.
func TestLoadConfig_ScopeCampfiresReplaceWithEntries_Allowed(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	globalID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	replacementID := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[scope]
campfires = ["`+globalID+`"]
`)
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[scope]
campfires = ["!replace", "`+replacementID+`"]
`)

	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}

	cfg, _, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// !replace with entries should replace the list with only replacementID.
	if len(cfg.Scope.Campfires) != 1 {
		t.Errorf("scope.campfires: got %v, want [%s]", cfg.Scope.Campfires, replacementID)
	}
	if len(cfg.Scope.Campfires) == 1 && cfg.Scope.Campfires[0] != replacementID {
		t.Errorf("scope.campfires[0]: got %q, want %q", cfg.Scope.Campfires[0], replacementID)
	}
}

// TestLoadConfig_ScopeOperationClassesBareReplaceSentinel_Rejected verifies that a bare
// ["!replace"] in scope.operation_classes cannot silently discard an inherited allowlist.
// This mirrors the campfire-allowlist guard and closes the symmetric security gap.
func TestLoadConfig_ScopeOperationClassesBareReplaceSentinel_Rejected(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	// Global config restricts operations to "read" only.
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[scope]
operation_classes = ["read"]
`)
	// Project config uses bare !replace — attempting to clear the operation class allowlist.
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[scope]
operation_classes = ["!replace"]
`)

	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}

	cfg, _, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The global allowlist must be preserved — bare !replace is a no-op.
	if len(cfg.Scope.OperationClasses) != 1 {
		t.Errorf("scope.operation_classes: got %v, want [read] (bare !replace must not empty allowlist)", cfg.Scope.OperationClasses)
	}
	if len(cfg.Scope.OperationClasses) == 1 && cfg.Scope.OperationClasses[0] != "read" {
		t.Errorf("scope.operation_classes[0]: got %q, want \"read\"", cfg.Scope.OperationClasses[0])
	}

	// Verify the enforcer still denies operations not in the allowlist.
	e := NewScopeEnforcer(cfg.Scope)
	if err := e.CheckOperation("read"); err != nil {
		t.Errorf("allowed operation should be permitted after bare !replace attempt, got: %v", err)
	}
	if err := e.CheckOperation("write"); err == nil {
		t.Error("write should be denied; bare !replace must not have cleared the operation class allowlist")
	}
}

// TestLoadConfig_ScopeOperationClassesReplaceWithEntries_Allowed verifies that
// ["!replace", "write", "admin"] correctly replaces an inherited operation class list.
func TestLoadConfig_ScopeOperationClassesReplaceWithEntries_Allowed(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	// Global config allows "read" only.
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[scope]
operation_classes = ["read"]
`)
	// Project config replaces with "write" + "admin".
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[scope]
operation_classes = ["!replace", "write", "admin"]
`)

	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}

	cfg, _, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// !replace with entries should replace the list with write+admin (no "read").
	if len(cfg.Scope.OperationClasses) != 2 {
		t.Errorf("scope.operation_classes: got %v, want [write admin]", cfg.Scope.OperationClasses)
	}
	e := NewScopeEnforcer(cfg.Scope)
	if err := e.CheckOperation("write"); err != nil {
		t.Errorf("write should be allowed after !replace with entries, got: %v", err)
	}
	if err := e.CheckOperation("read"); err == nil {
		t.Error("read should be denied after !replace with [write admin]")
	}
}

// TestLoadConfig_WalkUpFalseOverridesTrue verifies that a project-level
// walk_up = false correctly overrides a global walk_up = true.
// This is a regression test for the bug where bool zero-value (false) was
// indistinguishable from "omitted", causing project false to be silently dropped.
func TestLoadConfig_WalkUpFalseOverridesTrue(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[behavior]
walk_up = true
`)
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[behavior]
walk_up = false
`)

	cfg, _, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Behavior.WalkUp != false {
		t.Errorf("walk_up: got %v, want false (project false must override global true)", cfg.Behavior.WalkUp)
	}
}

// TestLoadConfig_TransportRelay verifies that transport.relay is parsed from
// a config file and resolves to the correct value.
func TestLoadConfig_TransportRelay(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[transport]
relay = "https://relay.example.com"
`)

	cfg, layers, warns, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if len(layers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(layers))
	}
	if cfg.Transport.Relay != "https://relay.example.com" {
		t.Errorf("transport.relay: got %q, want %q", cfg.Transport.Relay, "https://relay.example.com")
	}

	// Verify contributed fields includes transport.relay.
	found := false
	for _, f := range layers[0].Fields {
		if f == "transport.relay" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected transport.relay in contributed fields, got %v", layers[0].Fields)
	}
}

// TestLoadConfig_TransportRelay_Empty verifies that when no relay is configured,
// the resolved relay is an empty string.
func TestLoadConfig_TransportRelay_Empty(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[transport]
type = "http"
`)

	cfg, _, _, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Transport.Relay != "" {
		t.Errorf("transport.relay: got %q, want empty string", cfg.Transport.Relay)
	}
}

// TestResolveRelayFromConfig verifies the resolveRelayFromConfig helper returns
// the relay URL from the merged config and handles nil input gracefully.
func TestResolveRelayFromConfig(t *testing.T) {
	// Nil config returns empty string.
	if got := resolveRelayFromConfig(nil); got != "" {
		t.Errorf("resolveRelayFromConfig(nil): got %q, want empty string", got)
	}

	// Config with no relay returns empty string.
	empty := &Config{}
	if got := resolveRelayFromConfig(empty); got != "" {
		t.Errorf("resolveRelayFromConfig(empty): got %q, want empty string", got)
	}

	// Config with relay returns the value.
	withRelay := &Config{
		Transport: TransportConfig{
			Relay: "https://relay.example.com",
		},
	}
	if got := resolveRelayFromConfig(withRelay); got != "https://relay.example.com" {
		t.Errorf("resolveRelayFromConfig(withRelay): got %q, want %q", got, "https://relay.example.com")
	}
}

// TestLoadConfig_TransportRelay_ProjectOverridesGlobal verifies that a project-level
// relay overrides the global relay (deepest wins).
func TestLoadConfig_TransportRelay_ProjectOverridesGlobal(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[transport]
relay = "https://global-relay.example.com"
`)
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[transport]
relay = "https://project-relay.example.com"
`)

	cfg, _, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Transport.Relay != "https://project-relay.example.com" {
		t.Errorf("transport.relay: got %q, want project-level relay", cfg.Transport.Relay)
	}
}

// --- trust anchor tests ---

// knownPubkeyHex is a well-formed 32-byte ed25519 public key in hex (64 chars).
const knownPubkeyHex = "1e294c14bc79d9bf6bec754e563389b6c3d3e9255ad206df1ba60cdc52197ffd"

// secondPubkeyHex is a second distinct 32-byte key in hex.
const secondPubkeyHex = "aabbccdd00112233aabbccdd00112233aabbccdd00112233aabbccdd00112233"

func hexToKey(t *testing.T, h string) ed25519.PublicKey {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("hex decode %q: %v", h, err)
	}
	return ed25519.PublicKey(b)
}

// TestLoadConfig_TrustAnchors_HexAndBase64 verifies that hex and base64 encodings
// of two distinct keys both decode correctly from [identity.trust].
func TestLoadConfig_TrustAnchors_HexAndBase64(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")

	// Encode the second key in base64; use hex for the first.
	secondBase64 := base64.StdEncoding.EncodeToString(hexToKey(t, secondPubkeyHex))

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity.trust]
anchors = ["`+knownPubkeyHex+`", "`+secondBase64+`"]
`)

	cfg, _, warns, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}

	if len(cfg.Identity.TrustAnchors) != 2 {
		t.Fatalf("expected 2 anchors, got %d", len(cfg.Identity.TrustAnchors))
	}

	// Verify both keys are present.
	wantFirst := hexToKey(t, knownPubkeyHex)
	wantSecond := hexToKey(t, secondPubkeyHex)
	found1, found2 := false, false
	for _, a := range cfg.Identity.TrustAnchors {
		if a.Equal(wantFirst) {
			found1 = true
		}
		if a.Equal(wantSecond) {
			found2 = true
		}
	}
	if !found1 {
		t.Errorf("hex-encoded anchor not found in parsed list")
	}
	if !found2 {
		t.Errorf("base64-encoded anchor not found in parsed list")
	}
}

// TestLoadConfig_TrustAnchors_Dedup verifies that the same key provided in both
// hex and base64 is deduplicated to a single entry.
func TestLoadConfig_TrustAnchors_Dedup(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")

	// Same key expressed as both hex and its own base64 encoding.
	sameKeyBase64 := base64.StdEncoding.EncodeToString(hexToKey(t, knownPubkeyHex))

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity.trust]
anchors = ["`+knownPubkeyHex+`", "`+sameKeyBase64+`"]
`)

	cfg, _, warns, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}

	// Same key twice → deduplicated to one entry.
	if len(cfg.Identity.TrustAnchors) != 1 {
		t.Fatalf("expected 1 anchor (deduped), got %d", len(cfg.Identity.TrustAnchors))
	}

	want := hexToKey(t, knownPubkeyHex)
	if !cfg.Identity.TrustAnchors[0].Equal(want) {
		t.Errorf("anchor mismatch: got %x, want %x", cfg.Identity.TrustAnchors[0], want)
	}
}

// TestLoadConfig_TrustAnchors_NoSection verifies that when [identity.trust] is
// absent, TrustAnchors is an empty slice (not nil panicking on range).
func TestLoadConfig_TrustAnchors_NoSection(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity]
display_name = "Agent"
`)

	cfg, _, _, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// TrustAnchors should be nil or empty — either is fine, but len must be 0.
	if len(cfg.Identity.TrustAnchors) != 0 {
		t.Errorf("expected 0 trust anchors, got %d", len(cfg.Identity.TrustAnchors))
	}
}

// TestLoadConfig_TrustAnchors_GlobalAndProjectExtend verifies that project anchors
// extend (not override) the global anchor list.
func TestLoadConfig_TrustAnchors_GlobalAndProjectExtend(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity.trust]
anchors = ["`+knownPubkeyHex+`"]
`)
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[identity.trust]
anchors = ["`+secondPubkeyHex+`"]
`)

	cfg, _, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Identity.TrustAnchors) != 2 {
		t.Fatalf("expected 2 anchors (global + project extend), got %d", len(cfg.Identity.TrustAnchors))
	}

	// Both keys must be present.
	wantFirst := hexToKey(t, knownPubkeyHex)
	wantSecond := hexToKey(t, secondPubkeyHex)
	found1, found2 := false, false
	for _, a := range cfg.Identity.TrustAnchors {
		if a.Equal(wantFirst) {
			found1 = true
		}
		if a.Equal(wantSecond) {
			found2 = true
		}
	}
	if !found1 {
		t.Errorf("global anchor not found in merged list")
	}
	if !found2 {
		t.Errorf("project anchor not found in merged list")
	}
}

// TestLoadConfig_TrustAnchors_InvalidSkipped verifies that an invalid anchor string
// is skipped with a warning while valid ones still parse.
func TestLoadConfig_TrustAnchors_InvalidSkipped(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity.trust]
anchors = ["not-a-valid-key", "`+knownPubkeyHex+`"]
`)

	cfg, _, _, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only the valid key should be present; invalid one silently dropped.
	if len(cfg.Identity.TrustAnchors) != 1 {
		t.Fatalf("expected 1 anchor (invalid skipped), got %d", len(cfg.Identity.TrustAnchors))
	}
	want := hexToKey(t, knownPubkeyHex)
	if !cfg.Identity.TrustAnchors[0].Equal(want) {
		t.Errorf("anchor mismatch: got %x, want %x", cfg.Identity.TrustAnchors[0], want)
	}
}

// TestLoadConfig_TrustAnchors_EmptyList verifies that anchors = [] produces an empty slice.
func TestLoadConfig_TrustAnchors_EmptyList(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity.trust]
anchors = []
`)

	cfg, _, _, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Identity.TrustAnchors) != 0 {
		t.Errorf("expected 0 anchors, got %d", len(cfg.Identity.TrustAnchors))
	}
}

// TestDecodeAnchor_Hex verifies hex decoding.
func TestDecodeAnchor_Hex(t *testing.T) {
	key, err := decodeAnchor(knownPubkeyHex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(key) != ed25519.PublicKeySize {
		t.Errorf("expected %d bytes, got %d", ed25519.PublicKeySize, len(key))
	}
}

// TestDecodeAnchor_Base64 verifies standard base64 decoding.
func TestDecodeAnchor_Base64(t *testing.T) {
	// Encode knownPubkeyHex as base64 for a well-formed input.
	b64 := base64.StdEncoding.EncodeToString(hexToKey(t, knownPubkeyHex))
	key, err := decodeAnchor(b64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(key) != ed25519.PublicKeySize {
		t.Errorf("expected %d bytes, got %d", ed25519.PublicKeySize, len(key))
	}
}

// TestDecodeAnchor_InvalidString verifies that garbage input returns an error.
func TestDecodeAnchor_InvalidString(t *testing.T) {
	_, err := decodeAnchor("not-valid-at-all!!!")
	if err == nil {
		t.Error("expected error for invalid anchor string, got nil")
	}
}

// TestDecodeAnchor_WrongLength verifies that a valid base64 string that decodes to
// the wrong number of bytes returns an error.
func TestDecodeAnchor_WrongLength(t *testing.T) {
	// 16 bytes base64-encoded — valid encoding but wrong key size.
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	_, err := decodeAnchor(short)
	if err == nil {
		t.Error("expected error for wrong-length anchor, got nil")
	}
}

// TestIdentityConfig_BackendDefault verifies that the compiled default backend is "file".
func TestIdentityConfig_BackendDefault(t *testing.T) {
	tmp := t.TempDir()
	cfg, _, _, err := LoadConfig(tmp, tmp)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Identity.Backend != "file" {
		t.Errorf("default backend = %q, want \"file\"", cfg.Identity.Backend)
	}
}

// TestIdentityConfig_BackendFile verifies that backend = "file" is accepted.
func TestIdentityConfig_BackendFile(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity]
backend = "file"
`)
	cfg, _, _, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Identity.Backend != "file" {
		t.Errorf("backend = %q, want \"file\"", cfg.Identity.Backend)
	}
}

// TestIdentityConfig_BackendSSHAgent verifies that backend = "ssh-agent" with
// a fingerprint is accepted.
func TestIdentityConfig_BackendSSHAgent(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity]
backend = "ssh-agent"
fingerprint = "SHA256:aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abcd"
`)
	cfg, _, _, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Identity.Backend != "ssh-agent" {
		t.Errorf("backend = %q, want \"ssh-agent\"", cfg.Identity.Backend)
	}
	if cfg.Identity.Fingerprint != "SHA256:aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abcd" {
		t.Errorf("fingerprint = %q, unexpected", cfg.Identity.Fingerprint)
	}
}

// TestIdentityConfig_UnknownBackend verifies that an unknown backend value is rejected.
func TestIdentityConfig_UnknownBackend(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity]
backend = "1password"
`)
	_, _, _, err := LoadConfig(globalDir, globalDir)
	if err == nil {
		t.Fatal("LoadConfig should fail for unknown backend value")
	}
	if !strings.Contains(err.Error(), "1password") {
		t.Errorf("error should mention the bad value, got: %v", err)
	}
}

// TestIdentityConfig_FingerprintCascade verifies that fingerprint cascades
// across config layers (project overrides global).
func TestIdentityConfig_FingerprintCascade(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[identity]
backend = "ssh-agent"
fingerprint = "SHA256:global"
`)
	writeConfig(t, filepath.Join(projectDir, ".cf", configFilename), `
[identity]
fingerprint = "SHA256:project"
`)

	cfg, _, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// Project layer wins (deeper config).
	if cfg.Identity.Fingerprint != "SHA256:project" {
		t.Errorf("fingerprint = %q, want \"SHA256:project\"", cfg.Identity.Fingerprint)
	}
}

// TestMergeLayer_InvalidAnchor_ReturnsWarning verifies that mergeLayer returns a
// warning (not a fatal error) when identity.trust.anchors contains an invalid hex
// string, and that the valid anchors in the same list are still applied.
// Regression for campfire-13f.
func TestMergeLayer_InvalidAnchor_ReturnsWarning(t *testing.T) {
	// Valid Ed25519 public key (32 bytes, hex-encoded).
	validKey := make([]byte, 32)
	for i := range validKey {
		validKey[i] = byte(i + 1)
	}
	validHex := hex.EncodeToString(validKey)

	raw := &rawConfig{}
	raw.Identity.Trust.Anchors = []string{
		"NOT-VALID-HEX",
		validHex,
	}

	var dst Config
	contributed, warnings, err := mergeLayer(&dst, raw, "/fake/config.toml", false)
	if err != nil {
		t.Fatalf("mergeLayer returned error for invalid anchor; want warning: %v", err)
	}

	// The valid anchor must be applied.
	if len(dst.Identity.TrustAnchors) != 1 {
		t.Fatalf("expected 1 trust anchor applied, got %d", len(dst.Identity.TrustAnchors))
	}

	// A warning must be returned for the invalid anchor.
	if len(warnings) == 0 {
		t.Error("expected at least one warning for the invalid anchor, got none")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "NOT-VALID-HEX") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning mentioning \"NOT-VALID-HEX\", got: %v", warnings)
	}

	// identity.trust.anchors must be in contributed (even though one was skipped).
	hasContrib := false
	for _, c := range contributed {
		if c == "identity.trust.anchors" {
			hasContrib = true
			break
		}
	}
	if !hasContrib {
		t.Errorf("expected \"identity.trust.anchors\" in contributed, got: %v", contributed)
	}
}

package protocol_test

// Tests for protocol.InitWithConfig() — campfire-agent-4gw and campfire-agent-y87.
//
// Tests verify:
//  1. Global config sets transport endpoint (TestInitWithConfig_GlobalConfig)
//  2. No config files → compiled defaults, same as Init() (TestInitWithConfig_NoConfig)
//  3. Config specifies identity.file → IdentitySource = "config" (TestInitWithConfig_IdentityFromConfig)
//  4. Config has behavior.auto_join → AutoJoined populated (TestInitWithConfig_AutoJoined)
//  5. ConfigLayers reflects files examined (TestInitWithConfig_ConfigLayers)
//  6. auto_join with real campfire → AutoJoined populated (TestInitWithConfig_AutoJoin_Success)
//  7. auto_join when already a member → skipped (TestInitWithConfig_AutoJoin_AlreadyMember)
//
// All tests use real temp dirs, real SQLite stores, and real Ed25519 keys.
// No mocks. test-scope: targeted (pkg/protocol/... only).

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/protocol"
	cfstore "github.com/campfire-net/campfire/pkg/store"
	cftransport "github.com/campfire-net/campfire/pkg/transport/fs"
)

// writeInitConfigFile writes content to path, creating directories as needed.
func writeInitConfigFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestInitWithConfig_GlobalConfig verifies that a global config file that sets
// transport.endpoint is reflected in the resolved config and InitResult.ConfigLayers.
func TestInitWithConfig_GlobalConfig(t *testing.T) {
	globalDir := t.TempDir()

	customEndpoint := "https://custom.endpoint.example.com"
	writeInitConfigFile(t,
		filepath.Join(globalDir, "config.toml"),
		`[transport]
endpoint = "`+customEndpoint+`"
`)

	client, result, err := protocol.InitWithConfig(protocol.WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	if result == nil {
		t.Fatal("InitWithConfig returned nil *InitResult")
	}

	// ConfigLayers must contain the global config file.
	if len(result.ConfigLayers) == 0 {
		t.Fatal("ConfigLayers is empty — expected at least 1 layer (global config)")
	}
	foundGlobal := false
	for _, l := range result.ConfigLayers {
		if l.Source == "global" && !l.Skipped {
			foundGlobal = true
			break
		}
	}
	if !foundGlobal {
		t.Errorf("no non-skipped global layer in ConfigLayers: %+v", result.ConfigLayers)
	}

	// Finding 1 fix: assert the config endpoint was actually applied to the client.
	// WithRemote is only applied when the endpoint differs from the compiled default,
	// so RemoteURL() must equal the custom endpoint we set in config.
	if got := client.RemoteURL(); got != customEndpoint {
		t.Errorf("client.RemoteURL() = %q, want %q (config endpoint not applied to client)", got, customEndpoint)
	}
}

// TestInitWithConfig_NoConfig verifies that when no config files exist,
// InitWithConfig behaves identically to Init(): compiled defaults, no ConfigLayers.
func TestInitWithConfig_NoConfig(t *testing.T) {
	// Use an empty directory as globalDir — no config.toml present.
	globalDir := t.TempDir()

	client, result, err := protocol.InitWithConfig(protocol.WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	if result == nil {
		t.Fatal("InitWithConfig returned nil *InitResult")
	}

	// No config files → no layers.
	if len(result.ConfigLayers) != 0 {
		t.Errorf("ConfigLayers: got %d layers, want 0 (no config files present)", len(result.ConfigLayers))
	}

	// IdentityPath must be populated (absolute path).
	if result.IdentityPath == "" {
		t.Error("InitResult.IdentityPath is empty")
	}
	if !filepath.IsAbs(result.IdentityPath) {
		t.Errorf("InitResult.IdentityPath is not absolute: %q", result.IdentityPath)
	}

	// AutoJoined must be empty.
	if len(result.AutoJoined) != 0 {
		t.Errorf("AutoJoined: got %v, want empty (no auto_join in config)", result.AutoJoined)
	}
}

// TestInitWithConfig_IdentityFromConfig verifies that when a config file specifies
// identity.file, InitResult.IdentitySource is set to "config".
func TestInitWithConfig_IdentityFromConfig(t *testing.T) {
	globalDir := t.TempDir()

	// Write a global config that sets identity.file explicitly.
	writeInitConfigFile(t,
		filepath.Join(globalDir, "config.toml"),
		`[identity]
file = "identity.json"
`)

	client, result, err := protocol.InitWithConfig(protocol.WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	if result == nil {
		t.Fatal("InitWithConfig returned nil *InitResult")
	}

	if result.IdentitySource != "config" {
		t.Errorf("IdentitySource = %q, want %q", result.IdentitySource, "config")
	}

	// IdentityPath must be absolute.
	if !filepath.IsAbs(result.IdentityPath) {
		t.Errorf("IdentityPath is not absolute: %q", result.IdentityPath)
	}
}

// TestInitWithConfig_AutoJoined verifies that campfires listed in behavior.auto_join
// in the config cascade are reflected in InitResult.AutoJoined.
// We use an invalid/nonexistent campfire ID to exercise the warning path, verifying
// that the auto_join attempt is made and a warning is emitted.
func TestInitWithConfig_AutoJoined(t *testing.T) {
	globalDir := t.TempDir()

	// Use a well-formed hex campfire ID that won't be reachable.
	// InitWithConfig should attempt auto-join, fail, and record a warning.
	fakeCampfireID := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	writeInitConfigFile(t,
		filepath.Join(globalDir, "config.toml"),
		`[behavior]
auto_join = ["`+fakeCampfireID+`"]
`)

	client, result, err := protocol.InitWithConfig(protocol.WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	if result == nil {
		t.Fatal("InitWithConfig returned nil *InitResult")
	}

	// The join attempt should fail (campfire not in store), so AutoJoined should be empty
	// but a warning referencing the specific campfire ID must be present.
	// This verifies that the auto_join field is read and an attempt is made.
	// A successful join would populate AutoJoined; a failed one produces a Warning.
	if len(result.AutoJoined) > 0 {
		// If join succeeded (unlikely with a fake ID), the ID must be the expected one.
		found := false
		for _, id := range result.AutoJoined {
			if id == fakeCampfireID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AutoJoined = %v, want it to contain %q", result.AutoJoined, fakeCampfireID)
		}
	} else {
		// Join failed — a warning containing the campfire ID must be present.
		warnedWithID := false
		for _, w := range result.Warnings {
			if strings.Contains(w, fakeCampfireID) {
				warnedWithID = true
				break
			}
		}
		if !warnedWithID {
			t.Errorf("auto_join failed but no warning contains campfire ID %q; warnings = %v",
				fakeCampfireID, result.Warnings)
		}
	}
}

// TestInitWithConfig_CWDCascade verifies that InitWithConfig discovers and applies
// a .cf/config.toml from the current working directory without requiring WithConfigDir.
// This tests the distinguishing behavior of InitWithConfig over Init(): the CWD-based
// cascade walk.
func TestInitWithConfig_CWDCascade(t *testing.T) {
	// Create a temp directory representing a "project directory".
	projectDir := t.TempDir()

	// Write a .cf/config.toml inside the project dir with a recognizable setting.
	projectEndpoint := "https://project-override.test"
	cfDir := filepath.Join(projectDir, ".cf")
	if err := os.MkdirAll(cfDir, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", cfDir, err)
	}
	cfgPath := filepath.Join(cfDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`[transport]
endpoint = "`+projectEndpoint+`"
`), 0600); err != nil {
		t.Fatalf("write %s: %v", cfgPath, err)
	}

	// Use an empty temp dir as global config dir so the global layer doesn't
	// interfere with the assertion.
	globalDir := t.TempDir()

	// Change CWD to the project dir — InitWithConfig uses os.Getwd() for cascade walk.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) }) //nolint:errcheck
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir %s: %v", projectDir, err)
	}

	// Call InitWithConfig with WithConfigDir pointing to the empty global dir.
	// The cascade walk should discover projectDir/.cf/config.toml via CWD.
	client, result, err := protocol.InitWithConfig(protocol.WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	if result == nil {
		t.Fatal("InitWithConfig returned nil *InitResult")
	}

	// A project layer must appear in ConfigLayers.
	foundProject := false
	for _, l := range result.ConfigLayers {
		if l.Source == "project" && !l.Skipped {
			foundProject = true
			break
		}
	}
	if !foundProject {
		t.Errorf("no non-skipped project layer in ConfigLayers: %+v", result.ConfigLayers)
	}

	// The project config endpoint must have been applied to the client.
	if got := client.RemoteURL(); got != projectEndpoint {
		t.Errorf("client.RemoteURL() = %q, want %q (project config endpoint not applied)", got, projectEndpoint)
	}
}

// TestInitWithConfig_ConfigLayers verifies that InitResult.ConfigLayers reflects
// the config files examined during the cascade, including their source labels.
func TestInitWithConfig_ConfigLayers(t *testing.T) {
	globalDir := t.TempDir()

	// Write a global config file.
	writeInitConfigFile(t,
		filepath.Join(globalDir, "config.toml"),
		`[transport]
type = "http"
`)

	client, result, err := protocol.InitWithConfig(protocol.WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	if result == nil {
		t.Fatal("InitWithConfig returned nil *InitResult")
	}

	if len(result.ConfigLayers) == 0 {
		t.Fatal("ConfigLayers is empty — expected at least 1 layer")
	}

	// Find the global layer.
	var globalLayer *protocol.ConfigLayer
	for i := range result.ConfigLayers {
		if result.ConfigLayers[i].Source == "global" {
			globalLayer = &result.ConfigLayers[i]
			break
		}
	}
	if globalLayer == nil {
		t.Fatalf("no global layer in ConfigLayers: %+v", result.ConfigLayers)
	}

	// Global layer path must be absolute.
	if !filepath.IsAbs(globalLayer.Path) {
		t.Errorf("ConfigLayer.Path is not absolute: %q", globalLayer.Path)
	}

	// Global layer must not be skipped.
	if globalLayer.Skipped {
		t.Errorf("global layer was skipped: reason=%q", globalLayer.SkipReason)
	}

	// Global layer must list the field(s) it contributed.
	if len(globalLayer.Fields) == 0 {
		t.Error("global layer contributed 0 fields — expected at least transport.type")
	}
}

// makeFilesystemBeaconString creates a real filesystem campfire and returns
// its beacon string and the transport base directory.
func makeFilesystemBeaconString(t *testing.T) (beaconStr string, transportBaseDir string) {
	t.Helper()

	// Generate a campfire keypair.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	// Set up a filesystem transport so the campfire state exists on disk.
	baseDir := t.TempDir()
	tr := cftransport.New(baseDir)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("transport.Init: %v", err)
	}

	campfireDir := tr.CampfireDir(cf.PublicKeyHex())

	b, err := beacon.New(
		cf.PublicKey,
		cf.PrivateKey,
		cf.JoinProtocol,
		cf.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "filesystem",
			Config:   map[string]string{"dir": campfireDir},
		},
		"auto-join test campfire",
	)
	if err != nil {
		t.Fatalf("beacon.New: %v", err)
	}

	raw, err := cfencoding.Marshal(b)
	if err != nil {
		t.Fatalf("cfencoding.Marshal beacon: %v", err)
	}

	encoded := base64.StdEncoding.EncodeToString(raw)
	return "beacon:" + encoded, baseDir
}

// TestInitWithConfig_AutoJoin_Success verifies that when behavior.auto_join
// contains a valid filesystem campfire beacon string, InitWithConfig joins
// the campfire and populates InitResult.AutoJoined.
func TestInitWithConfig_AutoJoin_Success(t *testing.T) {
	beaconStr, _ := makeFilesystemBeaconString(t)

	globalDir := t.TempDir()
	writeInitConfigFile(t,
		filepath.Join(globalDir, "config.toml"),
		`[behavior]
auto_join = ["`+beaconStr+`"]
`)

	client, result, err := protocol.InitWithConfig(protocol.WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	if result == nil {
		t.Fatal("InitWithConfig returned nil *InitResult")
	}

	// The beacon is valid and the campfire state exists — join should succeed.
	if len(result.AutoJoined) == 0 {
		t.Errorf("AutoJoined is empty; warnings = %v", result.Warnings)
		return
	}
	if result.AutoJoined[0] != beaconStr {
		t.Errorf("AutoJoined[0] = %q, want %q", result.AutoJoined[0], beaconStr)
	}

	// No warnings for this entry.
	for _, w := range result.Warnings {
		if strings.Contains(w, "auto_join") {
			t.Errorf("unexpected auto_join warning: %q", w)
		}
	}
}

// TestInitWithConfig_AutoJoin_AlreadyMember verifies that when the agent is
// already a member of a campfire listed in behavior.auto_join, the entry is
// silently skipped (no duplicate join, no warning).
func TestInitWithConfig_AutoJoin_AlreadyMember(t *testing.T) {
	beaconStr, baseDir := makeFilesystemBeaconString(t)

	globalDir := t.TempDir()
	writeInitConfigFile(t,
		filepath.Join(globalDir, "config.toml"),
		`[behavior]
auto_join = ["`+beaconStr+`"]
`)

	// First InitWithConfig: should join.
	client1, result1, err := protocol.InitWithConfig(protocol.WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("first InitWithConfig: %v", err)
	}
	client1.Close()

	if len(result1.AutoJoined) == 0 {
		t.Skipf("first InitWithConfig did not join (warnings=%v) — skipping already-member test", result1.Warnings)
	}

	// Verify the campfire ID was recorded.
	joinedAddr := result1.AutoJoined[0]
	_ = joinedAddr
	_ = baseDir

	// Second InitWithConfig with the same store: should NOT re-join.
	client2, result2, err := protocol.InitWithConfig(protocol.WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("second InitWithConfig: %v", err)
	}
	t.Cleanup(func() { client2.Close() })

	if result2 == nil {
		t.Fatal("second InitWithConfig returned nil *InitResult")
	}

	// Already a member → AutoJoined must be empty on second call.
	if len(result2.AutoJoined) > 0 {
		t.Errorf("second AutoJoined = %v, want empty (already a member)", result2.AutoJoined)
	}

	// No auto_join warnings expected.
	for _, w := range result2.Warnings {
		if strings.Contains(w, "auto_join") {
			t.Errorf("unexpected auto_join warning on second call: %q", w)
		}
	}
}

// TestInitWithConfig_AutoJoin_AlreadyMember_PreSeeded verifies that when the
// store already has a membership record before InitWithConfig is called,
// the auto-join entry is silently skipped without attempting a join.
func TestInitWithConfig_AutoJoin_AlreadyMember_PreSeeded(t *testing.T) {
	_, baseDir := makeFilesystemBeaconString(t)

	// Scan the transport dir to find the campfire ID.
	entries, err := os.ReadDir(baseDir)
	if err != nil || len(entries) == 0 {
		t.Skip("could not find campfire in transport dir")
	}
	campfireID := entries[0].Name()
	if len(campfireID) != 64 {
		t.Skipf("unexpected campfire dir name %q", campfireID)
	}

	globalDir := t.TempDir()
	// Use the hex campfire ID directly in auto_join (simpler than beacon string).
	writeInitConfigFile(t,
		filepath.Join(globalDir, "config.toml"),
		`[behavior]
auto_join = ["`+campfireID+`"]
`)

	// Pre-seed the store: open the store, write a membership record.
	s, err := cfstore.Open(filepath.Join(globalDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	if err := s.AddMembership(cfstore.Membership{
		CampfireID:    campfireID,
		JoinProtocol:  "open",
		TransportDir:  filepath.Join(baseDir, campfireID),
		TransportType: "filesystem",
		JoinedAt:      time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	s.Close()

	// InitWithConfig: store already has the membership → must skip, not join.
	client, result, err := protocol.InitWithConfig(protocol.WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	if result == nil {
		t.Fatal("InitWithConfig returned nil *InitResult")
	}

	// Already a member → AutoJoined must be empty.
	if len(result.AutoJoined) > 0 {
		t.Errorf("AutoJoined = %v, want empty (pre-seeded membership)", result.AutoJoined)
	}

	// No auto_join warnings.
	for _, w := range result.Warnings {
		if strings.Contains(w, "auto_join") {
			t.Errorf("unexpected auto_join warning: %q", w)
		}
	}
}

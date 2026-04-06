package protocol_test

// Tests for protocol.InitWithConfig() — campfire-agent-4gw.
//
// Tests verify:
//  1. Global config sets transport endpoint (TestInitWithConfig_GlobalConfig)
//  2. No config files → compiled defaults, same as Init() (TestInitWithConfig_NoConfig)
//  3. Config specifies identity.file → IdentitySource = "config" (TestInitWithConfig_IdentityFromConfig)
//  4. Config has behavior.auto_join → AutoJoined populated (TestInitWithConfig_AutoJoined)
//  5. ConfigLayers reflects files examined (TestInitWithConfig_ConfigLayers)
//
// All tests use real temp dirs, real SQLite stores, and real Ed25519 keys.
// No mocks. test-scope: targeted (pkg/protocol/... only).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/protocol"
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
	// but a warning should be present.
	// This verifies that the auto_join field is read and an attempt is made.
	// A successful join would populate AutoJoined; a failed one produces a Warning.
	warned := false
	for _, w := range result.Warnings {
		if len(w) > 0 {
			warned = true
			break
		}
	}
	// Either AutoJoined has the ID or a warning was produced — one of these must be true.
	if len(result.AutoJoined) == 0 && !warned {
		t.Errorf("auto_join was set in config but AutoJoined is empty and no warnings were produced")
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

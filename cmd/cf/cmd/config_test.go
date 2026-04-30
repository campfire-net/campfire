package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// setupConfigTest sets up a temp environment with a fake home directory.
// Returns the globalDir (fake ~/.cf) and a cleanup function.
func setupConfigTest(t *testing.T) (globalDir string, projectDir string) {
	t.Helper()
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_HOME", "")
	cfHome = "" // reset the package-level flag variable

	globalDir = filepath.Join(fakeHome, ".cf")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatalf("creating global dir: %v", err)
	}

	projectDir = t.TempDir()
	return globalDir, projectDir
}

// writeConfig writes a TOML config file at dir/config.toml.
func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	cfgDir := filepath.Join(dir, ".cf")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("creating .cf dir under %s: %v", dir, err)
	}
	p := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatalf("writing config to %s: %v", p, err)
	}
}

// TestConfigList_ShowOrigin verifies that cf config list --show-origin
// annotates each value with its source config file.
func TestConfigList_ShowOrigin(t *testing.T) {
	globalDir, projectDir := setupConfigTest(t)

	// Write global config.
	if err := os.WriteFile(filepath.Join(globalDir, "config.toml"), []byte(`
[identity]
display_name = "Alice"
[transport]
type = "http"
endpoint = "https://mcp.getcampfire.dev"
`), 0600); err != nil {
		t.Fatalf("writing global config: %v", err)
	}

	// Write project config that overrides transport.type.
	writeConfig(t, projectDir, `
[transport]
type = "fs"
`)

	// Change to project dir so LoadConfig picks up the project config.
	orig, _ := os.Getwd()
	defer os.Chdir(orig) //nolint:errcheck
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir to project dir: %v", err)
	}

	// Force CFHome to point to the fake global dir.
	cfHome = globalDir

	cwd, _ := os.Getwd()
	cfg, layers, _, err := protocol.LoadConfig(globalDir, cwd)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	fields := configFieldsWithOrigin(cfg, layers)

	// transport.type should come from "project" layer.
	var transportTypeOrigin string
	var displayNameOrigin string
	for _, f := range fields {
		if f["key"] == "transport.type" {
			transportTypeOrigin = f["source"]
		}
		if f["key"] == "identity.display_name" {
			displayNameOrigin = f["source"]
		}
	}

	if transportTypeOrigin != "project" {
		t.Errorf("transport.type origin = %q, want %q", transportTypeOrigin, "project")
	}
	if displayNameOrigin != "global" {
		t.Errorf("identity.display_name origin = %q, want %q", displayNameOrigin, "global")
	}
}

// TestConfigGet_Key verifies that cf config get <key> returns the correct value.
func TestConfigGet_Key(t *testing.T) {
	globalDir, _ := setupConfigTest(t)
	cfHome = globalDir

	if err := os.WriteFile(filepath.Join(globalDir, "config.toml"), []byte(`
[transport]
type = "http"
endpoint = "https://mcp.getcampfire.dev"
`), 0600); err != nil {
		t.Fatalf("writing global config: %v", err)
	}

	cfg, _, _, err := protocol.LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	got, err := configGetValue(cfg, "transport.type")
	if err != nil {
		t.Fatalf("configGetValue: %v", err)
	}
	if got != "http" {
		t.Errorf("configGetValue(transport.type) = %q, want %q", got, "http")
	}
}

// TestConfigGet_UnknownKey verifies that an unknown key returns an error.
func TestConfigGet_UnknownKey(t *testing.T) {
	globalDir, _ := setupConfigTest(t)
	cfHome = globalDir

	configGetCmd.SetArgs([]string{"nonexistent.key"})
	err := configGetCmd.RunE(configGetCmd, []string{"nonexistent.key"})
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "unknown config key") {
		t.Errorf("expected 'unknown config key' in error, got: %v", err)
	}
}

// TestConfigSet_GlobalLayer verifies that cf config set --global writes to
// the global config file.
func TestConfigSet_GlobalLayer(t *testing.T) {
	globalDir, _ := setupConfigTest(t)
	cfHome = globalDir

	targetPath := filepath.Join(globalDir, "config.toml")

	err := configSetValue(targetPath, "identity.display_name", "Bob")
	if err != nil {
		t.Fatalf("configSetValue: %v", err)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if !strings.Contains(string(data), "Bob") {
		t.Errorf("expected 'Bob' in config file, got:\n%s", data)
	}
	if !strings.Contains(string(data), "display_name") {
		t.Errorf("expected 'display_name' in config file, got:\n%s", data)
	}
}

// TestConfigSet_ProjectLayer verifies that cf config set --project writes to
// the project config file (.cf/config.toml).
func TestConfigSet_ProjectLayer(t *testing.T) {
	_, projectDir := setupConfigTest(t)

	targetPath := filepath.Join(projectDir, ".cf", "config.toml")

	err := configSetValue(targetPath, "transport.type", "fs")
	if err != nil {
		t.Fatalf("configSetValue: %v", err)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading project config: %v", err)
	}
	if !strings.Contains(string(data), "fs") {
		t.Errorf("expected 'fs' in project config, got:\n%s", data)
	}
	if !strings.Contains(string(data), "transport") {
		t.Errorf("expected 'transport' section in project config, got:\n%s", data)
	}
}

// TestConfigSet_UnknownKey verifies that setting an unknown key returns an error.
func TestConfigSet_UnknownKey(t *testing.T) {
	_, projectDir := setupConfigTest(t)
	targetPath := filepath.Join(projectDir, ".cf", "config.toml")

	err := configSetValue(targetPath, "unknown.key", "value")
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "unknown config key") {
		t.Errorf("expected 'unknown config key' in error, got: %v", err)
	}
}

// TestConfigSet_ListField verifies that list fields are parsed from JSON array syntax.
func TestConfigSet_ListField(t *testing.T) {
	globalDir, _ := setupConfigTest(t)
	targetPath := filepath.Join(globalDir, "config.toml")

	err := configSetValue(targetPath, "behavior.auto_join", `["beacon:abc","beacon:def"]`)
	if err != nil {
		t.Fatalf("configSetValue list field: %v", err)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if !strings.Contains(string(data), "beacon:abc") {
		t.Errorf("expected 'beacon:abc' in config, got:\n%s", data)
	}
}

// TestConfigLayers_ShowsAll verifies that LoadConfig discovers both global and
// project config layers.
func TestConfigLayers_ShowsAll(t *testing.T) {
	globalDir, projectDir := setupConfigTest(t)
	cfHome = globalDir

	// Write global config.
	if err := os.WriteFile(filepath.Join(globalDir, "config.toml"), []byte(`
[transport]
type = "http"
`), 0600); err != nil {
		t.Fatalf("writing global config: %v", err)
	}

	// Write project config.
	writeConfig(t, projectDir, `
[transport]
type = "fs"
`)

	_, layers, _, err := protocol.LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	var hasGlobal, hasProject bool
	for _, layer := range layers {
		if layer.Source == "global" {
			hasGlobal = true
		}
		if layer.Source == "project" {
			hasProject = true
		}
	}

	if !hasGlobal {
		t.Error("expected a 'global' layer in results")
	}
	if !hasProject {
		t.Error("expected a 'project' layer in results")
	}
}

// TestConfigSet_NamingRoot_ProjectRejected verifies that setting naming.root in a
// project/local config file returns an error (fail-fast before write).
// naming.root is global-only (LoadConfig S6 check); cf config set should reject
// the write rather than writing a value that will fail at read time.
func TestConfigSet_NamingRoot_ProjectRejected(t *testing.T) {
	globalDir, projectDir := setupConfigTest(t)
	cfHome = globalDir

	projectConfigPath := filepath.Join(projectDir, ".cf", "config.toml")

	err := configSetValue(projectConfigPath, "naming.root", "c1a62854df1b")
	if err == nil {
		t.Fatal("expected error when setting naming.root in project config, got nil")
	}
	if !strings.Contains(err.Error(), "naming.root") {
		t.Errorf("error %q does not mention 'naming.root'", err.Error())
	}
	if !strings.Contains(err.Error(), "global") {
		t.Errorf("error %q does not mention 'global'", err.Error())
	}

	// The file must not have been written (fail-fast: no partial writes).
	if _, statErr := os.Stat(projectConfigPath); statErr == nil {
		t.Error("project config file was written even though the operation should have been rejected")
	}
}

// TestConfigSet_NamingRoot_GlobalAllowed verifies that naming.root IS allowed
// in the global config file.
func TestConfigSet_NamingRoot_GlobalAllowed(t *testing.T) {
	globalDir, _ := setupConfigTest(t)
	cfHome = globalDir

	globalConfigPath := filepath.Join(globalDir, "config.toml")

	err := configSetValue(globalConfigPath, "naming.root", "c1a62854df1b")
	if err != nil {
		t.Fatalf("expected no error when setting naming.root in global config, got: %v", err)
	}

	data, err := os.ReadFile(globalConfigPath)
	if err != nil {
		t.Fatalf("reading global config: %v", err)
	}
	if !strings.Contains(string(data), "naming") {
		t.Errorf("expected 'naming' section in global config, got:\n%s", data)
	}
}

// TestConfigLayers_JSONOutput verifies that configFieldsWithOrigin returns
// valid JSON-serializable output.
func TestConfigLayers_JSONOutput(t *testing.T) {
	globalDir, _ := setupConfigTest(t)
	cfHome = globalDir
	t.Cleanup(func() { cfHome = "" })

	if err := os.WriteFile(filepath.Join(globalDir, "config.toml"), []byte(`
[transport]
type = "http"
`), 0600); err != nil {
		t.Fatalf("writing global config: %v", err)
	}

	cfg, layers, _, err := protocol.LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	fields := configFieldsWithOrigin(cfg, layers)
	if len(fields) == 0 {
		t.Fatal("expected non-empty fields list")
	}

	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshalling fields to JSON: %v", err)
	}

	var result []map[string]string
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshalling JSON: %v\nJSON was: %s", err, b)
	}
	if len(result) == 0 {
		t.Error("expected at least one field in JSON output")
	}
}

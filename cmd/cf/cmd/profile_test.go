package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// TestInitDisplayName_StoresProfileJSON verifies that cf init --display-name
// writes profile.json to the CF_HOME directory with the given name.
func TestInitDisplayName_StoresProfileJSON(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	// Reset all init flags to defaults before running.
	initCmd.Flags().Set("force", "false")        //nolint:errcheck
	initCmd.Flags().Set("name", "")              //nolint:errcheck
	initCmd.Flags().Set("session", "false")      //nolint:errcheck
	initCmd.Flags().Set("display-name", "")      //nolint:errcheck
	initCmd.Flags().Set("durable", "false")      //nolint:errcheck

	rootCmd.SetArgs([]string{"init", "--display-name", "Alice"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("cf init --display-name Alice failed: %v", err)
	}

	// profile.json must exist at cfHomeDir/profile.json.
	profilePath := filepath.Join(cfHomeDir, "profile.json")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("profile.json not written: %v", err)
	}

	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("profile.json is not valid JSON: %v", err)
	}
	if raw["display_name"] != "Alice" {
		t.Errorf("display_name = %q, want Alice", raw["display_name"])
	}

	// Verify via LoadProfile helper too.
	p := protocol.LoadProfile(cfHomeDir)
	if p.DisplayName != "Alice" {
		t.Errorf("LoadProfile returned %q, want Alice", p.DisplayName)
	}

	// identity.json must still exist (init must have completed normally).
	if _, err := os.Stat(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Errorf("identity.json not found: %v", err)
	}
}

// TestInitNoDisplayName_NoProfileJSON verifies that cf init without --display-name
// does NOT create profile.json.
func TestInitNoDisplayName_NoProfileJSON(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	initCmd.Flags().Set("force", "false")   //nolint:errcheck
	initCmd.Flags().Set("name", "")         //nolint:errcheck
	initCmd.Flags().Set("session", "false") //nolint:errcheck
	initCmd.Flags().Set("display-name", "") //nolint:errcheck
	initCmd.Flags().Set("durable", "false") //nolint:errcheck

	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	profilePath := filepath.Join(cfHomeDir, "profile.json")
	if _, err := os.Stat(profilePath); err == nil {
		t.Errorf("profile.json should not exist when --display-name not provided")
	}
}

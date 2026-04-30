package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfile_NotFound(t *testing.T) {
	dir := t.TempDir()
	p := LoadProfile(dir)
	if p.DisplayName != "" {
		t.Errorf("expected empty display name for missing file, got %q", p.DisplayName)
	}
}

func TestSaveAndLoadProfile(t *testing.T) {
	dir := t.TempDir()
	want := ProfileFile{DisplayName: "Alice"}
	if err := SaveProfile(dir, want); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	got := LoadProfile(dir)
	if got.DisplayName != want.DisplayName {
		t.Errorf("display name mismatch: got %q, want %q", got.DisplayName, want.DisplayName)
	}

	// Verify file is at expected path
	path := filepath.Join(dir, "profile.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("profile.json not written: %v", err)
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("profile.json is not valid JSON: %v", err)
	}
	if raw["display_name"] != "Alice" {
		t.Errorf("JSON field display_name = %q, want Alice", raw["display_name"])
	}
}

func TestLoadProfile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "profile.json"), []byte("not json"), 0600)
	p := LoadProfile(dir)
	if p.DisplayName != "" {
		t.Errorf("expected empty for invalid JSON, got %q", p.DisplayName)
	}
}

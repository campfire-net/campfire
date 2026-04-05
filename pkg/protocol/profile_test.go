package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProfileCache_SetAndLookup(t *testing.T) {
	c := NewProfileCache()
	c.Set("abc123", "Alice")
	if got := c.Lookup("abc123"); got != "Alice" {
		t.Errorf("Lookup returned %q, want %q", got, "Alice")
	}
}

func TestProfileCache_LookupMiss(t *testing.T) {
	c := NewProfileCache()
	if got := c.Lookup("notfound"); got != "" {
		t.Errorf("expected empty string for miss, got %q", got)
	}
}

func TestProfileCache_SetEmptySkipped(t *testing.T) {
	c := NewProfileCache()
	c.Set("", "Alice")  // empty pubkey — ignored
	c.Set("abc", "")    // empty name — ignored
	if got := c.Lookup("abc"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestProfileCache_LoadFromMessages(t *testing.T) {
	c := NewProfileCache()

	payload, _ := json.Marshal(map[string]string{"display_name": "Bob"})
	msgs := []Message{
		{
			Sender:  "pubkey001",
			Tags:    []string{"identity:profile"},
			Payload: payload,
		},
		{
			Sender:  "pubkey002",
			Tags:    []string{"status"}, // not a profile message
			Payload: []byte(`{"display_name":"Eve"}`),
		},
	}
	c.LoadFromMessages(msgs)

	if got := c.Lookup("pubkey001"); got != "Bob" {
		t.Errorf("expected Bob, got %q", got)
	}
	if got := c.Lookup("pubkey002"); got != "" {
		t.Errorf("expected empty for non-profile message, got %q", got)
	}
}

func TestProfileCache_LoadFromMessages_InvalidPayloadSkipped(t *testing.T) {
	c := NewProfileCache()
	msgs := []Message{
		{
			Sender:  "pubkey003",
			Tags:    []string{"identity:profile"},
			Payload: []byte("not json"),
		},
	}
	c.LoadFromMessages(msgs) // should not panic
	if got := c.Lookup("pubkey003"); got != "" {
		t.Errorf("expected empty for invalid payload, got %q", got)
	}
}

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

func TestProfileCache_Overwrite(t *testing.T) {
	c := NewProfileCache()
	c.Set("abc", "Alice")
	c.Set("abc", "Alice2")
	if got := c.Lookup("abc"); got != "Alice2" {
		t.Errorf("expected Alice2, got %q", got)
	}
}

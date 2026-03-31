package naming

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadJoinPolicy_Absent(t *testing.T) {
	dir := t.TempDir()
	jp, err := LoadJoinPolicy(dir)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if jp != nil {
		t.Fatalf("expected nil JoinPolicy, got %+v", jp)
	}
}

func TestLoadJoinPolicy_Present(t *testing.T) {
	dir := t.TempDir()
	content := `{"join_policy":"consult","consult_campfire":"abc123","join_root":"root456"}`
	if err := os.WriteFile(filepath.Join(dir, joinPolicyFile), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	jp, err := LoadJoinPolicy(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jp == nil {
		t.Fatal("expected non-nil JoinPolicy")
	}
	if jp.JoinPolicy != "consult" {
		t.Errorf("JoinPolicy: got %q, want %q", jp.JoinPolicy, "consult")
	}
	if jp.ConsultCampfire != "abc123" {
		t.Errorf("ConsultCampfire: got %q, want %q", jp.ConsultCampfire, "abc123")
	}
	if jp.JoinRoot != "root456" {
		t.Errorf("JoinRoot: got %q, want %q", jp.JoinRoot, "root456")
	}
}

func TestSaveAndLoadJoinPolicy_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := &JoinPolicy{
		JoinPolicy:      "consult",
		ConsultCampfire: "deadbeef1234",
		JoinRoot:        "feedcafe5678",
	}

	if err := SaveJoinPolicy(dir, original); err != nil {
		t.Fatalf("SaveJoinPolicy failed: %v", err)
	}

	loaded, err := LoadJoinPolicy(dir)
	if err != nil {
		t.Fatalf("LoadJoinPolicy failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil JoinPolicy after round-trip")
	}
	if *loaded != *original {
		t.Errorf("round-trip mismatch: got %+v, want %+v", *loaded, *original)
	}
}

func TestLoadJoinPolicy_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, joinPolicyFile), []byte(`{bad json`), 0600); err != nil {
		t.Fatal(err)
	}

	jp, err := LoadJoinPolicy(dir)
	if err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
	if jp != nil {
		t.Errorf("expected nil JoinPolicy on error, got %+v", jp)
	}
}

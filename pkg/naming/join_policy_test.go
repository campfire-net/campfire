package naming

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validHexID is a valid 64-character lowercase hex campfire ID for use in tests.
const validHexID = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

// validHexID2 is a second valid 64-character lowercase hex campfire ID.
const validHexID2 = "deadbeef0102030405060708090a0b0c0d0e0f1011121314151617189a1b1c1d"

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
	content := `{"join_policy":"consult","consult_campfire":"` + validHexID + `","join_root":"` + validHexID2 + `"}`
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
	if jp.Policy != "consult" {
		t.Errorf("JoinPolicy: got %q, want %q", jp.Policy, "consult")
	}
	if jp.ConsultCampfire != validHexID {
		t.Errorf("ConsultCampfire: got %q, want %q", jp.ConsultCampfire, validHexID)
	}
	if jp.JoinRoot != validHexID2 {
		t.Errorf("JoinRoot: got %q, want %q", jp.JoinRoot, validHexID2)
	}
}

func TestSaveAndLoadJoinPolicy_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := &JoinPolicy{
		Policy:          "consult",
		ConsultCampfire: validHexID,
		JoinRoot:        validHexID2,
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

func TestLoadJoinPolicy_InvalidJoinRoot(t *testing.T) {
	cases := []struct {
		name     string
		joinRoot string
	}{
		{"short", "abc123"},
		{"uppercase", "A1B2C3D4E5F60718293A4B5C6D7E8F90A1B2C3D4E5F60718293A4B5C6D7E8F9"},
		{"too_long", validHexID + "00"},
		{"path_traversal", "../../../etc/passwd"},
		{"with_spaces", "a1b2 c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			content := `{"join_policy":"consult","consult_campfire":"` + validHexID + `","join_root":"` + tc.joinRoot + `"}`
			if err := os.WriteFile(filepath.Join(dir, joinPolicyFile), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			jp, err := LoadJoinPolicy(dir)
			if err == nil {
				t.Errorf("expected error for invalid join_root %q, got nil (jp=%+v)", tc.joinRoot, jp)
			} else if !strings.Contains(err.Error(), "invalid join_root") {
				t.Errorf("expected 'invalid join_root' in error, got: %v", err)
			}
		})
	}
}

func TestLoadJoinPolicy_InvalidConsultCampfire(t *testing.T) {
	cases := []struct {
		name    string
		consult string
	}{
		{"short", "abc123"},
		{"uppercase", "A1B2C3D4E5F60718293A4B5C6D7E8F90A1B2C3D4E5F60718293A4B5C6D7E8F9"},
		{"too_long", validHexID + "00"},
		{"path_traversal", "../../../etc/passwd"},
		{"with_spaces", "a1b2 c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			content := `{"join_policy":"consult","consult_campfire":"` + tc.consult + `","join_root":"` + validHexID2 + `"}`
			if err := os.WriteFile(filepath.Join(dir, joinPolicyFile), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			jp, err := LoadJoinPolicy(dir)
			if err == nil {
				t.Errorf("expected error for invalid consult_campfire %q, got nil (jp=%+v)", tc.consult, jp)
			} else if !strings.Contains(err.Error(), "invalid consult_campfire") {
				t.Errorf("expected 'invalid consult_campfire' in error, got: %v", err)
			}
		})
	}
}

func TestLoadJoinPolicy_FSWalkSentinelConsult(t *testing.T) {
	dir := t.TempDir()
	content := `{"join_policy":"consult","consult_campfire":"fs-walk","join_root":"` + validHexID2 + `"}`
	if err := os.WriteFile(filepath.Join(dir, joinPolicyFile), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	jp, err := LoadJoinPolicy(dir)
	if err != nil {
		t.Fatalf("expected fs-walk sentinel to be accepted, got error: %v", err)
	}
	if jp == nil {
		t.Fatal("expected non-nil JoinPolicy")
	}
	if jp.ConsultCampfire != FSWalkSentinel {
		t.Errorf("ConsultCampfire: got %q, want %q", jp.ConsultCampfire, FSWalkSentinel)
	}
}

func TestLoadJoinPolicy_EmptyFieldsSkipValidation(t *testing.T) {
	dir := t.TempDir()
	// Empty JoinRoot and ConsultCampfire should not trigger validation errors
	content := `{"join_policy":"consult","consult_campfire":"","join_root":""}`
	if err := os.WriteFile(filepath.Join(dir, joinPolicyFile), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	jp, err := LoadJoinPolicy(dir)
	if err != nil {
		t.Fatalf("expected no error for empty fields, got: %v", err)
	}
	if jp == nil {
		t.Fatal("expected non-nil JoinPolicy")
	}
}

package protocol

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScopeEnforcer_UnrestrictedCampfires verifies that an empty campfire allowlist
// permits any campfire ID.
func TestScopeEnforcer_UnrestrictedCampfires(t *testing.T) {
	e := NewScopeEnforcer(ScopeConfig{})
	if err := e.CheckCampfire("abc123"); err != nil {
		t.Errorf("empty allowlist should allow any campfire, got: %v", err)
	}
	if err := e.CheckCampfire(""); err != nil {
		t.Errorf("empty allowlist should allow empty campfire ID, got: %v", err)
	}
}

// TestScopeEnforcer_AllowlistedCampfire verifies that a campfire in the allowlist
// is permitted.
func TestScopeEnforcer_AllowlistedCampfire(t *testing.T) {
	id := "1e09ae035a4e0522fe6d0252d8f987474bd0ea07d04d0d6b65f85f1d9d47fa2d"
	e := NewScopeEnforcer(ScopeConfig{
		Campfires: []string{id, "other-campfire"},
	})
	if err := e.CheckCampfire(id); err != nil {
		t.Errorf("allowlisted campfire should be permitted, got: %v", err)
	}
}

// TestScopeEnforcer_DeniedCampfire verifies that a campfire not in the allowlist
// returns an error.
func TestScopeEnforcer_DeniedCampfire(t *testing.T) {
	e := NewScopeEnforcer(ScopeConfig{
		Campfires: []string{"allowed-id-1", "allowed-id-2"},
	})
	err := e.CheckCampfire("not-in-list")
	if err == nil {
		t.Error("campfire not in allowlist should return error, got nil")
	}
}

// TestScopeEnforcer_UnrestrictedOperations verifies that empty operation_classes
// permits any operation class.
func TestScopeEnforcer_UnrestrictedOperations(t *testing.T) {
	e := NewScopeEnforcer(ScopeConfig{})
	for _, class := range []string{"read", "write", "admin", "identity", "unknown"} {
		if err := e.CheckOperation(class); err != nil {
			t.Errorf("empty op_classes should allow %q, got: %v", class, err)
		}
	}
}

// TestScopeEnforcer_AllowedOperation verifies that an operation class in the allowed
// list is permitted.
func TestScopeEnforcer_AllowedOperation(t *testing.T) {
	e := NewScopeEnforcer(ScopeConfig{
		OperationClasses: []string{"read", "write"},
	})
	if err := e.CheckOperation("read"); err != nil {
		t.Errorf("allowed operation class should be permitted, got: %v", err)
	}
	if err := e.CheckOperation("write"); err != nil {
		t.Errorf("allowed operation class should be permitted, got: %v", err)
	}
}

// TestScopeEnforcer_DeniedOperation verifies that an operation class not in the
// allowed list returns an error.
func TestScopeEnforcer_DeniedOperation(t *testing.T) {
	e := NewScopeEnforcer(ScopeConfig{
		OperationClasses: []string{"read"},
	})
	err := e.CheckOperation("admin")
	if err == nil {
		t.Error("non-permitted operation class should return error, got nil")
	}
}

// TestLoadConfig_ScopeSection verifies that a [scope] block in TOML is parsed
// correctly into Config.Scope.
func TestLoadConfig_ScopeSection(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	campfireID1 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	campfireID2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	writeConfig(t, filepath.Join(globalDir, configFilename), `
[scope]
campfires = ["`+campfireID1+`", "`+campfireID2+`"]
operation_classes = ["read", "write"]
`)

	cfg, layers, _, err := LoadConfig(globalDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(layers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(layers))
	}

	if len(cfg.Scope.Campfires) != 2 {
		t.Fatalf("scope.campfires: got %v, want 2 entries", cfg.Scope.Campfires)
	}
	if cfg.Scope.Campfires[0] != campfireID1 {
		t.Errorf("scope.campfires[0]: got %q, want %q", cfg.Scope.Campfires[0], campfireID1)
	}
	if cfg.Scope.Campfires[1] != campfireID2 {
		t.Errorf("scope.campfires[1]: got %q, want %q", cfg.Scope.Campfires[1], campfireID2)
	}

	if len(cfg.Scope.OperationClasses) != 2 {
		t.Fatalf("scope.operation_classes: got %v, want 2 entries", cfg.Scope.OperationClasses)
	}
	if cfg.Scope.OperationClasses[0] != "read" {
		t.Errorf("scope.operation_classes[0]: got %q, want %q", cfg.Scope.OperationClasses[0], "read")
	}
	if cfg.Scope.OperationClasses[1] != "write" {
		t.Errorf("scope.operation_classes[1]: got %q, want %q", cfg.Scope.OperationClasses[1], "write")
	}

	// Verify contributed fields are tracked.
	hasScope := false
	for _, f := range layers[0].Fields {
		if f == "scope.campfires" || f == "scope.operation_classes" {
			hasScope = true
			break
		}
	}
	if !hasScope {
		t.Errorf("expected scope fields in layer.Fields, got: %v", layers[0].Fields)
	}
}

// TestLoadConfig_ScopeCascade verifies that scope lists cascade (merge) across
// global → project config layers.
func TestLoadConfig_ScopeCascade(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "global")
	projectDir := filepath.Join(tmp, "project")

	globalID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	projectID := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	writeConfig(t, filepath.Join(globalDir, configFilename), `
[scope]
campfires = ["`+globalID+`"]
operation_classes = ["read"]
`)
	writeConfig(t, filepath.Join(projectDir, cfDir, configFilename), `
[scope]
campfires = ["`+projectID+`"]
operation_classes = ["write"]
`)

	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}

	cfg, _, _, err := LoadConfig(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both campfire IDs should be present (append semantics).
	if len(cfg.Scope.Campfires) != 2 {
		t.Errorf("scope.campfires after cascade: got %v, want 2 entries", cfg.Scope.Campfires)
	}

	// Both operation classes should be present.
	if len(cfg.Scope.OperationClasses) != 2 {
		t.Errorf("scope.operation_classes after cascade: got %v, want 2 entries", cfg.Scope.OperationClasses)
	}
}

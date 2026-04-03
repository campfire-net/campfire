package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetAndShowContext(t *testing.T) {
	// Use a temp CF_HOME.
	tmp := t.TempDir()
	origCFHome := cfHome
	cfHome = tmp
	t.Cleanup(func() { cfHome = origCFHome })

	// Initially no context.
	id, err := resolveImplicitCampfire()
	if err != nil {
		t.Fatalf("resolveImplicitCampfire: %v", err)
	}
	if id != "" {
		t.Fatalf("expected empty context, got %q", id)
	}

	// Write a context file manually.
	campfireID := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	contextFile := filepath.Join(tmp, "current")
	if err := os.WriteFile(contextFile, []byte(campfireID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Should resolve from file.
	id, err = resolveImplicitCampfire()
	if err != nil {
		t.Fatalf("resolveImplicitCampfire: %v", err)
	}
	if id != campfireID {
		t.Fatalf("expected %q, got %q", campfireID, id)
	}

	// requireImplicitCampfire should also work.
	id, err = requireImplicitCampfire()
	if err != nil {
		t.Fatalf("requireImplicitCampfire: %v", err)
	}
	if id != campfireID {
		t.Fatalf("expected %q, got %q", campfireID, id)
	}
}

func TestClearContext(t *testing.T) {
	tmp := t.TempDir()
	origCFHome := cfHome
	cfHome = tmp
	t.Cleanup(func() { cfHome = origCFHome })

	// Write a context file.
	contextFile := filepath.Join(tmp, "current")
	if err := os.WriteFile(contextFile, []byte("abc123\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Clear it.
	if err := clearContext(); err != nil {
		t.Fatalf("clearContext: %v", err)
	}

	// Should be gone.
	id, err := resolveImplicitCampfire()
	if err != nil {
		t.Fatalf("resolveImplicitCampfire: %v", err)
	}
	if id != "" {
		t.Fatalf("expected empty context after clear, got %q", id)
	}
}

func TestCFContextEnvOverridesFile(t *testing.T) {
	tmp := t.TempDir()
	origCFHome := cfHome
	cfHome = tmp
	t.Cleanup(func() { cfHome = origCFHome })

	fileID := "1111111111111111111111111111111111111111111111111111111111111111"
	envID := "2222222222222222222222222222222222222222222222222222222222222222"

	// Write file context.
	contextFile := filepath.Join(tmp, "current")
	if err := os.WriteFile(contextFile, []byte(fileID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Set env override.
	t.Setenv("CF_CONTEXT", envID)

	// Env should win.
	id, err := resolveImplicitCampfire()
	if err != nil {
		t.Fatalf("resolveImplicitCampfire: %v", err)
	}
	if id != envID {
		t.Fatalf("expected env ID %q, got %q", envID, id)
	}
}

func TestRequireImplicitCampfire_NoContext(t *testing.T) {
	tmp := t.TempDir()
	origCFHome := cfHome
	cfHome = tmp
	t.Cleanup(func() { cfHome = origCFHome })
	t.Setenv("CF_CONTEXT", "")

	_, err := requireImplicitCampfire()
	if err == nil {
		t.Fatal("expected error when no context set")
	}
}

func TestContextFilePath(t *testing.T) {
	tmp := t.TempDir()
	origCFHome := cfHome
	cfHome = tmp
	t.Cleanup(func() { cfHome = origCFHome })

	expected := filepath.Join(tmp, "current")
	got := contextFilePath()
	if got != expected {
		t.Fatalf("contextFilePath() = %q, want %q", got, expected)
	}
}

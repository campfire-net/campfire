package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectRoot_Found(t *testing.T) {
	// Create a temp dir with .campfire/root
	dir := t.TempDir()
	campfireDir := filepath.Join(dir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0755); err != nil {
		t.Fatal(err)
	}
	id := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(id+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Change cwd to a subdirectory so we test walking up.
	subDir := filepath.Join(dir, "sub", "nested")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	if err := os.Chdir(subDir); err != nil {
		t.Fatal(err)
	}

	gotID, gotDir, ok := ProjectRoot()
	if !ok {
		t.Fatal("ProjectRoot() returned ok=false, want true")
	}
	if gotID != id {
		t.Errorf("ProjectRoot() campfireID = %q, want %q", gotID, id)
	}
	if gotDir != dir {
		t.Errorf("ProjectRoot() projectDir = %q, want %q", gotDir, dir)
	}
}

func TestProjectRoot_NotFound(t *testing.T) {
	// Create a temp dir with no .campfire/root
	dir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	_, _, ok := ProjectRoot()
	if ok {
		t.Fatal("ProjectRoot() returned ok=true in dir with no .campfire/root, want false")
	}
}

func TestProjectRoot_ShortIDIgnored(t *testing.T) {
	// Create a temp dir with .campfire/root containing a short (invalid) ID
	dir := t.TempDir()
	campfireDir := filepath.Join(dir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte("shortid\n"), 0644); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	_, _, ok := ProjectRoot()
	if ok {
		t.Fatal("ProjectRoot() returned ok=true for invalid (short) ID, want false")
	}
}

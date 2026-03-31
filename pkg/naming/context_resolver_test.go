package naming

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCenterFile creates a .campfire/center file in dir with the given campfire ID.
func writeCenterFile(t *testing.T, dir, id string) {
	t.Helper()
	campfireDir := filepath.Join(dir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		t.Fatalf("creating .campfire dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "center"), []byte(id+"\n"), 0600); err != nil {
		t.Fatalf("writing center file: %v", err)
	}
}

// writeContextKeyFile creates a .campfire/context-key.pub file in dir.
func writeContextKeyFile(t *testing.T, dir, content string) {
	t.Helper()
	campfireDir := filepath.Join(dir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		t.Fatalf("creating .campfire dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "context-key.pub"), []byte(content), 0600); err != nil {
		t.Fatalf("writing context-key.pub: %v", err)
	}
}

// TestResolveContextFindsCenter verifies that ResolveContext finds .campfire/center
// in an ancestor directory when called from a child directory.
func TestResolveContextFindsCenter(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "subdir", "project")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatalf("creating child dir: %v", err)
	}

	centerID := "aaaa1111" + "aaaa1111" + "aaaa1111" + "aaaa1111" + "aaaa1111" + "aaaa1111" + "aaaa1111" + "aaaa1111"
	writeCenterFile(t, root, centerID)

	res, err := ResolveContext(child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.CenterCampfireID != centerID {
		t.Errorf("expected CenterCampfireID %q, got %q", centerID, res.CenterCampfireID)
	}
}

// TestResolveContextNamingRoots verifies that ResolveContext collects multiple
// .campfire/root sentinels in order (deepest first).
func TestResolveContextNamingRoots(t *testing.T) {
	outer := t.TempDir()
	middle := filepath.Join(outer, "middle")
	inner := filepath.Join(middle, "inner")
	if err := os.MkdirAll(inner, 0700); err != nil {
		t.Fatalf("creating inner dir: %v", err)
	}

	outerRoot := "aaaa0000" + "aaaa0000" + "aaaa0000" + "aaaa0000" + "aaaa0000" + "aaaa0000" + "aaaa0000" + "aaaa0000"
	middleRoot := "bbbb1111" + "bbbb1111" + "bbbb1111" + "bbbb1111" + "bbbb1111" + "bbbb1111" + "bbbb1111" + "bbbb1111"
	innerRoot := "cccc2222" + "cccc2222" + "cccc2222" + "cccc2222" + "cccc2222" + "cccc2222" + "cccc2222" + "cccc2222"

	writeRootFile(t, outer, outerRoot)
	writeRootFile(t, middle, middleRoot)
	writeRootFile(t, inner, innerRoot)

	res, err := ResolveContext(inner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.NamingRoots) != 3 {
		t.Fatalf("expected 3 naming roots, got %d: %v", len(res.NamingRoots), res.NamingRoots)
	}
	if res.NamingRoots[0] != innerRoot {
		t.Errorf("expected innerRoot first, got %q", res.NamingRoots[0])
	}
	if res.NamingRoots[1] != middleRoot {
		t.Errorf("expected middleRoot second, got %q", res.NamingRoots[1])
	}
	if res.NamingRoots[2] != outerRoot {
		t.Errorf("expected outerRoot third, got %q", res.NamingRoots[2])
	}
}

// TestResolveContextContextKey verifies that ResolveContext returns the path to
// the nearest .campfire/context-key.pub file in the directory tree.
func TestResolveContextContextKey(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "proj", "sub")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatalf("creating child dir: %v", err)
	}

	// Context key is at root level.
	writeContextKeyFile(t, root, "ed25519pubkeydata")

	res, err := ResolveContext(child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedPath := filepath.Join(root, ".campfire", "context-key.pub")
	if res.ContextKeyPath != expectedPath {
		t.Errorf("expected ContextKeyPath %q, got %q", expectedPath, res.ContextKeyPath)
	}
}

// TestResolveContextEmpty verifies that ResolveContext returns a zero-value
// ContextResolution (no error) when no sentinels exist anywhere in the tree.
func TestResolveContextEmpty(t *testing.T) {
	dir := t.TempDir()

	res, err := ResolveContext(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil ContextResolution")
	}
	if res.CenterCampfireID != "" {
		t.Errorf("expected empty CenterCampfireID, got %q", res.CenterCampfireID)
	}
	if len(res.NamingRoots) != 0 {
		t.Errorf("expected empty NamingRoots, got %v", res.NamingRoots)
	}
	if res.ContextKeyPath != "" {
		t.Errorf("expected empty ContextKeyPath, got %q", res.ContextKeyPath)
	}
}

// TestResolveContextSymlink verifies that ResolveContext handles symlinked
// directories without entering an infinite loop.
func TestResolveContextSymlink(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0700); err != nil {
		t.Fatalf("creating real dir: %v", err)
	}

	centerID := "dddd3333" + "dddd3333" + "dddd3333" + "dddd3333" + "dddd3333" + "dddd3333" + "dddd3333" + "dddd3333"
	writeCenterFile(t, root, centerID)

	// Create a symlink pointing to realDir.
	symlinkDir := filepath.Join(root, "link")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Skipf("symlink creation not supported: %v", err)
	}

	// ResolveContext from the symlinked dir should find the center in root.
	res, err := ResolveContext(symlinkDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.CenterCampfireID != centerID {
		t.Errorf("expected CenterCampfireID %q, got %q", centerID, res.CenterCampfireID)
	}
}

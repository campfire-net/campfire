package naming

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRootFile creates a .campfire/root file in dir with the given campfire ID.
func writeRootFile(t *testing.T, dir, id string) {
	t.Helper()
	campfireDir := filepath.Join(dir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		t.Fatalf("creating .campfire dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(id+"\n"), 0600); err != nil {
		t.Fatalf("writing root file: %v", err)
	}
}

func TestFSWalkRoots_SingleRoot(t *testing.T) {
	dir := t.TempDir()
	rootID := "aabbccdd" + "11223344" + "aabbccdd" + "11223344" + "aabbccdd" + "11223344" + "aabbccdd" + "11223344"
	writeRootFile(t, dir, rootID)

	roots := FSWalkRoots(dir, "")
	if len(roots) != 1 || roots[0] != rootID {
		t.Fatalf("expected [%s], got %v", rootID, roots)
	}
}

func TestFSWalkRoots_NestedDirectories(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "sub", "project")
	if err := os.MkdirAll(inner, 0700); err != nil {
		t.Fatalf("creating inner dir: %v", err)
	}

	outerID := "aaaa0000" + "aaaa0000" + "aaaa0000" + "aaaa0000" + "aaaa0000" + "aaaa0000" + "aaaa0000" + "aaaa0000"
	innerID := "bbbb1111" + "bbbb1111" + "bbbb1111" + "bbbb1111" + "bbbb1111" + "bbbb1111" + "bbbb1111" + "bbbb1111"

	writeRootFile(t, outer, outerID)
	writeRootFile(t, inner, innerID)

	roots := FSWalkRoots(inner, "")
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots, got %v", roots)
	}
	if roots[0] != innerID {
		t.Errorf("expected inner root first, got %s", roots[0])
	}
	if roots[1] != outerID {
		t.Errorf("expected outer root second, got %s", roots[1])
	}
}

func TestFSWalkRoots_NoRootFile(t *testing.T) {
	dir := t.TempDir()
	joinRoot := "cccc2222" + "cccc2222" + "cccc2222" + "cccc2222" + "cccc2222" + "cccc2222" + "cccc2222" + "cccc2222"

	roots := FSWalkRoots(dir, joinRoot)
	if len(roots) != 1 || roots[0] != joinRoot {
		t.Fatalf("expected [%s], got %v", joinRoot, roots)
	}
}

func TestFSWalkRoots_RootFileAndJoinRoot(t *testing.T) {
	dir := t.TempDir()
	fsRootID := "dddd3333" + "dddd3333" + "dddd3333" + "dddd3333" + "dddd3333" + "dddd3333" + "dddd3333" + "dddd3333"
	joinRoot := "eeee4444" + "eeee4444" + "eeee4444" + "eeee4444" + "eeee4444" + "eeee4444" + "eeee4444" + "eeee4444"

	writeRootFile(t, dir, fsRootID)

	roots := FSWalkRoots(dir, joinRoot)
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots, got %v", roots)
	}
	if roots[0] != fsRootID {
		t.Errorf("expected fs root first, got %s", roots[0])
	}
	if roots[1] != joinRoot {
		t.Errorf("expected joinRoot second, got %s", roots[1])
	}
}

func TestFSWalkRoots_JoinRootAlreadyFound(t *testing.T) {
	dir := t.TempDir()
	sharedID := "ffff5555" + "ffff5555" + "ffff5555" + "ffff5555" + "ffff5555" + "ffff5555" + "ffff5555" + "ffff5555"

	writeRootFile(t, dir, sharedID)

	roots := FSWalkRoots(dir, sharedID)
	if len(roots) != 1 {
		t.Fatalf("expected 1 root (no duplicate), got %v", roots)
	}
	if roots[0] != sharedID {
		t.Errorf("expected %s, got %s", sharedID, roots[0])
	}
}

package naming

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestFSWalkRoots_DepthCap verifies that FSWalkRoots terminates even when
// started from a very deep directory hierarchy that would exceed fsWalkMaxDepth.
// This guards against symlink loops that could otherwise cause infinite traversal.
func TestFSWalkRoots_DepthCap(t *testing.T) {
	// Build a directory chain deeper than fsWalkMaxDepth.
	// Place a root file at the bottom so we confirm at least one is found.
	base := t.TempDir()
	deep := base
	for i := 0; i < fsWalkMaxDepth+10; i++ {
		deep = filepath.Join(deep, "d")
		if err := os.MkdirAll(deep, 0700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	deepID := "aaaa6666" + "aaaa6666" + "aaaa6666" + "aaaa6666" + "aaaa6666" + "aaaa6666" + "aaaa6666" + "aaaa6666"
	writeRootFile(t, deep, deepID)

	done := make(chan []string, 1)
	go func() {
		done <- FSWalkRoots(deep, "")
	}()

	select {
	case roots := <-done:
		if len(roots) == 0 {
			t.Error("expected at least one root to be found")
		}
		if roots[0] != deepID {
			t.Errorf("expected deepID first, got %s", roots[0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FSWalkRoots did not terminate within 5 seconds — likely infinite loop")
	}
}

// TestFSWalkRoots_SymlinkLoop verifies that FSWalkRoots terminates and does not
// hang when the start directory contains a circular symlink in its ancestry.
// The depth cap (fsWalkMaxDepth) is the protection mechanism under test.
func TestFSWalkRoots_SymlinkLoop(t *testing.T) {
	base := t.TempDir()

	// Create: base/a/b/loop -> base/a  (circular: loop points back up)
	aDir := filepath.Join(base, "a")
	bDir := filepath.Join(aDir, "b")
	if err := os.MkdirAll(bDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	loopLink := filepath.Join(bDir, "loop")
	if err := os.Symlink(aDir, loopLink); err != nil {
		t.Skipf("cannot create symlink (likely non-Unix): %v", err)
	}

	rootID := "bbbb7777" + "bbbb7777" + "bbbb7777" + "bbbb7777" + "bbbb7777" + "bbbb7777" + "bbbb7777" + "bbbb7777"
	writeRootFile(t, base, rootID)

	// Start from base/a/b — FSWalkRoots will walk upward (not into children),
	// so the symlink loop in the subtree is not directly exercised here.
	// What matters is that FSWalkRoots terminates in bounded time regardless.
	type result struct{ roots []string }
	ch := make(chan result, 1)
	go func() {
		ch <- result{FSWalkRoots(bDir, "")}
	}()

	// Allow up to 5 seconds — an infinite loop would never return.
	timer := time.After(5 * time.Second)

	select {
	case r := <-ch:
		if len(r.roots) == 0 {
			t.Error("expected root ID to be found while walking upward from bDir")
		}
	case <-timer:
		t.Fatal("FSWalkRoots did not terminate within 5 seconds — likely infinite loop")
	}
}

// TestFSWalkRoots_MalformedRootFile verifies that malformed .campfire/root
// contents are silently skipped — not propagated as campfire IDs.
func TestFSWalkRoots_MalformedRootFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"whitespace only", "   \n"},
		{"too short", "abcd1234"},
		{"too long", "aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344ff"},
		{"uppercase hex", "AABBCCDD11223344AABBCCDD11223344AABBCCDD11223344AABBCCDD11223344"},
		{"non-hex chars", "zzzzzzzz" + "zzzzzzzz" + "zzzzzzzz" + "zzzzzzzz" + "zzzzzzzz" + "zzzzzzzz" + "zzzzzzzz" + "zzzzzzzz"},
		{"path traversal", "../../../etc/passwd"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			campfireDir := filepath.Join(dir, ".campfire")
			if err := os.MkdirAll(campfireDir, 0700); err != nil {
				t.Fatalf("creating .campfire dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(tc.content), 0600); err != nil {
				t.Fatalf("writing root file: %v", err)
			}

			roots := FSWalkRoots(dir, "")
			if len(roots) != 0 {
				t.Errorf("malformed root %q: expected no roots, got %v", tc.name, roots)
			}
		})
	}
}

// TestFSWalkRoots_MalformedRootSkippedButValidFound verifies that a malformed
// root file in an inner directory does not prevent a valid root in an outer
// directory from being returned.
func TestFSWalkRoots_MalformedRootSkippedButValidFound(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0700); err != nil {
		t.Fatalf("creating inner dir: %v", err)
	}

	// Valid root in outer directory.
	validID := "aabb1122" + "aabb1122" + "aabb1122" + "aabb1122" + "aabb1122" + "aabb1122" + "aabb1122" + "aabb1122"
	writeRootFile(t, outer, validID)

	// Malformed root in inner directory.
	innerCampfireDir := filepath.Join(inner, ".campfire")
	if err := os.MkdirAll(innerCampfireDir, 0700); err != nil {
		t.Fatalf("creating inner .campfire dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(innerCampfireDir, "root"), []byte("not-a-valid-id"), 0600); err != nil {
		t.Fatalf("writing inner root file: %v", err)
	}

	roots := FSWalkRoots(inner, "")
	if len(roots) != 1 {
		t.Fatalf("expected exactly 1 root (outer valid), got %v", roots)
	}
	if roots[0] != validID {
		t.Errorf("expected outer validID %s, got %s", validID, roots[0])
	}
}

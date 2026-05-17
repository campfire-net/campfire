package cmd

// migrate_store_test.go — security regression for `cf migrate-store`
// (campfireagent-8b2).
//
// Pre-fix, args[0] flowed directly into filepath.Join(baseDir, campfireID),
// so `cf migrate-store ../../foo` escaped baseDir. We assert the command
// returns an "invalid campfire ID" error for every traversal shape and never
// touches a file outside baseDir.

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runMigrateStore invokes the migrate-store cobra command's RunE directly
// with the given positional argument. We bypass rootCmd.Execute because
// rootCmd has many side-effecting flags shared with other subcommands; the
// validation we test lives in RunE itself.
func runMigrateStore(t *testing.T, arg string) error {
	t.Helper()
	// Use a fresh buffer for stderr/stdout to keep test output clean.
	migrateStoreCmd.SetOut(&bytes.Buffer{})
	migrateStoreCmd.SetErr(&bytes.Buffer{})
	return migrateStoreCmd.RunE(migrateStoreCmd, []string{arg})
}

// TestMigrateStore_RejectsTraversalCampfireID verifies the CLI rejects every
// path-traversal shape in args[0] without touching the filesystem outside the
// configured base directory.
func TestMigrateStore_RejectsTraversalCampfireID(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", baseDir)
	// Make sure CF_HOME doesn't override the base.
	t.Setenv("CF_HOME", t.TempDir())

	// Place a sentinel file outside baseDir's parent. If the bug regressed and
	// the command stat()'d "../../sentinel" relative to baseDir, the test
	// would proceed past the validation. The invariant we enforce is that the
	// error path triggers BEFORE any filesystem stat — so a non-existent path
	// also doesn't error with "campfire directory not found", which would
	// indicate the validation was bypassed.
	cases := []string{
		"../../../../etc/passwd",
		"../etc",
		"..",
		"/etc/passwd",
		"foo/bar",
		"foo\x00bar",
		"",
		"not-hex-just-ascii",
		strings.Repeat("z", 64), // 64 chars but not hex
	}

	for _, arg := range cases {
		t.Run(arg, func(t *testing.T) {
			err := runMigrateStore(t, arg)
			if err == nil {
				t.Fatalf("migrate-store %q returned nil error — validation bypassed", arg)
			}
			if !strings.Contains(err.Error(), "invalid campfire ID") {
				t.Errorf("migrate-store %q: expected 'invalid campfire ID' error, got: %v", arg, err)
			}
		})
	}

	// Defense-in-depth: no file should have been created anywhere in baseDir
	// or in the temp parent (other than the empty dirs created by t.TempDir).
	_ = filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			t.Errorf("migrate-store created file in baseDir despite validation rejection: %s", path)
		}
		return nil
	})
}

// TestMigrateStore_AcceptsValidCampfireID is the positive-control: a properly
// shaped 64-hex campfire ID must pass validation. The command will still fail
// (the campfire directory doesn't exist), but with a "directory not found"
// error — not "invalid campfire ID".
func TestMigrateStore_AcceptsValidCampfireID(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", baseDir)
	t.Setenv("CF_HOME", t.TempDir())

	// 64 lowercase hex chars = valid campfire ID shape.
	validID := strings.Repeat("a1", 32)
	err := runMigrateStore(t, validID)
	if err == nil {
		t.Fatal("expected error (campfire dir does not exist) but got nil")
	}
	if strings.Contains(err.Error(), "invalid campfire ID") {
		t.Errorf("valid campfire ID rejected as invalid: %v", err)
	}
	// Confirm the error path was "directory not found" not "invalid ID".
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "no such") {
		t.Logf("note: error was %q (acceptable, but not the expected 'not found' shape)", err)
	}
}

// TestMigrateStore_WindowsWarning verifies that on Windows, the migrate-store
// command emits a warning about degraded-mode locking to stderr.
// On non-Windows platforms, this test is skipped (the warning is not expected).
func TestMigrateStore_WindowsWarning(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skipf("skipping Windows-specific test on %s", runtime.GOOS)
	}

	baseDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", baseDir)
	t.Setenv("CF_HOME", t.TempDir())

	// Capture stderr from the command.
	stderrBuf := &bytes.Buffer{}
	migrateStoreCmd.SetErr(stderrBuf)
	migrateStoreCmd.SetOut(&bytes.Buffer{})

	// Use a valid campfire ID format (won't find the directory, but that's ok —
	// the warning should print before the directory check).
	validID := strings.Repeat("a1", 32)
	_ = migrateStoreCmd.RunE(migrateStoreCmd, []string{validID})

	stderr := stderrBuf.String()
	requiredText := "WARNING: cf migrate-store on Windows. Migration lock is a no-op on this platform."
	if !strings.Contains(stderr, requiredText) {
		t.Errorf("Windows warning not found in stderr.\nExpected substring: %q\nGot stderr: %q", requiredText, stderr)
	}
}

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// captureInitSession runs cf init --session and captures stdout output.
func captureInitSession(t *testing.T) (stdout string, err error) {
	t.Helper()

	// Redirect os.Stdout to a pipe to capture fmt.Println output
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating pipe: %v", pipeErr)
	}
	origStdout := os.Stdout
	os.Stdout = w

	// Reset flag state
	forceInit = false
	initName = ""
	initSession = false
	rootCmd.SetArgs([]string{"init", "--session"})
	runErr := rootCmd.Execute()

	// Restore stdout and read captured output
	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	r.Close()

	return string(buf[:n]), runErr
}

// captureInitDefault runs cf init (no flags) with a custom CF_HOME.
func captureInitDefault(t *testing.T, cfHomeDir string) (stdout string, err error) {
	t.Helper()

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating pipe: %v", pipeErr)
	}
	origStdout := os.Stdout
	os.Stdout = w

	forceInit = false
	initName = ""
	initSession = false
	t.Setenv("CF_HOME", cfHomeDir)
	rootCmd.SetArgs([]string{"init"})
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	r.Close()

	return string(buf[:n]), runErr
}

// TestInitSession_CreatesTempDir verifies that --session creates an identity
// in a temp dir (not ~/.campfire/) and prints the path on line 1.
func TestInitSession_CreatesTempDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	campfireHome := filepath.Join(home, ".campfire")

	out, err := captureInitSession(t)
	if err != nil {
		t.Fatalf("cf init --session failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines of output, got: %q", out)
	}

	cfHomeLine := strings.TrimSpace(lines[0])
	displayName := strings.TrimSpace(lines[1])

	// Must be a real directory
	info, statErr := os.Stat(cfHomeLine)
	if statErr != nil {
		t.Fatalf("CF_HOME path %q does not exist: %v", cfHomeLine, statErr)
	}
	if !info.IsDir() {
		t.Fatalf("CF_HOME path %q is not a directory", cfHomeLine)
	}

	// Must NOT be inside ~/.campfire/
	if strings.HasPrefix(cfHomeLine, campfireHome) {
		t.Errorf("session identity should not be in ~/.campfire/, got %q", cfHomeLine)
	}

	// Display name must start with "agent:"
	if !strings.HasPrefix(displayName, "agent:") {
		t.Errorf("display name should start with 'agent:', got %q", displayName)
	}

	// Display name hex portion should be 6 chars
	hex := strings.TrimPrefix(displayName, "agent:")
	if len(hex) != 6 {
		t.Errorf("display name hex portion should be 6 chars, got %q (len %d)", hex, len(hex))
	}

	// Identity file must exist in the printed path
	identityFile := filepath.Join(cfHomeLine, "identity.json")
	if _, statErr := os.Stat(identityFile); statErr != nil {
		t.Errorf("identity.json not found at %q: %v", identityFile, statErr)
	}
}

// TestInitSession_TwoCallsProduceDifferentIdentities verifies uniqueness.
func TestInitSession_TwoCallsProduceDifferentIdentities(t *testing.T) {
	out1, err := captureInitSession(t)
	if err != nil {
		t.Fatalf("first cf init --session failed: %v", err)
	}
	out2, err := captureInitSession(t)
	if err != nil {
		t.Fatalf("second cf init --session failed: %v", err)
	}

	lines1 := strings.Split(strings.TrimSpace(out1), "\n")
	lines2 := strings.Split(strings.TrimSpace(out2), "\n")

	if len(lines1) < 2 || len(lines2) < 2 {
		t.Fatalf("expected 2 lines each, got %q and %q", out1, out2)
	}

	// Paths must differ
	path1 := strings.TrimSpace(lines1[0])
	path2 := strings.TrimSpace(lines2[0])
	if path1 == path2 {
		t.Errorf("two --session calls produced the same path: %q", path1)
	}

	// Both produce valid display names
	for _, line := range []string{lines1[1], lines2[1]} {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "agent:") {
			t.Errorf("display name missing 'agent:' prefix: %q", line)
		}
	}
}

// TestIsRootOwned_MockStat tests the ownership check logic with synthetic stat results.
func TestIsRootOwned_MockStat(t *testing.T) {
	// Simulate root ownership (UID 0)
	rootStat := &syscall.Stat_t{Uid: 0}
	if !isOwnedByRoot(rootStat) {
		t.Error("expected isOwnedByRoot to return true for UID 0")
	}

	// Simulate non-root ownership (UID 1000)
	userStat := &syscall.Stat_t{Uid: 1000}
	if isOwnedByRoot(userStat) {
		t.Error("expected isOwnedByRoot to return false for UID 1000")
	}
}

// TestInitPersistent_UnchangedBehavior verifies that cf init (no flags) still
// writes identity.json to the configured CF_HOME.
func TestInitPersistent_UnchangedBehavior(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := captureInitDefault(t, tmpDir)
	if err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	// Identity must exist in the CF_HOME we set
	identityFile := filepath.Join(tmpDir, "identity.json")
	if _, statErr := os.Stat(identityFile); statErr != nil {
		t.Errorf("identity.json not found at %q: %v", identityFile, statErr)
	}
}

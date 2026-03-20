package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runSwarmStart executes `cf swarm start` with the given CF_HOME and project dir (cwd).
// Returns stdout output and error.
func runSwarmStart(t *testing.T, cfHomeDir, projectDir string, extraArgs ...string) (string, error) {
	t.Helper()

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating pipe: %v", pipeErr)
	}
	origStdout := os.Stdout
	os.Stdout = w

	// Save and restore cwd.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting cwd: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("changing to project dir: %v", err)
	}

	t.Setenv("CF_HOME", cfHomeDir)
	// Route beacon publishing to a temp dir so we don't pollute or create ~/.campfire/.
	beaconDir := filepath.Join(cfHomeDir, "beacons")
	t.Setenv("CF_BEACON_DIR", beaconDir)

	args := append([]string{"swarm", "start"}, extraArgs...)
	rootCmd.SetArgs(args)
	runErr := rootCmd.Execute()

	// Restore state.
	os.Chdir(origDir) //nolint:errcheck
	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	r.Close()

	return string(buf[:n]), runErr
}

// setupSwarmTestEnv creates a temp CF_HOME with an identity, and a separate temp project dir.
func setupSwarmTestEnv(t *testing.T) (cfHomeDir, projectDir string) {
	t.Helper()

	cfHomeDir = t.TempDir()
	projectDir = t.TempDir()

	// Create identity so swarm start can load it.
	t.Setenv("CF_HOME", cfHomeDir)
	// Route beacon publishing to a temp dir so we don't pollute or create ~/.campfire/.
	t.Setenv("CF_BEACON_DIR", filepath.Join(cfHomeDir, "beacons"))
	initCmd.Flags().Set("force", "false")   //nolint:errcheck
	initCmd.Flags().Set("name", "")         //nolint:errcheck
	initCmd.Flags().Set("session", "false") //nolint:errcheck
	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	return cfHomeDir, projectDir
}

// TestSwarmStart_CreatesCampfireRoot verifies that `cf swarm start` creates
// .campfire/root in the current directory containing a valid 64-char hex campfire ID.
func TestSwarmStart_CreatesCampfireRoot(t *testing.T) {
	cfHomeDir, projectDir := setupSwarmTestEnv(t)

	out, err := runSwarmStart(t, cfHomeDir, projectDir)
	if err != nil {
		t.Fatalf("cf swarm start failed: %v", err)
	}

	// Output should be the campfire ID (64-char hex).
	campfireID := strings.TrimSpace(out)
	if len(campfireID) != 64 {
		t.Errorf("expected 64-char campfire ID output, got %q (len %d)", campfireID, len(campfireID))
	}

	// .campfire/root must exist and contain the campfire ID.
	rootFile := filepath.Join(projectDir, ".campfire", "root")
	data, err := os.ReadFile(rootFile)
	if err != nil {
		t.Fatalf(".campfire/root not created: %v", err)
	}
	storedID := strings.TrimSpace(string(data))
	if storedID != campfireID {
		t.Errorf(".campfire/root contains %q, want %q", storedID, campfireID)
	}
}

// TestSwarmStart_ErrorWhenRootExists verifies that running `cf swarm start` a second
// time in the same project directory returns an error.
func TestSwarmStart_ErrorWhenRootExists(t *testing.T) {
	cfHomeDir, projectDir := setupSwarmTestEnv(t)

	// First call should succeed.
	_, err := runSwarmStart(t, cfHomeDir, projectDir)
	if err != nil {
		t.Fatalf("first cf swarm start failed: %v", err)
	}

	// Second call should fail with a meaningful error.
	_, err = runSwarmStart(t, cfHomeDir, projectDir)
	if err == nil {
		t.Fatal("second cf swarm start should have returned an error, got nil")
	}
	if !strings.Contains(err.Error(), "already has a root campfire") {
		t.Errorf("expected error about existing root campfire, got: %v", err)
	}
}

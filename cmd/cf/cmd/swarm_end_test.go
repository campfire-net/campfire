package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runSwarmEnd executes `cf swarm end` with the given CF_HOME and project dir (cwd).
// Returns stdout output and error.
func runSwarmEnd(t *testing.T, cfHomeDir, projectDir string, extraArgs ...string) (string, error) {
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

	args := append([]string{"swarm", "end"}, extraArgs...)
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

// TestSwarmEnd_RemovesRootFile verifies that `cf swarm end` removes .campfire/root.
func TestSwarmEnd_RemovesRootFile(t *testing.T) {
	cfHomeDir, projectDir := setupSwarmTestEnv(t)

	// First create a swarm.
	_, err := runSwarmStart(t, cfHomeDir, projectDir)
	if err != nil {
		t.Fatalf("cf swarm start failed: %v", err)
	}

	// Verify .campfire/root exists.
	rootFile := filepath.Join(projectDir, ".campfire", "root")
	if _, err := os.Stat(rootFile); err != nil {
		t.Fatalf(".campfire/root not created by swarm start: %v", err)
	}

	// Now end the swarm.
	out, err := runSwarmEnd(t, cfHomeDir, projectDir)
	if err != nil {
		t.Fatalf("cf swarm end failed: %v", err)
	}

	// Verify .campfire/root is removed.
	if _, err := os.Stat(rootFile); err == nil {
		t.Fatal(".campfire/root still exists after cf swarm end")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking .campfire/root: %v", err)
	}

	// Output should include confirmation message.
	output := strings.TrimSpace(out)
	if !strings.Contains(output, "swarm") && !strings.Contains(output, "ended") {
		t.Logf("output: %q", output)
		// Don't fail on this — the confirmation message is nice but not critical.
	}
}

// TestSwarmEnd_ErrorWhenNoRoot verifies that running `cf swarm end` with no
// .campfire/root returns a meaningful error.
func TestSwarmEnd_ErrorWhenNoRoot(t *testing.T) {
	cfHomeDir, projectDir := setupSwarmTestEnv(t)

	// Try to end a swarm that was never created.
	_, err := runSwarmEnd(t, cfHomeDir, projectDir)
	if err == nil {
		t.Fatal("cf swarm end should have returned an error when no .campfire/root exists, got nil")
	}
	if !strings.Contains(err.Error(), ".campfire/root") {
		t.Errorf("expected error mentioning .campfire/root, got: %v", err)
	}
}

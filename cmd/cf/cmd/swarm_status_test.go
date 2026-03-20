package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSwarmStatus_NoRoot(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir) //nolint:errcheck

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	t.Setenv("CF_HOME", filepath.Join(tmpDir, "cfhome"))

	rootCmd.SetArgs([]string{"swarm", "status"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when no .campfire/root exists")
	}
	if !strings.Contains(err.Error(), "no active swarm") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSwarmStatus_WithRoot(t *testing.T) {
	tmpDir := t.TempDir()
	cfHome := filepath.Join(tmpDir, "cfhome")

	origDir, _ := os.Getwd()
	defer os.Chdir(origDir) //nolint:errcheck

	// First create a swarm to get a valid .campfire/root.
	projectDir := filepath.Join(tmpDir, "project")
	os.MkdirAll(projectDir, 0755) //nolint:errcheck
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	t.Setenv("CF_HOME", cfHome)
	t.Setenv("CF_BEACON_DIR", filepath.Join(cfHome, "beacons"))

	// Create identity first.
	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Start swarm.
	rootCmd.SetArgs([]string{"swarm", "start", "--description", "test swarm"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("swarm start: %v", err)
	}

	// Capture status output.
	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	rootCmd.SetArgs([]string{"swarm", "status"})
	err := rootCmd.Execute()

	w.Close()
	out := make([]byte, 4096)
	n, _ := r.Read(out)
	os.Stdout = origStdout

	if err != nil {
		t.Fatalf("swarm status: %v", err)
	}

	output := string(out[:n])
	if !strings.Contains(output, "Swarm campfire:") {
		t.Errorf("expected 'Swarm campfire:' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Messages:") {
		t.Errorf("expected 'Messages:' in output, got:\n%s", output)
	}
	// Filesystem swarm: self is NOT in peer_endpoints; solo creator = 1 member, not 2.
	if !strings.Contains(output, "Members:        1") {
		t.Errorf("expected 'Members:        1' for solo filesystem swarm, got:\n%s", output)
	}
}

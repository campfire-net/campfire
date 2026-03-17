package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureSwarmPrompt runs cf swarm prompt and captures stdout.
func captureSwarmPrompt(t *testing.T) (stdout string, err error) {
	t.Helper()

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating pipe: %v", pipeErr)
	}
	origStdout := os.Stdout
	os.Stdout = w

	rootCmd.SetArgs([]string{"swarm", "prompt"})
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 16384)
	n, _ := r.Read(buf)
	r.Close()

	return string(buf[:n]), runErr
}

// TestSwarmPrompt_OutputContainsKeyPhrases verifies the template contains expected content.
func TestSwarmPrompt_OutputContainsKeyPhrases(t *testing.T) {
	out, err := captureSwarmPrompt(t)
	if err != nil {
		t.Fatalf("cf swarm prompt failed: %v", err)
	}

	keyPhrases := []string{
		"Campfire Coordination",
		"cf init --session",
		"cf read",
		"cf send",
		"--tag status",
		"Post a plan before writing code",
		"Work Loop",
	}

	for _, phrase := range keyPhrases {
		if !strings.Contains(out, phrase) {
			t.Errorf("expected output to contain %q", phrase)
		}
	}
}

// TestSwarmPrompt_GenericTemplateWithoutProjectRoot verifies placeholder is used when not in a project.
func TestSwarmPrompt_GenericTemplateWithoutProjectRoot(t *testing.T) {
	// Save original state
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	// Change to a temp dir that's definitely not a project
	tmpDir := t.TempDir()
	os.Chdir(tmpDir)

	out, err := captureSwarmPrompt(t)
	if err != nil {
		t.Fatalf("cf swarm prompt failed: %v", err)
	}

	// Should contain placeholder or generic reference
	if !strings.Contains(out, "<campfire-id>") {
		t.Errorf("expected generic template to mention <campfire-id>, got: %s", out)
	}
}

// TestSwarmPrompt_ProjectTemplateWithProjectRoot verifies actual ID is embedded when in a project.
func TestSwarmPrompt_ProjectTemplateWithProjectRoot(t *testing.T) {
	// Create a temp project with .campfire/root
	tmpDir := t.TempDir()
	cfDir := filepath.Join(tmpDir, ".campfire")
	if err := os.MkdirAll(cfDir, 0700); err != nil {
		t.Fatalf("creating .campfire dir: %v", err)
	}

	testID := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	rootFile := filepath.Join(cfDir, "root")
	if err := os.WriteFile(rootFile, []byte(testID), 0600); err != nil {
		t.Fatalf("writing root file: %v", err)
	}

	// Save original state and change to project dir
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	os.Chdir(tmpDir)

	out, err := captureSwarmPrompt(t)
	if err != nil {
		t.Fatalf("cf swarm prompt failed: %v", err)
	}

	// Should contain the actual campfire ID
	if !strings.Contains(out, testID) {
		t.Errorf("expected output to contain campfire ID %q, got: %s", testID, out)
	}
}

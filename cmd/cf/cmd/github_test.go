//go:build !windows

package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveGitHubToken_CredFileSecurePerms verifies that no warning is emitted
// when the credential file has 0600 permissions.
func TestResolveGitHubToken_CredFileSecurePerms(t *testing.T) {
	cfHome := t.TempDir()
	credFile := filepath.Join(cfHome, "github-token")
	if err := os.WriteFile(credFile, []byte("ghp_testtoken\n"), 0600); err != nil {
		t.Fatalf("writing credential file: %v", err)
	}

	// Capture stderr.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	tok, err := resolveGitHubToken("", cfHome)

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r) //nolint:errcheck
	os.Stderr = origStderr

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_testtoken" {
		t.Errorf("token = %q, want %q", tok, "ghp_testtoken")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no warning for 0600 file, got: %s", buf.String())
	}
}

// TestResolveGitHubToken_CredFileInsecurePerms verifies that a warning is emitted
// when the credential file is world-readable.
func TestResolveGitHubToken_CredFileInsecurePerms(t *testing.T) {
	cfHome := t.TempDir()
	credFile := filepath.Join(cfHome, "github-token")
	if err := os.WriteFile(credFile, []byte("ghp_testtoken\n"), 0644); err != nil {
		t.Fatalf("writing credential file: %v", err)
	}

	// Capture stderr.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	tok, err := resolveGitHubToken("", cfHome)

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r) //nolint:errcheck
	os.Stderr = origStderr

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_testtoken" {
		t.Errorf("token = %q, want %q", tok, "ghp_testtoken")
	}
	warning := buf.String()
	if warning == "" {
		t.Error("expected a warning for 0644 file, got none")
	}
	// Warning should mention the file path and recommend 0600.
	for _, want := range []string{credFile, "0600"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not contain %q", warning, want)
		}
	}
}

// TestResolveGitHubToken_GroupReadablePerms verifies that a warning is emitted
// when the credential file is group-readable (0640).
func TestResolveGitHubToken_GroupReadablePerms(t *testing.T) {
	cfHome := t.TempDir()
	credFile := filepath.Join(cfHome, "github-token")
	if err := os.WriteFile(credFile, []byte("ghp_testtoken\n"), 0640); err != nil {
		t.Fatalf("writing credential file: %v", err)
	}

	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	_, err := resolveGitHubToken("", cfHome)

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r) //nolint:errcheck
	os.Stderr = origStderr

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected a warning for 0640 file, got none")
	}
}

// TestResolveGitHubToken_EnvVarTakesPrecedence verifies that GITHUB_TOKEN env var
// is used when set, without reading the credential file.
func TestResolveGitHubToken_EnvVarTakesPrecedence(t *testing.T) {
	cfHome := t.TempDir()
	// Write a world-readable file — should NOT be consulted because env var wins.
	credFile := filepath.Join(cfHome, "github-token")
	if err := os.WriteFile(credFile, []byte("ghp_filetoken\n"), 0644); err != nil {
		t.Fatalf("writing credential file: %v", err)
	}

	t.Setenv("GITHUB_TOKEN", "ghp_envtoken")

	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	tok, err := resolveGitHubToken("", cfHome)

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r) //nolint:errcheck
	os.Stderr = origStderr

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_envtoken" {
		t.Errorf("token = %q, want %q", tok, "ghp_envtoken")
	}
	// No warning — env var was used, file was never consulted.
	if buf.Len() != 0 {
		t.Errorf("unexpected warning when env var used: %s", buf.String())
	}
}



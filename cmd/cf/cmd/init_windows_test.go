//go:build windows

package cmd

import (
	"os"
	"strings"
	"testing"
)

// TestPassphraseSupportedFalseOnWindows verifies that passphraseSupported returns
// false on Windows, which triggers the plaintext fallback warning in cf init.
func TestPassphraseSupportedFalseOnWindows(t *testing.T) {
	if passphraseSupported() {
		t.Fatal("passphraseSupported() should return false on Windows")
	}
}

// captureInitDefaultWithStderr runs cf init with a custom CF_HOME, capturing both
// stdout and stderr.
func captureInitDefaultWithStderr(t *testing.T, cfHomeDir string) (stdout, stderr string, err error) {
	t.Helper()

	rOut, wOut, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating stdout pipe: %v", pipeErr)
	}
	rErr, wErr, pipeErr2 := os.Pipe()
	if pipeErr2 != nil {
		t.Fatalf("creating stderr pipe: %v", pipeErr2)
	}

	origStdout := os.Stdout
	origStderr := os.Stderr
	os.Stdout = wOut
	os.Stderr = wErr

	initCmd.Flags().Set("force", "false")   //nolint:errcheck
	initCmd.Flags().Set("name", "")         //nolint:errcheck
	initCmd.Flags().Set("session", "false") //nolint:errcheck
	t.Setenv("CF_HOME", cfHomeDir)
	rootCmd.SetArgs([]string{"init"})
	runErr := rootCmd.Execute()

	wOut.Close()
	wErr.Close()
	os.Stdout = origStdout
	os.Stderr = origStderr

	outBuf := make([]byte, 4096)
	nOut, _ := rOut.Read(outBuf)
	rOut.Close()

	errBuf := make([]byte, 4096)
	nErr, _ := rErr.Read(errBuf)
	rErr.Close()

	return string(outBuf[:nOut]), string(errBuf[:nErr]), runErr
}

// TestInitPlaintextFallback_WindowsWarning verifies that cf init on Windows emits
// a warning to stderr when falling back to plaintext identity storage.
// CF_PASSPHRASE is not set so the fallback fires.
func TestInitPlaintextFallback_WindowsWarning(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CF_PASSPHRASE", "") // ensure no passphrase from env

	_, stderr, err := captureInitDefaultWithStderr(t, tmpDir)
	if err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	if !strings.Contains(stderr, "identity saved without encryption") {
		t.Errorf("expected Windows plaintext fallback warning in stderr, got: %q", stderr)
	}
	if !strings.Contains(stderr, "CF_PASSPHRASE") {
		t.Errorf("expected CF_PASSPHRASE guidance in stderr, got: %q", stderr)
	}
}

// TestInitPlaintextFallback_WindowsNoWarningWithPassphrase verifies that no warning
// is emitted when CF_PASSPHRASE is set (encryption is used, no fallback).
func TestInitPlaintextFallback_WindowsNoWarningWithPassphrase(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CF_PASSPHRASE", "s3cr3t-test-passphrase")

	_, stderr, err := captureInitDefaultWithStderr(t, tmpDir)
	if err != nil {
		t.Fatalf("cf init with passphrase failed: %v", err)
	}

	if strings.Contains(stderr, "identity saved without encryption") {
		t.Errorf("should not emit plaintext warning when passphrase is set, stderr: %q", stderr)
	}
}

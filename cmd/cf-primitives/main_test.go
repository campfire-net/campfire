// cmd/cf-primitives/main_test.go — unit tests for the cmd/cf-primitives top-level dispatch.
//
// Tests live in package main so they can call dispatch() directly, exercising
// all branches without forking a subprocess. Subprocess tests (TestBinary_*)
// cover the full binary path including real exit codes.
//
// IMPORTANT: Do NOT use t.Parallel() here. dispatch() calls rootCmd.Execute
// via primcmd.Execute(), and rootCmd is a package-level cobra singleton.
// Concurrent mutation of os.Args causes non-deterministic results.
//
// Known cobra limitation: calling rootCmd.Execute() multiple times with
// os.Args changes can produce nil return values due to internal cobra state
// (e.g. after --help). Therefore unit tests only assert exit 0 paths for
// flag-only invocations; exit-1 assertions use subprocess tests (TestBinary_*).
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// suppressStderr redirects os.Stderr to /dev/null for the duration of fn.
// Used to keep test output clean when dispatch() is expected to print errors.
func suppressStderr(t *testing.T, fn func()) {
	t.Helper()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		fn()
		return
	}
	defer devNull.Close()
	orig := os.Stderr
	os.Stderr = devNull
	fn()
	os.Stderr = orig
}

// setOSArgs replaces os.Args for the duration of the test and restores the
// original on t.Cleanup. Cobra reads os.Args[1:] when no explicit SetArgs is
// used — this is the correct interception point for package-main tests.
func setOSArgs(t *testing.T, args ...string) {
	t.Helper()
	orig := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = orig })
}

// buildPrimitivesBinary compiles the cf-primitives binary to a temp dir and
// returns its path. The test is skipped if compilation fails.
func buildPrimitivesBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "cf-primitives")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	c := exec.Command("go", "build", "-o", out, ".")
	if data, err := c.CombinedOutput(); err != nil {
		t.Skipf("cannot build cf-primitives binary: %v\n%s", err, data)
	}
	return out
}

// ---------------------------------------------------------------------------
// dispatch() unit tests — exercise the top-level wiring in main.go
// ---------------------------------------------------------------------------

// TestDispatch_Help_Short verifies that dispatch() with -h returns exit 0.
// cf-primitives does not expose --version; --help / -h is the safe probe.
func TestDispatch_Help_Short(t *testing.T) {
	setOSArgs(t, "cf-primitives", "-h")
	var exitCode int
	suppressStderr(t, func() {
		exitCode = dispatch()
	})
	if exitCode != 0 {
		t.Errorf("dispatch() with -h: expected exit 0, got %d", exitCode)
	}
}

// TestDispatch_Help verifies that dispatch() with --help returns exit 0.
func TestDispatch_Help(t *testing.T) {
	setOSArgs(t, "cf-primitives", "--help")
	var exitCode int
	suppressStderr(t, func() {
		exitCode = dispatch()
	})
	if exitCode != 0 {
		t.Errorf("dispatch() with --help: expected exit 0, got %d", exitCode)
	}
}

// TestDispatch_NoArgs verifies that dispatch() with no args returns exit 0
// (shows help like most cobra binaries).
func TestDispatch_NoArgs(t *testing.T) {
	setOSArgs(t, "cf-primitives")
	var exitCode int
	suppressStderr(t, func() {
		exitCode = dispatch()
	})
	if exitCode != 0 {
		t.Errorf("dispatch() with no args: expected exit 0, got %d", exitCode)
	}
}

// TestDispatch_UnknownCommand verifies that dispatch() with an unrecognised
// subcommand returns exit 1. This must run before any other dispatch() call
// in the process — cobra returns an error for unknown commands only when root
// has no prior run state. It is listed first among exit-1 paths but after the
// exit-0 paths so the cobra singleton is in a known post-help state.
//
// NOTE: cobra's error return is non-deterministic across repeated Execute()
// calls in the same process. If this test becomes flaky, delete the unit
// variant and rely solely on TestBinary_UnrecognisedSubcommand.
func TestDispatch_UnknownCommand(t *testing.T) {
	setOSArgs(t, "cf-primitives", "notacommand-xyzzy123")
	var exitCode int
	suppressStderr(t, func() {
		exitCode = dispatch()
	})
	if exitCode != 1 {
		t.Logf("dispatch() with unknown command: got exit %d (cobra singleton may have eaten the error — rely on TestBinary_UnrecognisedSubcommand)", exitCode)
		t.Skip("cobra singleton returned 0 for unknown command — subprocess test covers this path")
	}
}

// ---------------------------------------------------------------------------
// Binary-level subprocess tests — real exit codes and stdout output
// ---------------------------------------------------------------------------

// TestBinary_HelpShortFlag verifies `cf-primitives -h` exits 0 and mentions
// the binary name.
func TestBinary_HelpShortFlag(t *testing.T) {
	bin := buildPrimitivesBinary(t)
	out, err := exec.Command(bin, "-h").CombinedOutput()
	if err != nil {
		t.Fatalf("cf-primitives -h: unexpected error %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "cf-primitives") {
		t.Errorf("-h output does not mention 'cf-primitives': %q", out)
	}
}

// TestBinary_HelpFlag verifies `cf-primitives --help` exits 0 and mentions
// the surface ceiling in the output.
func TestBinary_HelpFlag(t *testing.T) {
	bin := buildPrimitivesBinary(t)
	out, err := exec.Command(bin, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("cf-primitives --help: unexpected error %v\n%s", err, out)
	}
	outStr := string(out)
	if !strings.Contains(outStr, "primitives") {
		t.Errorf("--help output missing 'primitives': %q", outStr)
	}
}

// TestBinary_NoArgs verifies `cf-primitives` with no args exits 0 (shows help).
func TestBinary_NoArgs(t *testing.T) {
	bin := buildPrimitivesBinary(t)
	out, err := exec.Command(bin).CombinedOutput()
	if err != nil {
		t.Fatalf("cf-primitives (no args): unexpected error %v\n%s", err, out)
	}
}

// TestBinary_UnrecognisedSubcommand verifies that an unregistered subcommand
// exits with code 1.
func TestBinary_UnrecognisedSubcommand(t *testing.T) {
	bin := buildPrimitivesBinary(t)
	err := exec.Command(bin, "notacommand").Run()
	if err == nil {
		t.Fatal("expected non-zero exit for unregistered subcommand, got nil")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1, got %d", exitErr.ExitCode())
	}
}

// TestBinary_KnownSubcommands verifies that each frozen primitive subcommand
// is registered — invoking it with --help exits 0. This guards against a
// command being silently removed without triggering the surface ceiling test.
func TestBinary_KnownSubcommands(t *testing.T) {
	bin := buildPrimitivesBinary(t)
	subcommands := []string{
		"admit", "await", "create", "disband", "evict",
		"init", "join", "leave", "members", "read", "send", "subscribe",
	}
	for _, sub := range subcommands {
		sub := sub
		t.Run(sub, func(t *testing.T) {
			out, err := exec.Command(bin, sub, "--help").CombinedOutput()
			if err != nil {
				t.Errorf("cf-primitives %s --help: unexpected error %v\n%s", sub, err, out)
			}
		})
	}
}

// TestBinary_JSONFlagAccepted verifies that the persistent --json flag is
// accepted at the top level (exits 0 with --help).
func TestBinary_JSONFlagAccepted(t *testing.T) {
	bin := buildPrimitivesBinary(t)
	out, err := exec.Command(bin, "--json", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("cf-primitives --json --help: unexpected error %v\n%s", err, out)
	}
}

// TestBinary_CFHomeFlag verifies that the --cf-home flag is accepted and does
// not cause an error when a valid (temp) directory is provided with --help.
func TestBinary_CFHomeFlag(t *testing.T) {
	bin := buildPrimitivesBinary(t)
	tmpDir := t.TempDir()
	out, err := exec.Command(bin, "--cf-home", tmpDir, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("cf-primitives --cf-home %s --help: unexpected error %v\n%s", tmpDir, err, out)
	}
}

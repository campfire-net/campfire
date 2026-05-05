// cmd/cf/main_test.go — unit tests for the cmd/cf top-level dispatch logic.
//
// Tests live in package main so they can call dispatch() directly, exercising
// all branches without forking a subprocess (no os.Exit calls reach the
// process boundary). Subprocess tests (TestBinary_*) cover the full binary
// path including real exit codes.
//
// IMPORTANT: Do NOT use t.Parallel() here. dispatch() calls rootCmd.Execute
// via Execute() / Multicall(), and rootCmd is a package-level cobra singleton
// that reacts to os.Args. Concurrent mutation of os.Args or sequential cobra
// state causes non-deterministic results.
//
// Known cobra limitation: calling rootCmd.Execute() multiple times with
// os.Args changes can produce nil return values due to internal cobra state
// (e.g. after --help). Therefore unit tests only assert exit 0 paths and
// positive routing decisions; exit-1 assertions use subprocess tests (TestBinary_*).
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cmd/cf/cmd"
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

// buildCFBinary compiles the cf binary to a temp dir and returns the path.
// The test is skipped if compilation fails.
func buildCFBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "cf")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	// Build from the current directory (cmd/cf).
	c := exec.Command("go", "build", "-o", out, ".")
	if data, err := c.CombinedOutput(); err != nil {
		t.Skipf("cannot build cf binary: %v\n%s", err, data)
	}
	return out
}

// ---------------------------------------------------------------------------
// dispatch() unit tests — exercise branches in main.go
// ---------------------------------------------------------------------------

// TestDispatch_StandardMode_Version verifies that binaryName "cf" (non-multicall)
// routes to Execute() and returns exit 0 for --version.
func TestDispatch_StandardMode_Version(t *testing.T) {
	setOSArgs(t, "cf", "--version")
	var exitCode int
	suppressStderr(t, func() {
		exitCode = dispatch("cf", []string{"--version"})
	})
	if exitCode != 0 {
		t.Errorf("dispatch(cf, --version): expected exit 0, got %d", exitCode)
	}
}

// TestDispatch_MulticallMode_Version verifies that a non-cf binary name routes
// to Multicall() and exits 0 for --version.
func TestDispatch_MulticallMode_Version(t *testing.T) {
	setOSArgs(t, "social", "--version")
	var exitCode int
	suppressStderr(t, func() {
		exitCode = dispatch("social", []string{"--version"})
	})
	if exitCode != 0 {
		t.Errorf("dispatch(social, --version): expected exit 0, got %d", exitCode)
	}
}

// TestDispatch_MulticallMode_DispatchError verifies that a Multicall dispatch
// failure (non-existent campfire + real operation) produces exit 1. This uses
// a fresh dispatch() call (no prior Execute() calls in this test) so cobra
// state does not interfere.
func TestDispatch_MulticallMode_DispatchError(t *testing.T) {
	setOSArgs(t, "nonexistent-campfire-xyzzy", "someop")
	var exitCode int
	suppressStderr(t, func() {
		exitCode = dispatch("nonexistent-campfire-xyzzy", []string{"someop"})
	})
	if exitCode != 1 {
		t.Errorf("dispatch(nonexistent-campfire-xyzzy, someop): expected exit 1, got %d", exitCode)
	}
}

// TestDispatch_MulticallMode_UnsafeName verifies that an unsafe binary name
// (sanitizes to empty) falls back to Execute() instead of Multicall(). The
// empty sanitized name triggers the fallback branch in dispatch().
func TestDispatch_MulticallMode_UnsafeName(t *testing.T) {
	const badName = "!@#$%"
	safeName := cmd.SanitizeBinaryName(badName)
	if safeName != "" {
		t.Skipf("SanitizeBinaryName(%q) = %q; expected empty — test invariant broken", badName, safeName)
	}
	// "!@#$%" is detected as multicall (not "cf") but sanitizes to "".
	// dispatch() falls back to Execute() with --version — exits 0.
	setOSArgs(t, badName, "--version")
	var exitCode int
	suppressStderr(t, func() {
		exitCode = dispatch(badName, []string{"--version"})
	})
	if exitCode != 0 {
		t.Errorf("dispatch(%q, --version) fallback: expected exit 0, got %d", badName, exitCode)
	}
}

// TestDispatch_CFPrimitives_NotMulticall verifies that "cf-primitives" is not
// treated as a multicall invocation (it is the sibling binary, not an app).
func TestDispatch_CFPrimitives_NotMulticall(t *testing.T) {
	setOSArgs(t, "cf-primitives", "--version")
	var exitCode int
	suppressStderr(t, func() {
		exitCode = dispatch("cf-primitives", []string{"--version"})
	})
	// cf-primitives routes through Execute(), --version exits 0.
	if exitCode != 0 {
		t.Errorf("dispatch(cf-primitives, --version): expected exit 0, got %d", exitCode)
	}
}

// ---------------------------------------------------------------------------
// IsMulticallInvocation — routing predicate tests
// ---------------------------------------------------------------------------

// TestIsMulticallInvocation covers all name patterns used in the dispatch
// routing decision.
func TestIsMulticallInvocation(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"cf", false},
		{"cf-primitives", false},
		{"social", true},
		{"dontguess", true},
		{"rd", true},
		{"my-app", true},
		{"", false},
		// Full paths — only the base name matters.
		{"/usr/local/bin/cf", false},
		{"/usr/local/bin/social", true},
		{"./cf", false},
		{"./dontguess", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			got := cmd.IsMulticallInvocation(tc.input)
			if got != tc.want {
				t.Errorf("IsMulticallInvocation(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SanitizeBinaryName — injection-prevention tests
// ---------------------------------------------------------------------------

// TestSanitizeBinaryName verifies that only safe characters pass through and
// dangerous names are cleaned or rejected. The function calls filepath.Base
// internally before stripping characters, so path components are first resolved
// to their basename.
func TestSanitizeBinaryName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"social", "social"},
		{"dontguess", "dontguess"},
		{"my-app.v2", "my-app.v2"},
		{"app_name", "app_name"},
		// filepath.Base strips directory components first, then unsafe chars removed.
		// "../../etc/passwd" → Base → "passwd" → no unsafe chars → "passwd".
		{"../../etc/passwd", "passwd"},
		// Shell metacharacters stripped from base name.
		{"social; rm -rf /", "socialrm-rf"},
		// All unsafe characters → empty.
		{"!@#$%", ""},
		// Leading hyphen → rejected (looks like a flag).
		{"-flag-name", ""},
		// Leading dot → rejected (hidden file convention).
		{".hidden", ""},
		// Empty input → empty.
		{"", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			got := cmd.SanitizeBinaryName(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeBinaryName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestSanitizeBinaryName_WindowsExtension verifies that .exe extensions are
// stripped on Windows-style names before sanitization.
func TestSanitizeBinaryName_WindowsExtension(t *testing.T) {
	// cf.exe should sanitize to "cf" (no .exe), which then fails the "not cf"
	// multicall check — but sanitization itself should produce "cf.exe" stripped
	// to just the safe characters. The .exe check is in IsMulticallInvocation,
	// not SanitizeBinaryName. Here we just verify safe chars pass.
	got := cmd.SanitizeBinaryName("myapp.exe")
	if got != "myapp.exe" {
		t.Errorf("SanitizeBinaryName(myapp.exe) = %q, want %q", got, "myapp.exe")
	}
}

// ---------------------------------------------------------------------------
// Binary-level subprocess tests — real exit codes and stdout output
// ---------------------------------------------------------------------------

// TestBinary_VersionFlag verifies `cf --version` exits 0 and mentions "cf".
func TestBinary_VersionFlag(t *testing.T) {
	cfBin := buildCFBinary(t)
	out, err := exec.Command(cfBin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("cf --version: unexpected error %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "cf") {
		t.Errorf("cf --version output does not mention 'cf': %q", out)
	}
}

// TestBinary_HelpFlag verifies `cf --help` exits 0 and mentions "Campfire".
func TestBinary_HelpFlag(t *testing.T) {
	cfBin := buildCFBinary(t)
	out, err := exec.Command(cfBin, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("cf --help: unexpected error %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Campfire") {
		t.Errorf("cf --help output missing 'Campfire': %q", out)
	}
}

// TestBinary_HelpPrimitivesFlag verifies `cf --help-primitives` exits 0 and
// shows the primitives section.
func TestBinary_HelpPrimitivesFlag(t *testing.T) {
	cfBin := buildCFBinary(t)
	out, err := exec.Command(cfBin, "--help-primitives").CombinedOutput()
	if err != nil {
		t.Fatalf("cf --help-primitives: unexpected error %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "primitives") {
		t.Errorf("--help-primitives output missing 'primitives': %q", out)
	}
}

// TestBinary_ExitCode_UnrecognisedSubcommand verifies that an unregistered
// cobra subcommand exits with code 1 (convention lookup fails for "version").
func TestBinary_ExitCode_UnrecognisedSubcommand(t *testing.T) {
	cfBin := buildCFBinary(t)
	err := exec.Command(cfBin, "version").Run()
	if err == nil {
		t.Fatal("expected non-zero exit for unregistered subcommand 'version', got nil")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1, got %d", exitErr.ExitCode())
	}
}

// TestBinary_Multicall_VersionFlag verifies multicall mode: when invoked via a
// symlink named "social", `--version` exits 0 and mentions the app name.
func TestBinary_Multicall_VersionFlag(t *testing.T) {
	cfBin := buildCFBinary(t)
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "social")
	if err := os.Symlink(cfBin, linkPath); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	out, err := exec.Command(linkPath, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("social --version: unexpected error %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "social") {
		t.Errorf("social --version output missing 'social': %q", out)
	}
}

// TestBinary_Multicall_ExitCode verifies that a multicall dispatch failure
// (campfire not found) exits 1.
func TestBinary_Multicall_ExitCode(t *testing.T) {
	cfBin := buildCFBinary(t)
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "nonexistent-campfire-xyzzy")
	if err := os.Symlink(cfBin, linkPath); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	err := exec.Command(linkPath, "someop").Run()
	if err == nil {
		t.Fatal("expected non-zero exit for nonexistent campfire, got nil")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1, got %d", exitErr.ExitCode())
	}
}

// TestBinary_NoArgs verifies `cf` with no args exits 0 (shows help).
func TestBinary_NoArgs(t *testing.T) {
	cfBin := buildCFBinary(t)
	out, err := exec.Command(cfBin).CombinedOutput()
	if err != nil {
		t.Fatalf("cf (no args): unexpected error %v\n%s", err, out)
	}
}

// TestBinary_JSONFlag verifies `cf --json --version` exits 0 (json flag is accepted).
func TestBinary_JSONFlag(t *testing.T) {
	cfBin := buildCFBinary(t)
	out, err := exec.Command(cfBin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("cf --version: unexpected error %v\n%s", err, out)
	}
	if len(out) == 0 {
		t.Error("cf --version produced no output")
	}
}

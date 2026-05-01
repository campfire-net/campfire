// Package convention_test — depguard adversarial test (campfireagent-4d7, condition 3).
//
// This test proves two things:
//  1. The current cf-conventions/cf-convention/ tree has zero L2-no-extensions violations.
//  2. An intentional cross-layer import (L2 importing L3 extensions)
//     IS caught and rejected by the L2-no-extensions rule.
//
// This satisfies done-condition 3 (adversarial case):
//
//	"depguard probe: cf-conventions/cf-convention/ importing cf-conventions/cf-convention-extensions/*
//	is caught and rejected."
//
// Usage:
//
//	cd ~/projects/campfire && go test ./cf-conventions/cf-convention/ -run TestDepguardL2 -v
//
// The test requires golangci-lint on PATH (or ~/bin/golangci-lint).
// When golangci-lint is not available, the test skips with a clear message.
package convention_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDepguardL2NoExtensionsClean verifies the current cf-conventions/cf-convention/
// tree has zero L2-no-extensions depguard violations.
func TestDepguardL2NoExtensionsClean(t *testing.T) {
	lintBin := findGolangciLint(t)
	if lintBin == "" {
		t.Skip("golangci-lint not found; skipping depguard verification")
	}

	repoRoot := findRepoRoot(t)
	cmd := exec.Command(lintBin, "run", "--fast-only", "./cf-conventions/cf-convention/...")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "depguard") {
			t.Errorf("L2-no-extensions rule found violations in cf-conventions/cf-convention/:\n%s", out)
		}
	}
}

// TestDepguardL2NoExtensionsCatchesForbiddenImport proves the L2-no-extensions
// depguard rule fires when cf-conventions/cf-convention/ imports
// cf-conventions/cf-convention-extensions/* (L3).
//
// This is the adversarial condition from done-condition 3.
func TestDepguardL2NoExtensionsCatchesForbiddenImport(t *testing.T) {
	lintBin := findGolangciLint(t)
	if lintBin == "" {
		t.Skip("golangci-lint not found; skipping depguard adversarial verification")
	}

	repoRoot := findRepoRoot(t)

	// Create a temporary sub-package inside cf-conventions/cf-convention/ that imports
	// cf-conventions/cf-convention-extensions/identity — a deliberate L2 violation.
	// Placed inside cf-conventions/cf-convention/ so the depguard rule files pattern matches.
	violationDir := filepath.Join(repoRoot, "cf-conventions", "cf-convention", "depguard-l2-violation-probe")
	if err := os.MkdirAll(violationDir, 0750); err != nil {
		t.Fatalf("creating violation dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(violationDir) })

	violationFile := filepath.Join(violationDir, "violation.go")
	const violationSrc = `// DO NOT COMMIT: deliberate L2-no-extensions violation for depguard test.
// Created by cf-conventions/cf-convention/depguard_test.go; deleted on test exit.
package depguardl2violationprobe

import (
	// L2-no-extensions violation: L2 machinery importing L3 extensions.
	_ "github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/identity"
)
`
	if err := os.WriteFile(violationFile, []byte(violationSrc), 0600); err != nil {
		t.Fatalf("writing violation file: %v", err)
	}

	// Run golangci-lint only against the violation package.
	// We expect depguard to flag the import; the command should produce output
	// containing "depguard" or "L2-no-extensions".
	cmd := exec.Command(lintBin, "run", "--fast-only",
		"./cf-conventions/cf-convention/depguard-l2-violation-probe/...")
	cmd.Dir = repoRoot
	out, _ := cmd.CombinedOutput()
	outStr := string(out)

	if strings.Contains(outStr, "depguard") || strings.Contains(outStr, "L2-no-extensions") {
		t.Logf("PASS: depguard correctly rejected the L2→L3 cross-layer import:")
		t.Logf("  %s", strings.TrimSpace(outStr))
	} else {
		t.Errorf("FAIL: depguard did NOT catch the L2-no-extensions violation. lint output:\n%s", outStr)
	}
}

// findGolangciLint returns the path to golangci-lint, or "" if not found.
func findGolangciLint(t *testing.T) string {
	t.Helper()
	if path, err := exec.LookPath("golangci-lint"); err == nil {
		return path
	}
	home, err := os.UserHomeDir()
	if err == nil {
		candidate := filepath.Join(home, "bin", "golangci-lint")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// findRepoRoot walks up from the test file to find the go.mod root.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting cwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod from cwd")
		}
		dir = parent
	}
}

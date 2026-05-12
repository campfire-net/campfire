// Package protocol_test — depguard adversarial test (campfireagent-753, condition 3).
//
// This test proves two things:
//  1. The current cf-protocol/ tree has zero L1-narrow depguard violations.
//  2. An intentional cross-layer import (cf-protocol/ importing pkg/convention)
//     IS caught and rejected by the L1-narrow rule.
//
// This satisfies done-condition 3 (adversarial case):
//
//	"import a forbidden internal package from outside cf-protocol/ and prove
//	depguard catches it."
//
// Usage:
//
//	cd ~/projects/campfire && go test ./cf-protocol/protocol/ -run TestDepguard -v
//
// The test requires golangci-lint on PATH (or ~/bin/golangci-lint).
// When golangci-lint is not available, the test skips with a clear message.
package protocol_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDepguardL1NarrowClean verifies the current cf-protocol/ tree has
// zero L1-narrow depguard violations.
func TestDepguardL1NarrowClean(t *testing.T) {
	lintBin := findGolangciLint(t)
	if lintBin == "" {
		t.Skip("golangci-lint not found; skipping depguard verification")
	}

	repoRoot := findRepoRoot(t)
	cmd := exec.Command(lintBin, "run", "--fast-only", "./cf-protocol/...")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Only fail if the output mentions depguard — other linters don't apply here.
		if strings.Contains(string(out), "depguard") {
			t.Errorf("L1-narrow rule found violations in cf-protocol/:\n%s", out)
		}
		// Other lint errors (e.g. vet) are not our concern for this test.
	}
}

// TestDepguardL1NarrowCatchesForbiddenImport proves the L1-narrow depguard
// rule fires when cf-protocol/ code imports pkg/convention (L2).
//
// This is the adversarial condition from done-condition 3.
func TestDepguardL1NarrowCatchesForbiddenImport(t *testing.T) {
	lintBin := findGolangciLint(t)
	if lintBin == "" {
		t.Skip("golangci-lint not found; skipping depguard adversarial verification")
	}

	repoRoot := findRepoRoot(t)

	// Create a temporary sub-package inside cf-protocol/ that imports
	// pkg/convention — a deliberate L1-narrow violation.
	// Placed directly in cf-protocol/ (not under a _-prefixed directory)
	// so golangci-lint processes it.
	violationDir := filepath.Join(repoRoot, "cf-protocol", "depguard-violation-probe")
	if err := os.MkdirAll(violationDir, 0750); err != nil {
		t.Fatalf("creating violation dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(violationDir) })

	violationFile := filepath.Join(violationDir, "violation.go")
	const violationSrc = `// DO NOT COMMIT: deliberate L1-narrow violation for depguard test.
// Created by cf-protocol/protocol/depguard_test.go; deleted on test exit.
package depguardviolationprobe

import (
	// L1-narrow violation: cf-protocol/ importing L2 convention machinery.
	_ "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)
`
	if err := os.WriteFile(violationFile, []byte(violationSrc), 0600); err != nil {
		t.Fatalf("writing violation file: %v", err)
	}

	// Run golangci-lint only against the violation package.
	// We expect depguard to flag the import; the command should produce output
	// containing "depguard" or "L1-narrow".
	cmd := exec.Command(lintBin, "run", "--fast-only",
		"./cf-protocol/depguard-violation-probe/...")
	cmd.Dir = repoRoot
	out, _ := cmd.CombinedOutput()
	outStr := string(out)

	if strings.Contains(outStr, "depguard") || strings.Contains(outStr, "L1-narrow") {
		t.Logf("PASS: depguard correctly rejected the L1-narrow violation:")
		t.Logf("  %s", strings.TrimSpace(outStr))
	} else {
		t.Errorf("FAIL: depguard did NOT catch the L1-narrow violation. lint output:\n%s", outStr)
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

// TestDepguardL1ProtocolNoPkgProtocol proves the L1-protocol-no-pkgprotocol
// depguard rule fires when code under cf-protocol/protocol/ imports pkg/protocol.
//
// This satisfies campfireagent-9f4 done-condition 3: a probe verifies that
// pkg/* substrate imports are forbidden from cf-protocol/protocol/.
//
// Implementation note (campfireagent-db8 flake fix):
// The probe is written into a fresh sub-package under cf-protocol/protocol/,
// NOT directly into the cf-protocol/protocol package. Writing the probe as a
// top-level *.go file in cf-protocol/protocol/ caused transient build
// failures in any concurrent or follow-up `go test` invocation that imported
// cf-protocol/protocol (e.g. cf-conventions/cf-convention drives demo 28
// test/demo/28-reserved-op-floor-behavioral.sh — sweep run 5 captured the
// exact "cannot find module providing package
// github.com/campfire-net/campfire/pkg/protocol" failure). pkg/protocol no
// longer exists after the substrate strip (campfireagent-9f4), so the
// orphaned probe broke parent-package compiles until t.Cleanup ran.
// A sub-package is still covered by the depguard rule (files glob is
// **/cf-protocol/protocol/**/*.go) and is structurally isolated, so a
// cleanup race no longer poisons the parent package compile.
func TestDepguardL1ProtocolNoPkgProtocol(t *testing.T) {
	lintBin := findGolangciLint(t)
	if lintBin == "" {
		t.Skip("golangci-lint not found; skipping depguard adversarial verification")
	}

	repoRoot := findRepoRoot(t)

	// Create a temporary sub-package inside cf-protocol/protocol/ that imports
	// pkg/protocol — a deliberate L1-protocol-no-pkgprotocol violation.
	// Sub-package isolation (vs. a top-level *_test_only.go file in the protocol
	// package itself) prevents a cleanup race from breaking parent-package
	// compiles. The depguard files glob still matches this path.
	violationDir := filepath.Join(repoRoot, "cf-protocol", "protocol", "pkgprotocol-violation-probe")
	if err := os.MkdirAll(violationDir, 0750); err != nil {
		t.Fatalf("creating violation dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(violationDir) })

	violationFile := filepath.Join(violationDir, "violation.go")
	const violationSrc = `// DO NOT COMMIT: deliberate L1-protocol violation for depguard test.
// Created by cf-protocol/protocol/depguard_test.go; deleted on test exit.
package pkgprotocolviolationprobe

import (
	// L1-protocol violation: cf-protocol/protocol/ subtree importing old pkg/protocol substrate.
	_ "github.com/campfire-net/campfire/pkg/protocol"
)
`
	if err := os.WriteFile(violationFile, []byte(violationSrc), 0600); err != nil {
		t.Fatalf("writing violation file: %v", err)
	}

	// Run golangci-lint against just the violation sub-package.
	// We expect depguard to flag the import.
	cmd := exec.Command(lintBin, "run", "--fast-only",
		"./cf-protocol/protocol/pkgprotocol-violation-probe/...")
	cmd.Dir = repoRoot
	out, _ := cmd.CombinedOutput()
	outStr := string(out)

	if strings.Contains(outStr, "depguard") || strings.Contains(outStr, "L1-protocol") {
		t.Logf("PASS: depguard correctly rejected the L1-protocol pkg/protocol import:")
		t.Logf("  %s", strings.TrimSpace(outStr))
	} else {
		t.Errorf("FAIL: depguard did NOT catch the L1-protocol violation. lint output:\n%s", outStr)
	}
}

// TestDepguardL1InternalEncapsulation proves that the L1-internal-encapsulation
// depguard rule fires when code OUTSIDE cf-protocol/ tries to import
// cf-protocol/internal/ sub-packages directly (campfireagent-401d).
//
// This satisfies done-condition 4: "Extended depguard L1-narrow rule passes
// adversarial probe (external import of cf-protocol/internal/* fails)."
func TestDepguardL1InternalEncapsulation(t *testing.T) {
	lintBin := findGolangciLint(t)
	if lintBin == "" {
		t.Skip("golangci-lint not found; skipping depguard adversarial verification")
	}

	repoRoot := findRepoRoot(t)

	// Create a temporary package OUTSIDE cf-protocol/ that imports
	// cf-protocol/internal/store — a deliberate L1-internal-encapsulation violation.
	violationDir := filepath.Join(repoRoot, "depguard-internal-violation-probe")
	if err := os.MkdirAll(violationDir, 0750); err != nil {
		t.Fatalf("creating violation dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(violationDir) })

	violationFile := filepath.Join(violationDir, "violation.go")
	const violationSrc = `// DO NOT COMMIT: deliberate L1-internal-encapsulation violation for depguard test.
// Created by cf-protocol/protocol/depguard_test.go; deleted on test exit.
package depguardinternalviolationprobe

import (
	// L1-internal-encapsulation violation: external package importing cf-protocol/internal/.
	_ "github.com/campfire-net/campfire/cf-protocol/internal/store"
)
`
	if err := os.WriteFile(violationFile, []byte(violationSrc), 0600); err != nil {
		t.Fatalf("writing violation file: %v", err)
	}

	// Run golangci-lint against the violation package.
	// We expect depguard to flag the import with L1-internal or L1-internal-encapsulation.
	cmd := exec.Command(lintBin, "run", "--fast-only",
		"./depguard-internal-violation-probe/...")
	cmd.Dir = repoRoot
	out, _ := cmd.CombinedOutput()
	outStr := string(out)

	if strings.Contains(outStr, "depguard") || strings.Contains(outStr, "L1-internal") {
		t.Logf("PASS: depguard correctly rejected the L1-internal-encapsulation violation:")
		t.Logf("  %s", strings.TrimSpace(outStr))
	} else {
		t.Errorf("FAIL: depguard did NOT catch the L1-internal-encapsulation violation. lint output:\n%s", outStr)
	}
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

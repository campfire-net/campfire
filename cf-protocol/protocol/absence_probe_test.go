// absence_probe_test.go — TDD probe: asserts that center-finding symbols
// (recenter.go + walk_up.go) have been removed from cf-protocol/protocol.
//
// This test is the done-condition verifier for campfireagent-db1.
// It fails on HEAD (symbols present) and passes after deletion.
//
// Strategy: grep the package source for the removed symbols and fail
// if any are found. This is a source-level probe, not a runtime test.
package protocol_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCenterFindingSymbolsAbsent verifies that the center-finding symbols
// removed in campfireagent-db1 are no longer present in cf-protocol/protocol/.
//
// Symbols that must NOT exist:
//   - RecenterClaim (exported type in recenter.go)
//   - RecenterCanonicalPayload (exported func in recenter.go)
//   - maybeRecenter (unexported method in recenter.go)
//   - walkUpForCenter (unexported func in walk_up.go)
//   - WalkUpEnabled (exported method in authorize.go — removed with walk_up feature)
//   - WithWalkUp (exported func in options.go — removed with walk_up feature)
//   - WithNoWalkUp (exported func in options.go — removed with walk_up feature)
func TestCenterFindingSymbolsAbsent(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkgDir := filepath.Join(repoRoot, "cf-protocol", "protocol")

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}

	// Banned symbols: definition sites only (func/type/method declarations).
	// We check for definition-style patterns to avoid flagging comments that
	// reference the old names in removal notes.
	banned := []struct {
		pattern string
		desc    string
	}{
		{"func RecenterCanonicalPayload(", "RecenterCanonicalPayload function (recenter.go)"},
		{"type RecenterClaim ", "RecenterClaim type (recenter.go)"},
		{"func (c *Client) maybeRecenter(", "maybeRecenter method (recenter.go)"},
		{"func walkUpForCenter(", "walkUpForCenter function (walk_up.go)"},
		{"func (c *Client) WalkUpEnabled()", "WalkUpEnabled method"},
		{"func WithWalkUp()", "WithWalkUp option"},
		{"func WithNoWalkUp()", "WithNoWalkUp option"},
	}

	var violations []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		// Skip test files (we're looking for production code)
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		// Skip this file itself
		if entry.Name() == "absence_probe_test.go" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(pkgDir, entry.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}
		content := string(data)

		for _, b := range banned {
			if strings.Contains(content, b.pattern) {
				violations = append(violations, entry.Name()+": found "+b.desc)
			}
		}
	}

	if len(violations) > 0 {
		t.Errorf("center-finding symbols still present (campfireagent-db1 not complete):\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestRecenterWalkUpFilesAbsent verifies the deleted source files are gone.
func TestRecenterWalkUpFilesAbsent(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkgDir := filepath.Join(repoRoot, "cf-protocol", "protocol")

	deletedFiles := []string{"recenter.go", "walk_up.go"}
	for _, f := range deletedFiles {
		path := filepath.Join(pkgDir, f)
		if _, err := os.Stat(path); err == nil {
			t.Errorf("file should be deleted but still exists: %s", path)
		}
	}
}

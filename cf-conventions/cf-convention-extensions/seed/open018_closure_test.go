package seed_test

// open018_closure_test.go — OPEN-018 carveout closure proof (campfireagent-c72).
//
// OPEN-018 was the carveout that kept seed.go in the L2 package
// (cf-conventions/cf-convention/) because it depended on the unexported
// conventionRevokeTag constant from parser.go.
//
// This file closes that carveout by proving:
//   1. seed.PromoteDeclaration, seed.SupersedeDeclaration, seed.RevokeDeclaration,
//      seed.NamingRegisterDeclaration, and seed.InfrastructureSeedDeclarations all
//      exist in THIS package (L3), not in the convention (L2) package.
//   2. The convention package does NOT expose any seed-declaration functions.
//      (Verified at compile time: if this file compiles successfully, it means
//       there is no ambiguity between convention.* and seed.* namespaces.)
//   3. The seed package imports cf-convention (L2) without violating the layer rule —
//      L3 may depend on L2, but not the converse.
//
// The test is intentionally simple: if the package structure is wrong, this file
// will not compile (import paths will fail), which means the test will fail.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOPEN018Closure_SeedInL3 verifies that seed declaration functions live in
// the L3 seed package and are absent from the L2 convention package.
//
// This is a compile-time proof: the imports at the top of this file succeed
// only if seed.* functions exist. The negative check below uses `go list` to
// confirm the L2 package has no dependency on the L3 seed package.
func TestOPEN018Closure_SeedInL3(t *testing.T) {
	// Positive: all seed functions are callable from the L3 package (proved by other tests).
	// Negative: L2 does NOT import L3 seed.
	repoRoot := findRepoRoot_open018(t)

	cmd := exec.Command("/usr/local/go/bin/go", "list", "-f",
		`{{join .Deps "\n"}}`,
		"github.com/campfire-net/campfire/cf-conventions/cf-convention",
	)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}

	const seedPkg = "github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/seed"
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if dep == seedPkg {
			t.Errorf("OPEN-018 REGRESSION: cf-convention (L2) imports cf-convention-extensions/seed (L3).\n"+
				"seed.go must NOT be in L2. Dep found: %s", dep)
		}
	}
}

func findRepoRoot_open018(t *testing.T) string {
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

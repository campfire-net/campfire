package primcmd_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/campfire-net/campfire/cmd/cf-primitives/internal/primcmd"
)

// TestPrimitivesSurfaceCeiling asserts that the exposed primitive command set
// is exactly the frozen list defined in ExposedCommands.
//
// This is the surface ceiling for cf-primitives (design v2 §4.4, §5.3).
// Any change to ExposedCommands — addition, removal, or rename — requires a
// deliberate code change here AND Pass-1-style adversary review before merging.
//
// IF THIS TEST FAILS: do not modify it to pass. Instead:
//  1. Identify which command was added, removed, or renamed.
//  2. Open a design review item (Pass-1 adversary review required).
//  3. Update ExposedCommands only after the review approves the change.
func TestPrimitivesSurfaceCeiling(t *testing.T) {
	// The frozen command set — must match ExposedCommands exactly.
	// Sorted lexicographically for deterministic comparison.
	frozen := []string{
		"admit",
		"await",
		"create",
		"disband",
		"evict",
		"init",
		"join",
		"leave",
		"members",
		"read",
		"send",
		"subscribe",
	}

	got := make([]string, len(primcmd.ExposedCommands))
	copy(got, primcmd.ExposedCommands)
	slices.Sort(got)

	sortedFrozen := make([]string, len(frozen))
	copy(sortedFrozen, frozen)
	slices.Sort(sortedFrozen)

	if !slices.Equal(got, sortedFrozen) {
		var diff bytes.Buffer
		diff.WriteString("\nSURFACE CEILING VIOLATION: command set has changed without adversary review.\n")
		diff.WriteString("\nFrozen set:\n")
		for _, c := range sortedFrozen {
			diff.WriteString("  " + c + "\n")
		}
		diff.WriteString("\nActual set:\n")
		for _, c := range got {
			diff.WriteString("  " + c + "\n")
		}
		diff.WriteString("\nDo NOT modify this test to pass. Open a Pass-1 adversary review first.\n")
		t.Fatal(diff.String())
	}
}

// TestPrimitivesSurfaceCeilingCount verifies the count matches the frozen list
// as a secondary guard against off-by-one errors in ExposedCommands maintenance.
func TestPrimitivesSurfaceCeilingCount(t *testing.T) {
	const frozenCount = 12
	if got := len(primcmd.ExposedCommands); got != frozenCount {
		t.Errorf("surface ceiling count: got %d commands, want %d\n"+
			"Every addition or removal requires Pass-1 adversary review.", got, frozenCount)
	}
}

package reservedops_test

import (
	"testing"

	reservedops "github.com/campfire-net/campfire/cf-protocol/internal/reserved-ops"
)

// TestReservedOpCount verifies the list has exactly ten ops per design v2 §2.4.
func TestReservedOpCount(t *testing.T) {
	const want = 10
	if got := len(reservedops.ReservedOps); got != want {
		t.Errorf("ReservedOps has %d entries, want %d", got, want)
	}
}

// TestIsReservedKnown verifies each documented op is recognized.
func TestIsReservedKnown(t *testing.T) {
	known := []string{
		"disband", "evict", "admit", "grant", "revoke",
		"delegation-grant", "delegation-revoke", "delegation-accept",
		"member-roster", "compaction",
	}
	for _, op := range known {
		if !reservedops.IsReserved(op) {
			t.Errorf("IsReserved(%q) = false, want true", op)
		}
	}
}

// TestIsReservedUnknown verifies non-reserved ops are not recognized.
func TestIsReservedUnknown(t *testing.T) {
	nonReserved := []string{"claim", "send", "read", "my-convention:op", ""}
	for _, op := range nonReserved {
		if reservedops.IsReserved(op) {
			t.Errorf("IsReserved(%q) = true, want false", op)
		}
	}
}

// TestReservedOpsSorted verifies the list is sorted (for documentation purposes).
func TestReservedOpsSorted(t *testing.T) {
	for i := 1; i < len(reservedops.ReservedOps); i++ {
		if reservedops.ReservedOps[i] <= reservedops.ReservedOps[i-1] {
			t.Errorf("ReservedOps not sorted: %q >= %q at index %d",
				reservedops.ReservedOps[i-1], reservedops.ReservedOps[i], i)
		}
	}
}

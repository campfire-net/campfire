//go:build !windows

package cmd

import (
	"syscall"
	"testing"
)

// TestIsRootOwned_MockStat tests the ownership check logic with synthetic stat results.
func TestIsRootOwned_MockStat(t *testing.T) {
	// Simulate root ownership (UID 0)
	rootStat := &syscall.Stat_t{Uid: 0}
	if !isOwnedByRoot(rootStat) {
		t.Error("expected isOwnedByRoot to return true for UID 0")
	}

	// Simulate non-root ownership (UID 1000)
	userStat := &syscall.Stat_t{Uid: 1000}
	if isOwnedByRoot(userStat) {
		t.Error("expected isOwnedByRoot to return false for UID 1000")
	}
}

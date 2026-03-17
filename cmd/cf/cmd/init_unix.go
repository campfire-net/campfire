//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// isOwnedByRoot returns true if the given syscall.Stat_t indicates UID 0.
func isOwnedByRoot(st *syscall.Stat_t) bool {
	return st.Uid == 0
}

// checkCampfireDirOwnership checks if ~/.campfire/ exists and is root-owned.
// Returns an error with an actionable message if so.
func checkCampfireDirOwnership() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".campfire")
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		return nil
	}
	if isOwnedByRoot(&st) {
		return fmt.Errorf("error: ~/.campfire/ is owned by root (likely from a prior Docker run)\nfix:   sudo chown -R $(whoami) ~/.campfire/")
	}
	return nil
}

//go:build !windows

package fs

import (
	"fmt"
	"os"
	"syscall"
)

// acquireMigrateLockExclusive opens (or creates) the migration lockfile at path
// and acquires an exclusive (LOCK_EX) flock on it. Only one holder may hold
// LOCK_EX at a time; all concurrent LOCK_SH holders (WriteMessage) must release
// before this call returns.
//
// The caller MUST call the returned release function when done (typically
// deferred immediately after the non-nil error check).
//
// This is the exclusive counterpart to acquireMigrateLockShared used by
// migrate-store. The lockfile is a zero-byte file; only the flock state matters.
func acquireMigrateLockExclusive(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0700) //nolint:gosec // 0700 matches campfire dir perms
	if err != nil {
		return nil, fmt.Errorf("opening migrate lock %s: %w", path, err)
	}

	// LOCK_EX | blocking: wait until all LOCK_SH holders (active WriteMessage calls)
	// have released their locks, then take exclusive ownership.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquiring LOCK_EX on %s: %w", path, err)
	}

	release = func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return release, nil
}

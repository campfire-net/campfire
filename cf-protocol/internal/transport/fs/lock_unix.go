//go:build !windows

package fs

import (
	"fmt"
	"os"
	"syscall"
)

// acquireMigrateLockShared opens (or creates) the migration lockfile at path and
// acquires a shared (LOCK_SH) flock on it. Multiple callers may hold LOCK_SH
// concurrently; only an exclusive LOCK_EX (held by migrate-store) blocks them.
//
// The caller MUST call the returned release function when done (typically
// deferred immediately after the non-nil error check).
//
// Windows fallback: not used on this build tag; lock_windows.go covers that.
func acquireMigrateLockShared(path string) (release func(), err error) {
	// Create the lockfile if it doesn't exist. O_CREATE|O_APPEND: zero-byte file.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0700) //nolint:gosec // 0700 matches campfire dir perms
	if err != nil {
		return nil, fmt.Errorf("opening migrate lock %s: %w", path, err)
	}

	// LOCK_SH | LOCK_NB would fail fast; we want blocking behaviour so that
	// writers wait for any in-progress migration to finish.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquiring LOCK_SH on %s: %w", path, err)
	}

	release = func() {
		// Unlock then close. Errors are non-fatal — we've already written the data.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return release, nil
}

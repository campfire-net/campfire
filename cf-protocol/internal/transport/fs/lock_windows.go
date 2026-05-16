//go:build windows

package fs

// acquireMigrateLockShared on Windows is a no-op best-effort stub.
//
// Windows does not support flock(2). The migration lockfile discipline is
// load-bearing on POSIX (Linux/macOS) — the primary deployment targets.
// On Windows, migration races are an acceptable degraded mode. A full
// O_EXCL / spin-lock approach is deferred until a Windows deployment need is
// identified. For now, writes proceed without locking.
func acquireMigrateLockShared(path string) (release func(), err error) {
	// Windows: no-op. Document the limitation but do not fail writes.
	return func() {}, nil
}

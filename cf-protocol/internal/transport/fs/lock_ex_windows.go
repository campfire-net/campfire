//go:build windows

package fs

// acquireMigrateLockExclusive on Windows is a no-op best-effort stub.
// Windows does not support flock(2). Migration on Windows is an acceptable
// degraded mode (same policy as acquireMigrateLockShared on Windows).
func acquireMigrateLockExclusive(path string) (release func(), err error) {
	return func() {}, nil
}

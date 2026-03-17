//go:build windows

package main

// tryFlock is a no-op on Windows. File locking for session
// identity exclusion is not supported on Windows.
func tryFlock(fd uintptr) error {
	return nil
}

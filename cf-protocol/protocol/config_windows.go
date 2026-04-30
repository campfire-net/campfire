//go:build windows

package protocol

// defaultOwnerTrusted on Windows always returns true — ownership check is skipped.
func defaultOwnerTrusted(_ string) bool {
	return true
}

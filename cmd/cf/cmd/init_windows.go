//go:build windows

package cmd

// checkCampfireDirOwnership is a no-op on Windows.
// The root-ownership problem only occurs with Docker on Unix.
func checkCampfireDirOwnership() error {
	return nil
}

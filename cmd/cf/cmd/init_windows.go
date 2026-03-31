//go:build windows

package cmd

// checkCampfireDirOwnership is a no-op on Windows.
// The root-ownership problem only occurs with Docker on Unix.
func checkCampfireDirOwnership() error {
	return nil
}

// promptPassphrase is not supported on Windows (no terminal passphrase prompt).
// Returns nil — caller falls back to plaintext identity save.
func promptPassphrase() ([]byte, error) {
	return nil, nil
}

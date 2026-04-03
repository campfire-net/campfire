//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/term"
)

// isOwnedByRoot returns true if the given syscall.Stat_t indicates UID 0.
func isOwnedByRoot(st *syscall.Stat_t) bool {
	return st.Uid == 0
}

// promptPassphrase reads a passphrase from the terminal without echoing it.
// Returns nil if stdin is not a terminal (non-interactive mode) — caller
// should treat nil as "no passphrase" and fall back to plaintext save.
func promptPassphrase() ([]byte, error) {
	if !term.IsTerminal(int(syscall.Stdin)) {
		return nil, nil
	}
	fmt.Fprint(os.Stderr, "Enter passphrase (leave empty to skip): ")
	passphrase, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("reading passphrase: %w", err)
	}
	if len(passphrase) == 0 {
		return nil, nil
	}
	fmt.Fprint(os.Stderr, "Confirm passphrase: ")
	confirm, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("reading passphrase confirmation: %w", err)
	}
	if string(passphrase) != string(confirm) {
		return nil, fmt.Errorf("passphrases do not match")
	}
	return passphrase, nil
}

// passphraseSupported reports whether the current platform can prompt for a
// passphrase at identity creation time. Always true on Unix.
func passphraseSupported() bool {
	return true
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

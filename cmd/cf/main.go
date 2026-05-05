package main

import (
	"os"
	"path/filepath"

	"github.com/campfire-net/campfire/cmd/cf/cmd"
)

// dispatch is the core argv[0] dispatch logic. It resolves the binary name,
// determines whether to run in multicall mode or standard mode, and returns
// an exit code (0 = success, 1 = error). It is extracted from main() so that
// tests can exercise all dispatch branches without forking a subprocess.
func dispatch(binaryName string, args []string) int {
	// argv[0] dispatch: when the binary is invoked under a name other than "cf"
	// or "cf-primitives" (e.g. via a symlink "social" → cf), use Multicall mode.
	// Multicall treats the binary name as the campfire namespace prefix and
	// dispatches convention operations against it.
	//
	// Security: SanitizeBinaryName strips any characters that are not safe for
	// use as a campfire name, preventing argument injection via crafted binary names.
	if cmd.IsMulticallInvocation(binaryName) {
		safeName := cmd.SanitizeBinaryName(binaryName)
		if safeName == "" {
			// Binary name is unsafe (e.g. contains shell metacharacters).
			// Fall back to standard cf execution.
			if err := cmd.Execute(); err != nil {
				return 1
			}
			return 0
		}
		if err := cmd.Multicall(safeName, args); err != nil {
			return 1
		}
		return 0
	}
	if err := cmd.Execute(); err != nil {
		return 1
	}
	return 0
}

func main() {
	binaryName := filepath.Base(os.Args[0])
	os.Exit(dispatch(binaryName, os.Args[1:]))
}

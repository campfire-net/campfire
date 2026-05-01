package main

import (
	"os"
	"path/filepath"

	"github.com/campfire-net/campfire/cmd/cf/cmd"
)

func main() {
	// argv[0] dispatch: when the binary is invoked under a name other than "cf"
	// or "cf-primitives" (e.g. via a symlink "social" → cf), use Multicall mode.
	// Multicall treats the binary name as the campfire namespace prefix and
	// dispatches convention operations against it.
	//
	// Security: SanitizeBinaryName strips any characters that are not safe for
	// use as a campfire name, preventing argument injection via crafted binary names.
	binaryName := filepath.Base(os.Args[0])
	if cmd.IsMulticallInvocation(binaryName) {
		safeName := cmd.SanitizeBinaryName(binaryName)
		if safeName == "" {
			// Binary name is unsafe (e.g. contains shell metacharacters).
			// Fall back to standard cf execution.
			if err := cmd.Execute(); err != nil {
				os.Exit(1)
			}
			return
		}
		if err := cmd.Multicall(safeName, os.Args[1:]); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

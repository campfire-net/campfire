package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Multicall dispatches a convention operation using the binary name as the
// campfire identifier. When cf is invoked via a symlink (e.g. "dontguess"),
// the symlink name routes to the corresponding campfire and convention:
//
//	ln -s cf dontguess
//	dontguess buy --task "find me a linter"
//	  → resolves "dontguess" to a campfire ID via alias/naming
//	  → dispatches the "buy" convention operation with --task flag
//
// With no operation, lists available convention operations (help).
func Multicall(name string, args []string) error {
	// Handle --version anywhere.
	for _, a := range args {
		if a == "--version" {
			fmt.Printf("%s (cf multicall) %s\n", name, Version)
			return nil
		}
	}

	// Apply global flags that dispatchConventionOp expects.
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
		}
	}

	// Parse operation and flags from args.
	operationName := ""
	var flagArgs []string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		operationName = args[0]
		flagArgs = args[1:]
	} else {
		// No operation or only flags — check for bare --help/-h.
		for _, a := range args {
			if a == "--help" || a == "-h" {
				operationName = "help"
				flagArgs = nil
				break
			}
		}
	}

	if operationName == "" {
		operationName = "help"
	}

	ctx := context.Background()
	if err := dispatchConventionOp(ctx, name, operationName, flagArgs); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return err
	}
	return nil
}

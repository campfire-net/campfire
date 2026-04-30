package cmd

import (
	"fmt"
	"os"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// printInitResultVerbose writes InitResult diagnostic output to stderr when
// CF_VERBOSE=1. Call after any protocol.Init() invocation in a CLI command.
func printInitResultVerbose(result *protocol.InitResult) {
	if os.Getenv("CF_VERBOSE") != "1" {
		return
	}
	if result == nil {
		return
	}
	action := "loaded"
	if result.IdentityCreated {
		action = "created"
	}
	fmt.Fprintf(os.Stderr, "Init: %s identity at %s\n", action, result.IdentityPath)
	fmt.Fprintf(os.Stderr, "Init: opened store at %s\n", result.StorePath)
	if len(result.WalkUpPath) > 0 {
		fmt.Fprintf(os.Stderr, "Init: walk-up examined %d directories\n", len(result.WalkUpPath))
	} else {
		fmt.Fprintf(os.Stderr, "Init: walk-up disabled (default)\n")
	}
	if result.DelegationIssued {
		fmt.Fprintf(os.Stderr, "Init: delegation issued\n")
	}
	if result.Recentered {
		fmt.Fprintf(os.Stderr, "Init: recentered\n")
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Init: warning: %s\n", w)
	}
}

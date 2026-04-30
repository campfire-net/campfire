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
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Init: warning: %s\n", w)
	}
}

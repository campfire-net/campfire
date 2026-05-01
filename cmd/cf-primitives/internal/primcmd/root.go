// Package primcmd implements the cf-primitives CLI.
//
// Surface ceiling: ONLY cf-protocol public API methods are exposed.
// Any addition to ExposedCommands requires Pass-1-style adversary review.
package primcmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// ExposedCommands is the frozen set of primitive commands.
// This list is the surface ceiling — it is verified by TestPrimitivesSurfaceCeiling.
// Adding a command here without adversary review is a protocol violation.
var ExposedCommands = []string{
	"admit",
	"await",
	"create",
	"disband",
	"evict",
	"init",
	"join",
	"leave",
	"members",
	"read",
	"send",
	"subscribe",
}

var (
	jsonOutput bool
	cfHome     string
)

var rootCmd = &cobra.Command{
	Use:   "cf-primitives",
	Short: "Low-level campfire protocol primitives (sanctioned escape hatch)",
	Long: `cf-primitives — low-level campfire protocol primitives.

This binary exposes only the cf-protocol public API: Init, Create, Join, Leave,
Disband, Admit, Evict, Members, Send, Read, Subscribe, Await.

SURFACE CEILING: Any addition to the exposed command set requires
Pass-1-style adversary review (design v2 §4.4, §5.3).

Convention-layer features and higher-level workflows belong in cf, not here.`,
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	rootCmd.PersistentFlags().StringVar(&cfHome, "cf-home", "", "path to campfire home directory (default: ~/.cf)")

	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if cfHome != "" && os.Getenv("CF_HOME") == "" {
			os.Setenv("CF_HOME", cfHome) //nolint:errcheck
		}
	}

	rootCmd.AddCommand(
		newInitCmd(),
		newCreateCmd(),
		newJoinCmd(),
		newLeaveCmd(),
		newDisbandCmd(),
		newAdmitCmd(),
		newEvictCmd(),
		newMembersCmd(),
		newSendCmd(),
		newReadCmd(),
		newSubscribeCmd(),
		newAwaitCmd(),
	)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

// cfHomeDir returns the resolved campfire home directory.
func cfHomeDir() string {
	if cfHome != "" {
		return cfHome
	}
	if env := os.Getenv("CF_HOME"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return filepath.Join(home, ".cf")
}

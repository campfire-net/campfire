package cmd

import (
	"github.com/spf13/cobra"
)

var swarmCmd = &cobra.Command{
	Use:   "swarm",
	Short: "Agent swarm coordination (experimental)",
	Long: `Agent swarm coordination utilities.

Experimental features for coordinating multi-agent sessions.`,
}

func init() {
	rootCmd.AddCommand(swarmCmd)
}

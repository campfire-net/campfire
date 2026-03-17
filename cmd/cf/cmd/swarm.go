package cmd

import (
	"github.com/spf13/cobra"
)

var swarmCmd = &cobra.Command{
	Use:   "swarm",
	Short: "Manage project swarm campfire",
	Long: `Swarm campfire — a root campfire anchored to a project directory.

  cf swarm start    create a root campfire for this project
  cf swarm end      remove the root campfire anchor
  cf swarm status   show swarm member activity
  cf swarm prompt   emit bootstrap prompt template`,
}

func init() {
	rootCmd.AddCommand(swarmCmd)
}

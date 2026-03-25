package cmd

import (
	"github.com/spf13/cobra"
)

var conventionCmd = &cobra.Command{
	Use:   "convention",
	Short: "Convention development lifecycle: lint, test, promote",
	Long: `Convention development lifecycle tools.

  cf convention lint <file|->       validate a declaration payload
  cf convention test <file|dir>     spin up a local digital twin and test declarations
  cf convention promote <file|dir>  publish declarations to a live convention registry`,
}

func init() {
	rootCmd.AddCommand(conventionCmd)
}

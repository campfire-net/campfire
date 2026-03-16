package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/spf13/cobra"
)

var idCmd = &cobra.Command{
	Use:   "id",
	Short: "Display this agent's public key",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := IdentityPath()

		id, err := identity.Load(path)
		if err != nil {
			return fmt.Errorf("loading identity (run 'cf init' first): %w", err)
		}

		if jsonOutput {
			out := map[string]string{"public_key": id.PublicKeyHex()}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Println(id.PublicKeyHex())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(idCmd)
}

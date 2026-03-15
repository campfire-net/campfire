package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/3dl-dev/campfire/pkg/identity"
	"github.com/spf13/cobra"
)

var forceInit bool

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a new agent identity (Ed25519 keypair)",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := IdentityPath()

		if identity.Exists(path) && !forceInit {
			fmt.Fprintf(os.Stderr, "Identity already exists at %s\nUse --force to overwrite.\n", path)
			return fmt.Errorf("identity already exists")
		}

		id, err := identity.Generate()
		if err != nil {
			return fmt.Errorf("generating identity: %w", err)
		}

		if err := id.Save(path); err != nil {
			return fmt.Errorf("saving identity: %w", err)
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
	initCmd.Flags().BoolVar(&forceInit, "force", false, "overwrite existing identity")
	rootCmd.AddCommand(initCmd)
}

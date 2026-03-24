package cmd

import (
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/spf13/cobra"
)

var identityCmd = &cobra.Command{
	Use:   "identity",
	Short: "Manage agent identity",
}

var identityWrapCmd = &cobra.Command{
	Use:   "wrap",
	Short: "Wrap an existing identity's private key with a session token (writes v2 format)",
	Long: `Encrypt the private key in identity.json using a KEK derived from CF_SESSION_TOKEN.

The identity file is rewritten in v2 (wrapped) format. After wrapping, the file
no longer contains the raw private key — the key can only be recovered by
providing the correct session token via CF_SESSION_TOKEN (or --token).

Example:
  CF_SESSION_TOKEN=my-secret cf identity wrap
  CF_SESSION_TOKEN=my-secret cf id   # still works after wrapping`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := IdentityPath()

		// Resolve the session token: flag takes precedence, then env var.
		tokenStr, _ := cmd.Flags().GetString("token")
		if tokenStr == "" {
			tokenStr = os.Getenv("CF_SESSION_TOKEN")
		}
		if tokenStr == "" {
			return fmt.Errorf("session token required: set CF_SESSION_TOKEN or use --token")
		}
		token := []byte(tokenStr)

		// Load the existing identity (plain or already-wrapped via token).
		id, err := identity.LoadWithToken(path, token)
		if err != nil {
			// If the first load fails because the file is already wrapped with
			// a different token, surface a clear error. Otherwise try plain load.
			id, err = identity.Load(path)
			if err != nil {
				return fmt.Errorf("loading identity (run 'cf init' first): %w", err)
			}
		}

		if err := id.SaveWrapped(path, token); err != nil {
			return fmt.Errorf("wrapping identity: %w", err)
		}

		if jsonOutput {
			fmt.Printf(`{"status":"wrapped","public_key":%q}%s`, id.PublicKeyHex(), "\n")
			return nil
		}
		fmt.Printf("Identity wrapped (v2): %s\n", id.PublicKeyHex())
		fmt.Printf("Location: %s\n", path)
		return nil
	},
}

func init() {
	identityWrapCmd.Flags().String("token", "", "session token (overrides CF_SESSION_TOKEN)")
	identityCmd.AddCommand(identityWrapCmd)
	rootCmd.AddCommand(identityCmd)
}

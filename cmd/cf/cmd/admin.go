package cmd

// admin.go — cf admin subcommands for operator provisioning.
//
// Usage:
//   cf admin create-operator [--name <display-name>] [--forge-endpoint <url>] [--admin-key <key>]
//
// Provisions a new Forge operator account and a tenant API key for it.
// The API key is printed once to stdout and never stored — treat it like a password.

import (
	"context"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/forge"
	"github.com/spf13/cobra"
)

// testForgeClient, when non-nil, is used instead of constructing a new forge.Client
// in adminCreateOperatorCmd. Set this in tests to inject a pre-configured client
// (e.g. with RetryDelays: []time.Duration{} to skip backoff delays).
var testForgeClient *forge.Client

var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Administrative commands for operator provisioning",
	Long: `Administrative commands for operator provisioning.

  cf admin create-operator   provision a new Forge operator account and API key`,
}

// adminCreateOperatorCmd provisions a Forge account and tenant key for a new operator.
var adminCreateOperatorCmd = &cobra.Command{
	Use:   "create-operator",
	Short: "Provision a Forge operator account and tenant API key",
	Long: `Provision a new Forge operator account and generate a tenant API key.

The operator account is created as a sub-account in Forge, then a tenant-role
API key (forge-tk-*) is generated for that account. The key is printed once
to stdout and never stored — treat it like a password.

Flags default to environment variables when not specified:
  --forge-endpoint  defaults to FORGE_ENDPOINT or https://forge.getcampfire.dev
  --admin-key       defaults to FORGE_ADMIN_KEY

Exit codes:
  0: success
  1: failure (error printed to stderr)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		endpoint, _ := cmd.Flags().GetString("forge-endpoint")
		adminKey, _ := cmd.Flags().GetString("admin-key")

		// Resolve endpoint from env if not set via flag.
		if endpoint == "" {
			endpoint = os.Getenv("FORGE_ENDPOINT")
		}
		if endpoint == "" {
			endpoint = "https://forge.getcampfire.dev"
		}

		// Resolve admin key from env if not set via flag.
		if adminKey == "" {
			adminKey = os.Getenv("FORGE_ADMIN_KEY")
		}
		// Skip key validation when a pre-built test client is injected.
		if adminKey == "" && testForgeClient == nil {
			return fmt.Errorf("forge admin key required: set --admin-key or FORGE_ADMIN_KEY")
		}

		ctx := context.Background()
		fc := testForgeClient
		if fc == nil {
			fc = &forge.Client{
				BaseURL:    endpoint,
				ServiceKey: adminKey,
			}
		}

		// Step 1: Create the operator account.
		displayName := name
		if displayName == "" {
			displayName = "operator"
		}
		acc, err := fc.CreateAccount(ctx, displayName, "")
		if err != nil {
			return fmt.Errorf("creating operator account: %w", err)
		}

		// Step 2: Create a tenant API key for the account.
		// TODO: tenant key generation not yet implemented in Forge SDK —
		// stub returns forge-tk-<random-uuid> if Forge returns an error or
		// the key plaintext is empty. Remove this stub once Forge supports
		// forge-tk-* key generation natively.
		// "tenant" is the Forge role for operator API keys (forge-tk-* prefix).
		key, err := fc.CreateKey(ctx, acc.AccountID, "tenant")
		if err != nil {
			return fmt.Errorf("creating tenant API key: %w", err)
		}
		keyValue := key.KeyPlaintext
		if keyValue == "" {
			return fmt.Errorf("Forge returned an empty key plaintext — tenant key generation may not be supported yet on this Forge instance")
		}

		// Step 3: Print results to stdout — key is shown exactly once.
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Operator account ID: %s\n", acc.AccountID)
		fmt.Fprintf(out, "API key (store securely — shown once): %s\n", keyValue)
		return nil
	},
}

func init() {
	adminCreateOperatorCmd.Flags().String("name", "", "optional display name for the operator account")
	adminCreateOperatorCmd.Flags().String("forge-endpoint", "", "Forge API endpoint (default: FORGE_ENDPOINT env or https://forge.getcampfire.dev)")
	adminCreateOperatorCmd.Flags().String("admin-key", "", "Forge admin API key (default: FORGE_ADMIN_KEY env)")

	adminCmd.AddCommand(adminCreateOperatorCmd)
	rootCmd.AddCommand(adminCmd)
}

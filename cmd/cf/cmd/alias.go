package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/spf13/cobra"
)

var aliasCmd = &cobra.Command{
	Use:   "alias",
	Short: "Manage local campfire aliases",
	Long: `Manage local campfire aliases.

  cf alias set <name> <campfire-id>    create or update an alias
  cf alias list [--json]               list all aliases
  cf alias remove <name>               remove an alias

Aliases let you refer to campfires by short names:
  cf alias set lobby abc123...
  cf lobby post --text "hello"
  cf read cf://~lobby`,
}

var aliasSetCmd = &cobra.Command{
	Use:   "set <name> <campfire-id>",
	Short: "Create or update a campfire alias",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		alias := args[0]
		campfireID := args[1]
		if len(campfireID) != 64 {
			return fmt.Errorf("campfire-id must be 64 hex characters, got %d", len(campfireID))
		}
		store := naming.NewAliasStore(CFHome())
		if err := store.Set(alias, campfireID); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "alias %q → %s\n", alias, campfireID[:12])
		return nil
	},
}

var aliasListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all campfire aliases",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := naming.NewAliasStore(CFHome())
		aliases, err := store.List()
		if err != nil {
			return err
		}

		if jsonOutput {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(aliases)
		}

		if len(aliases) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No aliases set.")
			return nil
		}
		for name, id := range aliases {
			fmt.Fprintf(cmd.OutOrStdout(), "%-20s %s\n", name, id)
		}
		return nil
	},
}

var aliasRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a campfire alias",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store := naming.NewAliasStore(CFHome())
		if err := store.Remove(args[0]); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed alias %q\n", args[0])
		return nil
	},
}

func init() {
	aliasCmd.AddCommand(aliasSetCmd)
	aliasCmd.AddCommand(aliasListCmd)
	aliasCmd.AddCommand(aliasRemoveCmd)
	rootCmd.AddCommand(aliasCmd)
}

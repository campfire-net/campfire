package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/spf13/cobra"
)

var nameCmd = &cobra.Command{
	Use:   "name",
	Short: "Manage name registrations in root campfires",
	Long: `Manage name registrations in root campfires.

Names are registered as tagged messages in a root campfire (the nameserver).
Resolution is direct-read — no server process required.

  cf name register galtrader <id>   register a name in your operator root
  cf name unregister galtrader      remove a registration
  cf name list                      list all registered names
  cf name lookup galtrader          show resolution path for a name`,
}

var nameRegisterCmd = &cobra.Command{
	Use:   "register <name> <campfire-id>",
	Short: "Register a name in a root campfire",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		campfireID := args[1]

		rootID, err := nameResolveRoot(cmd)
		if err != nil {
			return err
		}

		ttl, _ := cmd.Flags().GetInt("ttl")

		client, err := protocol.Init(CFHome())
		if err != nil {
			return fmt.Errorf("initializing client: %w", err)
		}
		defer client.Close()

		var opts *naming.RegisterOptions
		if ttl > 0 {
			opts = &naming.RegisterOptions{TTL: ttl}
		}

		msg, err := naming.Register(context.Background(), client, rootID, name, campfireID, opts)
		if err != nil {
			return fmt.Errorf("registering name: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "registered: %s → %s (msg %s)\n", name, campfireID[:shortIDLen], msg.ID[:shortIDLen])
		return nil
	},
}

var nameUnregisterCmd = &cobra.Command{
	Use:   "unregister <name>",
	Short: "Remove a name registration from a root campfire",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		rootID, err := nameResolveRoot(cmd)
		if err != nil {
			return err
		}

		client, err := protocol.Init(CFHome())
		if err != nil {
			return fmt.Errorf("initializing client: %w", err)
		}
		defer client.Close()

		if err := naming.Unregister(context.Background(), client, rootID, name); err != nil {
			return fmt.Errorf("unregistering name: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "unregistered: %s\n", name)
		return nil
	},
}

var nameListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all registered names in a root campfire",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		rootID, err := nameResolveRoot(cmd)
		if err != nil {
			return err
		}

		client, err := protocol.Init(CFHome())
		if err != nil {
			return fmt.Errorf("initializing client: %w", err)
		}
		defer client.Close()

		regs, err := naming.List(context.Background(), client, rootID)
		if err != nil {
			return fmt.Errorf("listing names: %w", err)
		}

		if len(regs) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no names registered")
			return nil
		}

		// Print table header.
		fmt.Fprintf(cmd.OutOrStdout(), "%-32s  %-16s  %s\n", "NAME", "CAMPFIRE_ID", "TTL")
		fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", strings.Repeat("-", 32), strings.Repeat("-", 16), strings.Repeat("-", 8))
		for _, r := range regs {
			shortID := r.CampfireID
			if len(shortID) > 16 {
				shortID = shortID[:16]
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-32s  %-16s  %d\n", r.Name, shortID, r.TTL)
		}
		return nil
	},
}

var nameLookupCmd = &cobra.Command{
	Use:   "lookup <name>",
	Short: "Show the resolution path for a name",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		out := cmd.OutOrStdout()

		// Step 1: Check alias store.
		aliases := naming.NewAliasStore(CFHome())
		if id, err := aliases.Get(name); err == nil {
			fmt.Fprintf(out, "Resolved %q via alias store → %s\n", name, id)
			return nil
		}

		// Step 2: Check membership prefix in local store.
		s, err := openStore()
		if err == nil {
			defer s.Close()
			memberships, err := s.ListMemberships()
			if err == nil {
				for _, m := range memberships {
					if strings.HasPrefix(m.CampfireID, name) {
						fmt.Fprintf(out, "Resolved %q via membership prefix → %s\n", name, m.CampfireID)
						return nil
					}
				}
			}
		}

		// Steps 3 and 4 both need a protocol client — init once and share it.
		operatorRoot, _ := naming.LoadOperatorRoot(CFHome())
		registryRootID := getRootRegistryID()
		if operatorRoot != nil || registryRootID != "" {
			client, err := protocol.Init(CFHome())
			if err == nil {
				defer client.Close()

				// Step 3: Try naming resolve via operator root.
				if operatorRoot != nil {
					if resp, err := naming.Resolve(context.Background(), client, operatorRoot.CampfireID, name); err == nil {
						fmt.Fprintf(out, "Resolved %q via naming in root %s → %s\n", name, operatorRoot.CampfireID[:shortIDLen], resp.CampfireID)
						return nil
					}
				}

				// Step 4: Try public root registry (CF_ROOT_REGISTRY).
				if registryRootID != "" {
					if resp, err := naming.Resolve(context.Background(), client, registryRootID, name); err == nil {
						fmt.Fprintf(out, "Resolved %q via naming in root-registry %s → %s\n", name, registryRootID[:shortIDLen], resp.CampfireID)
						return nil
					}
				}
			}
		}

		fmt.Fprintf(out, "Name %q not found in any reachable root\n", name)
		return nil
	},
}

// nameResolveRoot determines the root campfire ID for name operations.
// Priority: --root flag > --public flag (CF_ROOT_REGISTRY) > operator-root.json
func nameResolveRoot(cmd *cobra.Command) (string, error) {
	// Explicit --root override.
	if rootID, _ := cmd.Flags().GetString("root"); rootID != "" {
		return rootID, nil
	}

	// --public: use CF_ROOT_REGISTRY.
	if public, _ := cmd.Flags().GetBool("public"); public {
		rootID := getRootRegistryID()
		if rootID == "" {
			return "", fmt.Errorf("--public requires CF_ROOT_REGISTRY to be set")
		}
		return rootID, nil
	}

	// Default: operator root from operator-root.json.
	operatorRoot, err := naming.LoadOperatorRoot(CFHome())
	if err != nil {
		return "", fmt.Errorf("loading operator root: %w", err)
	}
	if operatorRoot == nil {
		return "", fmt.Errorf("no operator root configured — run `cf root init` or use --root or --public")
	}
	return operatorRoot.CampfireID, nil
}

// addNameRootFlags adds the shared --root and --public flags to a command.
func addNameRootFlags(cmd *cobra.Command) {
	cmd.Flags().String("root", "", "explicit root campfire ID")
	cmd.Flags().Bool("public", false, "use CF_ROOT_REGISTRY as root")
}

func init() {
	// Register flags on subcommands.
	addNameRootFlags(nameRegisterCmd)
	nameRegisterCmd.Flags().Int("ttl", 0, "time-to-live in seconds (default: 3600, max: 86400)")

	addNameRootFlags(nameUnregisterCmd)
	addNameRootFlags(nameListCmd)

	// lookup intentionally has no --root/--public: it walks all sources.

	nameCmd.AddCommand(nameRegisterCmd)
	nameCmd.AddCommand(nameUnregisterCmd)
	nameCmd.AddCommand(nameListCmd)
	nameCmd.AddCommand(nameLookupCmd)

	rootCmd.AddCommand(nameCmd)
}

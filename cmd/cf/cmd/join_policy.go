package cmd

import (
	"fmt"

	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/spf13/cobra"
)

var joinPolicyCmd = &cobra.Command{
	Use:   "join-policy",
	Short: "Manage operator join policy",
	Long: `Manage the operator join policy — controls how incoming join requests
are handled and which campfire is used as the default join root.

  cf join-policy set --consult <campfire-id> --join-root <root-id>   configure consult policy
  cf join-policy set --fs-walk --join-root <root-id>                 use filesystem walk-up
  cf join-policy show                                                 display current policy`,
}

var joinPolicySetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set the join policy",
	Long: `Set the join policy configuration.

Use --consult to forward join requests to an agent campfire for approval.
Use --fs-walk to use the built-in filesystem walk-up for root selection.
--join-root is required for both.

Examples:
  cf join-policy set --consult abc123... --join-root def456...
  cf join-policy set --fs-walk --join-root def456...`,
	RunE: func(cmd *cobra.Command, args []string) error {
		consult, _ := cmd.Flags().GetString("consult")
		fsWalk, _ := cmd.Flags().GetBool("fs-walk")
		joinRoot, _ := cmd.Flags().GetString("join-root")

		// Validate mutual exclusivity
		if consult != "" && fsWalk {
			return fmt.Errorf("--consult and --fs-walk are mutually exclusive")
		}
		if consult == "" && !fsWalk {
			return fmt.Errorf("one of --consult or --fs-walk is required")
		}
		if joinRoot == "" {
			return fmt.Errorf("--join-root is required")
		}

		var consultCampfire string
		if fsWalk {
			consultCampfire = "fs-walk"
		} else {
			consultCampfire = consult
		}

		jp := &naming.JoinPolicy{
			Policy:          "consult",
			ConsultCampfire: consultCampfire,
			JoinRoot:        joinRoot,
		}

		if err := naming.SaveJoinPolicy(CFHome(), jp); err != nil {
			return fmt.Errorf("saving join policy: %w", err)
		}

		if fsWalk {
			fmt.Fprintf(cmd.OutOrStdout(), "join policy saved\n  policy:  consult\n  consult: fs-walk (built-in filesystem walk-up)\n  root:    %s\n", joinRoot)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "join policy saved\n  policy:  consult\n  consult: %s\n  root:    %s\n", consult, joinRoot)
		}
		return nil
	},
}

var joinPolicyShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the current join policy",
	Long:  `Show the current join policy configuration.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		jp, err := naming.LoadJoinPolicy(CFHome())
		if err != nil {
			return fmt.Errorf("loading join policy: %w", err)
		}

		if jp == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "No join policy configured. Name resolution uses ProjectRoot() walk-up and CF_ROOT_REGISTRY fallback.")
			return nil
		}

		fmt.Fprintf(cmd.OutOrStdout(), "join policy:\n  policy:  %s\n  consult: %s\n  root:    %s\n",
			jp.Policy, jp.ConsultCampfire, jp.JoinRoot)
		return nil
	},
}

func init() {
	joinPolicySetCmd.Flags().String("consult", "", "campfire ID to consult for join approval")
	joinPolicySetCmd.Flags().Bool("fs-walk", false, "use built-in filesystem walk-up for root selection")
	joinPolicySetCmd.Flags().String("join-root", "", "default root campfire ID for joins")

	joinPolicyCmd.AddCommand(joinPolicySetCmd)
	joinPolicyCmd.AddCommand(joinPolicyShowCmd)
	rootCmd.AddCommand(joinPolicyCmd)
}

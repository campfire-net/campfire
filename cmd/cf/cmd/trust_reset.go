package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/campfire-net/campfire/pkg/trust"
	"github.com/spf13/cobra"
)

var trustResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Clear TOFU pins — scoped by campfire, convention, or all",
	Long: `Clear TOFU-pinned declarations from the local pin store.

Exactly one scope flag is required:
  --campfire <id>       clear all pins for a specific campfire
  --convention <slug>   clear all pins for a specific convention (across all campfires)
  --all                 clear all pins (requires confirmation)

Pins are TOFU anchors set when you first encounter a declaration from a campfire.
Clearing a pin forces re-evaluation on next contact. Use this when you know a
convention has legitimately changed and want to re-pin the new declaration.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireFlag, _ := cmd.Flags().GetString("campfire")
		conventionFlag, _ := cmd.Flags().GetString("convention")
		allFlag, _ := cmd.Flags().GetBool("all")

		// Exactly one scope flag is required.
		set := 0
		if campfireFlag != "" {
			set++
		}
		if conventionFlag != "" {
			set++
		}
		if allFlag {
			set++
		}
		if set == 0 {
			return fmt.Errorf("one of --campfire, --convention, or --all is required")
		}
		if set > 1 {
			return fmt.Errorf("only one of --campfire, --convention, or --all may be specified")
		}

		// Confirmation gate for --all.
		if allFlag {
			yes, _ := cmd.Flags().GetBool("yes")
			if !yes {
				fmt.Print("Clear all TOFU pins? This cannot be undone. [y/N] ")
				scanner := bufio.NewScanner(os.Stdin)
				scanner.Scan()
				answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
				if answer != "y" && answer != "yes" {
					fmt.Println("Aborted.")
					return nil
				}
			}
		}

		// Resolve campfire ID if given as alias.
		resolvedCampfire := campfireFlag
		if campfireFlag != "" {
			s, err := openStore()
			if err != nil {
				return err
			}
			resolved, resolveErr := resolveCampfireID(campfireFlag, s)
			s.Close()
			if resolveErr == nil {
				resolvedCampfire = resolved
			}
			// If resolve fails, use the raw value — may still match stored pins.
		}

		ps, err := loadPinStore()
		if err != nil {
			return err
		}

		// Count pins before clearing for the success message.
		before := ps.ListPins()
		beforeCount := len(before)

		scope := trust.PinScope{
			CampfireID: resolvedCampfire,
			Convention: conventionFlag,
			All:        allFlag,
		}
		ps.ClearPins(scope)

		after := ps.ListPins()
		cleared := beforeCount - len(after)

		if err := ps.Save(); err != nil {
			return fmt.Errorf("saving pin store: %w", err)
		}

		if jsonOutput {
			fmt.Printf(`{"cleared":%d,"remaining":%d}`+"\n", cleared, len(after))
			return nil
		}

		switch {
		case allFlag:
			fmt.Printf("Cleared all pins (%d removed).\n", cleared)
		case campfireFlag != "":
			fmt.Printf("Cleared pins for campfire %s (%d removed).\n",
				resolvedCampfire[:min(len(resolvedCampfire), 12)], cleared)
		case conventionFlag != "":
			fmt.Printf("Cleared pins for convention %q (%d removed).\n", conventionFlag, cleared)
		}

		return nil
	},
}

func init() {
	trustResetCmd.Flags().String("campfire", "", "clear pins for this campfire ID or alias")
	trustResetCmd.Flags().String("convention", "", "clear pins for a specific convention slug")
	trustResetCmd.Flags().Bool("all", false, "clear all pins")
	trustResetCmd.Flags().Bool("yes", false, "skip confirmation prompt for --all")
	trustCmd.AddCommand(trustResetCmd)
}

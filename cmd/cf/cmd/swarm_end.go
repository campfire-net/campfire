package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/spf13/cobra"
)

var swarmEndCmd = &cobra.Command{
	Use:   "end",
	Short: "Tear down the root campfire",
	Long: `Remove the .campfire/root file, ending the root campfire anchor for this project.

Sends a farewell message to the root campfire announcing the shutdown.
Does not remove .campfire/beacons/ (sub-campfire beacons may still be useful).

If no .campfire/root exists, returns an error.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Get the current working directory (project directory).
		projectDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting current directory: %w", err)
		}

		// Check if .campfire/root exists.
		rootFile := filepath.Join(projectDir, ".campfire", "root")
		campfireID, err := os.ReadFile(rootFile)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("no active swarm — no .campfire/root found")
			}
			return fmt.Errorf("reading .campfire/root: %w", err)
		}

		// Load agent identity to get short ID for the farewell message.
		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity: %w", err)
		}

		campfireIDStr := strings.TrimSpace(string(campfireID))

		// Get agent short ID for farewell message.
		agentShortID := agentID.PublicKeyHex()
		if len(agentShortID) > 12 {
			agentShortID = agentShortID[:12]
		}

		// Send farewell message to the root campfire.
		// This is best-effort; if it fails, we still remove the root file.
		s, err := store.Open(store.StorePath(CFHome()))
		if err == nil {
			defer s.Close()

			farewell := fmt.Sprintf("swarm ended by %s", agentShortID)
			rootMembership, merr := s.GetMembership(campfireIDStr)
			if merr == nil && rootMembership != nil {
				_, serr := sendFilesystem(campfireIDStr, farewell, []string{"status"}, nil, agentID, rootMembership.TransportDir)
				if serr != nil {
					fmt.Fprintf(os.Stderr, "warning: could not send farewell message: %v\n", serr)
				}
			}
		}

		// Remove .campfire/root.
		if err := os.Remove(rootFile); err != nil {
			return fmt.Errorf("removing .campfire/root: %w", err)
		}

		fmt.Println("swarm ended")
		return nil
	},
}

func init() {
	swarmCmd.AddCommand(swarmEndCmd)
}

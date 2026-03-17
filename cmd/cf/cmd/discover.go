package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/spf13/cobra"
)

var discoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "List campfire beacons visible from here",
	RunE: func(cmd *cobra.Command, args []string) error {
		globalDir := BeaconDir()
		globalBeacons, err := beacon.Scan(globalDir)
		if err != nil {
			return fmt.Errorf("scanning beacons: %w", err)
		}

		// Check for project-local beacons
		var projectBeacons []beacon.Beacon
		projectDir, hasProject := ProjectDir()
		if hasProject {
			projBeaconDir := filepath.Join(projectDir, ".campfire", "beacons")
			projectBeacons, err = beacon.Scan(projBeaconDir)
			if err != nil {
				return fmt.Errorf("scanning project beacons: %w", err)
			}
		}

		if jsonOutput {
			type entry struct {
				CampfireID            string   `json:"campfire_id"`
				JoinProtocol          string   `json:"join_protocol"`
				ReceptionRequirements []string `json:"reception_requirements"`
				Transport             string   `json:"transport"`
				Description           string   `json:"description"`
				SignatureValid        bool     `json:"signature_valid"`
			}
			allBeacons := append(projectBeacons, globalBeacons...)
			var entries []entry
			for _, b := range allBeacons {
				entries = append(entries, entry{
					CampfireID:            b.CampfireIDHex(),
					JoinProtocol:          b.JoinProtocol,
					ReceptionRequirements: b.ReceptionRequirements,
					Transport:             b.Transport.Protocol,
					Description:           b.Description,
					SignatureValid:        b.Verify(),
				})
			}
			if entries == nil {
				entries = []entry{}
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(entries)
		}

		if len(projectBeacons) == 0 && len(globalBeacons) == 0 {
			fmt.Println("No beacons found.")
			return nil
		}

		printBeacon := func(b beacon.Beacon) {
			idFull := b.CampfireIDHex()
			idShort := idFull
			if len(idShort) > 12 {
				idShort = idShort[:12]
			}
			sigStatus := "valid"
			if !b.Verify() {
				sigStatus = "INVALID"
			}

			desc := b.Description
			if desc == "" {
				desc = "(no description)"
			}

			reqs := "(none)"
			if len(b.ReceptionRequirements) > 0 {
				reqs = strings.Join(b.ReceptionRequirements, ", ")
			}

			fmt.Printf("%s  %s  %s  sig:%s\n", idShort, b.JoinProtocol, desc, sigStatus)
			fmt.Printf("  id: %s\n", idFull)
			fmt.Printf("  transport: %s  requires: %s\n", b.Transport.Protocol, reqs)
		}

		if hasProject {
			fmt.Println("Project beacons:")
			if len(projectBeacons) == 0 {
				fmt.Println("  (none)")
			} else {
				for _, b := range projectBeacons {
					printBeacon(b)
				}
			}
			fmt.Println("Global beacons:")
			if len(globalBeacons) == 0 {
				fmt.Println("  (none)")
			} else {
				for _, b := range globalBeacons {
					printBeacon(b)
				}
			}
		} else {
			for _, b := range globalBeacons {
				printBeacon(b)
			}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(discoverCmd)
}

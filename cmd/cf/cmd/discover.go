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

		// Scan routing:beacon messages from campfire memberships (in-band discovery).
		// Errors are non-fatal — store may not exist on first run.
		var campfireBeacons []beacon.Beacon
		if s, err := openStore(); err == nil {
			defer s.Close()
			campfireBeacons, _ = beacon.ScanAllMemberships(s)
		}

		if jsonOutput {
			type entry struct {
				CampfireID            string   `json:"campfire_id"`
				JoinProtocol          string   `json:"join_protocol"`
				ReceptionRequirements []string `json:"reception_requirements"`
				Transport             string   `json:"transport"`
				Description           string   `json:"description"`
				SignatureValid        bool     `json:"signature_valid"`
				Source                string   `json:"source"`
			}
			// Collect all beacons with their source label.
			type srcBeacon struct {
				b      beacon.Beacon
				source string
			}
			var all []srcBeacon
			for _, b := range projectBeacons {
				all = append(all, srcBeacon{b, "project"})
			}
			for _, b := range globalBeacons {
				all = append(all, srcBeacon{b, "global"})
			}
			for _, b := range campfireBeacons {
				all = append(all, srcBeacon{b, "campfire"})
			}
			var entries []entry
			for _, sb := range all {
				entries = append(entries, entry{
					CampfireID:            sb.b.CampfireIDHex(),
					JoinProtocol:          sb.b.JoinProtocol,
					ReceptionRequirements: sb.b.ReceptionRequirements,
					Transport:             sb.b.Transport.Protocol,
					Description:           sb.b.Description,
					SignatureValid:        sb.b.Verify(),
					Source:                sb.source,
				})
			}
			if entries == nil {
				entries = []entry{}
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(entries)
		}

		if len(projectBeacons) == 0 && len(globalBeacons) == 0 && len(campfireBeacons) == 0 {
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

		if len(campfireBeacons) > 0 {
			fmt.Println("Campfire beacons (in-band):")
			for _, b := range campfireBeacons {
				printBeacon(b)
			}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(discoverCmd)
}

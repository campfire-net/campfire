package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/3dl-dev/campfire/pkg/beacon"
	"github.com/spf13/cobra"
)

var discoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "List campfire beacons visible from here",
	RunE: func(cmd *cobra.Command, args []string) error {
		beaconDir := BeaconDir()
		beacons, err := beacon.Scan(beaconDir)
		if err != nil {
			return fmt.Errorf("scanning beacons: %w", err)
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
			var entries []entry
			for _, b := range beacons {
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

		if len(beacons) == 0 {
			fmt.Println("No beacons found.")
			return nil
		}

		for _, b := range beacons {
			idShort := b.CampfireIDHex()
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
			fmt.Printf("  transport: %s  requires: %s\n", b.Transport.Protocol, reqs)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(discoverCmd)
}

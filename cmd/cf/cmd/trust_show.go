package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/campfire-net/campfire/pkg/trust"
	"github.com/spf13/cobra"
)

var trustShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Display local trust policy — adopted conventions and pin status",
	Long: `Display the local trust state:
  - Adopted conventions: slug, operation, version, fingerprint, source
  - Pin status: TOFU-pinned declarations for campfire:convention:operation triples
  - Trust summary: overall initialization state

Adopted conventions come from the home campfire declarations (seed policy).
Pins are TOFU-pinned declarations from campfires you have joined.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		engine := loadLocalPolicyEngine(s)
		adopted := engine.ListAdopted()

		// Sort for stable output.
		sort.Slice(adopted, func(i, j int) bool {
			ki := adopted[i].Convention + ":" + adopted[i].Operation
			kj := adopted[j].Convention + ":" + adopted[j].Operation
			return ki < kj
		})

		// Load pins (best-effort; missing pin file is not an error).
		pins := map[string]*trust.Pin{}
		ps, pinErr := loadPinStore()
		if pinErr == nil {
			pins = ps.ListPins()
		}

		if jsonOutput {
			type adoptedJSON struct {
				Convention  string `json:"convention"`
				Operation   string `json:"operation"`
				Version     string `json:"version"`
				Fingerprint string `json:"fingerprint"`
				Source      string `json:"source"`
				SourceID    string `json:"source_id,omitempty"`
				AdoptedAt   string `json:"adopted_at"`
			}
			type pinJSON struct {
				Key         string `json:"key"`
				ContentHash string `json:"content_hash"`
				SignerKey   string `json:"signer_key"`
				SignerType  string `json:"signer_type"`
				TrustStatus string `json:"trust_status"`
				PinnedAt    string `json:"pinned_at"`
			}

			adoptedList := make([]adoptedJSON, 0, len(adopted))
			for _, ac := range adopted {
				adoptedList = append(adoptedList, adoptedJSON{
					Convention:  ac.Convention,
					Operation:   ac.Operation,
					Version:     ac.Version,
					Fingerprint: ac.Fingerprint,
					Source:      string(ac.Source),
					SourceID:    ac.SourceID,
					AdoptedAt:   ac.AdoptedAt.UTC().Format("2006-01-02T15:04:05Z"),
				})
			}
			pinList := make([]pinJSON, 0, len(pins))
			for k, p := range pins {
				pinList = append(pinList, pinJSON{
					Key:         k,
					ContentHash: p.ContentHash,
					SignerKey:   p.SignerKey,
					SignerType:  string(p.SignerType),
					TrustStatus: string(p.TrustStatus),
					PinnedAt:    p.PinnedAt.UTC().Format("2006-01-02T15:04:05Z"),
				})
			}
			sort.Slice(pinList, func(i, j int) bool { return pinList[i].Key < pinList[j].Key })

			out := map[string]interface{}{
				"initialized": engine.IsInitialized(),
				"adopted":     adoptedList,
				"pins":        pinList,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		// Human-readable output.
		fmt.Printf("Trust policy  (initialized: %v)\n\n", engine.IsInitialized())

		if len(adopted) == 0 {
			fmt.Println("Adopted conventions: none")
		} else {
			fmt.Printf("Adopted conventions (%d):\n", len(adopted))
			for _, ac := range adopted {
				sourceStr := string(ac.Source)
				if ac.SourceID != "" {
					sourceStr += " (" + ac.SourceID[:min(len(ac.SourceID), 12)] + ")"
				}
				fmt.Printf("  %-30s  v%-8s  %-12s  %s\n",
					ac.Convention+":"+ac.Operation,
					ac.Version,
					sourceStr,
					shortFingerprint(ac.Fingerprint),
				)
			}
		}

		fmt.Println()
		if len(pins) == 0 {
			fmt.Println("Pins: none")
		} else {
			fmt.Printf("Pins (%d):\n", len(pins))
			keys := make([]string, 0, len(pins))
			for k := range pins {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				p := pins[k]
				parts := strings.SplitN(k, ":", 3)
				campfireID, convention, operation := "", "", ""
				if len(parts) >= 1 {
					campfireID = parts[0]
				}
				if len(parts) >= 2 {
					convention = parts[1]
				}
				if len(parts) >= 3 {
					operation = parts[2]
				}
				fmt.Printf("  %-12s  %-20s  %-10s  %s\n",
					campfireID[:min(len(campfireID), 12)],
					convention+":"+operation,
					string(p.TrustStatus),
					shortFingerprint(p.ContentHash),
				)
			}
		}

		return nil
	},
}

// shortFingerprint returns a short display form of a fingerprint.
// Input may be "sha256:hexstring" or just "hexstring".
func shortFingerprint(fp string) string {
	if strings.HasPrefix(fp, "sha256:") {
		return fp[:min(len(fp), 23)] + "..."
	}
	if len(fp) > 16 {
		return fp[:16] + "..."
	}
	return fp
}

func init() {
	trustCmd.AddCommand(trustShowCmd)
}

package cmd

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// trustPinCmd — cf trust pin <campfire-id> <pubkey>
// Pins an ed25519 public key as the TOFU-accepted key for a campfire.
var trustPinCmd = &cobra.Command{
	Use:   "pin <campfire-id> <pubkey>",
	Short: "Pin a public key as the TOFU-accepted key for a campfire",
	Long: `Record an explicit operator TOFU acceptance: "I trust <pubkey> as the
signing key for <campfire-id>."

The pubkey must be a 64-char hex-encoded ed25519 public key (32 bytes).
The campfire-id may be a full hex ID or a registered alias.

Pinning is idempotent — re-pinning the same campfire replaces the existing pin.

Example:
  cf trust pin aaaa...bbbb cccc...dddd`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireArg := args[0]
		pubkeyArg := args[1]

		// Validate pubkey format: 64 hex chars (32 bytes ed25519 public key).
		pubkeyBytes, err := hex.DecodeString(pubkeyArg)
		if err != nil {
			return fmt.Errorf("invalid pubkey %q: %w", pubkeyArg, err)
		}
		if len(pubkeyBytes) != ed25519.PublicKeySize {
			return fmt.Errorf("pubkey must be 64 hex chars (32 bytes), got %d bytes", len(pubkeyBytes))
		}

		// Resolve campfire ID (supports aliases).
		s, err := openStore()
		if err != nil {
			return err
		}
		campfireID, resolveErr := resolveCampfireID(campfireArg, s)
		s.Close()
		if resolveErr != nil {
			// If resolution fails, use the raw arg — may be a valid bare ID.
			campfireID = campfireArg
		}

		kps, err := loadKeyPinStore()
		if err != nil {
			return fmt.Errorf("opening key pin store: %w", err)
		}

		kps.SetKeyPin(campfireID, pubkeyArg)

		if err := kps.Save(); err != nil {
			return fmt.Errorf("saving key pin store: %w", err)
		}

		if jsonOutput {
			type result struct {
				CampfireID string `json:"campfire_id"`
				PubKey     string `json:"pubkey"`
				PinnedAt   string `json:"pinned_at"`
			}
			pin := kps.GetKeyPin(campfireID)
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result{
				CampfireID: campfireID,
				PubKey:     pin.PubKey,
				PinnedAt:   pin.PinnedAt.UTC().Format(time.RFC3339),
			})
		}

		fmt.Printf("pinned: campfire=%s pubkey=%s\n",
			campfireID[:min(len(campfireID), 12)],
			pubkeyArg[:min(len(pubkeyArg), 16)]+"...",
		)
		return nil
	},
}

// trustUnpinCmd — cf trust unpin <campfire-id>
// Removes the key pin for a campfire.
var trustUnpinCmd = &cobra.Command{
	Use:   "unpin <campfire-id>",
	Short: "Remove the TOFU key pin for a campfire",
	Long: `Remove the operator TOFU key pin for a campfire.

After unpinning, the next trust evaluation for this campfire will be treated
as a fresh TOFU encounter — the key must be re-pinned or re-evaluated.

The campfire-id may be a full hex ID or a registered alias.

Example:
  cf trust unpin aaaa...bbbb`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireArg := args[0]

		// Resolve campfire ID (supports aliases).
		s, err := openStore()
		if err != nil {
			return err
		}
		campfireID, resolveErr := resolveCampfireID(campfireArg, s)
		s.Close()
		if resolveErr != nil {
			campfireID = campfireArg
		}

		kps, err := loadKeyPinStore()
		if err != nil {
			return fmt.Errorf("opening key pin store: %w", err)
		}

		// Check if the pin exists before removing.
		existing := kps.GetKeyPin(campfireID)
		kps.RemoveKeyPin(campfireID)

		if err := kps.Save(); err != nil {
			return fmt.Errorf("saving key pin store: %w", err)
		}

		if jsonOutput {
			removed := existing != nil
			fmt.Printf(`{"campfire_id":%q,"removed":%v}`+"\n", campfireID, removed)
			return nil
		}

		if existing == nil {
			fmt.Printf("no pin found for campfire %s\n", campfireID[:min(len(campfireID), 12)])
		} else {
			fmt.Printf("unpinned: campfire=%s\n", campfireID[:min(len(campfireID), 12)])
		}
		return nil
	},
}

// trustListCmd — cf trust list
// Lists all key pins.
var trustListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all TOFU key pins",
	Long: `Display all operator TOFU key pins: pubkey, campfire-id, when pinned, last-used.

Use --json for machine-readable output.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		kps, err := loadKeyPinStore()
		if err != nil {
			return fmt.Errorf("opening key pin store: %w", err)
		}

		pins := kps.ListKeyPins()

		// Sort by campfire ID for stable output.
		sort.Slice(pins, func(i, j int) bool {
			return pins[i].CampfireID < pins[j].CampfireID
		})

		if jsonOutput {
			type pinJSON struct {
				CampfireID string `json:"campfire_id"`
				PubKey     string `json:"pubkey"`
				PinnedAt   string `json:"pinned_at"`
				LastUsed   string `json:"last_used,omitempty"`
			}
			list := make([]pinJSON, 0, len(pins))
			for _, p := range pins {
				entry := pinJSON{
					CampfireID: p.CampfireID,
					PubKey:     p.PubKey,
					PinnedAt:   p.PinnedAt.UTC().Format(time.RFC3339),
				}
				if !p.LastUsed.IsZero() {
					entry.LastUsed = p.LastUsed.UTC().Format(time.RFC3339)
				}
				list = append(list, entry)
			}
			if list == nil {
				list = []pinJSON{}
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(list)
		}

		if len(pins) == 0 {
			fmt.Println("No key pins.")
			return nil
		}

		fmt.Printf("Key pins (%d):\n", len(pins))
		for _, p := range pins {
			campfireShort := p.CampfireID
			if len(campfireShort) > 12 {
				campfireShort = campfireShort[:12]
			}
			pubkeyShort := p.PubKey
			if len(pubkeyShort) > 16 {
				pubkeyShort = pubkeyShort[:16] + "..."
			}
			lastUsedStr := "never"
			if !p.LastUsed.IsZero() {
				lastUsedStr = p.LastUsed.UTC().Format("2006-01-02")
			}
			fmt.Printf("  %-12s  %-20s  pinned=%-20s  last-used=%s\n",
				campfireShort,
				pubkeyShort,
				p.PinnedAt.UTC().Format("2006-01-02"),
				lastUsedStr,
			)
		}
		return nil
	},
}

// trustPruneCmd — cf trust prune
// Removes key pins for campfires the operator has left or that have been disbanded.
var trustPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove key pins for campfires no longer in local membership",
	Long: `Remove TOFU key pins for campfires that the operator is no longer a member of
(left, disbanded, or never joined on this machine).

Pruning is safe — it only removes pins for campfires absent from the local store's
membership table. Campfires that still appear in "cf ls" output are NOT pruned.

Use --dry-run to preview which pins would be removed without making changes.

Example:
  cf trust prune
  cf trust prune --dry-run`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// Load the local membership set from the store.
		s, err := openStore()
		if err != nil {
			return err
		}
		memberships, err := s.ListMemberships()
		s.Close()
		if err != nil {
			return fmt.Errorf("listing memberships: %w", err)
		}

		activeIDs := make(map[string]struct{}, len(memberships))
		for _, m := range memberships {
			activeIDs[m.CampfireID] = struct{}{}
		}

		kps, err := loadKeyPinStore()
		if err != nil {
			return fmt.Errorf("opening key pin store: %w", err)
		}

		// Preview which pins would be pruned.
		pins := kps.ListKeyPins()
		var toPrune []string
		for _, p := range pins {
			if _, active := activeIDs[p.CampfireID]; !active {
				toPrune = append(toPrune, p.CampfireID)
			}
		}
		sort.Strings(toPrune)

		if dryRun {
			if jsonOutput {
				type dryResult struct {
					WouldPrune []string `json:"would_prune"`
					Count      int      `json:"count"`
				}
				if toPrune == nil {
					toPrune = []string{}
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(dryResult{WouldPrune: toPrune, Count: len(toPrune)})
			}
			if len(toPrune) == 0 {
				fmt.Println("No pins to prune.")
			} else {
				fmt.Printf("Would prune %d pin(s):\n", len(toPrune))
				for _, id := range toPrune {
					fmt.Printf("  %s\n", id[:min(len(id), 12)])
				}
			}
			return nil
		}

		// Execute pruning.
		pruned := kps.PruneKeyPins(activeIDs)
		sort.Strings(pruned)

		if len(pruned) > 0 {
			if err := kps.Save(); err != nil {
				return fmt.Errorf("saving key pin store: %w", err)
			}
		}

		if jsonOutput {
			if pruned == nil {
				pruned = []string{}
			}
			type pruneResult struct {
				Pruned    []string `json:"pruned"`
				Count     int      `json:"count"`
				Remaining int      `json:"remaining"`
			}
			remaining := len(kps.ListKeyPins())
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(pruneResult{
				Pruned:    pruned,
				Count:     len(pruned),
				Remaining: remaining,
			})
		}

		if len(pruned) == 0 {
			fmt.Println("No pins to prune.")
		} else {
			shorts := make([]string, len(pruned))
			for i, id := range pruned {
				shorts[i] = id[:min(len(id), 12)]
			}
			fmt.Printf("Pruned %d pin(s): %s\n", len(pruned), strings.Join(shorts, ", "))
		}
		return nil
	},
}

func init() {
	trustPruneCmd.Flags().Bool("dry-run", false, "preview what would be pruned without removing anything")
	trustCmd.AddCommand(trustPinCmd)
	trustCmd.AddCommand(trustUnpinCmd)
	trustCmd.AddCommand(trustListCmd)
	trustCmd.AddCommand(trustPruneCmd)
}


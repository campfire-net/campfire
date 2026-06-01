package cmd

// gc.go — cf gc: local garbage collection of dead campfires.
//
// The filesystem store accumulates campfire directories and store rows
// indefinitely. Short-lived campfires (e.g. swarm/bakeoff engagement campfires)
// are created, used, and abandoned but never reclaimed — the mallcop-pro
// bottleneck report observed 2,971 directories / 1.6 GB grown in 5 days
// (report PART A / A1).
//
// `cf gc` reclaims that space by purging filesystem-transport campfires that are
// dead: empty (no messages) OR idle (newest message older than --older-than).
// It is DRY-RUN by default — it prints what it would remove and changes nothing
// unless --yes is given. It never touches:
//   - the home (identity) campfire,
//   - campfires joined more recently than --older-than (brand-new campfires),
//   - non-filesystem transports (p2p-http, etc. — not local fs cruft).
//
// gc is a LOCAL maintenance operation: it does not disband the campfire for
// other members or post any protocol event. It only deletes this machine's copy
// (transport directory + store rows). Item: campfireagent-4b9.

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport"
	"github.com/spf13/cobra"
)

// gcCandidate is a campfire selected for purging, with the reason.
type gcCandidate struct {
	CampfireID   string `json:"campfire_id"`
	TransportDir string `json:"transport_dir"`
	Reason       string `json:"reason"`      // "empty" or "idle"
	AgeSeconds   int64  `json:"age_seconds"` // since newest message; -1 when empty
}

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Reclaim local store space by purging dead (empty or idle) filesystem campfires",
	Long: `Garbage-collect dead local campfires to bound store growth.

A campfire is a purge candidate when ALL of:
  - it uses the filesystem transport (p2p-http and other transports are skipped),
  - it is NOT your home (identity) campfire,
  - it was joined longer ago than --older-than (recently-joined campfires are kept), AND
  - it is empty (no messages) OR idle (newest message older than --older-than).

DRY RUN BY DEFAULT: cf gc only reports candidates and changes nothing. Pass --yes
to actually delete. Deletion removes this machine's copy only — the campfire's
transport directory and all local store rows (messages, cursors, projections,
key material, membership). It does NOT disband the campfire for other members or
post any protocol event.

Examples:
  cf gc                         # dry run, default cutoff (24h)
  cf gc --older-than 72h        # dry run, 3-day cutoff
  cf gc --older-than 24h --yes  # actually purge campfires idle/empty >24h`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		olderThan, _ := cmd.Flags().GetDuration("older-than")
		yes, _ := cmd.Flags().GetBool("yes")
		jsonOut, _ := cmd.Flags().GetBool("json")

		if olderThan < 0 {
			return fmt.Errorf("--older-than must not be negative")
		}

		_, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		// Resolve the home campfire so we never reap it. Best-effort: if no home
		// alias is set, homeID stays empty and nothing is protected by it.
		homeID, _ := resolveCampfireID("home", s)

		memberships, err := s.ListMemberships()
		if err != nil {
			return fmt.Errorf("listing memberships: %w", err)
		}

		nowNano := time.Now().UnixNano()
		cutoffNano := nowNano - olderThan.Nanoseconds()

		candidates, err := gcSelectCandidates(memberships, homeID, cutoffNano, nowNano, s)
		if err != nil {
			return err
		}

		if jsonOut {
			return gcEmitJSON(candidates, yes, s)
		}

		if len(candidates) == 0 {
			fmt.Printf("cf gc: nothing to collect (no empty/idle filesystem campfires older than %s)\n", olderThan)
			return nil
		}

		fmt.Printf("cf gc: %d candidate(s) (cutoff %s):\n", len(candidates), olderThan)
		for _, c := range candidates {
			age := "empty"
			if c.Reason == "idle" {
				age = fmt.Sprintf("idle %s", (time.Duration(c.AgeSeconds) * time.Second))
			}
			fmt.Printf("  %s  [%s]  %s\n", c.CampfireID[:min(16, len(c.CampfireID))], age, c.TransportDir)
		}

		if !yes {
			fmt.Printf("\nDRY RUN — nothing deleted. Re-run with --yes to purge these %d campfire(s).\n", len(candidates))
			return nil
		}

		purged, failed := gcPurge(candidates, s)
		fmt.Printf("\ncf gc: purged %d campfire(s)", purged)
		if failed > 0 {
			fmt.Printf(", %d failed", failed)
		}
		fmt.Println(".")
		if failed > 0 {
			return fmt.Errorf("%d campfire(s) failed to purge", failed)
		}
		return nil
	},
}

// gcSelectCandidates returns the campfires eligible for purging given the cutoff
// timestamp (nanoseconds). A campfire is a candidate when it is a filesystem
// campfire, is not the home campfire, was joined before the cutoff, and is either
// empty (no messages) or idle (newest message before the cutoff). Pure given the
// store's MaxMessageTimestamp, so it is unit-testable without CF_HOME or cobra.
func gcSelectCandidates(memberships []store.Membership, homeID string, cutoffNano, nowNano int64, s store.Store) ([]gcCandidate, error) {
	var candidates []gcCandidate
	for _, m := range memberships {
		if m.CampfireID == homeID {
			continue // never reap the identity campfire
		}
		if transport.ResolveType(m) != transport.TypeFilesystem {
			continue // only local filesystem campfires are gc cruft
		}
		if m.JoinedAt > cutoffNano {
			continue // joined too recently — keep
		}
		maxTS, err := s.MaxMessageTimestamp(m.CampfireID, 0)
		if err != nil {
			return nil, fmt.Errorf("reading max timestamp for %s: %w", m.CampfireID, err)
		}
		switch {
		case maxTS == 0:
			candidates = append(candidates, gcCandidate{
				CampfireID: m.CampfireID, TransportDir: m.TransportDir,
				Reason: "empty", AgeSeconds: -1,
			})
		case maxTS < cutoffNano:
			candidates = append(candidates, gcCandidate{
				CampfireID: m.CampfireID, TransportDir: m.TransportDir,
				Reason: "idle", AgeSeconds: (nowNano - maxTS) / int64(time.Second),
			})
		}
	}
	return candidates, nil
}

// gcPurge removes the transport directory and all local store rows for each
// candidate. Returns counts of purged and failed campfires. A per-campfire
// failure is reported but does not abort the rest.
func gcPurge(candidates []gcCandidate, s store.Store) (purged, failed int) {
	for _, c := range candidates {
		// Remove the transport directory first; even if it is already gone
		// (os.RemoveAll is nil on a missing path) we still purge the store rows.
		if c.TransportDir != "" {
			if err := os.RemoveAll(c.TransportDir); err != nil {
				fmt.Fprintf(os.Stderr, "cf gc: removing transport dir for %s: %v\n", c.CampfireID, err)
				failed++
				continue
			}
		}
		if err := s.PurgeCampfire(c.CampfireID); err != nil {
			fmt.Fprintf(os.Stderr, "cf gc: purging store rows for %s: %v\n", c.CampfireID, err)
			failed++
			continue
		}
		purged++
	}
	return purged, failed
}

// gcEmitJSON prints the candidate set (and, when applying, performs the purge)
// as a JSON object for programmatic callers.
func gcEmitJSON(candidates []gcCandidate, apply bool, s store.Store) error {
	out := map[string]any{
		"candidates": candidates,
		"count":      len(candidates),
		"applied":    apply,
	}
	if apply {
		purged, failed := gcPurge(candidates, s)
		out["purged"] = purged
		out["failed"] = failed
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if apply {
		if f, _ := out["failed"].(int); f > 0 {
			return fmt.Errorf("%d campfire(s) failed to purge", f)
		}
	}
	return nil
}

func init() {
	gcCmd.Flags().Duration("older-than", 24*time.Hour, "purge empty campfires and campfires whose newest message is older than this duration")
	gcCmd.Flags().Bool("yes", false, "actually delete (default is a dry run that only reports candidates)")
	gcCmd.Flags().Bool("json", false, "emit candidates (and purge results with --yes) as JSON")
	rootCmd.AddCommand(gcCmd)
}

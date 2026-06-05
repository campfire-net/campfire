package cmd

// gc.go — cf gc: local garbage collection of dead campfires.
//
// The filesystem store accumulates campfire directories and store rows
// indefinitely. Short-lived campfires (e.g. swarm/bakeoff engagement campfires)
// are created, used, and abandoned but never reclaimed — the mallcop-pro
// bottleneck report observed 2,971 directories / 1.6 GB grown in 5 days
// (report PART A / A1).
//
// `cf gc` reclaims that space by purging dead filesystem-transport campfires,
// HONORING the Durability Convention (campfireagent-246 — a blanket idle purge
// destroyed the dontguess exchange, an idle-but-priceless message ledger):
//   - durability:lifecycle:persistent       → NEVER a candidate
//   - durability:lifecycle:ephemeral:<dur>  → candidate once idle longer than <dur>
//   - durability:lifecycle:bounded:<date>   → candidate once <date> passes
//   - undeclared, EMPTY (no messages)       → candidate (nothing of value to lose)
//   - undeclared, with messages             → permanent-by-default; idle purge
//     requires the explicit --include-undeclared opt-in
//
// It is DRY-RUN by default — it prints what it would remove and changes nothing
// unless --yes is given. It never touches:
//   - the home (identity) campfire,
//   - campfires joined more recently than --older-than (brand-new campfires),
//   - non-filesystem transports (p2p-http, etc. — not local fs cruft).
//
// gc is a LOCAL maintenance operation: it does not disband the campfire for
// other members or post any protocol event. It only deletes this machine's copy
// (transport directory + store rows). Items: campfireagent-4b9, campfireagent-246.

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	durability "github.com/campfire-net/campfire/cf-conventions/cf-durability"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport"
	"github.com/spf13/cobra"
)

// gcCandidate is a campfire selected for purging, with the reason.
type gcCandidate struct {
	CampfireID   string `json:"campfire_id"`
	TransportDir string `json:"transport_dir"`
	Reason       string `json:"reason"`      // "empty", "idle-undeclared", "ephemeral-elapsed", or "bounded-elapsed"
	AgeSeconds   int64  `json:"age_seconds"` // since newest message; -1 when empty
	Lifecycle    string `json:"lifecycle,omitempty"` // declared lifecycle driving eligibility, if any
}

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Reclaim local store space by purging dead (empty or idle) filesystem campfires",
	Long: `Garbage-collect dead local campfires to bound store growth, honoring the
Durability Convention.

A campfire is a purge candidate when ALL of:
  - it uses the filesystem transport (p2p-http and other transports are skipped),
  - it is NOT your home (identity) campfire,
  - it was joined longer ago than --older-than (recently-joined campfires are kept), AND
  - its lifecycle declaration (see 'cf lifecycle') makes it eligible:
      persistent              never eligible
      ephemeral:<duration>    eligible once idle longer than <duration>
      bounded:<iso8601>       eligible once the date passes
      (undeclared)            empty campfires (no messages) are eligible;
                              campfires WITH messages are permanent-by-default
                              and require --include-undeclared to purge when
                              idle past --older-than

lifecycle:quota declarations govern message compaction, not campfire deletion,
and are ignored by gc.

DRY RUN BY DEFAULT: cf gc only reports candidates and changes nothing. Pass --yes
to actually delete. Deletion removes this machine's copy only — the campfire's
transport directory and all local store rows (messages, cursors, projections,
key material, membership). It does NOT disband the campfire for other members or
post any protocol event.

Examples:
  cf gc                                      # dry run, default cutoff (24h)
  cf gc --older-than 72h                     # dry run, 3-day cutoff
  cf gc --yes                                # purge eligible campfires
  cf gc --include-undeclared --yes           # ALSO purge idle undeclared campfires
                                             # (pre-convention behavior — know what
                                             # you are deleting)`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		olderThan, _ := cmd.Flags().GetDuration("older-than")
		yes, _ := cmd.Flags().GetBool("yes")
		jsonOut, _ := cmd.Flags().GetBool("json")
		includeUndeclared, _ := cmd.Flags().GetBool("include-undeclared")

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

		candidates, err := gcSelectCandidates(memberships, homeID, cutoffNano, nowNano, s, includeUndeclared)
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
			age := c.Reason
			if c.AgeSeconds >= 0 {
				age = fmt.Sprintf("%s %s", c.Reason, (time.Duration(c.AgeSeconds) * time.Second))
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
// timestamp (nanoseconds), honoring Durability Convention lifecycle
// declarations (campfireagent-246):
//
//   - persistent: never a candidate.
//   - ephemeral:<dur>: candidate when its newest activity (newest message, or
//     JoinedAt when empty) is older than the DECLARED timeout — the
//     declaration's own semantics, not --older-than.
//   - bounded:<date>: candidate once the declared date passes.
//   - undeclared + empty (no messages): candidate — nothing of value to lose.
//   - undeclared + messages: permanent-by-default. Only a candidate when the
//     caller opted in with --include-undeclared AND the campfire is idle past
//     the cutoff.
//
// The home campfire, non-filesystem transports, and recently-joined campfires
// are never candidates. Pure given the store, so it is unit-testable without
// CF_HOME or cobra.
func gcSelectCandidates(memberships []store.Membership, homeID string, cutoffNano, nowNano int64, s store.Store, includeUndeclared bool) ([]gcCandidate, error) {
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
		lc, err := resolveCampfireLifecycle(s, m.CampfireID)
		if err != nil {
			return nil, fmt.Errorf("resolving lifecycle for %s: %w", m.CampfireID, err)
		}

		ageSeconds := int64(-1)
		if maxTS > 0 {
			ageSeconds = (nowNano - maxTS) / int64(time.Second)
		}

		if !lc.Declared {
			switch {
			case maxTS == 0:
				// Empty: no messages, nothing of value to lose. This is the
				// original gc use case (thousands of abandoned swarm dirs).
				candidates = append(candidates, gcCandidate{
					CampfireID: m.CampfireID, TransportDir: m.TransportDir,
					Reason: "empty", AgeSeconds: -1,
				})
			case includeUndeclared && maxTS < cutoffNano:
				// Permanent-by-default overridden by explicit operator opt-in.
				candidates = append(candidates, gcCandidate{
					CampfireID: m.CampfireID, TransportDir: m.TransportDir,
					Reason: "idle-undeclared", AgeSeconds: ageSeconds,
				})
			}
			continue
		}

		switch lc.Type {
		case durability.LifecyclePersistent:
			// Declared continuity: never a candidate.
		case durability.LifecycleEphemeral:
			ephemeralCutoff, ok := lifecycleEphemeralCutoff(lc.Value, nowNano)
			if !ok {
				continue // unreadable timeout — never delete on a value we cannot read
			}
			// Newest activity: newest message, or the join itself when empty.
			activityTS := maxTS
			if activityTS == 0 {
				activityTS = m.JoinedAt
			}
			if activityTS < ephemeralCutoff {
				candidates = append(candidates, gcCandidate{
					CampfireID: m.CampfireID, TransportDir: m.TransportDir,
					Reason: "ephemeral-elapsed", AgeSeconds: ageSeconds,
					Lifecycle: string(durability.LifecycleEphemeral) + ":" + lc.Value,
				})
			}
		case durability.LifecycleBounded:
			if lifecycleBoundedElapsed(lc.Value, time.Unix(0, nowNano)) {
				candidates = append(candidates, gcCandidate{
					CampfireID: m.CampfireID, TransportDir: m.TransportDir,
					Reason: "bounded-elapsed", AgeSeconds: ageSeconds,
					Lifecycle: string(durability.LifecycleBounded) + ":" + lc.Value,
				})
			}
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
	gcCmd.Flags().Duration("older-than", 24*time.Hour, "keep recently-joined campfires, and (with --include-undeclared) campfires whose newest message is newer than this duration")
	gcCmd.Flags().Bool("include-undeclared", false, "ALSO purge idle campfires with no lifecycle declaration (overrides the convention's permanent-by-default rule)")
	gcCmd.Flags().Bool("yes", false, "actually delete (default is a dry run that only reports candidates)")
	gcCmd.Flags().Bool("json", false, "emit candidates (and purge results with --yes) as JSON")
	rootCmd.AddCommand(gcCmd)
}

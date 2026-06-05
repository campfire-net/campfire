package cmd

// lifecycle.go — cf lifecycle: declare and inspect a campfire's continuity
// intention (Durability Convention v0.1), and the shared resolution logic the
// gc eligibility filter consults (campfireagent-246).
//
// A lifecycle declaration is an in-campfire message tagged
// durability:lifecycle:<decl>. Living in the campfire's signed history, it
// syncs to every member's store, so a single declaration protects (or
// schedules) the campfire on ALL member machines — unlike per-machine
// metadata. cf gc resolves the latest authorized declaration from the local
// store before considering a campfire for purging.
//
// Authority model (mirrors convention declarations):
//   - persistent — accepted from ANY member. Protecting a campfire is the
//     safe direction; a member pinning a shared campfire cannot destroy data.
//   - ephemeral / bounded — accepted only when campfire-key-signed (the
//     sender IS the campfire). These declarations make the campfire
//     deletable, so a lone member must not be able to schedule destruction
//     of other members' local copies.
//
// `cf lifecycle` signs every declaration with the campfire key (members on
// the filesystem transport hold campfire.cbor), satisfying both rules.

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	durability "github.com/campfire-net/campfire/cf-conventions/cf-durability"
	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cftransport "github.com/campfire-net/campfire/cf-protocol/transport"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/spf13/cobra"
)

// lifecycleTagPrefix is the message-tag prefix carrying a campfire lifecycle
// declaration (Durability Convention v0.1).
const lifecycleTagPrefix = "durability:lifecycle:"

// campfireLifecycle is a campfire's resolved continuity intention.
type campfireLifecycle struct {
	Type     durability.LifecycleType
	Value    string // ephemeral timeout or bounded date; "" for persistent
	Declared bool   // false: no (authorized, valid) declaration — permanent-by-default
}

// lifecycleTimestampSkew is how far into the future a declaration's
// sender-asserted timestamp may sit and still be honored. Beyond it the
// declaration is ignored until its time actually comes: timestamps are chosen
// freely by the signer, so without this clamp a member could stamp a
// declaration with a far-future time and permanently win newest-first
// resolution against every later campfire-key declaration (campfireagent-a4b).
const lifecycleTimestampSkew = 5 * time.Minute

// resolveCampfireLifecycle returns the campfire's effective lifecycle from
// the durability:lifecycle:* messages in the local store, as of nowNano.
//
// Resolution is AUTHORITY-TIERED, then newest-first within a tier:
//
//  1. Campfire-key-signed declarations (sender == campfire pubkey) carry
//     operational authority and always outrank member declarations — a lone
//     member must not be able to override the campfire's own continuity
//     schedule, in either direction (campfireagent-c4f, campfireagent-dae).
//  2. Member declarations are honored only for PERSISTENT (the protective
//     direction), and only when the campfire key has not declared at all —
//     any member may protect an otherwise-undeclared campfire, but cannot
//     loosen or pin one the campfire has spoken for.
//
// Within each tier the newest non-future-stamped declaration wins (see
// lifecycleTimestampSkew). Malformed values are skipped. No declaration
// means permanent-by-default per the convention.
//
// Sender is trustworthy here: every message that enters the store through a
// sync path is Ed25519-verified against its Sender key before insertion.
func resolveCampfireLifecycle(s store.Store, campfireID string, nowNano int64) (campfireLifecycle, error) {
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		TagPrefixes: []string{lifecycleTagPrefix},
		Reverse:     true, // newest first
	})
	if err != nil {
		return campfireLifecycle{}, fmt.Errorf("listing lifecycle declarations: %w", err)
	}
	maxTimestamp := nowNano + lifecycleTimestampSkew.Nanoseconds()

	// Tier 1: campfire-key declarations, newest first.
	for _, m := range msgs {
		if m.Timestamp > maxTimestamp {
			continue // future-stamped — not yet (campfireagent-a4b)
		}
		if m.Sender != campfireID {
			continue
		}
		if lt, lv, ok := parseLifecycleTags(m.Tags); ok {
			return campfireLifecycle{Type: lt, Value: lv, Declared: true}, nil
		}
	}

	// Tier 2: member declarations — persistent only, newest first.
	for _, m := range msgs {
		if m.Timestamp > maxTimestamp {
			continue
		}
		if m.Sender == campfireID {
			continue // already considered in tier 1
		}
		if lt, lv, ok := parseLifecycleTags(m.Tags); ok && lt == durability.LifecyclePersistent {
			return campfireLifecycle{Type: lt, Value: lv, Declared: true}, nil
		}
	}

	return campfireLifecycle{}, nil
}

// parseLifecycleTags returns the first well-formed lifecycle declaration among
// the message's durability:lifecycle:* tags, skipping malformed values.
func parseLifecycleTags(tags []string) (durability.LifecycleType, string, bool) {
	for _, tag := range tags {
		if !strings.HasPrefix(tag, lifecycleTagPrefix) {
			continue
		}
		lt, lv, err := durability.ParseLifecycle(strings.TrimPrefix(tag, lifecycleTagPrefix))
		if err != nil {
			continue // malformed declaration — ignore
		}
		return lt, lv, true
	}
	return "", "", false
}

// publishLifecycleDeclaration posts a campfire-key-signed lifecycle
// declaration message to the campfire (transport + local store) and returns
// the message ID. decl must already be validated with ParseLifecycle.
func publishLifecycleDeclaration(s store.Store, tr *fs.Transport, campfireID, decl string) (string, error) {
	// Encrypted campfires reject plaintext store inserts (downgrade
	// prevention), which would leave the declaration on the transport but
	// invisible to gc — a silent half-write (campfireagent-904). Refuse with
	// a clear error instead.
	if m, mErr := s.GetMembership(campfireID); mErr == nil && m != nil && m.Encrypted {
		return "", fmt.Errorf("lifecycle declarations are not yet supported for encrypted campfires (the plaintext declaration payload would be rejected by downgrade prevention)")
	}

	state, err := tr.ReadState(campfireID)
	if err != nil {
		return "", fmt.Errorf("reading campfire state: %w", err)
	}
	members, err := tr.ListMembers(campfireID)
	if err != nil {
		return "", fmt.Errorf("listing members: %w", err)
	}

	signer, err := message.NewEd25519Signer(
		ed25519.PrivateKey(state.PrivateKey),
		ed25519.PublicKey(state.PublicKey),
	)
	if err != nil {
		return "", fmt.Errorf("creating campfire-key signer: %w", err)
	}
	payload, _ := json.Marshal(map[string]string{"lifecycle": decl})
	msg, err := message.NewMessage(signer, payload, []string{lifecycleTagPrefix + decl}, nil)
	if err != nil {
		return "", fmt.Errorf("creating declaration message: %w", err)
	}

	cf := campfireFromState(state, members)
	if err := msg.AddHop(
		state.PrivateKey, state.PublicKey,
		cf.MembershipHash(), len(members),
		state.JoinProtocol, state.ReceptionRequirements,
		campfire.RoleFull,
	); err != nil {
		return "", fmt.Errorf("adding provenance hop: %w", err)
	}

	if err := tr.WriteMessage(campfireID, msg); err != nil {
		return "", fmt.Errorf("writing declaration message: %w", err)
	}
	// Store it locally too, so this machine's gc sees the declaration without
	// waiting for the next sync.
	if _, err := s.AddMessage(store.MessageRecordFromMessage(campfireID, msg, store.NowNano())); err != nil {
		return "", fmt.Errorf("storing declaration message: %w", err)
	}
	return msg.ID, nil
}

var lifecycleCmd = &cobra.Command{
	Use:   "lifecycle <campfire> [persistent|ephemeral:<duration>|bounded:<iso8601>]",
	Short: "Declare or inspect a campfire's continuity intention (Durability Convention)",
	Long: `Declare or inspect a campfire's lifecycle.

With no declaration argument, prints the campfire's effective lifecycle as
resolved from the latest authorized durability:lifecycle declaration in the
local store. "undeclared" means permanent-by-default: the campfire is not
eligible for 'cf gc' unless --include-undeclared is passed.

With a declaration argument, posts a campfire-key-signed declaration message:

  persistent              never a gc candidate, no matter how idle
  ephemeral:<duration>    gc candidate once idle longer than <duration> (e.g. 72h);
                          the clock runs from the campfire's newest message —
                          including the declaration itself, so (re)declaring
                          restarts the timeout
  bounded:<iso8601>       gc candidate once the date passes (e.g. 2027-01-01T00:00:00Z)

Authority: campfire-key-signed declarations always outrank member-posted ones.
A member may post 'persistent' to protect an otherwise-undeclared campfire,
but cannot override a campfire-key declaration in either direction.

The declaration is part of the campfire's signed message history: it syncs to
every member, so one declaration protects the campfire on all machines.

Examples:
  cf lifecycle ed4b6d62d996                 # show effective lifecycle
  cf lifecycle ed4b6d62d996 persistent      # protect forever
  cf lifecycle swarm-cf ephemeral:72h       # reclaim after 3 idle days`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")

		_, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		campfireID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}

		// Inspect mode.
		if len(args) == 1 {
			lc, err := resolveCampfireLifecycle(s, campfireID, time.Now().UnixNano())
			if err != nil {
				return err
			}
			if jsonOut {
				out := map[string]any{
					"campfire_id": campfireID,
					"declared":    lc.Declared,
				}
				if lc.Declared {
					out["lifecycle"] = string(lc.Type)
					if lc.Value != "" {
						out["value"] = lc.Value
					}
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			if !lc.Declared {
				fmt.Println("undeclared (permanent-by-default — not gc-eligible without --include-undeclared)")
				return nil
			}
			if lc.Value != "" {
				fmt.Printf("%s:%s\n", lc.Type, lc.Value)
			} else {
				fmt.Println(string(lc.Type))
			}
			return nil
		}

		// Declare mode.
		decl := args[1]
		if _, _, err := durability.ParseLifecycle(decl); err != nil {
			return fmt.Errorf("invalid lifecycle declaration: %w", err)
		}

		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("reading membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", shortID12(campfireID))
		}
		// Declarations are filesystem-transport messages. On relay/p2p-http
		// campfires a local fs write would never reach other members (their
		// copies sync from the relay), silently failing the "one declaration
		// protects everywhere" promise — refuse instead.
		if cftransport.ResolveType(*m) != cftransport.TypeFilesystem {
			return fmt.Errorf("cf lifecycle supports filesystem campfires; %s uses the %s transport", shortID12(campfireID), m.TransportType)
		}
		tr := fs.ForDir(m.TransportDir)

		msgID, err := publishLifecycleDeclaration(s, tr, campfireID, decl)
		if err != nil {
			return err
		}

		if jsonOut {
			out := map[string]any{
				"campfire_id": campfireID,
				"lifecycle":   decl,
				"message_id":  msgID,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		fmt.Printf("declared %s on %s (message %s)\n", decl, shortID12(campfireID), msgID)
		return nil
	},
}

// shortID12 returns the first 12 characters of an ID for display.
func shortID12(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// lifecycleBoundedElapsed reports whether a bounded lifecycle's date has
// passed. Malformed dates (which ParseLifecycle should have prevented) are
// treated as NOT elapsed — never delete on a value we cannot read.
func lifecycleBoundedElapsed(value string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return false
	}
	return now.After(t)
}

// lifecycleEphemeralCutoff returns the idle cutoff (in nanoseconds) for an
// ephemeral lifecycle: a campfire is eligible when its newest activity is
// older than the declared timeout. Malformed timeouts return ok=false —
// never delete on a value we cannot read.
func lifecycleEphemeralCutoff(value string, nowNano int64) (int64, bool) {
	d, err := durability.ParseMaxTTL(value)
	if err != nil || d <= 0 {
		return 0, false
	}
	return nowNano - d.Nanoseconds(), true
}

func init() {
	lifecycleCmd.Flags().Bool("json", false, "emit JSON")
	rootCmd.AddCommand(lifecycleCmd)
}

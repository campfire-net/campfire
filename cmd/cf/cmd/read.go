package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/spf13/cobra"
)


// campfireEntry pairs a campfire ID with its membership record for read operations.
type campfireEntry struct {
	id         string
	membership *store.Membership
}

// resolveCampfireEntries resolves which campfires to read from and builds the
// campfireEntry list. If args contains a campfire ID, only that campfire is used.
// Otherwise all memberships are returned, auto-joining the project root if needed.
func resolveCampfireEntries(args []string, agentID *identity.Identity, s *store.Store) ([]string, []campfireEntry, error) {
	var campfireIDs []string
	if len(args) > 0 {
		resolved, err := resolveCampfireID(args[0], s)
		if err != nil {
			return nil, nil, err
		}
		campfireIDs = []string{resolved}
	} else {
		// No explicit campfire — auto-join the project root if not yet a member.
		if rootID, _, ok := ProjectRoot(); ok {
			m, err := s.GetMembership(rootID)
			if err != nil {
				return nil, nil, fmt.Errorf("querying membership: %w", err)
			}
			if m == nil {
				if err := autoJoinRootCampfire(rootID, agentID, s); err != nil {
					return nil, nil, fmt.Errorf("auto-joining root campfire: %w", err)
				}
			}
		}

		memberships, err := s.ListMemberships()
		if err != nil {
			return nil, nil, fmt.Errorf("listing memberships: %w", err)
		}
		for _, m := range memberships {
			campfireIDs = append(campfireIDs, m.CampfireID)
		}
	}

	var entries []campfireEntry
	for _, cfID := range campfireIDs {
		m, err := s.GetMembership(cfID)
		if err != nil || m == nil {
			continue
		}
		entries = append(entries, campfireEntry{id: cfID, membership: m})
	}
	return campfireIDs, entries, nil
}

// runFollowMode runs the --follow polling loop: sync → query → print → sleep,
// until a SIGINT/SIGTERM is received. Cursor advancement respects peek and all flags.
func runFollowMode(entries []campfireEntry, agentID *identity.Identity, s *store.Store, fieldSet map[string]bool, all, peek bool, tagFilters []string, senderFilter string) error {
	// Determine poll interval — use the shortest interval across all campfires.
	interval := 2 * time.Second
	for _, e := range entries {
		if i := followIntervalForTransport(*e.membership); i < interval {
			interval = i
		}
	}

	// Set up signal handling for clean exit.
	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stopCh)

	// Show description headers once.
	shown := map[string]bool{}
	for _, e := range entries {
		if !shown[e.id] {
			shown[e.id] = true
			if e.membership.Description != "" {
				fmt.Printf("# %s\n", e.membership.Description)
			}
		}
	}

	// Track cursors per campfire for detecting new messages.
	cursors := map[string]int64{}
	if !all {
		for _, e := range entries {
			c, _ := s.GetReadCursor(e.id)
			cursors[e.id] = c
		}
	}

	for {
		// Check for stop signal (non-blocking).
		select {
		case <-stopCh:
			return nil
		default:
		}

		// Sync all campfires.
		for _, e := range entries {
			syncCampfire(e.id, e.membership, agentID, s)
		}

		// Query new messages since last cursor.
		var newMessages []store.MessageRecord
		for _, e := range entries {
			msgs, err := s.ListMessages(e.id, cursors[e.id])
			if err != nil {
				continue
			}
			newMessages = append(newMessages, msgs...)
		}

		// Apply post-query filters for display.
		// Cursor advances based on ALL new messages (pre-filter) so filtered-out
		// messages don't re-appear on the next poll.
		if len(newMessages) > 0 {
			printMessagesWithFields(filterMessages(newMessages, tagFilters, senderFilter), s, fieldSet)

			if !peek {
				for _, m := range newMessages {
					if m.Timestamp > cursors[m.CampfireID] {
						cursors[m.CampfireID] = m.Timestamp
					}
				}
				for cfID, ts := range cursors {
					s.SetReadCursor(cfID, ts) //nolint:errcheck
				}
			}
		}

		// Sleep with signal check.
		select {
		case <-stopCh:
			return nil
		case <-time.After(interval):
		}
	}
}

// runOneShotMode performs a single sync → query → print → cursor-advance cycle.
// Compaction is respected unless all is set. Cursor advancement is skipped for
// all and peek modes.
func runOneShotMode(campfireIDs []string, entries []campfireEntry, agentID *identity.Identity, s *store.Store, fieldSet map[string]bool, all, peek bool, tagFilters []string, senderFilter string) error {
	// Sync all campfires.
	for _, e := range entries {
		syncCampfire(e.id, e.membership, agentID, s)
	}

	// Fetch unfiltered messages first to compute pre-filter cursors, then fetch
	// filtered messages for display. This preserves the invariant that cursor
	// advancement accounts for ALL messages (so filtered-out messages don't
	// reappear on the next read), while pushing tag/sender filtering into SQL.
	preCursors := map[string]int64{}
	sqlFilter := store.MessageFilter{
		Tags:              tagFilters,
		Sender:            senderFilter,
		RespectCompaction: !all,
	}
	var allMessages []store.MessageRecord
	for _, cfID := range campfireIDs {
		var afterTS int64
		if !all {
			afterTS, _ = s.GetReadCursor(cfID)
		}
		unfiltered, err := s.ListMessages(cfID, afterTS)
		if err != nil {
			return fmt.Errorf("listing messages: %w", err)
		}
		for _, m := range unfiltered {
			if m.Timestamp > preCursors[m.CampfireID] {
				preCursors[m.CampfireID] = m.Timestamp
			}
		}
		filtered, err := s.ListMessages(cfID, afterTS, sqlFilter)
		if err != nil {
			return fmt.Errorf("listing messages (filtered): %w", err)
		}
		allMessages = append(allMessages, filtered...)
	}

	if jsonOutput {
		if err := encodeMessagesJSONWithFields(allMessages, fieldSet, os.Stdout); err != nil {
			return err
		}
	} else {
		// Show description header for each campfire with a description.
		shown := map[string]bool{}
		for _, cfID := range campfireIDs {
			if !shown[cfID] {
				shown[cfID] = true
				mem, _ := s.GetMembership(cfID)
				if mem != nil && mem.Description != "" {
					fmt.Printf("# %s\n", mem.Description)
				}
			}
		}
		if len(allMessages) == 0 {
			fmt.Println("No new messages.")
		}
		printMessagesWithFields(allMessages, s, fieldSet)
	}

	// Update read cursors from pre-filter timestamps (unless all or peek).
	if !all && !peek && len(preCursors) > 0 {
		for cfID, ts := range preCursors {
			s.SetReadCursor(cfID, ts) //nolint:errcheck
		}
	}
	return nil
}

var readCmd = &cobra.Command{
	Use:   "read [campfire-id]",
	Short: "Read messages",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		readAll, _ := cmd.Flags().GetBool("all")
		readPeek, _ := cmd.Flags().GetBool("peek")
		readFollow, _ := cmd.Flags().GetBool("follow")
		readPull, _ := cmd.Flags().GetString("pull")
		readTagFilters, _ := cmd.Flags().GetStringArray("tag")
		readSenderFilter, _ := cmd.Flags().GetString("sender")
		readFields, _ := cmd.Flags().GetString("fields")

		// --pull is mutually exclusive with --all, --peek, --follow.
		// Parse --fields early so we can error before any I/O.
		fieldSet, err := parseFieldSet(readFields)
		if err != nil {
			return err
		}

		if readPull != "" {
			if readAll || readPeek || readFollow {
				return fmt.Errorf("--pull is mutually exclusive with --all, --peek, and --follow")
			}
			return runPull(readPull, fieldSet)
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		campfireIDs, entries, err := resolveCampfireEntries(args, agentID, s)
		if err != nil {
			return err
		}

		if readFollow {
			return runFollowMode(entries, agentID, s, fieldSet, readAll, readPeek, readTagFilters, readSenderFilter)
		}
		return runOneShotMode(campfireIDs, entries, agentID, s, fieldSet, readAll, readPeek, readTagFilters, readSenderFilter)
	},
}

// runPull fetches specific messages by ID (comma-separated) from the local store.
// It does NOT advance the read cursor and does NOT sync transports.
// fieldSet controls which fields appear in output; nil means all fields.
func runPull(idsArg string, fieldSet map[string]bool) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ids := strings.Split(idsArg, ",")
	var messages []store.MessageRecord
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		msg, err := s.GetMessageByPrefix(id)
		if err != nil {
			return err
		}
		if msg == nil {
			return fmt.Errorf("message not found: %s", id)
		}
		messages = append(messages, *msg)
	}

	if jsonOutput {
		return encodeMessagesJSONWithFields(messages, fieldSet, os.Stdout)
	}

	printMessagesWithFields(messages, s, fieldSet)
	return nil
}

func init() {
	readCmd.Flags().Bool("all", false, "show all messages (not just unread)")
	readCmd.Flags().Bool("peek", false, "show unread messages without updating cursor")
	readCmd.Flags().Bool("follow", false, "stream messages in real time (NAT mode: keep polling)")
	readCmd.Flags().String("pull", "", "fetch specific messages by ID (comma-separated)")
	readCmd.Flags().String("endpoint", "", "this agent's own HTTP endpoint (empty = NAT mode, poll peers)")
	readCmd.Flags().StringArray("tag", nil, "filter messages by tag (OR semantics, repeatable)")
	readCmd.Flags().String("sender", "", "filter messages by sender hex prefix")
	readCmd.Flags().String("fields", "", "comma-separated list of fields to include (e.g. id,sender,payload); omit for all fields")
	rootCmd.AddCommand(readCmd)
}

package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
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
func resolveCampfireEntries(args []string, agentID *identity.Identity, s store.Store) ([]string, []campfireEntry, error) {
	var campfireIDs []string
	if len(args) > 0 {
		resolved, err := resolveCampfireID(args[0], s)
		if err != nil {
			return nil, nil, err
		}
		campfireIDs = []string{resolved}
	} else if ctxID, err := resolveImplicitCampfire(); err != nil {
		return nil, nil, err
	} else if ctxID != "" {
		campfireIDs = []string{ctxID}
	} else {
		// No explicit campfire or context — auto-join the project root if not yet a member.
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

// runFollowMode runs the --follow polling loop: read → print → sleep,
// until a SIGINT/SIGTERM is received. Cursor advancement respects peek and all flags.
func runFollowMode(entries []campfireEntry, agentID *identity.Identity, s store.Store, fieldSet map[string]bool, all, peek bool, mf store.MessageFilter) error {
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

	client := protocol.New(s, agentID)

	for {
		// Check for stop signal (non-blocking).
		select {
		case <-stopCh:
			return nil
		default:
		}

		// Read new messages since last cursor via protocol.Client (includes sync).
		var newMessages []protocol.Message
		for _, e := range entries {
			result, err := client.Read(protocol.ReadRequest{
				CampfireID:       e.id,
				Tags:             mf.Tags,
				TagPrefixes:      mf.TagPrefixes,
				ExcludeTags:      mf.ExcludeTags,
				ExcludeTagPrefixes: mf.ExcludeTagPrefixes,
				Sender:           mf.Sender,
				AfterTimestamp:   cursors[e.id],
				IncludeCompacted: all,
			})
			if err != nil {
				continue
			}
			// Advance cursor based on pre-filter MaxTimestamp so filtered-out
			// messages don't re-appear on the next poll.
			if !peek && result.MaxTimestamp > cursors[e.id] {
				cursors[e.id] = result.MaxTimestamp
				s.SetReadCursor(e.id, result.MaxTimestamp) //nolint:errcheck
			}
			newMessages = append(newMessages, result.Messages...)
		}

		if len(newMessages) > 0 {
			printMessagesWithFields(newMessages, s, fieldSet)
		}

		// Sleep with signal check.
		select {
		case <-stopCh:
			return nil
		case <-time.After(interval):
		}
	}
}

// runOneShotMode performs a single read → print → cursor-advance cycle via
// protocol.Client. Compaction is respected unless all is set. Cursor advancement
// is skipped for all and peek modes.
func runOneShotMode(campfireIDs []string, entries []campfireEntry, agentID *identity.Identity, s store.Store, fieldSet map[string]bool, all, peek bool, mf store.MessageFilter) error {
	client := protocol.New(s, agentID)

	preCursors := map[string]int64{}
	var allMessages []protocol.Message
	for _, cfID := range campfireIDs {
		var afterTS int64
		if !all {
			afterTS, _ = s.GetReadCursor(cfID)
		}
		result, err := client.Read(protocol.ReadRequest{
			CampfireID:       cfID,
			Tags:             mf.Tags,
			TagPrefixes:      mf.TagPrefixes,
			ExcludeTags:      mf.ExcludeTags,
			ExcludeTagPrefixes: mf.ExcludeTagPrefixes,
			Sender:           mf.Sender,
			AfterTimestamp:   afterTS,
			IncludeCompacted: all,
		})
		if err != nil {
			return fmt.Errorf("reading campfire %s: %w", cfID[:min(12, len(cfID))], err)
		}
		allMessages = append(allMessages, result.Messages...)
		if result.MaxTimestamp > preCursors[cfID] {
			preCursors[cfID] = result.MaxTimestamp
		}
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
		readTagFiltersRaw, _ := cmd.Flags().GetStringArray("tag")
		readExcludeTagsRaw, _ := cmd.Flags().GetStringArray("exclude-tag")
		readSenderFilter, _ := cmd.Flags().GetString("sender")
		readConvention, _ := cmd.Flags().GetString("convention")
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

		// Build message filter from flags.
		// --convention is sugar: --convention galtrader is equivalent to
		// --tag 'galtrader:*' --exclude-tag convention:operation
		if readConvention != "" {
			readTagFiltersRaw = append(readTagFiltersRaw, readConvention+":*")
			readExcludeTagsRaw = append(readExcludeTagsRaw, "convention:operation")
		}

		// Split glob patterns: "galtrader:*" → TagPrefixes, exact → Tags.
		var readTagFilters, readTagPrefixes []string
		for _, t := range readTagFiltersRaw {
			if strings.HasSuffix(t, "*") {
				readTagPrefixes = append(readTagPrefixes, strings.TrimSuffix(t, "*"))
			} else {
				readTagFilters = append(readTagFilters, t)
			}
		}
		var readExcludeTags, readExcludeTagPrefixes []string
		for _, t := range readExcludeTagsRaw {
			if strings.HasSuffix(t, "*") {
				readExcludeTagPrefixes = append(readExcludeTagPrefixes, strings.TrimSuffix(t, "*"))
			} else {
				readExcludeTags = append(readExcludeTags, t)
			}
		}

		mf := store.MessageFilter{
			Tags:               readTagFilters,
			TagPrefixes:        readTagPrefixes,
			ExcludeTags:        readExcludeTags,
			ExcludeTagPrefixes: readExcludeTagPrefixes,
			Sender:             readSenderFilter,
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
			return runFollowMode(entries, agentID, s, fieldSet, readAll, readPeek, mf)
		}
		return runOneShotMode(campfireIDs, entries, agentID, s, fieldSet, readAll, readPeek, mf)
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
	var messages []protocol.Message
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
		messages = append(messages, protocol.MessageFromRecord(*msg))
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
	readCmd.Flags().StringArray("tag", nil, "filter by tag (OR semantics, repeatable; suffix with * for prefix match)")
	readCmd.Flags().StringArray("exclude-tag", nil, "exclude messages with tag (repeatable; suffix with * for prefix match)")
	readCmd.Flags().String("convention", "", "filter to a convention's commands (sugar for --tag '<slug>:*' --exclude-tag convention:operation)")
	readCmd.Flags().String("sender", "", "filter messages by sender hex prefix")
	readCmd.Flags().String("fields", "", "comma-separated list of fields to include (e.g. id,sender,payload); omit for all fields")
	rootCmd.AddCommand(readCmd)
}

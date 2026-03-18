package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	ghtr "github.com/campfire-net/campfire/pkg/transport/github"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var (
	readAll          bool
	readPeek         bool
	readFollow       bool
	readPull         string
	readSelfEndpoint string
	readTagFilters   []string
	readSenderFilter string
)

// natPollConfig holds all parameters for the NAT poll loop.
type natPollConfig struct {
	campfireID  string
	peers       []store.PeerEndpoint
	cursor      int64
	follow      bool
	id          *identity.Identity
	timeoutSecs int
	// st is used to resolve key display names. May be nil (falls back to unknown://).
	st *store.Store
	// stopCh receives a signal to terminate the loop. If nil, runNATPoll
	// registers its own SIGINT/SIGTERM handler.
	stopCh chan os.Signal
}

// errNoReachablePeers is returned by runNATPoll when no non-empty peer endpoints exist.
var errNoReachablePeers = errors.New("no reachable peers to poll")

// computeInitialCursor derives the starting poll cursor from the local store.
// Returns the maximum ReceivedAt nanosecond timestamp across all messages in
// the campfire, or 0 if the store is empty.
func computeInitialCursor(s *store.Store, campfireID string) (int64, error) {
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		return 0, fmt.Errorf("listing messages for cursor: %w", err)
	}
	var max int64
	for _, m := range msgs {
		if m.ReceivedAt > max {
			max = m.ReceivedAt
		}
	}
	return max, nil
}

// runNATPoll is the NAT-mode poll loop. It polls the first reachable peer and
// prints received messages to w. When cfg.follow is false, it exits after the
// first successful response (even if empty). When cfg.follow is true, it loops
// until cfg.stopCh receives a signal.
func runNATPoll(cfg natPollConfig, w io.Writer) error {
	// Filter to peers with non-empty endpoints.
	var peers []store.PeerEndpoint
	for _, p := range cfg.peers {
		if p.Endpoint != "" {
			peers = append(peers, p)
		}
	}
	if len(peers) == 0 {
		return errNoReachablePeers
	}

	// Set up signal handling if no external stopCh was provided.
	stopCh := cfg.stopCh
	if stopCh == nil {
		stopCh = make(chan os.Signal, 1)
		signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(stopCh)
	}

	cursor := cfg.cursor
	peerIdx := 0
	timeout := cfg.timeoutSecs
	if timeout <= 0 {
		timeout = 30
	}

	for {
		// Check for stop signal (non-blocking).
		select {
		case <-stopCh:
			return nil
		default:
		}

		msgs, newCursor, err := cfhttp.Poll(peers[peerIdx].Endpoint, cfg.campfireID, cursor, timeout, cfg.id)
		if err != nil {
			// Rotate to next peer on error.
			peerIdx = (peerIdx + 1) % len(peers)
			time.Sleep(1 * time.Second)
			// Re-check stop after sleep.
			select {
			case <-stopCh:
				return nil
			default:
			}
			if !cfg.follow {
				// In one-shot mode, do not retry indefinitely; return after exhausting all peers once.
				if peerIdx == 0 {
					return fmt.Errorf("polling peers: %w", err)
				}
			}
			continue
		}

		if len(msgs) > 0 {
			cursor = newCursor
			printNATMessages(cfg.campfireID, msgs, w, cfg.st)
		}

		if !cfg.follow {
			break
		}

		// Check stop signal before blocking again.
		select {
		case <-stopCh:
			return nil
		default:
		}
	}
	return nil
}

// followIntervalForTransport returns the poll interval for --follow based on transport type.
// GitHub campfires use 5s to avoid API rate limiting; all others use 2s.
func followIntervalForTransport(transportDir, campfireID string) time.Duration {
	if isGitHubCampfire(transportDir) {
		return 5 * time.Second
	}
	return 2 * time.Second
}

// syncCampfire runs the appropriate sync function for a single campfire based on its transport.
func syncCampfire(cfID string, m *store.Membership, agentID *identity.Identity, s *store.Store) {
	if isGitHubCampfire(m.TransportDir) {
		syncFromGitHub(cfID, m.TransportDir, s)
	} else if isPeerHTTPCampfire(m.TransportDir, cfID) {
		syncFromHTTPPeers(cfID, agentID, s)
	} else {
		syncFromFilesystem(cfID, m.TransportDir, s)
	}
}

// printMessages prints message records in the standard human-readable format.
// Returns the messages that were printed (for cursor tracking).
func printMessages(allMessages []store.MessageRecord, s *store.Store) {
	if len(allMessages) == 0 {
		return
	}
	for _, m := range allMessages {
		var tags []string
		json.Unmarshal([]byte(m.Tags), &tags)
		var antecedents []string
		json.Unmarshal([]byte(m.Antecedents), &antecedents)

		cfShort := m.CampfireID
		if len(cfShort) > 6 {
			cfShort = cfShort[:6]
		}
		senderShort := m.Sender
		if len(senderShort) > 6 {
			senderShort = senderShort[:6]
		}
		senderDisplay := "agent:" + senderShort
		if m.Instance != "" {
			senderDisplay += " (" + m.Instance + ")"
		}
		ts := time.Unix(0, m.Timestamp).Format("2006-01-02 15:04:05")

		// Status markers
		var markers []string
		for _, t := range tags {
			if t == "future" {
				refs, _ := s.ListReferencingMessages(m.ID)
				fulfilled := false
				for _, ref := range refs {
					var refTags []string
					json.Unmarshal([]byte(ref.Tags), &refTags)
					for _, rt := range refTags {
						if rt == "fulfills" {
							fulfilled = true
						}
					}
				}
				if fulfilled {
					markers = append(markers, "fulfilled")
				} else {
					markers = append(markers, "future")
				}
			}
		}

		statusStr := ""
		if len(markers) > 0 {
			statusStr = " [" + strings.Join(markers, ", ") + "]"
		}

		fmt.Printf("[campfire:%s] %s %s%s\n", cfShort, ts, senderDisplay, statusStr)
		if len(tags) > 0 {
			fmt.Printf("  tags: %s\n", strings.Join(tags, ", "))
		}
		if len(antecedents) > 0 {
			shortAnts := make([]string, len(antecedents))
			for i, a := range antecedents {
				if len(a) > 8 {
					shortAnts[i] = a[:8]
				} else {
					shortAnts[i] = a
				}
			}
			fmt.Printf("  antecedents: %s\n", strings.Join(shortAnts, ", "))
		}
		fmt.Printf("  %s\n\n", string(m.Payload))
	}
}

var readCmd = &cobra.Command{
	Use:   "read [campfire-id]",
	Short: "Read messages",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// --pull is mutually exclusive with --all, --peek, --follow.
		if readPull != "" {
			if readAll || readPeek || readFollow {
				return fmt.Errorf("--pull is mutually exclusive with --all, --peek, and --follow")
			}
			return runPull(readPull)
		}

		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity: %w", err)
		}

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		// Determine which campfires to read from.
		var campfireIDs []string
		if len(args) > 0 {
			resolved, err := resolveCampfireID(args[0], s)
			if err != nil {
				return err
			}
			campfireIDs = []string{resolved}
		} else {
			// No explicit campfire — auto-join the project root if not yet a member.
			if rootID, _, ok := ProjectRoot(); ok {
				m, err := s.GetMembership(rootID)
				if err != nil {
					return fmt.Errorf("querying membership: %w", err)
				}
				if m == nil {
					if err := autoJoinRootCampfire(rootID, agentID, s); err != nil {
						return fmt.Errorf("auto-joining root campfire: %w", err)
					}
				}
			}

			memberships, err := s.ListMemberships()
			if err != nil {
				return fmt.Errorf("listing memberships: %w", err)
			}
			for _, m := range memberships {
				campfireIDs = append(campfireIDs, m.CampfireID)
			}
		}

		// Build membership lookup for campfires.
		type campfireEntry struct {
			id         string
			membership *store.Membership
		}
		var entries []campfireEntry
		for _, cfID := range campfireIDs {
			m, err := s.GetMembership(cfID)
			if err != nil || m == nil {
				continue
			}
			entries = append(entries, campfireEntry{id: cfID, membership: m})
		}

		// --follow: loop sync → query → print → sleep for ALL transports.
		if readFollow {
			// Determine poll interval from the first campfire's transport.
			// If following multiple campfires, use the shortest interval.
			interval := 2 * time.Second
			for _, e := range entries {
				i := followIntervalForTransport(e.membership.TransportDir, e.id)
				if i < interval {
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
				if shown[e.id] {
					continue
				}
				shown[e.id] = true
				if e.membership.Description != "" {
					fmt.Printf("# %s\n", e.membership.Description)
				}
			}

			// Track cursors per campfire for detecting new messages.
			cursors := map[string]int64{}
			if !readAll {
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
					afterTS := cursors[e.id]
					msgs, err := s.ListMessages(e.id, afterTS)
					if err != nil {
						continue
					}
					newMessages = append(newMessages, msgs...)
				}

				// Apply post-query filters for display.
				filteredMessages := filterMessages(newMessages, readTagFilters, readSenderFilter)

				// Print and advance cursors.
				// Note: cursor advances based on ALL new messages (pre-filter),
				// so filtered-out messages don't re-appear on the next poll.
				if len(newMessages) > 0 {
					printMessages(filteredMessages, s)

					// Update cursors (unless --peek).
					if !readPeek {
						for _, m := range newMessages {
							if m.Timestamp > cursors[m.CampfireID] {
								cursors[m.CampfireID] = m.Timestamp
							}
						}
						for cfID, ts := range cursors {
							s.SetReadCursor(cfID, ts)
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

		// One-shot mode: sync → query → print → exit.
		for _, e := range entries {
			syncCampfire(e.id, e.membership, agentID, s)
		}

		// Query messages.
		// Fetch all (unfiltered) first to compute pre-filter cursors, then fetch
		// filtered messages for display. This preserves the invariant that cursor
		// advancement accounts for ALL messages (so filtered-out messages don't
		// reappear on the next read), while pushing tag/sender filtering into SQL.
		preCursors := map[string]int64{}
		sqlFilter := store.MessageFilter{Tags: readTagFilters, Sender: readSenderFilter}
		var allMessages []store.MessageRecord
		for _, cfID := range campfireIDs {
			var afterTS int64
			if !readAll {
				afterTS, _ = s.GetReadCursor(cfID)
			}
			// Unfiltered fetch for cursor computation.
			unfiltered, err := s.ListMessages(cfID, afterTS)
			if err != nil {
				return fmt.Errorf("listing messages: %w", err)
			}
			for _, m := range unfiltered {
				if m.Timestamp > preCursors[m.CampfireID] {
					preCursors[m.CampfireID] = m.Timestamp
				}
			}
			// SQL-filtered fetch for display.
			filtered, err := s.ListMessages(cfID, afterTS, sqlFilter)
			if err != nil {
				return fmt.Errorf("listing messages (filtered): %w", err)
			}
			allMessages = append(allMessages, filtered...)
		}

		if jsonOutput {
			type jsonMsg struct {
				ID          string          `json:"id"`
				CampfireID  string          `json:"campfire_id"`
				Sender      string          `json:"sender"`
				Instance    string          `json:"instance,omitempty"`
				Payload     string          `json:"payload"`
				Tags        []string        `json:"tags"`
				Antecedents []string        `json:"antecedents"`
				Timestamp   int64           `json:"timestamp"`
				Provenance  json.RawMessage `json:"provenance"`
			}
			var out []jsonMsg
			for _, m := range allMessages {
				var tags []string
				json.Unmarshal([]byte(m.Tags), &tags)
				var antecedents []string
				json.Unmarshal([]byte(m.Antecedents), &antecedents)
				if antecedents == nil {
					antecedents = []string{}
				}
				out = append(out, jsonMsg{
					ID:          m.ID,
					CampfireID:  m.CampfireID,
					Sender:      m.Sender,
					Instance:    m.Instance,
					Payload:     string(m.Payload),
					Tags:        tags,
					Antecedents: antecedents,
					Timestamp:   m.Timestamp,
					Provenance:  json.RawMessage(m.Provenance),
				})
			}
			if out == nil {
				out = []jsonMsg{}
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(out); err != nil {
				return err
			}
		} else {
			// Show description header for each campfire with a description.
			shown := map[string]bool{}
			for _, cfID := range campfireIDs {
				if shown[cfID] {
					continue
				}
				shown[cfID] = true
				mem, _ := s.GetMembership(cfID)
				if mem != nil && mem.Description != "" {
					fmt.Printf("# %s\n", mem.Description)
				}
			}

			if len(allMessages) == 0 {
				fmt.Println("No new messages.")
			}
			printMessages(allMessages, s)
		}

		// Update read cursors from pre-filter timestamps (unless --all or --peek).
		if !readAll && !readPeek && len(preCursors) > 0 {
			for cfID, ts := range preCursors {
				s.SetReadCursor(cfID, ts)
			}
		}

		return nil
	},
}

// syncFromGitHub polls the GitHub Issue for new comments and stores verified messages
// in the local SQLite store. Non-fatal errors are silently ignored (caller continues).
func syncFromGitHub(cfID, transportDir string, s *store.Store) {
	meta, ok := parseGitHubTransportDir(transportDir)
	if !ok {
		return
	}

	token, err := resolveGitHubToken("", CFHome())
	if err != nil {
		// No token available — skip silently (offline mode).
		return
	}

	cfg := ghtr.Config{
		Repo:        meta.Repo,
		IssueNumber: meta.IssueNumber,
		Token:       token,
		BaseURL:     meta.BaseURL,
	}
	tr, err := ghtr.New(cfg, s)
	if err != nil {
		return
	}
	tr.RegisterCampfire(cfID, meta.IssueNumber)

	// Poll returns verified messages and stores them in SQLite internally.
	tr.Poll(cfID)
}

// syncFromFilesystem reads messages from the filesystem transport into the local store.
func syncFromFilesystem(cfID string, transportDir string, s *store.Store) {
	baseDir := fs.DefaultBaseDir()
	if transportDir != "" {
		baseDir = filepath.Dir(transportDir)
	}
	transport := fs.New(baseDir)
	fsMessages, err := transport.ListMessages(cfID)
	if err != nil {
		return
	}
	for _, fsMsg := range fsMessages {
		provJSON, err := json.Marshal(fsMsg.Provenance)
		if err != nil {
			continue
		}
		tagsJSON, err := json.Marshal(fsMsg.Tags)
		if err != nil {
			continue
		}
		antJSON, err := json.Marshal(fsMsg.Antecedents)
		if err != nil {
			continue
		}
		senderHex := fmt.Sprintf("%x", fsMsg.Sender)
		s.AddMessage(store.MessageRecord{ //nolint:errcheck
			ID:          fsMsg.ID,
			CampfireID:  cfID,
			Sender:      senderHex,
			Payload:     fsMsg.Payload,
			Tags:        string(tagsJSON),
			Antecedents: string(antJSON),
			Timestamp:   fsMsg.Timestamp,
			Signature:   fsMsg.Signature,
			Provenance:  string(provJSON),
			ReceivedAt:  store.NowNano(),
			Instance:    fsMsg.Instance,
		})
	}
}

// syncFromHTTPPeers pulls messages from all known peer endpoints for a p2p-http campfire.
func syncFromHTTPPeers(cfID string, agentID *identity.Identity, s *store.Store) {
	peers, err := s.ListPeerEndpoints(cfID)
	if err != nil {
		return
	}

	// Get the sync cursor for this campfire.
	since, _ := s.GetReadCursor(cfID)

	for _, peer := range peers {
		if peer.MemberPubkey == agentID.PublicKeyHex() || peer.Endpoint == "" {
			continue
		}
		msgs, err := cfhttp.Sync(peer.Endpoint, cfID, since, agentID)
		if err != nil {
			// Non-fatal: peer may be offline.
			continue
		}
		for _, msg := range msgs {
			tagsJSON, _ := json.Marshal(msg.Tags)
			anteJSON, _ := json.Marshal(msg.Antecedents)
			provJSON, _ := json.Marshal(msg.Provenance)
			senderHex := fmt.Sprintf("%x", msg.Sender)
			s.AddMessage(store.MessageRecord{ //nolint:errcheck
				ID:          msg.ID,
				CampfireID:  cfID,
				Sender:      senderHex,
				Payload:     msg.Payload,
				Tags:        string(tagsJSON),
				Antecedents: string(anteJSON),
				Timestamp:   msg.Timestamp,
				Signature:   msg.Signature,
				Provenance:  string(provJSON),
				ReceivedAt:  store.NowNano(),
				Instance:    msg.Instance,
			})
		}
	}
}

// printNATMessages prints messages received via long-poll to w in the same
// human-readable format as the direct-mode read path.
// campfireID is passed separately because message.Message has no CampfireID field.
func printNATMessages(campfireID string, msgs []message.Message, w io.Writer, s *store.Store) {
	cfShort := campfireID
	if len(cfShort) > 6 {
		cfShort = cfShort[:6]
	}
	for _, m := range msgs {
		senderHex := fmt.Sprintf("%x", m.Sender)
		senderShort := senderHex
		if len(senderShort) > 6 {
			senderShort = senderShort[:6]
		}
		senderDisplay := "agent:" + senderShort
		if m.Instance != "" {
			senderDisplay += " (" + m.Instance + ")"
		}
		ts := time.Unix(0, m.Timestamp).Format("2006-01-02 15:04:05")

		fmt.Fprintf(w, "[campfire:%s] %s %s\n", cfShort, ts, senderDisplay)
		if len(m.Tags) > 0 {
			fmt.Fprintf(w, "  tags: %s\n", strings.Join(m.Tags, ", "))
		}
		if len(m.Antecedents) > 0 {
			shortAnts := make([]string, len(m.Antecedents))
			for i, a := range m.Antecedents {
				if len(a) > 8 {
					shortAnts[i] = a[:8]
				} else {
					shortAnts[i] = a
				}
			}
			fmt.Fprintf(w, "  antecedents: %s\n", strings.Join(shortAnts, ", "))
		}
		fmt.Fprintf(w, "  %s\n\n", string(m.Payload))
	}
}

// runPull fetches specific messages by ID (comma-separated) from the local store.
// It does NOT advance the read cursor and does NOT sync transports.
func runPull(idsArg string) error {
	s, err := store.Open(store.StorePath(CFHome()))
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
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
		type jsonMsg struct {
			ID          string          `json:"id"`
			CampfireID  string          `json:"campfire_id"`
			Sender      string          `json:"sender"`
			Instance    string          `json:"instance,omitempty"`
			Payload     string          `json:"payload"`
			Tags        []string        `json:"tags"`
			Antecedents []string        `json:"antecedents"`
			Timestamp   int64           `json:"timestamp"`
			Provenance  json.RawMessage `json:"provenance"`
		}
		var out []jsonMsg
		for _, m := range messages {
			var tags []string
			json.Unmarshal([]byte(m.Tags), &tags)
			var antecedents []string
			json.Unmarshal([]byte(m.Antecedents), &antecedents)
			if antecedents == nil {
				antecedents = []string{}
			}
			out = append(out, jsonMsg{
				ID:          m.ID,
				CampfireID:  m.CampfireID,
				Sender:      m.Sender,
				Instance:    m.Instance,
				Payload:     string(m.Payload),
				Tags:        tags,
				Antecedents: antecedents,
				Timestamp:   m.Timestamp,
				Provenance:  json.RawMessage(m.Provenance),
			})
		}
		if out == nil {
			out = []jsonMsg{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	for _, m := range messages {
		var tags []string
		json.Unmarshal([]byte(m.Tags), &tags)
		var antecedents []string
		json.Unmarshal([]byte(m.Antecedents), &antecedents)

		cfShort := m.CampfireID
		if len(cfShort) > 6 {
			cfShort = cfShort[:6]
		}
		senderShort := m.Sender
		if len(senderShort) > 6 {
			senderShort = senderShort[:6]
		}
		senderDisplay := "agent:" + senderShort
		if m.Instance != "" {
			senderDisplay += " (" + m.Instance + ")"
		}
		ts := time.Unix(0, m.Timestamp).Format("2006-01-02 15:04:05")

		fmt.Printf("[campfire:%s] %s %s\n", cfShort, ts, senderDisplay)
		if len(tags) > 0 {
			fmt.Printf("  tags: %s\n", strings.Join(tags, ", "))
		}
		if len(antecedents) > 0 {
			shortAnts := make([]string, len(antecedents))
			for i, a := range antecedents {
				if len(a) > 8 {
					shortAnts[i] = a[:8]
				} else {
					shortAnts[i] = a
				}
			}
			fmt.Printf("  antecedents: %s\n", strings.Join(shortAnts, ", "))
		}
		fmt.Printf("  %s\n\n", string(m.Payload))
	}

	return nil
}

func init() {
	readCmd.Flags().BoolVar(&readAll, "all", false, "show all messages (not just unread)")
	readCmd.Flags().BoolVar(&readPeek, "peek", false, "show unread messages without updating cursor")
	readCmd.Flags().BoolVar(&readFollow, "follow", false, "stream messages in real time (NAT mode: keep polling)")
	readCmd.Flags().StringVar(&readPull, "pull", "", "fetch specific messages by ID (comma-separated)")
	readCmd.Flags().StringVar(&readSelfEndpoint, "endpoint", "", "this agent's own HTTP endpoint (empty = NAT mode, poll peers)")
	readCmd.Flags().StringArrayVar(&readTagFilters, "tag", nil, "filter messages by tag (OR semantics, repeatable)")
	readCmd.Flags().StringVar(&readSenderFilter, "sender", "", "filter messages by sender hex prefix")
	rootCmd.AddCommand(readCmd)
}

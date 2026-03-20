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
	"github.com/campfire-net/campfire/pkg/transport"
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
	readFields       string
)

// validFieldNames is the set of field names accepted by --fields.
var validFieldNames = map[string]bool{
	"id":          true,
	"sender":      true,
	"payload":     true,
	"tags":        true,
	"timestamp":   true,
	"antecedents": true,
	"signature":   true,
	"provenance":  true,
	"instance":    true,
	"campfire_id": true,
}

// parseFieldSet parses a comma-separated list of field names and returns a set.
// Returns (nil, nil) when s is empty (meaning all fields).
// Returns an error when any field name is unknown.
func parseFieldSet(s string) (map[string]bool, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	fs := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !validFieldNames[p] {
			return nil, fmt.Errorf("unknown field %q; valid fields: id, sender, payload, tags, timestamp, antecedents, signature, provenance, instance, campfire_id", p)
		}
		fs[p] = true
	}
	return fs, nil
}

// printSingleMessage renders one message in the canonical human-readable format to w.
// This is the shared formatting kernel used by printMessagesWithFields (default path)
// and printNATMessages so the display logic lives in exactly one place.
func printSingleMessage(w io.Writer, cfShort, ts, senderDisplay string, tags, antecedents []string, payload []byte) {
	fmt.Fprintf(w, "[campfire:%s] %s %s\n", cfShort, ts, senderDisplay)
	if len(tags) > 0 {
		fmt.Fprintf(w, "  tags: %s\n", strings.Join(tags, ", "))
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
		fmt.Fprintf(w, "  antecedents: %s\n", strings.Join(shortAnts, ", "))
	}
	fmt.Fprintf(w, "  %s\n\n", sanitizePayload(payload))
}

// printMessagesWithFields prints messages in human-readable format, filtering to
// only the requested fields. When fields is nil, all fields are printed using the
// original output format (backward compatible). When fields is non-nil, only the
// requested fields are included.
func printMessagesWithFields(allMessages []store.MessageRecord, s *store.Store, fields map[string]bool) {
	if len(allMessages) == 0 {
		return
	}

	// Default path: nil fields means all fields, use the original output format exactly.
	if fields == nil {
		for _, m := range allMessages {
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

			// Status markers (future/fulfilled) — appended to sender display.
			var markers []string
			if s != nil {
				for _, t := range m.Tags {
					if t == "future" {
						refs, _ := s.ListReferencingMessages(m.ID)
						fulfilled := false
						for _, ref := range refs {
							for _, rt := range ref.Tags {
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
			}
			if len(markers) > 0 {
				senderDisplay += " [" + strings.Join(markers, ", ") + "]"
			}

			printSingleMessage(os.Stdout, cfShort, ts, senderDisplay, m.Tags, m.Antecedents, m.Payload)
		}
		return
	}

	// Projection path: only emit the requested fields.
	for _, m := range allMessages {
		// Header line — always printed so output is parseable, but only includes
		// requested header-level fields.
		cfShort := m.CampfireID
		if len(cfShort) > 6 {
			cfShort = cfShort[:6]
		}
		senderShort := m.Sender
		if len(senderShort) > 6 {
			senderShort = senderShort[:6]
		}

		var headerParts []string
		if fields["campfire_id"] {
			headerParts = append(headerParts, fmt.Sprintf("[campfire:%s]", cfShort))
		}
		if fields["timestamp"] {
			ts := time.Unix(0, m.Timestamp).Format("2006-01-02 15:04:05")
			headerParts = append(headerParts, ts)
		}
		if fields["sender"] {
			senderDisplay := "agent:" + senderShort
			if fields["instance"] && m.Instance != "" {
				senderDisplay += " (" + m.Instance + ")"
			}
			headerParts = append(headerParts, senderDisplay)
		} else if fields["instance"] && m.Instance != "" {
			headerParts = append(headerParts, "("+m.Instance+")")
		}

		if len(headerParts) > 0 {
			fmt.Println(strings.Join(headerParts, " "))
		}

		if fields["id"] {
			idDisplay := m.ID
			if len(idDisplay) > 8 {
				idDisplay = idDisplay[:8]
			}
			fmt.Printf("  id: %s\n", idDisplay)
		}
		if fields["tags"] && len(m.Tags) > 0 {
			fmt.Printf("  tags: %s\n", strings.Join(m.Tags, ", "))
		}
		if fields["antecedents"] && len(m.Antecedents) > 0 {
			shortAnts := make([]string, len(m.Antecedents))
			for i, a := range m.Antecedents {
				if len(a) > 8 {
					shortAnts[i] = a[:8]
				} else {
					shortAnts[i] = a
				}
			}
			fmt.Printf("  antecedents: %s\n", strings.Join(shortAnts, ", "))
		}
		if fields["payload"] {
			fmt.Printf("  %s\n", sanitizePayload(m.Payload))
		}
		fmt.Println()
	}
}

// encodeMessagesJSONWithFields encodes messages to JSON on w, including only the
// fields specified in the fields set. When fields is nil, all fields are included.
func encodeMessagesJSONWithFields(allMessages []store.MessageRecord, fields map[string]bool, w io.Writer) error {
	all := fields == nil

	var out []map[string]interface{}
	for _, m := range allMessages {
		obj := make(map[string]interface{})
		if all || fields["id"] {
			obj["id"] = m.ID
		}
		if all || fields["campfire_id"] {
			obj["campfire_id"] = m.CampfireID
		}
		if all || fields["sender"] {
			obj["sender"] = m.Sender
		}
		if all || fields["instance"] {
			if m.Instance != "" {
				obj["instance"] = m.Instance
			}
		}
		if all || fields["payload"] {
			obj["payload"] = string(m.Payload)
		}
		if all || fields["tags"] {
			tags := m.Tags
			if tags == nil {
				tags = []string{}
			}
			obj["tags"] = tags
		}
		if all || fields["antecedents"] {
			antecedents := m.Antecedents
			if antecedents == nil {
				antecedents = []string{}
			}
			obj["antecedents"] = antecedents
		}
		if all || fields["timestamp"] {
			obj["timestamp"] = m.Timestamp
		}
		if all || fields["provenance"] {
			obj["provenance"] = m.Provenance
		}
		if all || fields["signature"] {
			obj["signature"] = m.Signature
		}
		out = append(out, obj)
	}
	if out == nil {
		out = []map[string]interface{}{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

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
	// tagFilters and senderFilter apply the same --tag/--sender semantics as
	// the direct-mode read path. Empty values mean no filtering.
	tagFilters   []string
	senderFilter string
}

// filterNATMessages applies tag and sender filters to a slice of message.Message.
// tagFilters uses OR semantics: a message matches if it has ANY of the specified tags.
// senderFilter matches on a hex prefix of the sender bytes (case-insensitive).
// Empty values mean no filtering.
func filterNATMessages(msgs []message.Message, tagFilters []string, senderFilter string) []message.Message {
	if len(tagFilters) == 0 && senderFilter == "" {
		return msgs
	}

	tagSet := make(map[string]bool, len(tagFilters))
	for _, t := range tagFilters {
		tagSet[strings.ToLower(t)] = true
	}
	senderPrefix := strings.ToLower(senderFilter)

	var result []message.Message
	for _, m := range msgs {
		senderHex := fmt.Sprintf("%x", m.Sender)
		if senderPrefix != "" && !strings.HasPrefix(strings.ToLower(senderHex), senderPrefix) {
			continue
		}
		if len(tagSet) > 0 {
			matched := false
			for _, tg := range m.Tags {
				if tagSet[strings.ToLower(tg)] {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		result = append(result, m)
	}
	return result
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
	// firstErr records the first poll error in one-shot mode so it is reported
	// instead of the last error when all peers have been tried.
	var firstErr error

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
			if !cfg.follow && firstErr == nil {
				firstErr = err
			}
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
					return fmt.Errorf("polling peers: %w", firstErr)
				}
			}
			continue
		}
		// Reset firstErr on success so subsequent failures report fresh errors.
		firstErr = nil

		if len(msgs) > 0 {
			cursor = newCursor
			filtered := filterNATMessages(msgs, cfg.tagFilters, cfg.senderFilter)
			if len(filtered) > 0 {
				printNATMessages(cfg.campfireID, filtered, w, cfg.st)
			}
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
func followIntervalForTransport(m store.Membership) time.Duration {
	if transport.ResolveType(m) == transport.TypeGitHub {
		return 5 * time.Second
	}
	return 2 * time.Second
}

// syncCampfire runs the appropriate sync function for a single campfire based on its transport.
func syncCampfire(cfID string, m *store.Membership, agentID *identity.Identity, s *store.Store) {
	switch transport.ResolveType(*m) {
	case transport.TypeGitHub:
		syncFromGitHub(cfID, m.TransportDir, s)
	case transport.TypePeerHTTP:
		syncFromHTTPPeers(cfID, agentID, s)
	default:
		syncFromFilesystem(cfID, m.TransportDir, s)
	}
}

// printMessages prints message records in the standard human-readable format.
// It is a backward-compatible wrapper around printMessagesWithFields with no field projection.
func printMessages(allMessages []store.MessageRecord, s *store.Store) {
	printMessagesWithFields(allMessages, s, nil)
}

// sanitizePayload strips terminal control characters (escape sequences and other
// non-printable bytes) from a message payload before displaying it. This prevents
// terminal injection via crafted message content.
func sanitizePayload(payload []byte) string {
	out := make([]byte, 0, len(payload))
	for _, b := range payload {
		// Allow printable ASCII (0x20-0x7E), tab (0x09), and newline (0x0A, 0x0D).
		// Reject ESC (0x1B) and all other control characters to block escape sequences.
		if b == 0x09 || b == 0x0A || b == 0x0D || (b >= 0x20 && b <= 0x7E) || b >= 0x80 {
			out = append(out, b)
		}
	}
	return string(out)
}

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
// until a SIGINT/SIGTERM is received. Cursor advancement respects --peek and --all.
func runFollowMode(entries []campfireEntry, agentID *identity.Identity, s *store.Store, fieldSet map[string]bool) error {
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
			printMessagesWithFields(filterMessages(newMessages, readTagFilters, readSenderFilter), s, fieldSet)

			if !readPeek {
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
// Compaction is respected unless --all is set. Cursor advancement is skipped for
// --all and --peek modes.
func runOneShotMode(campfireIDs []string, entries []campfireEntry, agentID *identity.Identity, s *store.Store, fieldSet map[string]bool) error {
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
		Tags:              readTagFilters,
		Sender:            readSenderFilter,
		RespectCompaction: !readAll,
	}
	var allMessages []store.MessageRecord
	for _, cfID := range campfireIDs {
		var afterTS int64
		if !readAll {
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

	// Update read cursors from pre-filter timestamps (unless --all or --peek).
	if !readAll && !readPeek && len(preCursors) > 0 {
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

		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity: %w", err)
		}

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		campfireIDs, entries, err := resolveCampfireEntries(args, agentID, s)
		if err != nil {
			return err
		}

		if readFollow {
			return runFollowMode(entries, agentID, s, fieldSet)
		}
		return runOneShotMode(campfireIDs, entries, agentID, s, fieldSet)
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
// Only messages with valid Ed25519 signatures are stored; invalid messages are silently
// skipped to prevent injection of unsigned content via shared filesystem directories.
// Provenance hops are also verified; any hop with an invalid signature is rejected.
func syncFromFilesystem(cfID string, transportDir string, s *store.Store) {
	baseDir := fs.DefaultBaseDir()
	if transportDir != "" {
		baseDir = filepath.Dir(transportDir)
	}
	fsTransport := fs.New(baseDir)
	fsMessages, err := fsTransport.ListMessages(cfID)
	if err != nil {
		return
	}
	for _, fsMsg := range fsMessages {
		// workspace-h0t: verify message signature before storing.
		if !fsMsg.VerifySignature() {
			continue
		}
		// workspace-h0t: verify all provenance hops.
		hopOK := true
		for _, hop := range fsMsg.Provenance {
			if !message.VerifyHop(fsMsg.ID, hop) {
				hopOK = false
				break
			}
		}
		if !hopOK {
			continue
		}
		s.AddMessage(store.MessageRecordFromMessage(cfID, &fsMsg, store.NowNano())) //nolint:errcheck
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
			s.AddMessage(store.MessageRecordFromMessage(cfID, &msg, store.NowNano())) //nolint:errcheck
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
		printSingleMessage(w, cfShort, ts, senderDisplay, m.Tags, m.Antecedents, m.Payload)
	}
}

// runPull fetches specific messages by ID (comma-separated) from the local store.
// It does NOT advance the read cursor and does NOT sync transports.
// fieldSet controls which fields appear in output; nil means all fields.
func runPull(idsArg string, fieldSet map[string]bool) error {
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
		return encodeMessagesJSONWithFields(messages, fieldSet, os.Stdout)
	}

	printMessagesWithFields(messages, s, fieldSet)
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
	readCmd.Flags().StringVar(&readFields, "fields", "", "comma-separated list of fields to include (e.g. id,sender,payload); omit for all fields")
	rootCmd.AddCommand(readCmd)
}

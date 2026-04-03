package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"github.com/spf13/cobra"
)

// NOTE: protocol.Bridge (pkg/protocol/bridge.go) re-publishes messages via
// client.Send(), which creates new messages with the bridge's own sender and
// signature. This CLI bridge RELAYS messages preserving the original sender and
// signature (provenance-preserving relay via cfhttp.DeliverToAll). This
// semantic difference is critical: relay maintains cryptographic attribution to
// the original author, while re-publish attributes messages to the bridge
// operator. Migrating to protocol.Bridge would break provenance verification
// for downstream consumers. A partial migration is not safe here — all message
// forwarding must preserve original sender/signature.

var bridgeCmd = &cobra.Command{
	Use:   "bridge [campfire-id]",
	Short: "Relay messages bidirectionally between filesystem and HTTP transports",
	Long: `Run a continuous bidirectional message pump between the local filesystem
transport and a remote HTTP transport endpoint. Messages written to the
filesystem appear at the HTTP endpoint and vice versa.

The bridge uses your agent identity. It joins the campfire on the HTTP
side (you are already a member on the filesystem side).

When --tag is specified, only messages matching any of the given tags are
relayed. Untagged messages are still stored locally but not forwarded.

Ctrl-C triggers graceful shutdown.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bridgeTo, _ := cmd.Flags().GetString("to")
		bridgeAll, _ := cmd.Flags().GetBool("all")
		tagFilters, _ := cmd.Flags().GetStringArray("tag")

		if bridgeTo == "" {
			return fmt.Errorf("--to is required (e.g. --to http://localhost:9000)")
		}
		if !bridgeAll && len(args) == 0 {
			return fmt.Errorf("either provide a campfire-id or use --all")
		}
		if bridgeAll && len(args) > 0 {
			return fmt.Errorf("--all and explicit campfire-id are mutually exclusive")
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		// Set up signal handling for graceful shutdown.
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		if bridgeAll {
			return runBridgeAll(ctx.Done(), agentID, s, bridgeTo, tagFilters)
		}

		campfireID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}

		// Verify membership.
		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", campfireID[:12])
		}

		fmt.Fprintf(os.Stderr, "bridging campfire %s to %s\n", campfireID[:12], bridgeTo)
		return runBridge(ctx.Done(), campfireID, m.TransportDir, agentID, s, bridgeTo, tagFilters)
	},
}

func init() {
	bridgeCmd.Flags().String("to", "", "HTTP endpoint to bridge to (required)")
	bridgeCmd.Flags().Bool("all", false, "bridge all filesystem campfires")
	bridgeCmd.Flags().StringArray("tag", nil, "only relay messages matching this tag (repeatable, OR semantics)")
	rootCmd.AddCommand(bridgeCmd)
}

// runBridge runs a bidirectional message pump for a single campfire.
// It blocks until done is closed.
// tagFilters, when non-empty, restricts relay to messages matching any of the given tags.
//
// Note: This bridge RELAYS messages (preserving original sender identity and
// provenance). protocol.Bridge re-publishes (creating new messages signed by
// the bridge agent). The relay semantic is correct here because consumers on
// both sides need to see the original author. When protocol.Bridge gains a
// relay mode, this can be migrated. The bridge uses protocol.Client for
// fs-side sync-before-read via client.Read(), keeping the manual pump for
// HTTP delivery (cfhttp.DeliverToAll) and HTTP pull sync (cfhttp.Sync).
func runBridge(done <-chan struct{}, campfireID, transportDir string, agentID *identity.Identity, s store.Store, httpEndpoint string, tagFilters []string) error {
	fsTransport := fs.ForDir(transportDir)

	// Use protocol.Client for sync-before-read on the fs side.
	client := protocol.New(s, agentID)

	// Build initial forwarded-ID set by scanning existing fs messages.
	forwarded := buildForwardedSet(campfireID, fsTransport, s)

	// Get the HTTP sync cursor.
	httpCursor, _ := s.GetReadCursor(campfireID)

	// Initialize membership sync state.
	memberState := newMembershipSyncState()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		// Sync fs messages into the store via protocol.Client (handles
		// signature and provenance verification).
		syncFSViaClient(client, campfireID)

		// Pump A: fs → store → HTTP (relay already-verified messages)
		pumpFSToHTTP(campfireID, fsTransport, s, agentID, httpEndpoint, forwarded, tagFilters)

		// Pump B: HTTP → store → fs
		httpCursor = pumpHTTPToFS(campfireID, fsTransport, s, agentID, httpEndpoint, httpCursor)

		// Sync membership between fs and HTTP transports.
		syncMembership(campfireID, fsTransport, s, agentID, httpEndpoint, memberState)

		select {
		case <-done:
			return nil
		case <-ticker.C:
		}
	}
}

// runBridgeAll discovers all filesystem campfires and bridges them.
// tagFilters, when non-empty, restricts relay to messages matching any of the given tags.
func runBridgeAll(done <-chan struct{}, agentID *identity.Identity, s store.Store, httpEndpoint string, tagFilters []string) error {
	type bridgeState struct {
		forwarded   map[string]bool
		httpCursor  int64
		memberState *membershipSyncState
	}

	baseDir := fs.DefaultBaseDir()
	fsTransport := fs.New(baseDir)
	bridges := make(map[string]*bridgeState)

	// Use protocol.Client for sync-before-read on the fs side.
	client := protocol.New(s, agentID)

	fmt.Fprintf(os.Stderr, "bridging all campfires in %s to %s\n", baseDir, httpEndpoint)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	rescanTicker := time.NewTicker(30 * time.Second)
	defer rescanTicker.Stop()

	scanCampfires := func() {
		ids := discoverFSCampfires(baseDir)
		for _, cfID := range ids {
			if _, exists := bridges[cfID]; exists {
				continue
			}
			forwarded := buildForwardedSet(cfID, fsTransport, s)
			cursor, _ := s.GetReadCursor(cfID)
			bridges[cfID] = &bridgeState{forwarded: forwarded, httpCursor: cursor, memberState: newMembershipSyncState()}
			fmt.Fprintf(os.Stderr, "discovered campfire %s\n", cfID[:min(12, len(cfID))])
		}
	}

	// Initial scan.
	scanCampfires()

	for {
		for cfID, bs := range bridges {
			syncFSViaClient(client, cfID)
			pumpFSToHTTP(cfID, fsTransport, s, agentID, httpEndpoint, bs.forwarded, tagFilters)
			bs.httpCursor = pumpHTTPToFS(cfID, fsTransport, s, agentID, httpEndpoint, bs.httpCursor)
			syncMembership(cfID, fsTransport, s, agentID, httpEndpoint, bs.memberState)
		}

		select {
		case <-done:
			return nil
		case <-rescanTicker.C:
			scanCampfires()
		case <-ticker.C:
		}
	}
}

// syncFSViaClient triggers a sync-before-read on the fs side via protocol.Client.
// This ensures fs messages are verified (signature + provenance) and stored in
// SQLite before the pump reads them. Errors are non-fatal (the store may have
// older messages that are still useful).
func syncFSViaClient(client *protocol.Client, campfireID string) {
	// Read with no filter and AfterTimestamp=0 triggers sync-before-query.
	// We don't need the result — the side effect (fs → store sync) is what matters.
	_, _ = client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Limit:      1, // minimize work — we only need the sync side-effect
	})
}

// pumpFSToHTTP reads fs messages into the store and delivers new ones to HTTP peers.
// tagFilters, when non-empty, applies OR semantics: only messages carrying at least one
// of the specified tags are relayed. Messages that don't match are still stored locally
// (so the cursor advances and they aren't reprocessed) but are not forwarded to HTTP.
func pumpFSToHTTP(campfireID string, fsTransport *fs.Transport, s store.Store, agentID *identity.Identity, httpEndpoint string, forwarded map[string]bool, tagFilters []string) {
	fsMessages, err := fsTransport.ListMessages(campfireID)
	if err != nil {
		return
	}

	endpoints := []string{httpEndpoint}

	for _, fsMsg := range fsMessages {
		if forwarded[fsMsg.ID] {
			continue
		}

		// Verify signature before storing.
		if !fsMsg.VerifySignature() {
			continue
		}
		// Reject messages with invalid or missing provenance hops.
		if !fsMsg.VerifyProvenance() {
			continue
		}

		// Store in local SQLite (dedup via INSERT OR IGNORE).
		s.AddMessage(store.MessageRecordFromMessage(campfireID, &fsMsg, store.NowNano())) //nolint:errcheck

		// Apply tag filter: relay only if no filter specified, or message matches any tag.
		if len(tagFilters) == 0 || messageMatchesAnyTag(&fsMsg, tagFilters) {
			msg := fsMsg // copy for pointer
			cfhttp.DeliverToAll(endpoints, campfireID, &msg, agentID)
		}

		forwarded[fsMsg.ID] = true
	}
}

// messageMatchesAnyTag returns true if the message has at least one tag in the filter list.
func messageMatchesAnyTag(msg *message.Message, tagFilters []string) bool {
	for _, want := range tagFilters {
		for _, have := range msg.Tags {
			if have == want {
				return true
			}
		}
	}
	return false
}

// pumpHTTPToFS pulls messages from the HTTP endpoint and writes them to the filesystem.
// The cursor is advanced using the maximum message.Timestamp from the returned messages,
// not store.NowNano(). handleSync interprets 'since' as afterTimestamp (creation time),
// so the cursor must live in that same space to avoid missing messages whose creation
// timestamp is earlier than the bridge's local wall clock.
func pumpHTTPToFS(campfireID string, fsTransport *fs.Transport, s store.Store, agentID *identity.Identity, httpEndpoint string, cursor int64) int64 {
	msgs, err := cfhttp.Sync(httpEndpoint, campfireID, cursor, agentID)
	if err != nil {
		return cursor
	}

	maxCursor := cursor
	for _, msg := range msgs {
		// Advance the cursor in message.Timestamp space (matching how handleSync
		// interprets the 'since' query parameter via store.ListMessages afterTimestamp).
		if msg.Timestamp > maxCursor {
			maxCursor = msg.Timestamp
		}

		// Store in local SQLite (dedup).
		inserted, _ := s.AddMessage(store.MessageRecordFromMessage(campfireID, &msg, store.NowNano()))
		if !inserted {
			continue
		}

		// Write to filesystem transport.
		m := msg                                 // copy for pointer
		fsTransport.WriteMessage(campfireID, &m) //nolint:errcheck
	}

	return maxCursor
}

// buildForwardedSet scans existing fs messages and returns a set of IDs
// that are already in the store (i.e., have already been processed).
func buildForwardedSet(campfireID string, fsTransport *fs.Transport, s store.Store) map[string]bool {
	forwarded := make(map[string]bool)
	fsMessages, err := fsTransport.ListMessages(campfireID)
	if err != nil {
		return forwarded
	}
	for _, msg := range fsMessages {
		has, _ := s.HasMessage(msg.ID)
		if has {
			forwarded[msg.ID] = true
		}
	}
	return forwarded
}

// discoverFSCampfires returns the IDs of all campfire directories under baseDir.
func discoverFSCampfires(baseDir string) []string {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		msgDir := filepath.Join(baseDir, e.Name(), "messages")
		if _, err := os.Stat(msgDir); err == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

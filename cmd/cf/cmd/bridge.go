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
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"github.com/spf13/cobra"
)

var bridgeCmd = &cobra.Command{
	Use:   "bridge [campfire-id]",
	Short: "Relay messages bidirectionally between filesystem and HTTP transports",
	Long: `Run a continuous bidirectional message pump between the local filesystem
transport and a remote HTTP transport endpoint. Messages written to the
filesystem appear at the HTTP endpoint and vice versa.

The bridge uses your agent identity. It joins the campfire on the HTTP
side (you are already a member on the filesystem side).

Ctrl-C triggers graceful shutdown.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bridgeTo, _ := cmd.Flags().GetString("to")
		bridgeAll, _ := cmd.Flags().GetBool("all")

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
			return runBridgeAll(ctx.Done(), agentID, s, bridgeTo)
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
		return runBridge(ctx.Done(), campfireID, m.TransportDir, agentID, s, bridgeTo)
	},
}

func init() {
	bridgeCmd.Flags().String("to", "", "HTTP endpoint to bridge to (required)")
	bridgeCmd.Flags().Bool("all", false, "bridge all filesystem campfires")
	rootCmd.AddCommand(bridgeCmd)
}

// runBridge runs a bidirectional message pump for a single campfire.
// It blocks until done is closed.
func runBridge(done <-chan struct{}, campfireID, transportDir string, agentID *identity.Identity, s *store.Store, httpEndpoint string) error {
	baseDir := fs.DefaultBaseDir()
	if transportDir != "" {
		baseDir = filepath.Dir(transportDir)
	}
	fsTransport := fs.New(baseDir)

	// Build initial forwarded-ID set by scanning existing fs messages.
	forwarded := buildForwardedSet(campfireID, fsTransport, s)

	// Get the HTTP sync cursor.
	httpCursor, _ := s.GetReadCursor(campfireID)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		// Pump A: fs → store → HTTP
		pumpFSToHTTP(campfireID, fsTransport, s, agentID, httpEndpoint, forwarded)

		// Pump B: HTTP → store → fs
		httpCursor = pumpHTTPToFS(campfireID, fsTransport, s, agentID, httpEndpoint, httpCursor)

		select {
		case <-done:
			return nil
		case <-ticker.C:
		}
	}
}

// runBridgeAll discovers all filesystem campfires and bridges them.
func runBridgeAll(done <-chan struct{}, agentID *identity.Identity, s *store.Store, httpEndpoint string) error {
	type bridgeState struct {
		forwarded  map[string]bool
		httpCursor int64
	}

	baseDir := fs.DefaultBaseDir()
	fsTransport := fs.New(baseDir)
	bridges := make(map[string]*bridgeState)

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
			bridges[cfID] = &bridgeState{forwarded: forwarded, httpCursor: cursor}
			fmt.Fprintf(os.Stderr, "discovered campfire %s\n", cfID[:min(12, len(cfID))])
		}
	}

	// Initial scan.
	scanCampfires()

	for {
		for cfID, bs := range bridges {
			pumpFSToHTTP(cfID, fsTransport, s, agentID, httpEndpoint, bs.forwarded)
			bs.httpCursor = pumpHTTPToFS(cfID, fsTransport, s, agentID, httpEndpoint, bs.httpCursor)
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

// pumpFSToHTTP reads fs messages into the store and delivers new ones to HTTP peers.
func pumpFSToHTTP(campfireID string, fsTransport *fs.Transport, s *store.Store, agentID *identity.Identity, httpEndpoint string, forwarded map[string]bool) {
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
		// Verify provenance hops.
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

		// Store in local SQLite (dedup via INSERT OR IGNORE).
		s.AddMessage(store.MessageRecordFromMessage(campfireID, &fsMsg, store.NowNano())) //nolint:errcheck

		// Deliver to HTTP endpoint.
		msg := fsMsg // copy for pointer
		cfhttp.DeliverToAll(endpoints, campfireID, &msg, agentID)

		forwarded[fsMsg.ID] = true
	}
}

// pumpHTTPToFS pulls messages from the HTTP endpoint and writes them to the filesystem.
func pumpHTTPToFS(campfireID string, fsTransport *fs.Transport, s *store.Store, agentID *identity.Identity, httpEndpoint string, cursor int64) int64 {
	msgs, err := cfhttp.Sync(httpEndpoint, campfireID, cursor, agentID)
	if err != nil {
		return cursor
	}

	maxCursor := cursor
	for _, msg := range msgs {
		now := store.NowNano()
		if now > maxCursor {
			maxCursor = now
		}

		// Store in local SQLite (dedup).
		inserted, _ := s.AddMessage(store.MessageRecordFromMessage(campfireID, &msg, now))
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
func buildForwardedSet(campfireID string, fsTransport *fs.Transport, s *store.Store) map[string]bool {
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

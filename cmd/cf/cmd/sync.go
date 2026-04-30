package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// followIntervalForTransport returns the poll interval for --follow based on transport type.
// GitHub transport was removed in v0.30.0; all remaining transports use 2s.
func followIntervalForTransport(m store.Membership) time.Duration {
	// GitHub transport was removed in v0.30.0; all remaining transports use 2s.
	return 2 * time.Second
}

// computeInitialCursor derives the starting poll cursor from the local store.
// Returns the maximum ReceivedAt nanosecond timestamp across all messages in
// the campfire, or 0 if the store is empty.
func computeInitialCursor(s store.Store, campfireID string) (int64, error) {
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

// syncCampfire runs the appropriate sync function for a single campfire based on its transport.
// Returns an error only for hard transport failures (e.g. deleted filesystem directory).
// Network-level failures (GitHub token missing, HTTP peer offline) are silently ignored so
// that transient outages do not terminate subscriptions.
func syncCampfire(cfID string, m *store.Membership, agentID *identity.Identity, s store.Store) error {
	switch transport.ResolveType(*m) {
	case transport.TypeGitHub:
		// GitHub transport was removed in v0.30.0; skip silently to avoid
		// panicking on campfires created before the removal.
	case transport.TypePeerHTTP:
		syncFromHTTPPeers(cfID, agentID, s)
	default:
		if err := syncFromFilesystem(cfID, m.TransportDir, s); err != nil {
			return err
		}
	}
	return nil
}


// syncFromFilesystem reads messages from the filesystem transport into the local store.
// Only messages with valid Ed25519 signatures are stored; invalid messages are silently
// skipped to prevent injection of unsigned content via shared filesystem directories.
// Provenance hops are also verified; any hop with an invalid signature is rejected.
//
// Returns an error if the transport directory does not exist or is otherwise unreadable.
// ListMessages returns nil/nil for a missing directory (resilient design for one-shot reads),
// so we use os.Stat to explicitly detect a missing directory — the same approach used by
// syncIfFilesystem in pkg/protocol/read.go. This lets callers that need to detect permanent
// transport failures (e.g. Subscribe via StoreSyncer) surface the error.
func syncFromFilesystem(cfID string, transportDir string, s store.Store) error {
	fsTransport := fs.ForDir(transportDir)

	// Detect transport directory removal explicitly.
	// ListMessages returns nil/nil on IsNotExist, so we must stat first.
	campfireDir := fsTransport.CampfireDir(cfID)
	if _, err := os.Stat(campfireDir); os.IsNotExist(err) {
		return fmt.Errorf("campfire transport directory removed: %s", campfireDir)
	}

	fsMessages, err := fsTransport.ListMessages(cfID)
	if err != nil {
		return fmt.Errorf("reading filesystem transport %q: %w", transportDir, err)
	}
	for _, fsMsg := range fsMessages {
		// workspace-h0t: verify message signature before storing.
		if !fsMsg.VerifySignature() {
			continue
		}
		// Reject messages with invalid or missing provenance hops.
		if !fsMsg.VerifyProvenance() {
			continue
		}
		s.AddMessage(store.MessageRecordFromMessage(cfID, &fsMsg, store.NowNano())) //nolint:errcheck
	}
	return nil
}

// syncFromHTTPPeers pulls messages from all known peer endpoints for a p2p-http campfire.
func syncFromHTTPPeers(cfID string, agentID *identity.Identity, s store.Store) {
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
		for i := range msgs {
			// Verify Ed25519 signature before accepting — matches syncIfFilesystem behaviour.
			// A compromised peer could otherwise inject forged messages.
			if !msgs[i].VerifySignature() {
				continue
			}
			// Reject messages with invalid or missing provenance hops.
			if !msgs[i].VerifyProvenance() {
				continue
			}
			s.AddMessage(store.MessageRecordFromMessage(cfID, &msgs[i], store.NowNano())) //nolint:errcheck
		}
	}
}

// StoreSyncer implements protocol.Syncer by wrapping syncCampfire so that
// protocol.Client.Read/Subscribe/Await sync all transport types (not just
// filesystem). The MCP server is push-based and should NOT set a StoreSyncer
// on its client — only cmd/cf commands that need pull-sync should use one.
type StoreSyncer struct {
	agentID *identity.Identity
	store   store.Store
}

// NewStoreSyncer creates a StoreSyncer for use with protocol.Client.SetSyncer.
// It implements the protocol.Syncer interface by delegating to syncCampfire,
// which dispatches to the appropriate transport-specific sync function.
func NewStoreSyncer(agentID *identity.Identity, s store.Store) protocol.Syncer {
	return &StoreSyncer{agentID: agentID, store: s}
}

// Sync implements protocol.Syncer. It looks up the membership for campfireID
// in the store and calls syncCampfire with the appropriate transport handler.
// Returns an error if the membership is missing (caller decides fatality).
func (ss *StoreSyncer) Sync(campfireID string) error {
	m, err := ss.store.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("getting membership: %w", err)
	}
	if m == nil {
		// No membership — nothing to sync (mirrors syncIfFilesystem behaviour).
		return nil
	}
	if err := syncCampfire(campfireID, m, ss.agentID, ss.store); err != nil {
		return fmt.Errorf("syncing campfire %s: %w", campfireID, err)
	}
	return nil
}

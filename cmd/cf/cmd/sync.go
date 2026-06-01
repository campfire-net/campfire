package cmd

import (
	"fmt"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/identity"
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

// syncCampfire syncs a single campfire by transport. Thin shim over the canonical
// protocol.SyncCampfire — the one implementation shared by the SDK and the CLI
// (campfire-d80). The membership arg is accepted for call-site compatibility;
// protocol.SyncCampfire re-resolves it.
func syncCampfire(cfID string, _ *store.Membership, agentID *identity.Identity, s store.Store) error {
	return protocol.SyncCampfire(s, agentID, cfID)
}

// syncFromFilesystem incrementally imports a filesystem campfire's messages into
// the store. Thin shim over protocol.SyncFilesystem.
func syncFromFilesystem(cfID string, transportDir string, s store.Store) error {
	return protocol.SyncFilesystem(s, cfID, transportDir)
}

// syncFromHTTPPeers pulls a p2p-http campfire's peer messages into the store.
// Thin shim over protocol.SyncHTTPPeers (peer-offline is non-fatal).
func syncFromHTTPPeers(cfID string, agentID *identity.Identity, s store.Store) {
	_ = protocol.SyncHTTPPeers(s, agentID, cfID)
}

// StoreSyncer implements protocol.Syncer so the cf CLI's Read/Subscribe/Await
// calls sync all transport types. It is now a thin adapter over the canonical
// protocol.SyncCampfire; the actual sync logic lives in the protocol package
// (one implementation, no drift — campfire-d80). The MCP server is push-based
// and does NOT set a StoreSyncer on its client.
type StoreSyncer struct {
	agentID *identity.Identity
	store   store.Store
}

// NewStoreSyncer creates a StoreSyncer for use with protocol.Client.SetSyncer.
func NewStoreSyncer(agentID *identity.Identity, s store.Store) protocol.Syncer {
	return &StoreSyncer{agentID: agentID, store: s}
}

// Sync implements protocol.Syncer by delegating to the canonical sync. Returns an
// error for hard transport failures (e.g. a removed filesystem directory) so a
// Subscribe using this syncer terminates cleanly; peer-offline is non-fatal.
func (ss *StoreSyncer) Sync(campfireID string) error {
	return protocol.SyncCampfire(ss.store, ss.agentID, campfireID)
}

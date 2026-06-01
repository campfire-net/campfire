package protocol

// sync.go — the single canonical per-transport sync implementation.
//
// Before campfire-d80 there were two copies of this logic: the protocol package
// handled only filesystem (in syncIfFilesystem), while cmd/cf/cmd owned a
// separate StoreSyncer that handled filesystem AND p2p-http. SDK consumers
// (convention servers) use protocol.Client with no injected syncer, so they got
// the filesystem-only path and silently never synced p2p-http relays — their
// Subscribe loop never dispatched. This file consolidates both transports into
// one implementation that the Client, the Subscribe loop, and the cmd shims all
// call, so there is no second copy to drift.

import (
	"fmt"
	"os"

	"github.com/campfire-net/campfire/cf-protocol/internal/store"
	"github.com/campfire-net/campfire/cf-protocol/internal/transport"
	"github.com/campfire-net/campfire/cf-protocol/internal/transport/fs"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/internal/transport/http"
	"github.com/campfire-net/campfire/pkg/identity"
)

// SyncCampfire pulls messages for a single campfire from its transport into the
// store, dispatching on the campfire's transport type. It is the canonical sync
// entry point shared by the SDK (Client subscribe path) and the cf CLI shims.
// Returns nil when there is no membership (nothing to sync).
//
// Filesystem errors (e.g. a removed transport directory) are returned so callers
// that need to detect permanent transport failure (Subscribe) can terminate.
// p2p-http peer-unreachable conditions are non-fatal (a peer may be transiently
// offline) and do not produce an error.
func SyncCampfire(s store.Store, id *identity.Identity, campfireID string) error {
	m, err := s.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("getting membership: %w", err)
	}
	if m == nil {
		return nil
	}
	return syncCampfireInto(s, id, campfireID, m)
}

// syncForSubscribe is the sync-before-poll step used by Subscribe. Unlike the
// Read/Await default (syncIfFilesystem, filesystem-only — which keeps the hosted
// relay from HTTP-pulling from peers on its own reads), this syncs ALL transports
// when no external Syncer is injected, so SDK convention servers receive
// p2p-http relay messages and dispatch correctly (campfire-d80). An injected
// Syncer still takes precedence (e.g. the cf CLI's shim).
func (c *Client) syncForSubscribe(campfireID string) error {
	if c.syncer != nil {
		return c.syncer.Sync(campfireID)
	}
	return SyncCampfire(c.store, c.identity, campfireID)
}

// syncCampfireInto dispatches to the transport-specific sync for a known membership.
func syncCampfireInto(s store.Store, id *identity.Identity, campfireID string, m *store.Membership) error {
	switch transport.ResolveType(*m) {
	case transport.TypePeerHTTP:
		return SyncHTTPPeers(s, id, campfireID)
	case transport.TypeGitHub:
		// GitHub transport was removed in v0.30.0; skip silently for campfires
		// created before the removal.
		return nil
	default:
		return SyncFilesystem(s, campfireID, m.TransportDir)
	}
}

// SyncFilesystem incrementally imports messages from a filesystem-transport
// campfire directory into the store. Only messages with valid Ed25519 signatures
// and provenance hops are stored. Returns an error if the transport directory has
// been removed (so Subscribe can terminate); a missing messages/ subdir is not an
// error (brand-new campfire).
//
// Incremental: a per-campfire leaf-filename cursor (with a clock-skew lookback)
// means only messages written since the last sync are read and verified — O(new),
// not O(total). See ListMessagesSince / LookbackCursor.
func SyncFilesystem(s store.Store, campfireID, transportDir string) error {
	fsTransport := fs.ForDir(transportDir)

	// Detect transport directory removal explicitly: ListMessages returns nil/nil
	// on IsNotExist, so stat first to distinguish "removed" from "no messages".
	campfireDir := fsTransport.CampfireDir(campfireID)
	if _, statErr := os.Stat(campfireDir); os.IsNotExist(statErr) {
		return fmt.Errorf("campfire transport directory removed: %s", campfireDir)
	}

	cursor, err := s.GetFSSyncCursor(campfireID)
	if err != nil {
		return fmt.Errorf("reading fs sync cursor: %w", err)
	}

	fsMessages, err := fsTransport.ListMessagesSince(campfireID, fs.LookbackCursor(cursor, fsSyncLookback))
	if err != nil {
		return fmt.Errorf("listing filesystem messages: %w", err)
	}

	advanceTo := cursor
	for i := range fsMessages {
		fsMsg := fsMessages[i].Message
		if !fsMsg.VerifySignature() {
			advanceTo = fsMessages[i].Leaf
			continue
		}
		if !fsMsg.VerifyProvenance() {
			advanceTo = fsMessages[i].Leaf
			continue
		}
		if _, addErr := s.AddMessage(store.MessageRecordFromMessage(campfireID, &fsMsg, store.NowNano())); addErr != nil {
			break // leave cursor before this message so it is retried next sync
		}
		advanceTo = fsMessages[i].Leaf
	}

	if advanceTo > cursor {
		if err := s.SetFSSyncCursor(campfireID, advanceTo); err != nil {
			return fmt.Errorf("advancing fs sync cursor: %w", err)
		}
	}
	return nil
}

// SyncHTTPPeers pulls messages from all known peer endpoints for a p2p-http
// campfire into the store. Only validly-signed messages with valid provenance are
// stored (a compromised peer must not be able to inject forged messages). The
// caller's own endpoint and empty endpoints are skipped. Peer-unreachable is
// non-fatal. Messages are pulled since the campfire's read cursor.
func SyncHTTPPeers(s store.Store, id *identity.Identity, campfireID string) error {
	peers, err := s.ListPeerEndpoints(campfireID)
	if err != nil {
		return fmt.Errorf("listing peer endpoints: %w", err)
	}
	since, _ := s.GetReadCursor(campfireID)
	selfPub := ""
	if id != nil {
		selfPub = id.PublicKeyHex()
	}
	for _, peer := range peers {
		if peer.Endpoint == "" || (selfPub != "" && peer.MemberPubkey == selfPub) {
			continue
		}
		msgs, err := cfhttp.Sync(peer.Endpoint, campfireID, since, id)
		if err != nil {
			continue // peer offline — non-fatal
		}
		for i := range msgs {
			if !msgs[i].VerifySignature() {
				continue
			}
			if !msgs[i].VerifyProvenance() {
				continue
			}
			s.AddMessage(store.MessageRecordFromMessage(campfireID, &msgs[i], store.NowNano())) //nolint:errcheck
		}
	}
	return nil
}

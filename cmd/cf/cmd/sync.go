package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
	"github.com/campfire-net/campfire/pkg/identity"
)

// fsSyncLookback is how far the incremental filesystem sync rewinds its leaf
// cursor on each poll, to tolerate a backward clock step between message writes
// without re-scanning the entire history. Re-reads inside this window are
// idempotent (INSERT OR IGNORE).
//
// The default (2s) comfortably covers typical NTP step corrections. Its only
// cost is re-reading messages whose leaf timestamp falls within the window of
// the newest message — negligible for time-spread workloads (e.g. an rd
// workspace: ~0 messages in any 2s window), larger only for dense bursts.
// Operators can tune or disable it via CF_FS_SYNC_LOOKBACK_MS (0 = strict
// cursor, exact under a monotonic clock). It is a var so tests can override it
// directly without real-time sleeps. The env parsing lives in the fs package
// (fs.SyncLookbackFromEnv) so the cmd and protocol sync paths share one source.
var fsSyncLookback = fs.SyncLookbackFromEnv()

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

	// Incremental sync (campfireagent-e58): import only messages written after the
	// last imported leaf. Without this, every Read/Await/Subscribe poll re-reads
	// and re-verifies the campfire's entire on-disk history — O(total messages) per
	// op, which on a multi-thousand-message campfire (e.g. an rd workspace) costs
	// seconds per call. The cursor is keyed on the on-disk leaf filename, distinct
	// from the timestamp-keyed read (subscription-delivery) cursor.
	cursor, err := s.GetFSSyncCursor(cfID)
	if err != nil {
		return fmt.Errorf("reading fs sync cursor for %s: %w", cfID, err)
	}

	// Rewind the read bound by a small window so a backward clock step cannot
	// permanently hide a message just under the cursor. Re-reads inside the window
	// are idempotent and cheap; the stored cursor still advances to the true max.
	fsMessages, err := fsTransport.ListMessagesSince(cfID, fs.LookbackCursor(cursor, fsSyncLookback))
	if err != nil {
		return fmt.Errorf("reading filesystem transport %q: %w", transportDir, err)
	}

	// Advance the cursor only across leaves we fully processed, in chronological
	// order. A transient AddMessage failure (e.g. DB error) halts advancement at
	// the prior leaf so the message is retried on the next sync rather than
	// silently skipped. Signature/provenance rejections are permanent, so the
	// cursor still advances past them — re-reading an invalid file can never
	// succeed, and not advancing would re-scan it on every future sync.
	advanceTo := cursor
	for i := range fsMessages {
		fsMsg := fsMessages[i].Message
		// workspace-h0t: verify message signature before storing.
		if !fsMsg.VerifySignature() {
			advanceTo = fsMessages[i].Leaf
			continue
		}
		// Reject messages with invalid or missing provenance hops.
		if !fsMsg.VerifyProvenance() {
			advanceTo = fsMessages[i].Leaf
			continue
		}
		if _, err := s.AddMessage(store.MessageRecordFromMessage(cfID, &fsMsg, store.NowNano())); err != nil {
			// Stop advancing: leave the cursor before this message so it is retried.
			break
		}
		advanceTo = fsMessages[i].Leaf
	}

	// Persist the cursor only when it moves forward, to avoid a redundant write on
	// a no-op sync. advanceTo is monotonic because fsMessages is in lex order.
	if advanceTo > cursor {
		if err := s.SetFSSyncCursor(cfID, advanceTo); err != nil {
			return fmt.Errorf("advancing fs sync cursor for %s: %w", cfID, err)
		}
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

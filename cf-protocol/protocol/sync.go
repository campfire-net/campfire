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
	"errors"
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
// not O(total). See ListMessagesPage / LookbackCursor.
//
// The sync proceeds in pages of syncChunkSize messages, so memory stays bounded
// even on the first sync of a very large history (campfireagent-6d3: the
// previous single-page implementation loaded a 2GB history into memory at once
// and committed each message in its own fsync'd transaction).
//
// Store-add failures stop the sync but return nil — matching the historical
// behaviour Subscribe depends on (a transient store error must not terminate
// the subscription; the cursor was not advanced past the failure, so the next
// sync retries).
func SyncFilesystem(s store.Store, campfireID, transportDir string) error {
	for {
		done, _, err := SyncFilesystemChunk(s, campfireID, transportDir, syncChunkSize)
		if err != nil {
			if isStoreAddError(err) {
				return nil // historical semantics: stop silently, retry next sync
			}
			return err
		}
		if done {
			return nil
		}
	}
}

// syncChunkSize is the page size for chunked filesystem sync: how many message
// files are read into memory, verified, and committed per transaction. Large
// enough to amortize the per-transaction fsync, small enough to bound memory
// (~10KB/message → ~10MB/page) and to give budgeted callers fine-grained stop
// points.
const syncChunkSize = 1000

// SyncFilesystemChunk imports at most maxMessages messages from the campfire's
// filesystem transport directory into the store, starting at the persisted
// per-campfire sync cursor and advancing it. Returns done == true when the end
// of the on-disk history was reached (nothing left beyond the cursor), along
// with the number of messages stored by this chunk.
//
// This is the budget-friendly primitive behind bounded sync (campfireagent-6d3):
// a caller that must stay responsive (e.g. an MCP tool handler) runs chunks
// until its budget expires and lets a background worker finish the rest; the
// cursor makes every chunk durable progress.
//
// Verification matches SyncFilesystem: messages failing signature or provenance
// checks are skipped and the cursor advances past them. Messages are committed
// in a single batch transaction when the store supports it (see
// batchMessageAdder); on batch failure nothing in the chunk is stored and the
// cursor does not advance, so the next sync retries the chunk (idempotent via
// INSERT OR IGNORE). maxMessages <= 0 means no cap (the whole remaining
// history in one chunk).
func SyncFilesystemChunk(s store.Store, campfireID, transportDir string, maxMessages int) (done bool, synced int, err error) {
	fsTransport := fs.ForDir(transportDir)

	// Detect transport directory removal explicitly: ListMessagesPage returns an
	// empty page on IsNotExist, so stat first to distinguish "removed" from "no
	// messages".
	campfireDir := fsTransport.CampfireDir(campfireID)
	if _, statErr := os.Stat(campfireDir); os.IsNotExist(statErr) {
		return false, 0, fmt.Errorf("campfire transport directory removed: %s", campfireDir)
	}

	cursor, err := s.GetFSSyncCursor(campfireID)
	if err != nil {
		return false, 0, fmt.Errorf("reading fs sync cursor: %w", err)
	}

	page, err := fsTransport.ListMessagesPage(campfireID, fs.LookbackCursor(cursor, fsSyncLookback), maxMessages)
	if err != nil {
		return false, 0, fmt.Errorf("listing filesystem messages: %w", err)
	}
	if page.More && page.LastListed <= cursor {
		// The entire page sits inside the clock-skew lookback window — a write
		// burst denser than the page size. Everything at or before the cursor
		// was already processed (the cursor only advances past listed leaves),
		// so re-list from the strict cursor to guarantee forward progress;
		// otherwise a chunk loop would re-read the same window forever.
		// Trade-off: within such a burst, lookback straggler recovery (a
		// backward clock step hiding a message just under the cursor) is
		// skipped for this chunk — acceptable, since a >page-size burst inside
		// the 2s window coinciding with a clock step is vanishingly rare, and
		// the lookback re-examination resumes on the next sync operation.
		page, err = fsTransport.ListMessagesPage(campfireID, cursor, maxMessages)
		if err != nil {
			return false, 0, fmt.Errorf("listing filesystem messages: %w", err)
		}
	}

	// Verify first, then store. Invalid messages are dropped here, so the
	// records slice is exactly what must be persisted.
	records := make([]store.MessageRecord, 0, len(page.Messages))
	for i := range page.Messages {
		fsMsg := page.Messages[i].Message
		if !fsMsg.VerifySignature() {
			continue
		}
		if !fsMsg.VerifyProvenance() {
			continue
		}
		records = append(records, store.MessageRecordFromMessage(campfireID, &fsMsg, store.NowNano()))
	}

	synced, addErr := addMessages(s, records)
	if addErr != nil {
		// The cursor is not advanced, so the next sync retries this chunk.
		return false, synced, &storeAddError{err: addErr}
	}

	// Advance the cursor to the last LISTED leaf — not the last decoded or
	// verified message — so undecodable or unverifiable files at the end of a
	// page cannot stall the cursor permanently.
	if page.LastListed > cursor {
		if err := s.SetFSSyncCursor(campfireID, page.LastListed); err != nil {
			return false, synced, fmt.Errorf("advancing fs sync cursor: %w", err)
		}
	}
	return !page.More, synced, nil
}

// storeAddError marks a sync failure that occurred while storing verified
// messages (as opposed to listing/cursor failures). SyncFilesystem swallows it
// to preserve the historical "stop silently, retry next sync" semantics that
// Subscribe depends on; budgeted callers may treat it as retryable.
type storeAddError struct{ err error }

func (e *storeAddError) Error() string { return "storing synced messages: " + e.err.Error() }
func (e *storeAddError) Unwrap() error { return e.err }

// isStoreAddError reports whether err is (or wraps) a storeAddError.
func isStoreAddError(err error) bool {
	var sae *storeAddError
	return errors.As(err, &sae)
}

// batchMessageAdder is the optional bulk-insert capability of a store. The
// SQLite store implements it (one transaction per batch — one fsync instead of
// one per message). Store decorators that must observe every individual
// AddMessage (e.g. the rate-limit wrapper) intentionally do NOT expose it, and
// take the per-message fallback.
type batchMessageAdder interface {
	AddMessagesBatch(ms []store.MessageRecord) (int, error)
}

// addMessages stores the records via the batch capability when available,
// falling back to per-message AddMessage. Returns the number stored. On error
// the caller must not advance the cursor past the failed record: the batch
// path stores nothing on error; the fallback path stops at the first failure.
func addMessages(s store.Store, records []store.MessageRecord) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}
	if batcher, ok := s.(batchMessageAdder); ok {
		return batcher.AddMessagesBatch(records)
	}
	stored := 0
	for i := range records {
		if _, err := s.AddMessage(records[i]); err != nil {
			return stored, err
		}
		stored++
	}
	return stored, nil
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

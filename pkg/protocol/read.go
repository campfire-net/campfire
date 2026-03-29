package protocol

import (
	"fmt"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// ReadRequest specifies the parameters for a Read operation.
type ReadRequest struct {
	// CampfireID is the campfire to read from. Required.
	CampfireID string

	// Tags filters messages to those carrying at least one of the listed exact tags.
	// Multiple tags are ORed. Nil or empty means no tag filtering.
	Tags []string

	// TagPrefixes filters messages to those carrying a tag that starts with any
	// of the listed prefixes (e.g. "galtrader:" matches "galtrader:move").
	// ORed with Tags. Nil or empty means no prefix filtering.
	TagPrefixes []string

	// ExcludeTags excludes messages carrying any of the listed exact tags.
	ExcludeTags []string

	// ExcludeTagPrefixes excludes messages carrying a tag starting with any prefix.
	ExcludeTagPrefixes []string

	// Sender filters messages to those from the given sender pubkey hex string.
	// Empty means no sender filtering.
	Sender string

	// AfterTimestamp returns only messages with timestamp > AfterTimestamp.
	// When 0, all messages are returned (or messages after the stored read cursor
	// if AdvanceCursor is true).
	AfterTimestamp int64

	// IncludeCompacted controls whether messages superseded by a compaction event
	// are included (true = include all; false = exclude compacted messages).
	IncludeCompacted bool

	// SkipSync skips the sync-before-query step even for filesystem-transport
	// campfires. Use this when the caller has already synced, or for HTTP-mode
	// campfires where messages are delivered via push. If the Membership is nil,
	// sync is automatically skipped.
	SkipSync bool

	// Limit caps the number of returned messages. 0 means no limit.
	Limit int
}

// ReadResult is the return value from a Read operation.
type ReadResult struct {
	// Messages are the messages matching the ReadRequest, ordered by timestamp.
	Messages []store.MessageRecord

	// MaxTimestamp is the highest timestamp seen across all messages in the
	// campfire for the query window (pre-filter). Use this to advance a cursor
	// on a subsequent call so filtered-out messages do not re-appear.
	MaxTimestamp int64
}

// Read syncs from the campfire's transport (for filesystem campfires) and then
// queries the local store with the filters specified in req.
//
// Sync-before-query: if the campfire uses filesystem transport and req.SkipSync
// is false, messages are read from the filesystem transport directory and stored
// locally before the query runs. This ensures newly-written messages are visible
// to the caller without requiring a separate sync step.
//
// For HTTP-transport campfires (where messages arrive via push), sync is skipped
// automatically when no Membership is found in the store or the transport type
// resolves to TypePeerHTTP.
//
// Returns a ReadResult containing the matching messages and the pre-filter max
// timestamp (useful for advancing read cursors).
func (c *Client) Read(req ReadRequest) (*ReadResult, error) {
	if req.CampfireID == "" {
		return nil, fmt.Errorf("protocol.Client.Read: CampfireID is required")
	}

	// Sync-before-query for filesystem-transport campfires.
	if !req.SkipSync {
		if err := c.syncIfFilesystem(req.CampfireID); err != nil {
			// Sync failures are non-fatal: the store may have older messages
			// that are still useful. Log-worthy but not blocking.
			_ = err
		}
	}

	// Build the store filter from the request.
	f := store.MessageFilter{
		Tags:               req.Tags,
		TagPrefixes:        req.TagPrefixes,
		ExcludeTags:        req.ExcludeTags,
		ExcludeTagPrefixes: req.ExcludeTagPrefixes,
		Sender:             req.Sender,
		RespectCompaction:  !req.IncludeCompacted,
	}

	msgs, err := c.store.ListMessages(req.CampfireID, req.AfterTimestamp, f)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Read: listing messages: %w", err)
	}

	// Compute pre-filter max timestamp using the scalar MAX query so we don't
	// load all unfiltered payloads just to find the highest timestamp.
	// This mirrors the runOneShotMode approach in cmd/cf/cmd/read.go.
	maxTS, err := c.store.MaxMessageTimestamp(req.CampfireID, req.AfterTimestamp)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Read: querying max timestamp: %w", err)
	}

	if req.Limit > 0 && len(msgs) > req.Limit {
		msgs = msgs[:req.Limit]
		// When Limit truncates results, use the last returned message's
		// timestamp as cursor — MaxTimestamp would skip unread messages.
		if len(msgs) > 0 {
			maxTS = msgs[len(msgs)-1].Timestamp
		}
	}

	return &ReadResult{
		Messages:     msgs,
		MaxTimestamp: maxTS,
	}, nil
}

// syncIfFilesystem syncs messages from the filesystem transport into the store
// for the given campfire. If the campfire uses a non-filesystem transport (GitHub,
// HTTP peer), this is a no-op.
//
// Only messages with valid Ed25519 signatures and valid provenance hops are stored.
// Invalid messages are silently skipped, consistent with syncFromFilesystem in
// cmd/cf/cmd/sync.go.
func (c *Client) syncIfFilesystem(campfireID string) error {
	m, err := c.store.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("getting membership: %w", err)
	}
	if m == nil {
		// No membership — nothing to sync.
		return nil
	}

	tt := transport.ResolveType(*m)
	if tt != transport.TypeFilesystem {
		// GitHub and HTTP campfires don't use filesystem sync.
		return nil
	}

	fsTransport := fs.ForDir(m.TransportDir)
	fsMessages, err := fsTransport.ListMessages(campfireID)
	if err != nil {
		return fmt.Errorf("listing filesystem messages: %w", err)
	}

	for _, fsMsg := range fsMessages {
		// Verify message signature before storing (security: workspace-h0t).
		if !fsMsg.VerifySignature() {
			continue
		}
		// Verify all provenance hops.
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
		c.store.AddMessage(store.MessageRecordFromMessage(campfireID, &fsMsg, store.NowNano())) //nolint:errcheck
	}

	return nil
}

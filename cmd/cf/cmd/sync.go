package cmd

import (
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport"
	ghtr "github.com/campfire-net/campfire/pkg/transport/github"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// followIntervalForTransport returns the poll interval for --follow based on transport type.
// GitHub campfires use 5s to avoid API rate limiting; all others use 2s.
func followIntervalForTransport(m store.Membership) time.Duration {
	if transport.ResolveType(m) == transport.TypeGitHub {
		return 5 * time.Second
	}
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
func syncCampfire(cfID string, m *store.Membership, agentID *identity.Identity, s store.Store) {
	switch transport.ResolveType(*m) {
	case transport.TypeGitHub:
		syncFromGitHub(cfID, m.TransportDir, s)
	case transport.TypePeerHTTP:
		syncFromHTTPPeers(cfID, agentID, s)
	default:
		syncFromFilesystem(cfID, m.TransportDir, s)
	}
}

// syncFromGitHub polls the GitHub Issue for new comments and stores verified messages
// in the local SQLite store. Non-fatal errors are silently ignored (caller continues).
func syncFromGitHub(cfID, transportDir string, s store.Store) {
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
func syncFromFilesystem(cfID string, transportDir string, s store.Store) {
	fsTransport := fs.ForDir(transportDir)
	fsMessages, err := fsTransport.ListMessages(cfID)
	if err != nil {
		return
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
		for _, msg := range msgs {
			s.AddMessage(store.MessageRecordFromMessage(cfID, &msg, store.NowNano())) //nolint:errcheck
		}
	}
}

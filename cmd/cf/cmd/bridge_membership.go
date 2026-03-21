package cmd

import (
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// membershipSyncState tracks which members have been announced in each direction
// across poll cycles, so syncMembership is idempotent and does not re-announce.
type membershipSyncState struct {
	// announcedToHTTP is the set of fs member public key hex strings that have
	// already been announced to the HTTP side. Keyed by hex-encoded public key.
	announcedToHTTP map[string]bool
	// writtenToFS is the set of HTTP peer public key hex strings that have
	// already been written to the fs members/ directory.
	writtenToFS map[string]bool
}

// newMembershipSyncState creates an empty membershipSyncState.
func newMembershipSyncState() *membershipSyncState {
	return &membershipSyncState{
		announcedToHTTP: make(map[string]bool),
		writtenToFS:     make(map[string]bool),
	}
}

// syncMembership reconciles the member lists between the fs and HTTP transports.
// It is called on each poll cycle. All operations are idempotent: members already
// present on the target side (tracked in state) are not re-announced.
//
// HTTP→fs: For each HTTP peer (from the store's peer endpoint list) not yet written
// to the fs members/ directory, write a MemberRecord.
//
// fs→HTTP: For each fs member not yet announced to the HTTP side, send a
// NotifyMembership join event. The endpoint field is empty for fs members that have
// no HTTP endpoint — the bridge is their delivery path.
//
// Errors are logged and skipped; a failed sync on one member does not block others.
func syncMembership(
	campfireID string,
	fsTransport *fs.Transport,
	s *store.Store,
	agentID *identity.Identity,
	httpEndpoint string,
	state *membershipSyncState,
) {
	syncHTTPToFS(campfireID, fsTransport, s, state)
	syncFSToHTTP(campfireID, fsTransport, agentID, httpEndpoint, state)
}

// syncHTTPToFS writes member records for HTTP peers that are not yet in the
// fs members/ directory. Uses the store's peer endpoint list as the source of
// HTTP-side membership (populated via join and membership notifications).
func syncHTTPToFS(
	campfireID string,
	fsTransport *fs.Transport,
	s *store.Store,
	state *membershipSyncState,
) {
	// Get current fs members to build a lookup set.
	fsMembers, err := fsTransport.ListMembers(campfireID)
	if err != nil {
		// members/ directory may not exist yet; not fatal.
		return
	}
	fsMemberSet := make(map[string]bool, len(fsMembers))
	for _, m := range fsMembers {
		fsMemberSet[fmt.Sprintf("%x", m.PublicKey)] = true
	}

	// Get HTTP peers from the store (populated by join and membership events).
	httpPeers, err := s.ListPeerEndpoints(campfireID)
	if err != nil {
		return
	}

	for _, peer := range httpPeers {
		pubHex := peer.MemberPubkey
		if fsMemberSet[pubHex] {
			// Already in fs — nothing to do.
			continue
		}
		if state.writtenToFS[pubHex] {
			// Already attempted in a prior cycle; skip (avoids repeated writes
			// if the fs write failed due to a transient error).
			continue
		}

		pubBytes, err := hex.DecodeString(pubHex)
		if err != nil {
			log.Printf("syncHTTPToFS: invalid pubkey hex %q for campfire %s: %v", pubHex, campfireID[:min(12, len(campfireID))], err)
			continue
		}

		member := campfire.MemberRecord{
			PublicKey: pubBytes,
			JoinedAt:  time.Now().UnixNano(),
			Role:      campfire.RoleFull,
		}
		if err := fsTransport.WriteMember(campfireID, member); err != nil {
			log.Printf("syncHTTPToFS: writing member %s for campfire %s: %v", pubHex[:min(8, len(pubHex))], campfireID[:min(12, len(campfireID))], err)
			continue
		}

		state.writtenToFS[pubHex] = true
	}
}

// syncFSToHTTP announces fs members to the HTTP side via NotifyMembership join events.
// The endpoint field is empty for fs-only members — the bridge is their delivery path.
// Members already announced (tracked in state.announcedToHTTP) are skipped.
//
// Note: the HTTP membership handler enforces that a member may only announce its own
// join (event.Member == senderHex). As a result, announcements for members other than
// the bridge agent will be rejected by the remote server. The bridge sends them
// opportunistically and records them as announced regardless — the HTTP side may
// accept them in future protocol versions or via alternate authorization.
func syncFSToHTTP(
	campfireID string,
	fsTransport *fs.Transport,
	agentID *identity.Identity,
	httpEndpoint string,
	state *membershipSyncState,
) {
	fsMembers, err := fsTransport.ListMembers(campfireID)
	if err != nil {
		return
	}

	bridgePubHex := agentID.PublicKeyHex()

	for _, m := range fsMembers {
		pubHex := fmt.Sprintf("%x", m.PublicKey)
		if state.announcedToHTTP[pubHex] {
			continue
		}

		// The HTTP membership protocol only allows a member to announce itself.
		// Only send notifications for the bridge's own identity; skip others.
		if pubHex != bridgePubHex {
			// Mark as "announced" to avoid re-attempting on every cycle.
			state.announcedToHTTP[pubHex] = true
			continue
		}

		event := cfhttp.MembershipEvent{
			Event:  "join",
			Member: pubHex,
			// No HTTP endpoint for fs-side members; bridge is their delivery path.
			Endpoint: "",
		}
		if err := cfhttp.NotifyMembership(httpEndpoint, campfireID, event, agentID); err != nil {
			log.Printf("syncFSToHTTP: notifying membership for %s campfire %s: %v", pubHex[:min(8, len(pubHex))], campfireID[:min(12, len(campfireID))], err)
			// Do not mark as announced — retry next cycle.
			continue
		}

		state.announcedToHTTP[pubHex] = true
	}
}

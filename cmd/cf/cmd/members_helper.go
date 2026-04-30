package cmd

import (
	"encoding/hex"
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// listMembersByTransport returns the member list for a campfire, dispatching
// to the correct backend based on transport type:
//   - filesystem: reads .cbor member files from the transport directory.
//   - p2p-http: queries the store's peer_endpoints table.
//   - github: queries the store's peer_endpoints table (same as p2p-http).
//
// This replaces the bare fs.ForDir(m.TransportDir).ListMembers() calls in
// ls.go and members.go, which silently returned empty for non-filesystem
// campfires (campfireagent-968).
func listMembersByTransport(m store.Membership, s store.Store) ([]campfire.MemberRecord, error) {
	switch transport.ResolveType(m) {
	case transport.TypePeerHTTP, transport.TypeGitHub:
		return listMembersFromStore(m.CampfireID, s)
	default:
		// TypeFilesystem — existing behavior.
		return fs.ForDir(m.TransportDir).ListMembers(m.CampfireID)
	}
}

// listMembersFromStore converts peer endpoint records into campfire.MemberRecord
// values. Only admitted peers are stored in peer_endpoints (they pass admission
// before UpsertPeerEndpoint is called), so this enumerates exactly the admitted
// member set. JoinedAt is unavailable in peer_endpoints, so it is set to 0.
func listMembersFromStore(campfireID string, s store.Store) ([]campfire.MemberRecord, error) {
	peers, err := s.ListPeerEndpoints(campfireID)
	if err != nil {
		return nil, fmt.Errorf("listing peer endpoints: %w", err)
	}
	members := make([]campfire.MemberRecord, 0, len(peers))
	for _, p := range peers {
		pubkeyBytes, err := hex.DecodeString(p.MemberPubkey)
		if err != nil {
			// Skip malformed entries rather than aborting the whole list.
			continue
		}
		members = append(members, campfire.MemberRecord{
			PublicKey: pubkeyBytes,
			JoinedAt:  0, // not stored in peer_endpoints
			Role:      campfire.EffectiveRole(p.Role),
		})
	}
	return members, nil
}

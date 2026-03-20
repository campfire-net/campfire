package transport

import (
	"github.com/campfire-net/campfire/pkg/store"
)

// Type identifies which transport backs a campfire membership.
type Type int

const (
	TypeFilesystem Type = iota
	TypeGitHub
	TypePeerHTTP
)

// String returns a human-readable name for the transport type.
func (t Type) String() string {
	switch t {
	case TypeFilesystem:
		return "filesystem"
	case TypeGitHub:
		return "github"
	case TypePeerHTTP:
		return "p2p-http"
	default:
		return "unknown"
	}
}

// ResolveType returns the transport type for a membership record.
//
// When the membership has an explicit TransportType field (populated by
// store.AddMembership and backfilled by store.Open migration), that value is
// used directly — no filesystem I/O occurs.
//
// For legacy rows that somehow still have an empty TransportType (should not
// happen after migration, but defended against for safety), the function falls
// back to the string-prefix heuristic on TransportDir. The p2p-http cbor-file
// probe is NOT performed in the fallback — those rows will resolve as
// "filesystem" rather than making a disk call on every hot-path invocation.
// The correct fix for any such row is to re-run store.Open (migration backfill).
func ResolveType(m store.Membership) Type {
	switch m.TransportType {
	case "github":
		return TypeGitHub
	case "p2p-http":
		return TypePeerHTTP
	case "filesystem":
		return TypeFilesystem
	default:
		// Fallback for legacy rows with empty TransportType. Use the cheap
		// string-prefix check only (no filesystem I/O). If the prefix matches
		// github, return GitHub; otherwise return Filesystem. p2p-http rows
		// should have been backfilled by store.Open migration; logging a safe
		// fallback here avoids the hot-path os.Stat call entirely.
		if isGitHubTransportDir(m.TransportDir) {
			return TypeGitHub
		}
		return TypeFilesystem
	}
}

// GitHubTransportPrefix is the prefix used in the TransportDir column to identify
// GitHub-transport campfires.
const GitHubTransportPrefix = "github:"

// isGitHubTransportDir checks whether a TransportDir indicates a GitHub-transport campfire.
func isGitHubTransportDir(transportDir string) bool {
	return len(transportDir) >= len(GitHubTransportPrefix) &&
		transportDir[:len(GitHubTransportPrefix)] == GitHubTransportPrefix
}

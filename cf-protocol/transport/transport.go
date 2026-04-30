// Package transport — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the transport package from cf-protocol/internal/transport.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package transport

import transport "github.com/campfire-net/campfire/cf-protocol/internal/transport"

// Re-export types.
type Type = transport.Type

// Re-export transport type constants.
const (
	TypeFilesystem = transport.TypeFilesystem
	TypePeerHTTP   = transport.TypePeerHTTP
	TypeGitHub     = transport.TypeGitHub
)

// GitHubTransportPrefix is the prefix for GitHub transport dirs.
const GitHubTransportPrefix = transport.GitHubTransportPrefix

// Re-export functions.
var ResolveType = transport.ResolveType

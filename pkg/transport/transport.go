// Package transport provides the unified Transport interface and transport
// resolution for the Campfire protocol.
//
// Three transport backends are supported:
//
//   - Filesystem (fs): local directory-based transport for same-host campfires.
//   - GitHub Issues (github): relay via GitHub Issue comments.
//   - P2P HTTP (http): direct peer-to-peer HTTP transport.
//
// Transport detection is based on the TransportDir field stored in the
// membership record:
//
//   - GitHub: TransportDir has prefix "github:" followed by JSON metadata.
//   - P2P HTTP: A .cbor file exists at <TransportDir>/<campfireID>.cbor.
//   - Filesystem: everything else (including empty TransportDir).
package transport

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/campfire-net/campfire/pkg/store"
)

// Type identifies the transport backend used by a campfire.
type Type int

const (
	// TypeFilesystem is the local filesystem transport.
	TypeFilesystem Type = iota
	// TypeGitHub is the GitHub Issues relay transport.
	TypeGitHub
	// TypePeerHTTP is the P2P HTTP transport.
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

// githubTransportPrefix is the TransportDir prefix used for GitHub campfires.
// Mirrors the constant in cmd/cf/cmd/github.go — kept here so the transport
// package can resolve types without depending on the cmd layer.
const githubTransportPrefix = "github:"

// ResolveType returns the transport Type for a membership record.
// The detection logic mirrors the routing used in send.go and read.go:
//
//  1. GitHub: TransportDir starts with "github:".
//  2. P2P HTTP: <TransportDir>/<campfireID>.cbor exists on disk.
//  3. Filesystem: everything else.
func ResolveType(m store.Membership) Type {
	if strings.HasPrefix(m.TransportDir, githubTransportPrefix) {
		return TypeGitHub
	}
	if m.TransportDir != "" {
		statePath := filepath.Join(m.TransportDir, m.CampfireID+".cbor")
		if _, err := os.Stat(statePath); err == nil {
			return TypePeerHTTP
		}
	}
	return TypeFilesystem
}

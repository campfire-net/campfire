package transport

import (
	"os"
	"path/filepath"
	"strings"

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

// ResolveType infers the transport type from a membership record.
//
// Detection order:
//  1. GitHub  — TransportDir begins with "github:" (JSON-encoded github transport meta)
//  2. PeerHTTP — a <campfire-id>.cbor state file exists in TransportDir (p2p HTTP state)
//  3. Filesystem — everything else (local /tmp/campfire layout)
func ResolveType(m store.Membership) Type {
	if isGitHubTransportDir(m.TransportDir) {
		return TypeGitHub
	}
	if isPeerHTTPTransportDir(m.TransportDir, m.CampfireID) {
		return TypePeerHTTP
	}
	return TypeFilesystem
}

// GitHubTransportPrefix is the prefix used in the TransportDir column to identify
// GitHub-transport campfires.
const GitHubTransportPrefix = "github:"

// isGitHubTransportDir checks whether a TransportDir indicates a GitHub-transport campfire.
func isGitHubTransportDir(transportDir string) bool {
	return strings.HasPrefix(transportDir, GitHubTransportPrefix)
}

// isPeerHTTPTransportDir mirrors the cmd-layer isPeerHTTPCampfire check.
// A p2p-HTTP campfire stores a <campfire-id>.cbor file in the transport directory.
func isPeerHTTPTransportDir(transportDir, campfireID string) bool {
	if transportDir == "" || campfireID == "" {
		return false
	}
	statePath := filepath.Join(transportDir, campfireID+".cbor")
	_, err := os.Stat(statePath)
	return err == nil
}

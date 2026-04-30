// Package fs — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the filesystem transport from cf-protocol/internal/transport/fs.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package fs

import fs "github.com/campfire-net/campfire/cf-protocol/internal/transport/fs"

// Re-export types.
type (
	Transport      = fs.Transport
	PushSubscriber = fs.PushSubscriber
)

// Re-export functions.
var (
	DefaultBaseDir = fs.DefaultBaseDir
	New            = fs.New
	NewPathRooted  = fs.NewPathRooted
	ForDir         = fs.ForDir
)

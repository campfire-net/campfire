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

	// Migration types.
	MigrateStoreOptions              = fs.MigrateStoreOptions
	MigrationInconsistentLayoutError = fs.MigrationInconsistentLayoutError
	MigrationCountMismatchError      = fs.MigrationCountMismatchError
	MigrationByteMismatchError       = fs.MigrationByteMismatchError
	MigrationCorruptedOtherStateError = fs.MigrationCorruptedOtherStateError
)

// Re-export functions.
var (
	DefaultBaseDir = fs.DefaultBaseDir
	New            = fs.New
	NewPathRooted  = fs.NewPathRooted
	ForDir         = fs.ForDir

	// MigrateStore migrates a campfire directory from the flat v0.19.2 layout
	// to the v0.31 bucketed YYYY-MM/DD/ layout (design §3).
	MigrateStore = fs.MigrateStore

	// IsMigrationError reports whether err is a migration sentinel error.
	IsMigrationError = fs.IsMigrationError

	// MigrateLockPath returns the path of the migration lockfile for the given
	// campfire directory. Used by callers that need to verify LOCK_EX behavior.
	MigrateLockPath = fs.MigrateLockPath
)

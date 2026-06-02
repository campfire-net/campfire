// Package storage provides a repository-wrapper interface over the campfire
// store substrate (cf-protocol/store). Call sites consume the Storage
// interface instead of touching store.Store directly, which lets the
// local-vs-cloud persistence distinction become invisible at the call site.
//
// Two implementations sit behind the interface:
//
//   - CloudStorage: a faithful passthrough over the Azure Table Storage store
//     (pkg/store/aztable). aztable is the authoritative source of truth for
//     hosted deployments; CloudStorage forwards every method unchanged, so
//     hosted behavior is byte-identical to using the aztable store directly.
//
//   - LocalStorage: a SQLite-backed store where the filesystem transport
//     directory is the source of truth and SQLite is a rebuildable index.
//     LocalStorage implements the fs-truth-over-cache behavior for
//     GetMembership: on a SQLite cache miss it rehydrates from the filesystem
//     transport directory and warms the cache. The Storage interface is shaped
//     so that this fallback is confined entirely to LocalStorage without
//     changing the interface or any call site.
//
// GetMembership is the one operation where local and cloud diverge: a nil
// membership from the cloud store is an authoritative "not a member", while a
// nil membership from the local index may be a cache miss that the filesystem
// can satisfy. All call sites use Storage.GetMembership and the implementation
// decides — so call sites remain backend-agnostic.
package storage

import (
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// Backend identifies which persistence implementation backs a Storage value.
type Backend string

const (
	// BackendLocal is a SQLite-backed LocalStorage (single-machine / dev).
	BackendLocal Backend = "local"
	// BackendCloud is an Azure Table Storage-backed CloudStorage (hosted).
	BackendCloud Backend = "cloud"
)

// Storage is the persistence surface that call sites consume. It embeds the
// full store.Store interface so every existing store operation is available
// unchanged through Storage, and adds the small set of operations whose
// semantics must differ between the local and cloud backends.
//
// Embedding store.Store (rather than re-declaring its methods) means new store
// methods flow through automatically and the store-only categories
// (threshold_shares, epoch_secrets, invites, peers, read_cursors,
// pending_messages, projections) pass through with no special handling.
type Storage interface {
	store.Store

	// Backend reports which implementation backs this Storage. Intended for
	// diagnostics, deployment assertions, and tests — NOT for call sites to
	// branch persistence behavior on. Call sites must remain backend-agnostic.
	Backend() Backend
}

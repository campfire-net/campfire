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
//   - LocalStorage: a SQLite-backed store where, eventually, the filesystem
//     transport directory is the source of truth and SQLite is a rebuildable
//     index. THIS PACKAGE ships LocalStorage as a faithful passthrough over
//     SQLite only — the filesystem-rehydrate behavior for membership reads is
//     a downstream item (campfireagent-3fc). The Storage interface is shaped so
//     that fs-fallback can be added inside LocalStorage WITHOUT changing the
//     interface or any call site.
//
// The membership-existence decision is the one place where local and cloud
// diverge: a nil membership from the cloud store is an authoritative
// "not a member", while a nil membership from the local index may be a cache
// miss that should be answered by consulting the filesystem. To keep call
// sites uniform, that decision lives behind Storage.MembershipExists rather
// than at the call site (which must not know which backend it is talking to).
package storage

import (
	"fmt"

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

	// MembershipExists reports whether the caller is a member of campfireID.
	//
	// This is the membership-existence decision lifted OUT of call sites and
	// INTO the Storage implementation. For CloudStorage, a nil membership from
	// the authoritative aztable store means "not a member" (false). For
	// LocalStorage, a nil membership from the SQLite index currently means the
	// same (false) — but the downstream item (campfireagent-3fc) will extend
	// LocalStorage to consult the filesystem transport directory on a SQLite
	// miss before answering false. Because the answer is a plain (bool, error),
	// neither change is visible to call sites.
	MembershipExists(campfireID string) (bool, error)
}

// Config selects and parameterizes the backend. It is the single input to Open.
type Config struct {
	// ConnectionString is the Azure Storage connection string. When non-empty,
	// Open selects CloudStorage (aztable). When empty, Open selects
	// LocalStorage (SQLite). This mirrors the existing hosted-selection logic
	// at cmd/cf-mcp/main.go (the AZURE_STORAGE_CONNECTION_STRING gate).
	ConnectionString string

	// LocalPath is the SQLite database path used when ConnectionString is
	// empty. Ignored on the cloud branch.
	LocalPath string
}

// Open selects and constructs the backend per cfg, returning a ready Storage.
//
// Selection logic (subsumes cmd/cf-mcp/main.go's AZURE_STORAGE_CONNECTION_STRING
// gate): a non-empty cfg.ConnectionString selects CloudStorage over a real
// aztable TableStore; otherwise LocalStorage over a real SQLite store at
// cfg.LocalPath. Both branches open real store implementations — there is no
// in-memory or mock path here.
func Open(cfg Config) (Storage, error) {
	if cfg.ConnectionString != "" {
		return openCloud(cfg.ConnectionString)
	}
	return openLocal(cfg.LocalPath)
}

// openLocal opens a SQLite store and wraps it in LocalStorage.
func openLocal(path string) (Storage, error) {
	if path == "" {
		return nil, fmt.Errorf("storage: LocalPath required when ConnectionString is empty")
	}
	st, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("storage: open local sqlite store: %w", err)
	}
	return NewLocalStorage(st), nil
}

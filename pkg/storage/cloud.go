package storage

import (
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// CloudStorage is a faithful passthrough over a store.Store backed by Azure
// Table Storage (pkg/store/aztable). It embeds the wrapped store so every
// store.Store method forwards unchanged — aztable is the authoritative source
// of truth and is NOT rewritten. CloudStorage adds only the Storage-specific
// method (Backend), keeping hosted behavior byte-identical to using the aztable
// store directly.
type CloudStorage struct {
	store.Store
}

// Compile-time assertion that *CloudStorage satisfies Storage.
var _ Storage = (*CloudStorage)(nil)

// NewCloudStorage wraps an already-constructed store.Store (expected to be an
// aztable TableStore) as a CloudStorage. Exposed so call sites that already
// hold a hosted store (e.g. the per-session namespaced store factory in
// cmd/cf-mcp) can adopt the Storage interface without re-opening the backend.
func NewCloudStorage(st store.Store) *CloudStorage {
	return &CloudStorage{Store: st}
}

// Backend reports the cloud backend.
func (c *CloudStorage) Backend() Backend { return BackendCloud }


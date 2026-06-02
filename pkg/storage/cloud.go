package storage

import (
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/store/aztable"
)

// CloudStorage is a faithful passthrough over a store.Store backed by Azure
// Table Storage (pkg/store/aztable). It embeds the wrapped store so every
// store.Store method forwards unchanged — aztable is the authoritative source
// of truth and is NOT rewritten. CloudStorage adds only the Storage-specific
// methods (Backend, MembershipExists), keeping hosted behavior byte-identical
// to using the aztable store directly.
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

// openCloud constructs a real aztable TableStore from connStr and wraps it.
func openCloud(connStr string) (Storage, error) {
	st, err := aztable.NewTableStore(connStr)
	if err != nil {
		return nil, fmt.Errorf("storage: open aztable store: %w", err)
	}
	return NewCloudStorage(st), nil
}

// Backend reports the cloud backend.
func (c *CloudStorage) Backend() Backend { return BackendCloud }

// MembershipExists answers the membership-existence question authoritatively:
// aztable is the source of truth, so a nil membership means "not a member".
// There is no filesystem to fall back to in the cloud, so this is the final
// answer — unlike LocalStorage, which (in campfireagent-3fc) will consult the
// filesystem on a SQLite miss before answering false.
func (c *CloudStorage) MembershipExists(campfireID string) (bool, error) {
	m, err := c.GetMembership(campfireID)
	if err != nil {
		return false, err
	}
	return m != nil, nil
}

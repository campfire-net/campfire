package storage

import "github.com/campfire-net/campfire/cf-protocol/store"

// LocalStorage is the single-machine backend. The design intent is that the
// filesystem transport directory is the source of truth and the embedded
// SQLite store is a rebuildable index. THIS PACKAGE ships LocalStorage as a
// faithful passthrough over SQLite only: the filesystem-rehydrate behavior for
// membership reads is explicitly out of scope here and lands in the downstream
// item campfireagent-3fc.
//
// LocalStorage embeds store.Store so every store operation forwards to SQLite
// unchanged. The fs-fallback, when it lands, will be confined to the
// Storage-specific methods below (notably MembershipExists) — no call site and
// no interface method signature changes.
type LocalStorage struct {
	store.Store
}

// Compile-time assertion that *LocalStorage satisfies Storage.
var _ Storage = (*LocalStorage)(nil)

// NewLocalStorage wraps a SQLite-backed store.Store as a LocalStorage.
func NewLocalStorage(st store.Store) *LocalStorage {
	return &LocalStorage{Store: st}
}

// Backend reports the local backend.
func (l *LocalStorage) Backend() Backend { return BackendLocal }

// MembershipExists answers the membership-existence question from the SQLite
// index.
//
// PASSTHROUGH-FOR-NOW: a nil membership currently returns false, identical to
// CloudStorage. The downstream item campfireagent-3fc will extend this method
// to consult the filesystem transport directory when the SQLite index reports
// nil — turning a local cache miss into a filesystem-authoritative lookup —
// without changing this signature or any call site. The interface is shaped so
// that change is internal to LocalStorage.
func (l *LocalStorage) MembershipExists(campfireID string) (bool, error) {
	m, err := l.GetMembership(campfireID)
	if err != nil {
		return false, err
	}
	return m != nil, nil
}

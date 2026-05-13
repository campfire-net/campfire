package store

// This file isolates the in-memory store helper from the production store
// surface. OpenMemory is strictly a test helper; production code must use
// Open(path) instead. Keeping the helper in its own file makes the boundary
// visible to reviewers and lets us swap the implementation (or hide it behind
// a build tag) without disturbing the surrounding production code paths.
//
// Callers outside cf-protocol/ reach this via the cf-protocol/store and
// cf-protocol/protocol re-export wrappers, which also live in dedicated
// test-helper files (store_inmemory.go and surface_inmemory.go respectively).
//
// History: campfireagent-c5a (this file) is the follow-up to
// campfireagent-90f, which surfaced confusion about whether OpenMemory was
// part of the production API.

import (
	"database/sql"
	"fmt"
)

// OpenMemory opens an in-memory SQLite store with the given unique name.
// All instances with the same name share the same in-memory database (SQLite
// shared-cache URI mode), so callers must pass a unique name per logical store.
//
// OpenMemory is intended for tests that need fast store creation without disk
// I/O. Production code should use Open. The same-name shared-store invariant
// is covered by TestOpenMemory_SameNameSharedStore (campfireagent-7b2).
func OpenMemory(name string) (Store, error) {
	if name == "" {
		return nil, fmt.Errorf("OpenMemory: name must not be empty")
	}
	// file:<name>?mode=memory&cache=shared opens a named in-memory database.
	// The cache=shared flag is required to keep the database alive as long as
	// at least one connection is open. Without it, the database is destroyed
	// as soon as sql.Open returns and no *sql.DB holds a connection.
	dsn := "file:" + name + "?mode=memory&cache=shared"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening in-memory database %q: %w", name, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema for %q: %w", name, err)
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations for %q: %w", name, err)
	}
	return &SQLiteStore{
		db:              db,
		supersededCache: make(map[string]supersededCacheEntry),
	}, nil
}

// convention_dispatch.go — T4: ConventionDispatcher wiring for handleSend.
//
// Provides loadConventionServersForCampfire, which lazily loads registered
// convention server handlers from Azure Table Storage into the ConventionDispatcher
// on the first send to each campfire.
//
// Design notes:
//   - Registration is idempotent (RegisterTier1/RegisterTier2 replace on conflict).
//   - We track loaded campfires in a sync.Map to avoid redundant store round-trips.
//   - Tier 2 handlers require a HandlerURL; Tier 1 Go handlers are not stored in
//     the table and cannot be rehydrated at startup — Tier 1 registration from the
//     store is not supported in this revision (TODO: future Tier 1 bootstrap path).
//   - Disabled records are skipped.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// conventionServerCacheTTL is how long a campfire's convention server
// registrations are considered fresh. After this duration, the next send
// triggers a reload from the store — picking up handlers registered on
// other instances or handlers added/removed since the last load.
const conventionServerCacheTTL = 60 * time.Second

// conventionCacheEntry records when a campfire's convention servers were loaded.
type conventionCacheEntry struct {
	loadedAt time.Time
}

// conventionServerCache replaces the old never-evicting sync.Map with a
// TTL-aware cache. Access is guarded by conventionServerCacheMu.
var (
	conventionServerCacheMu      sync.Mutex
	conventionServerCacheEntries = make(map[string]*conventionCacheEntry)
)

// conventionServerCacheGet returns true if the campfire's convention servers
// were loaded within the TTL window. Stale entries are evicted on access.
func conventionServerCacheGet(campfireID string) bool {
	conventionServerCacheMu.Lock()
	defer conventionServerCacheMu.Unlock()
	entry, ok := conventionServerCacheEntries[campfireID]
	if !ok {
		return false
	}
	if time.Since(entry.loadedAt) > conventionServerCacheTTL {
		delete(conventionServerCacheEntries, campfireID)
		return false
	}
	return true
}

// conventionServerCacheSet marks a campfire's convention servers as freshly loaded.
func conventionServerCacheSet(campfireID string) {
	conventionServerCacheMu.Lock()
	defer conventionServerCacheMu.Unlock()
	conventionServerCacheEntries[campfireID] = &conventionCacheEntry{loadedAt: time.Now()}
}

// conventionServerCacheDelete removes a campfire from the cache (e.g. on load error).
func conventionServerCacheDelete(campfireID string) {
	conventionServerCacheMu.Lock()
	defer conventionServerCacheMu.Unlock()
	delete(conventionServerCacheEntries, campfireID)
}

// loadConventionServersForCampfire loads all enabled convention server records
// for the given campfire from the store and registers them with the dispatcher.
//
// This is a no-op when:
//   - conventionDispatcher is nil
//   - conventionServerStore is nil
//   - the campfire has already been loaded (tracked via conventionServerLoadedCampfires)
//
// Tier 1 Go handler functions cannot be restored from the table store — those
// are registered programmatically via RegisterTier1Handler at process startup.
// Only Tier 2 HTTP handlers are loaded from the table.
func (s *server) loadConventionServersForCampfire(ctx context.Context, campfireID string) {
	if s.conventionDispatcher == nil || s.conventionServerStore == nil {
		return
	}
	if conventionServerCacheGet(campfireID) {
		return
	}

	servers, err := s.conventionServerStore.ListConventionServers(ctx, campfireID)
	if err != nil {
		// Fail open: log and continue without registrations. The dispatcher
		// will simply find no handler and return false.
		fmt.Printf("convention dispatch: load servers for %s: %v\n", campfireID, err)
		// Remove from loaded set so next send will retry.
		conventionServerCacheDelete(campfireID)
		return
	}

	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}
		// Only Tier 2 (HTTP) handlers can be loaded from the store.
		// Tier 1 Go handlers are registered programmatically at startup.
		if srv.Tier == 2 && srv.HandlerURL != "" {
			s.conventionDispatcher.RegisterTier2Handler(
				campfireID,
				srv.Convention,
				srv.Operation,
				srv.HandlerURL,
				nil, // no client needed for Tier 2 — response is async via HTTP
				srv.ServerID,
				srv.ForgeAccountID,
			)
		}
	}

	conventionServerCacheSet(campfireID)
}

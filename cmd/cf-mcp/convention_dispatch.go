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
)

// conventionServerLoadedCampfires tracks which campfire IDs have already had
// their convention server registrations loaded into the dispatcher. This avoids
// repeated store round-trips on every send.
//
// The key is the campfireID string; the value is always true.
var conventionServerLoadedCampfires sync.Map

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
	if _, loaded := conventionServerLoadedCampfires.LoadOrStore(campfireID, true); loaded {
		return
	}

	servers, err := s.conventionServerStore.ListConventionServers(ctx, campfireID)
	if err != nil {
		// Fail open: log and continue without registrations. The dispatcher
		// will simply find no handler and return false.
		fmt.Printf("convention dispatch: load servers for %s: %v\n", campfireID, err)
		// Remove from loaded set so next send will retry.
		conventionServerLoadedCampfires.Delete(campfireID)
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
				"", // ForgeAccountID: not stored in convention server records yet
			)
		}
	}
}

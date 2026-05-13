package protocol

// This file re-exports the in-memory test helper from cf-protocol/internal/store.
// OpenMemory is for tests only; production callers must use Open(path).
//
// campfireagent-c5a moved this re-export out of surface.go to make the
// test-only intent visually obvious. The example_surface_test.go suite is
// the primary intended consumer — it documents the SDK by showing how
// tests should construct a store without disk I/O.

import (
	store "github.com/campfire-net/campfire/cf-protocol/internal/store"
)

// OpenMemory opens an in-memory campfire store. TEST HELPER ONLY — production
// SDK callers must use Open(path).
var OpenMemory = store.OpenMemory

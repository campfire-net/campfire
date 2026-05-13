package store

// This file re-exports the in-memory test helper from
// cf-protocol/internal/store. OpenMemory is for tests only; production code
// outside cf-protocol/ must use Open(path).
//
// campfireagent-c5a moved this re-export out of store.go to make the
// test-only intent visually obvious — both at the call site and to
// reviewers scanning the production API surface.

import (
	store "github.com/campfire-net/campfire/cf-protocol/internal/store"
)

// OpenMemory opens an in-memory campfire store. TEST HELPER ONLY — production
// callers must use Open(path).
var OpenMemory = store.OpenMemory

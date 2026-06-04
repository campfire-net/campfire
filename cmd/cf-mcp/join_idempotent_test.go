package main

// Regression test for campfireagent-b4b / dontguess-042 (v0.33.0 re-join
// regression).
//
// Re-joining a campfire the identity already belongs to must be idempotent.
// After fd69 routed on-disk members past the "already a member" guard, the
// alreadyOnDisk branch called admission.AdmitMember unconditionally; with the
// membership already recorded in the store (campfire_create records the
// creator), AddMembership attempted a duplicate INSERT:
//
//	admitting member: admission: recording membership: adding membership:
//	constraint failed: UNIQUE constraint failed: campfire_memberships.campfire_id
//
// This is exactly the dontguess repro: create+self-join, then campfire_join —
// every join errored under v0.33.x, all were idempotent under v0.32.0.
//
// Fix under test: handleJoin's alreadyOnDisk branch skips AdmitMember when the
// membership is already recorded (or just rehydrated) in the store.

import (
	"encoding/json"
	"testing"
)

func TestJoin_ReJoinOwnCampfireIsIdempotent(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("campfire_create returned no campfire_id")
	}

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
	})

	// The creator is already a member (on disk via create, recorded in store).
	// campfire_join must be idempotent — success, not a UNIQUE-constraint error.
	resp1 := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if resp1.Error != nil {
		t.Fatalf("re-join of own campfire must be idempotent (campfireagent-b4b), got: %s", resp1.Error.Message)
	}

	// And again — stable under repetition.
	resp2 := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if resp2.Error != nil {
		t.Fatalf("second re-join must also be idempotent, got: %s", resp2.Error.Message)
	}
}

package main

// Tests for fsSyncManager invariants that the join repro test cannot prove
// (campfireagent-6d3 review findings d63, b6d).

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/campfire-net/campfire/pkg/identity"
)

// seedFSCampfire creates a campfire on the shared transport root with n
// campfire-key-signed messages and pre-admits the joiner identity on disk
// (the legion jail shape). Returns the campfire ID.
func seedFSCampfire(t *testing.T, sharedRoot string, srvCreator *server, joiner *identity.Identity, n int) string {
	t.Helper()
	createResp := srvCreator.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("campfire_create returned no campfire_id")
	}

	tr := fs.New(sharedRoot)
	state, err := tr.ReadState(campfireID)
	if err != nil {
		t.Fatalf("reading campfire state: %v", err)
	}
	members, err := tr.ListMembers(campfireID)
	if err != nil {
		t.Fatalf("listing members: %v", err)
	}
	cf := state.ToCampfire(members)
	signer, err := message.NewEd25519Signer(
		ed25519.PrivateKey(state.PrivateKey),
		ed25519.PublicKey(state.PublicKey),
	)
	if err != nil {
		t.Fatalf("creating signer: %v", err)
	}
	for i := 0; i < n; i++ {
		msg, msgErr := message.NewMessage(signer, []byte(fmt.Sprintf(`{"i":%d}`, i)), []string{"noise"}, nil)
		if msgErr != nil {
			t.Fatalf("NewMessage: %v", msgErr)
		}
		if hopErr := msg.AddHop(
			state.PrivateKey, state.PublicKey,
			cf.MembershipHash(), len(members),
			state.JoinProtocol, state.ReceptionRequirements,
			campfire.RoleFull,
		); hopErr != nil {
			t.Fatalf("AddHop: %v", hopErr)
		}
		if wErr := tr.WriteMessage(campfireID, msg); wErr != nil {
			t.Fatalf("WriteMessage: %v", wErr)
		}
	}

	if joiner != nil {
		if err := tr.WriteMember(campfireID, campfire.MemberRecord{
			PublicKey: joiner.PublicKey,
			JoinedAt:  time.Now().UnixNano(),
			Role:      campfire.RoleFull,
		}); err != nil {
			t.Fatalf("pre-admitting joiner on disk: %v", err)
		}
	}
	return campfireID
}

// TestFSSync_BypassesSessionRateLimiter proves that transport sync does not
// pass through the session store's rate limiter (review finding d63). The
// joiner uses the DEFAULT free-tier limiter (100 msg/min, 1000 msg/month). A
// regression that routes sync back through the session store would silently
// drop everything past the limits — observed as a store that never reaches
// the full message count.
func TestFSSync_BypassesSessionRateLimiter(t *testing.T) {
	if testing.Short() {
		t.Skip("writes >1000 messages; skipped in -short")
	}
	sharedRoot := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", sharedRoot)

	srvA, _ := newTestServerWithStore(t)
	doInit(t, srvA)

	// srvB: DEFAULT rate-limit config — 100/min, 1000/month. The sync volume
	// (1300 messages) exceeds both.
	srvB, storeB := newTestServerWithStore(t)
	if respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`)); respB.Error != nil {
		t.Fatalf("init srvB: %v", respB.Error)
	}
	joinerID, err := identity.Load(srvB.identityPath())
	if err != nil {
		t.Fatalf("loading srvB identity: %v", err)
	}

	const total = 1300 // > monthly cap (1000) and > per-minute rate (100)
	campfireID := seedFSCampfire(t, sharedRoot, srvA, joinerID, total)

	joinArgs, _ := json.Marshal(map[string]interface{}{"campfire_id": campfireID})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if joinResp.Error != nil {
		t.Fatalf("campfire_join: %s", joinResp.Error.Message)
	}

	// All messages must land despite the limiter: sync of already-on-disk
	// messages is not message ingestion.
	deadline := time.Now().Add(90 * time.Second)
	for {
		msgs, msgsErr := storeB.ListMessages(campfireID, 0)
		if msgsErr == nil && len(msgs) >= total {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 90s the store holds %d/%d messages — sync is being rate-limited or dropped (campaign limiter must not apply to transport sync)", len(msgs), total)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestFSSyncManager_BackgroundErrorRetainsCallbacks covers the manager error
// path (review findings b6d + campfire-751): a background worker that dies on
// a chunk error must clear bgLive and RETAIN the onComplete callbacks; the
// next foreground sync must then either restart a worker or — when it
// completes the history itself — drain the stranded callbacks.
func TestFSSyncManager_BackgroundErrorRetainsCallbacks(t *testing.T) {
	sharedRoot := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", sharedRoot)

	srvA, _ := newTestServerWithStore(t)
	doInit(t, srvA)
	campfireID := seedFSCampfire(t, sharedRoot, srvA, nil, 30)

	// Shrink the foreground budget so the first sync cannot finish 30 messages.
	origChunk, origChunks := fsSyncChunkSize, fsSyncForegroundChunks
	fsSyncChunkSize, fsSyncForegroundChunks = 10, 1
	t.Cleanup(func() { fsSyncChunkSize, fsSyncForegroundChunks = origChunk, origChunks })

	mgr := srvA.fsSyncMgr()
	transportDir := srvA.fsTransport().CampfireDir(campfireID)

	// Hold the background worker at the gate, then break the transport dir so
	// its first chunk errors.
	fsSyncBgGate = make(chan struct{})
	t.Cleanup(func() { fsSyncBgGate = nil })

	callbackRan := make(chan struct{}, 1)
	complete, err := mgr.syncForeground(transportDir, campfireID, func() { callbackRan <- struct{}{} })
	if err != nil {
		t.Fatalf("syncForeground: %v", err)
	}
	if complete {
		t.Fatal("foreground completed 30 messages with a 10-message budget — budget not applied")
	}

	brokenDir := transportDir + ".moved"
	if err := os.Rename(transportDir, brokenDir); err != nil {
		t.Fatalf("breaking transport dir: %v", err)
	}
	close(fsSyncBgGate) // background worker now errors on its first chunk

	// Wait for the worker to die: bgLive false.
	state := mgr.state(campfireID)
	deadline := time.Now().Add(10 * time.Second)
	for {
		state.mu.Lock()
		live, pending := state.bgLive, len(state.onComplete)
		state.mu.Unlock()
		if !live {
			if pending != 1 {
				t.Fatalf("background died with %d pending callbacks, want 1 (callbacks must survive chunk errors)", pending)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background worker did not exit after transport error")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Restore the transport and let a foreground sync finish the history —
	// the stranded callback must be drained (campfire-751).
	if err := os.Rename(brokenDir, transportDir); err != nil {
		t.Fatalf("restoring transport dir: %v", err)
	}
	fsSyncBgGate = nil
	fsSyncChunkSize, fsSyncForegroundChunks = 100, 5 // enough to finish the remaining ~20
	complete, err = mgr.syncForeground(transportDir, campfireID, nil)
	if err != nil {
		t.Fatalf("syncForeground(retry): %v", err)
	}
	if !complete {
		t.Fatal("retry foreground did not complete the 30-message history")
	}
	select {
	case <-callbackRan:
	default:
		t.Fatal("onComplete callback stranded after background error + foreground completion (campfire-751)")
	}
}

package main

// Regression test for campfireagent-6d3.
//
// Symptom (automata-island/automataisland-2724, Lara's embodiment worker,
// 2026-06-05): campfire_join hangs indefinitely when the target campfire has a
// long message history (real body cfs: >260k messages, 2GB on disk). The b991
// fix made handleJoin run syncFSVerified — a FULL history scan that reads
// every .cbor into memory (2GB RSS observed) and inserts each message in its
// own autocommit SQLite transaction (one fsync per message). At that cost a
// 260k-message campfire takes hours per join. Every wake. Never completes.
//
// Fix shape under test (approved 2026-06-05): bounded foreground sync. The
// join syncs at most a bounded number of messages from the incremental-sync
// cursor (declarations are published at campfire creation, so they live at the
// HEAD of history — Lara's body cf has them as messages #1-121), registers the
// convention tools it found, and returns. The remaining history continues
// syncing in a background goroutine; when it completes, late declarations are
// also registered.
//
// The bounded-sync assertion is structural, not timing-based: at join-return
// the session store must NOT contain the full history. On the pre-fix code the
// join syncs all messages before returning, so the assertion fails — that is
// the bug.

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/ratelimit"
)

// newTestServerWithPermissiveStore is newTestServerWithStore with rate limits
// raised far above the message volume of this test. The default test limiter
// (100 msg/min, 1000 msg/mo) otherwise masks the unbounded-scan bug by
// silently rejecting most of the sync's AddMessage calls — which is itself a
// defect (sync of already-on-disk messages must not be rate-limited), covered
// by the fix under test via limiter-free sync connections.
func newTestServerWithPermissiveStore(t *testing.T) (*server, store.Store) {
	t.Helper()
	srv := newTestServer(t)
	rawStore, err := store.Open(store.StorePath(srv.cfHome))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { rawStore.Close() })
	rl := ratelimit.New(rawStore, ratelimit.Config{
		MaxMessagesPerMinute: 1_000_000,
		MonthlyMessageCap:    1_000_000,
	})
	srv.st = rl
	return srv, rl
}

// joinLongHistoryNoiseCount is sized well above the join's foreground sync
// budget so that a bounded join provably leaves part of the history unsynced
// at return time, while an unbounded join (the bug) syncs everything.
const joinLongHistoryNoiseCount = 2400

func TestJoin_LongHistory_BoundedForegroundSync(t *testing.T) {
	if testing.Short() {
		t.Skip("long-history repro writes thousands of messages; skipped in -short")
	}

	// ---- Shared filesystem transport root (the legion jail shape) ----
	sharedRoot := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", sharedRoot)

	// ---- srvA creates the campfire ----
	srvA, _ := newTestServerWithStore(t)
	doInit(t, srvA)
	createResp := srvA.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("campfire_create returned no campfire_id")
	}

	trA := fs.New(sharedRoot)
	state, err := trA.ReadState(campfireID)
	if err != nil {
		t.Fatalf("reading campfire state: %v", err)
	}
	members, err := trA.ListMembers(campfireID)
	if err != nil {
		t.Fatalf("listing members: %v", err)
	}
	cf := state.ToCampfire(members)
	signer, err := message.NewEd25519Signer(
		ed25519.PrivateKey(state.PrivateKey),
		ed25519.PublicKey(state.PublicKey),
	)
	if err != nil {
		t.Fatalf("creating campfire-key signer: %v", err)
	}
	writeSigned := func(payload []byte, tags []string) {
		t.Helper()
		msg, msgErr := message.NewMessage(signer, payload, tags, nil)
		if msgErr != nil {
			t.Fatalf("creating message: %v", msgErr)
		}
		if hopErr := msg.AddHop(
			state.PrivateKey, state.PublicKey,
			cf.MembershipHash(), len(members),
			state.JoinProtocol, state.ReceptionRequirements,
			campfire.RoleFull,
		); hopErr != nil {
			t.Fatalf("adding provenance hop: %v", hopErr)
		}
		if wErr := trA.WriteMessage(campfireID, msg); wErr != nil {
			t.Fatalf("writing message: %v", wErr)
		}
	}

	// ---- HEAD: a convention declaration, like a real body cf (msgs #1-121
	// of Lara's body cf are its declarations, published at creation) ----
	writeSigned([]byte(`{
		"convention": "freeso-embodiment",
		"version": "1.0",
		"operation": "query-self",
		"description": "Query the automaton's own body state",
		"args": [{"name": "detail", "type": "string", "required": false}],
		"antecedents": "none",
		"signing": "campfire_key"
	}`), []string{"convention:operation"})

	// ---- BULK: the perception/dialog/verb firehose ----
	for i := 0; i < joinLongHistoryNoiseCount; i++ {
		writeSigned([]byte(fmt.Sprintf(`{"kind":"perception","tick":%d}`, i)), []string{"noise"})
	}

	// ---- TAIL: a declaration published late in the campfire's life. It must
	// eventually register too (via background sync completion), but is NOT
	// required at join-return time. ----
	writeSigned([]byte(`{
		"convention": "freeso-embodiment",
		"version": "1.0",
		"operation": "query-tail",
		"description": "Late-published declaration",
		"args": [],
		"antecedents": "none",
		"signing": "campfire_key"
	}`), []string{"convention:operation"})

	totalMessages := joinLongHistoryNoiseCount + 2

	// ---- srvB: separate identity, PRE-ADMITTED on disk (the jail shape) ----
	srvB, storeB := newTestServerWithPermissiveStore(t)
	if respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`)); respB.Error != nil {
		t.Fatalf("init srvB: %v", respB.Error)
	}
	joinerID, err := identity.Load(srvB.identityPath())
	if err != nil {
		t.Fatalf("loading srvB identity: %v", err)
	}
	if err := trA.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: joinerID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("pre-admitting srvB on disk: %v", err)
	}

	// ---- Join, with the background worker GATED ----
	// The bounded-foreground assertions below inspect the store immediately
	// after the join returns; without the gate, a fast background worker could
	// complete the remaining history before the assertions execute (a
	// scheduling race — campfireagent-46a). The gate holds the background
	// worker before its first chunk until the bounded state is verified.
	fsSyncBgGate = make(chan struct{})
	bgGateClosed := false
	t.Cleanup(func() {
		if !bgGateClosed {
			close(fsSyncBgGate)
		}
		fsSyncBgGate = nil
	})

	joinArgs, _ := json.Marshal(map[string]interface{}{"campfire_id": campfireID})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if joinResp.Error != nil {
		t.Fatalf("campfire_join: %s", joinResp.Error.Message)
	}

	// The join must disclose the incomplete sync (campfireagent-2cb).
	joinFields := extractCreateResult(t, joinResp)
	if sc, ok := joinFields["sync_complete"].(bool); !ok || sc {
		t.Fatalf("join result sync_complete = %v (present=%v), want false while history backfills", joinFields["sync_complete"], ok)
	}

	// ---- THE 6d3 ASSERTION: the foreground sync must be BOUNDED ----
	// At join-return the session store must not contain the full history.
	// Pre-fix, handleJoin's syncFSVerified scans and stores everything before
	// returning — on a real body cf that is hours of fsync-per-message inserts
	// (the observed indefinite hang). The bound proves the join no longer
	// scales with total history size.
	msgs, msgsErr := storeB.ListMessages(campfireID, 0)
	if msgsErr != nil {
		t.Fatalf("listing session store messages: %v", msgsErr)
	}
	if len(msgs) >= totalMessages {
		t.Fatalf("join foreground sync is unbounded: session store has all %d/%d messages at join return (campfireagent-6d3); join cost scales with total history size", len(msgs), totalMessages)
	}
	if len(msgs) == 0 {
		t.Fatal("join synced nothing: head-of-history declarations cannot register (would reintroduce campfireagent-b991)")
	}

	// ---- The b991 guarantee must hold: head declaration registered at return ----
	if srvB.conventionTools == nil {
		t.Fatal("conventionTools map not initialized after join")
	}
	hasTool := func(name string) bool {
		for _, tool := range srvB.conventionTools.list() {
			if tool.Name == name {
				return true
			}
		}
		return false
	}
	if !hasTool("query-self") {
		t.Fatalf("head-of-history convention tool 'query-self' not registered at join return (campfireagent-b991 regression)")
	}

	// ---- Release the background worker and verify eventual completeness:
	// it finishes the history and registers the tail declaration ----
	close(fsSyncBgGate)
	bgGateClosed = true
	deadline := time.Now().Add(90 * time.Second)
	for {
		msgs, msgsErr = storeB.ListMessages(campfireID, 0)
		if msgsErr == nil && len(msgs) == totalMessages && hasTool("query-tail") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background sync incomplete after 90s: %d/%d messages synced, query-tail registered=%v",
				len(msgs), totalMessages, hasTool("query-tail"))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

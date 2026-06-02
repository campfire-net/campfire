package storage_test

import (
	"encoding/hex"
	"path/filepath"
	"sync"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/campfire-net/campfire/pkg/storage"
)

// TestLocalStorageGetMembershipRehydratesFromFilesystem is the regression test
// for campfireagent-3fc. It builds a REAL filesystem transport root containing
// a campfire dir with a campfire.cbor and a members/<pk>.cbor for identity X,
// pairs it with an EMPTY real SQLite store (cache miss guaranteed), wraps both
// in LocalStorage, and asserts that GetMembership reconstructs a fully-populated
// record (correct Role AND TransportDir) from the filesystem — proving the
// fs-truth-over-sqlite-cache behavior. It also asserts that a store-only
// category (epoch secrets) still returns a miss, proving no spurious fs
// fallback was added for categories that have no filesystem source.
//
// Ground-source: real fs transport + real SQLite store. No mocks.
func TestLocalStorageGetMembershipRehydratesFromFilesystem(t *testing.T) {
	transportRoot := t.TempDir()

	// Self identity X — a fixed 32-byte Ed25519-shaped public key.
	selfPub := make([]byte, 32)
	for i := range selfPub {
		selfPub[i] = byte(i + 1)
	}
	selfHex := hex.EncodeToString(selfPub)

	// A real fs transport rooted at transportRoot. The campfire dir is
	// transportRoot/<campfireID>.
	tr := fs.New(transportRoot)

	// Create a campfire on disk with state + a member file for identity X.
	cfState := &campfire.CampfireState{
		PublicKey:    selfPub, // any non-empty pubkey; this campfire's own key
		JoinProtocol: "open",
		Threshold:    1,
	}
	c := cfState.ToCampfire(nil)
	campfireID := c.PublicKeyHex()
	if err := tr.Init(c); err != nil {
		t.Fatalf("transport.Init: %v", err)
	}
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: selfPub,
		JoinedAt:  4242,
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("transport.WriteMember: %v", err)
	}

	// A real, EMPTY SQLite store: GetMembership against it alone is a miss.
	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	// Sanity: the bare store is empty — the cache miss is real, not faked.
	if m, err := st.GetMembership(campfireID); err != nil {
		t.Fatalf("bare store GetMembership: %v", err)
	} else if m != nil {
		t.Fatalf("bare store GetMembership = %+v, want nil (empty store)", m)
	}

	// Wrap in LocalStorage configured to know its own identity and where the
	// filesystem transport root lives.
	ls := storage.NewLocalStorage(st,
		storage.WithSelfPubkeyHex(selfHex),
		storage.WithTransportBaseDir(transportRoot),
	)

	// Cache miss → fs rehydrate → fully-populated record.
	got, err := ls.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("LocalStorage.GetMembership: %v", err)
	}
	if got == nil {
		t.Fatalf("LocalStorage.GetMembership = nil, want rehydrated record")
	}
	if got.Role != campfire.RoleFull {
		t.Errorf("rehydrated Role = %q, want %q", got.Role, campfire.RoleFull)
	}
	wantDir := tr.CampfireDir(campfireID)
	if got.TransportDir != wantDir {
		t.Errorf("rehydrated TransportDir = %q, want %q", got.TransportDir, wantDir)
	}
	if got.TransportType != "filesystem" {
		t.Errorf("rehydrated TransportType = %q, want %q", got.TransportType, "filesystem")
	}
	if got.JoinedAt != 4242 {
		t.Errorf("rehydrated JoinedAt = %d, want 4242", got.JoinedAt)
	}

	// Idempotent + warm cache: the rehydrate must have written back into the
	// SQLite cache, so a direct store read now hits.
	if warm, err := st.GetMembership(campfireID); err != nil {
		t.Fatalf("warm store GetMembership: %v", err)
	} else if warm == nil {
		t.Fatalf("rehydrate did not warm the cache: store still returns nil")
	} else if warm.Role != campfire.RoleFull || warm.TransportDir != wantDir {
		t.Fatalf("warm cache record mismatch: Role=%q TransportDir=%q", warm.Role, warm.TransportDir)
	}

	// Second call is harmless and returns the same record.
	again, err := ls.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("second LocalStorage.GetMembership: %v", err)
	}
	if again == nil || again.Role != campfire.RoleFull || again.TransportDir != wantDir {
		t.Fatalf("second GetMembership mismatch: %+v", again)
	}

	// MembershipExists must agree with GetMembership.
	exists, err := ls.MembershipExists(campfireID)
	if err != nil {
		t.Fatalf("MembershipExists: %v", err)
	}
	if !exists {
		t.Fatalf("MembershipExists = false after rehydrate, want true")
	}

	// HARD CONSTRAINT: store-only categories must NOT gain an fs fallback. The
	// epoch secret category has no filesystem source — a miss is authoritative.
	// Rehydrating membership must not cause epoch secrets to be fabricated.
	if sec, err := ls.GetEpochSecret(campfireID, 0); err != nil {
		t.Fatalf("GetEpochSecret: %v", err)
	} else if sec != nil {
		t.Fatalf("GetEpochSecret returned a record for an empty store — spurious fs fallback")
	}
}

// TestLocalStorageGetMembershipConcurrentRehydrateNoRace is the regression test
// for the warm-cache INSERT race (S2 security review, campfireagent-913). When
// many gates (parallel Send/Read/Members in cf-mcp) hit a cold cache at once,
// they all miss, all rehydrate, and all try to AddMembership the same row.
// AddMembership is a plain INSERT against a primary key, so all but one lose a
// UNIQUE-constraint race. The fix treats that as a benign warm-race (re-read the
// winner's row). This test asserts every concurrent caller succeeds with the
// correct record — none gets a spurious "warming membership cache" failure.
//
// Ground-source: real fs transport + real SQLite store + real goroutines.
func TestLocalStorageGetMembershipConcurrentRehydrateNoRace(t *testing.T) {
	transportRoot := t.TempDir()

	selfPub := make([]byte, 32)
	for i := range selfPub {
		selfPub[i] = byte(i + 7)
	}
	selfHex := hex.EncodeToString(selfPub)

	tr := fs.New(transportRoot)
	cfState := &campfire.CampfireState{PublicKey: selfPub, JoinProtocol: "open", Threshold: 1}
	c := cfState.ToCampfire(nil)
	campfireID := c.PublicKeyHex()
	if err := tr.Init(c); err != nil {
		t.Fatalf("transport.Init: %v", err)
	}
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: selfPub, JoinedAt: 4242, Role: campfire.RoleFull,
	}); err != nil {
		t.Fatalf("transport.WriteMember: %v", err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	ls := storage.NewLocalStorage(st,
		storage.WithSelfPubkeyHex(selfHex),
		storage.WithTransportBaseDir(transportRoot),
	)

	const goroutines = 12
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	recs := make([]*store.Membership, goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // maximize contention: all fire together on a cold cache
			recs[idx], errs[idx] = ls.GetMembership(campfireID)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < goroutines; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d: GetMembership errored (warm-race not handled): %v", i, errs[i])
		}
		if recs[i] == nil {
			t.Errorf("goroutine %d: GetMembership = nil, want rehydrated record", i)
		} else if recs[i].Role != campfire.RoleFull {
			t.Errorf("goroutine %d: Role = %q, want %q", i, recs[i].Role, campfire.RoleFull)
		}
	}
}

// TestLocalStorageGetMembershipNoSelfPubkeyIsPassthrough verifies that without
// a configured self-pubkey, LocalStorage cannot identify which on-disk member
// is "me" and therefore falls back to pure passthrough (no rehydrate). This
// preserves the original passthrough behavior for callers that do not opt in.
func TestLocalStorageGetMembershipNoSelfPubkeyIsPassthrough(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	ls := storage.NewLocalStorage(st) // no self-pubkey configured
	got, err := ls.GetMembership("some-campfire")
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if got != nil {
		t.Fatalf("GetMembership = %+v, want nil (passthrough, no self identity)", got)
	}
}

package cmd

// Regression test for campfireagent-fd69:
// cf join fails with UNIQUE constraint error for a pre-admitted member when
// the store is wrapped in a rehydrating LocalStorage (storage.LocalStorage).
//
// Root cause: the "already a member" guard called s.GetMembership() before
// checking disk. LocalStorage.GetMembership() rehydrates a cold cache on a
// cache miss, writing the membership into SQLite as a side effect. The
// subsequent admission.AdmitMember call in joinFilesystem then hit a UNIQUE
// constraint on campfire_id.
//
// Fix: check isMemberOnDisk() first; only call s.GetMembership() when the
// member is NOT on disk (mirroring cf-mcp handleJoin lines 1808–1835).

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/storage"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// TestJoinFilesystem_PreAdmittedMember_RehydratingStore verifies that a
// pre-admitted member (on-disk member file, empty SQLite cache) can call
// joinFilesystem and picks up the admitted role when the store is wrapped
// in a rehydrating LocalStorage.
//
// This is the regression case from campfireagent-fd69: the rehydrating store
// was causing "UNIQUE constraint failed: campfire_memberships.campfire_id"
// because the guard called GetMembership (triggering rehydration → INSERT),
// then joinFilesystem called AdmitMember → AddMembership → duplicate INSERT.
func TestJoinFilesystem_PreAdmittedMember_RehydratingStore(t *testing.T) {
	// ---- Setup: create a campfire with a pre-admitted member on disk ----
	transportBaseDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	// Generate identities: campfire, creator, and pre-admitted joiner.
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	joinerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}

	campfireID := cfID.PublicKeyHex()
	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	// Write campfire state (invite-only — pre-admitted members bypass invite check).
	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	// Write the joiner as a pre-admitted member on disk with role "writer".
	// This simulates "cf admit <campfire> <joiner-pubkey> --role writer".
	tr := fs.New(transportBaseDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: joinerID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleWriter,
	}); err != nil {
		t.Fatalf("writing pre-admitted member record: %v", err)
	}

	// Verify the member file is on disk.
	memberFilePath := filepath.Join(cfDir, "members", fmt.Sprintf("%s.cbor", joinerID.PublicKeyHex()))
	if _, err := os.Stat(memberFilePath); err != nil {
		t.Fatalf("pre-admitted member file not found at %s: %v", memberFilePath, err)
	}

	// ---- Open a raw SQLite store wrapped in LocalStorage (rehydrating) ----
	// This is what requireAgentAndStore() returns in production (via wrapLocalStore).
	rawStore, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer rawStore.Close()

	// Wrap exactly as wrapLocalStore does with a known identity pubkey.
	wrappedStore := storage.NewLocalStorage(rawStore,
		storage.WithSelfPubkeyHex(joinerID.PublicKeyHex()),
		storage.WithTransportBaseDir(transportBaseDir),
	)

	// Pre-condition: no membership in the SQLite cache.
	pre, err := rawStore.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("pre-condition GetMembership (raw): %v", err)
	}
	if pre != nil {
		t.Fatal("pre-condition: expected empty SQLite cache before join")
	}

	// ---- Exercise: joinFilesystem with the wrapped (rehydrating) store ----
	// Before the fix this would fail with:
	//   "admission: recording membership: adding membership: constraint failed:
	//    UNIQUE constraint failed: campfire_memberships.campfire_id"
	if err := joinFilesystem(campfireID, joinerID, wrappedStore); err != nil {
		t.Fatalf("joinFilesystem with rehydrating store: %v", err)
	}

	// ---- Verify: membership recorded with the pre-admitted role ----
	m, err := wrappedStore.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership after join: %v", err)
	}
	if m == nil {
		t.Fatal("membership not recorded after joinFilesystem")
	}
	if m.Role != campfire.RoleWriter {
		t.Errorf("membership Role = %q, want %q (pre-admitted role must be preserved)", m.Role, campfire.RoleWriter)
	}
	if m.CampfireID != campfireID {
		t.Errorf("membership CampfireID = %q, want %q", m.CampfireID, campfireID)
	}
}

// TestJoinFilesystem_NewMember_NotAffected verifies that a brand-new joiner
// (not on disk, empty SQLite cache) still joins successfully — the fix must
// not change behavior for the normal join path.
func TestJoinFilesystem_NewMember_NotAffected(t *testing.T) {
	transportBaseDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	joinerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}

	campfireID := cfID.PublicKeyHex()
	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	// No member file on disk — this is a brand-new joiner.

	rawStore, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer rawStore.Close()

	wrappedStore := storage.NewLocalStorage(rawStore,
		storage.WithSelfPubkeyHex(joinerID.PublicKeyHex()),
		storage.WithTransportBaseDir(transportBaseDir),
	)

	// New-member join must succeed.
	if err := joinFilesystem(campfireID, joinerID, wrappedStore); err != nil {
		t.Fatalf("joinFilesystem for new member: %v", err)
	}

	m, err := wrappedStore.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership after join: %v", err)
	}
	if m == nil {
		t.Fatal("membership not recorded after joinFilesystem for new member")
	}
	// New joiner gets full role by default.
	if m.Role != campfire.RoleFull {
		t.Errorf("new member Role = %q, want %q", m.Role, campfire.RoleFull)
	}
}

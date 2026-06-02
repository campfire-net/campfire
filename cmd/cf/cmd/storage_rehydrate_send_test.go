package cmd

// storage_rehydrate_send_test.go — regression for campfireagent-27a.
//
// Proves that a protocol.Client constructed via the REAL production wiring
// (the same storage.NewLocalStorage(... WithSelfPubkeyHex ...) wrap that
// openStore/requireAgentAndStore now apply in helpers.go) can Send AND Read
// over a populated filesystem campfire directory while its SQLite cache is
// EMPTY — with NO separate AddMembership priming and NO sync-before-send call.
//
// Before this item the production store was a bare *store.Store: Send's
// membership gate (c.store.GetMembership) returned nil on the cold cache and
// the send was rejected with ErrNotMember. The fs-rehydrate fix
// (campfireagent-3fc) was inert because it was never wired at the construction
// sites. This test exercises the wired path end-to-end.
//
// Ground-source: real filesystem transport + real SQLite store. No mocks.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/pkg/storage"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// TestStorageRehydrateColdCacheSend verifies the production wiring: a
// LocalStorage-wrapped store (self-pubkey set) over an empty SQLite cache but a
// populated campfire dir lets Send and Read succeed without priming.
func TestStorageRehydrateColdCacheSend(t *testing.T) {
	baseDir := t.TempDir()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	// Populate the filesystem campfire dir (base-dir mode: baseDir/<id>/...),
	// exactly as Create/Join would leave it on disk.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}
	cf.AddMember(agentID.PublicKey)

	tr := fs.New(baseDir)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("init transport: %v", err)
	}
	campfireID := cf.PublicKeyHex()
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	// A real, EMPTY SQLite store — the membership cache is guaranteed cold.
	rawStore, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer rawStore.Close()

	// Sanity: the bare store has no membership. Without the wrapper, Send would
	// be rejected with ErrNotMember — this is the bug the wiring fixes.
	if m, err := rawStore.GetMembership(campfireID); err != nil {
		t.Fatalf("bare GetMembership: %v", err)
	} else if m != nil {
		t.Fatalf("bare GetMembership = %+v, want nil (empty cache)", m)
	}

	// PRODUCTION WIRING: wrap exactly as helpers.go now does (self-pubkey set so
	// the fs-rehydrate fallback can pick "me"; base dir pinned to the populated
	// campfire root).
	wrapped := storage.NewLocalStorage(rawStore,
		storage.WithSelfPubkeyHex(agentID.PublicKeyHex()),
		storage.WithTransportBaseDir(baseDir),
	)

	client := protocol.New(wrapped, agentID)

	// Send with NO prior AddMembership and NO sync — the rehydrate inside the
	// store gate must reconstruct the membership (and its TransportDir) from disk.
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("cold-cache send via rehydrate"),
	})
	if err != nil {
		t.Fatalf("Send over cold cache: %v (rehydrate did not fire — production wiring is inert)", err)
	}
	if msg == nil {
		t.Fatal("Send returned nil message")
	}

	// The rehydrate must have warmed the cache as a side effect.
	if warm, err := rawStore.GetMembership(campfireID); err != nil {
		t.Fatalf("warm GetMembership: %v", err)
	} else if warm == nil {
		t.Fatal("cache not warmed after rehydrating Send")
	}

	// Read back through the same client with NO separate sync/priming call. Send
	// mirrors the message into the local store, so it is immediately readable.
	res, err := client.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("Read after cold-cache send: %v", err)
	}
	found := false
	for _, m := range res.Messages {
		if m.ID == msg.ID {
			found = true
			if string(m.Payload) != "cold-cache send via rehydrate" {
				t.Errorf("unexpected payload: %q", string(m.Payload))
			}
		}
	}
	if !found {
		t.Fatalf("sent message %s not readable after cold-cache send; got %d messages", msg.ID[:8], len(res.Messages))
	}
}

package protocol_test

// Tests for SyncFilesystemChunk — the budget-friendly chunked sync primitive
// behind bounded foreground sync (campfireagent-6d3) — and for the chunked
// reimplementation of SyncFilesystem on top of it.

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// newChunkTestCampfire creates a campfire on a fresh fs transport dir and
// publishes n campfire-key-signed messages (valid signature + provenance hop,
// so the verified sync stores them). Returns the campfire ID and its
// transport directory.
func newChunkTestCampfire(t *testing.T, n int) (campfireID, cfDir string) {
	t.Helper()
	creator := newJoinClient(t)
	base := t.TempDir()
	createResult, err := creator.Create(protocol.CreateRequest{
		Transport:    &protocol.FilesystemTransport{Dir: base},
		JoinProtocol: "open",
		BeaconDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	campfireID = createResult.CampfireID

	tr := fs.New(base)
	state, err := tr.ReadState(campfireID)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	members, err := tr.ListMembers(campfireID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	cf := state.ToCampfire(members)
	signer, err := message.NewEd25519Signer(
		ed25519.PrivateKey(state.PrivateKey),
		ed25519.PublicKey(state.PublicKey),
	)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	for i := 0; i < n; i++ {
		msg, msgErr := message.NewMessage(signer, []byte(fmt.Sprintf(`{"i":%d}`, i)), []string{"chunktest"}, nil)
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
	return campfireID, filepath.Join(base, campfireID)
}

func openChunkTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func countMessages(t *testing.T, s store.Store, campfireID string) int {
	t.Helper()
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	return len(msgs)
}

func TestSyncFilesystemChunk_BoundedDurableProgress(t *testing.T) {
	campfireID, cfDir := newChunkTestCampfire(t, 7)
	s := openChunkTestStore(t)

	// Chunk 1: 3 of 7.
	done, synced, err := protocol.SyncFilesystemChunk(s, campfireID, cfDir, 3)
	if err != nil {
		t.Fatalf("chunk 1: %v", err)
	}
	if done || synced != 3 {
		t.Fatalf("chunk 1: done=%v synced=%d, want false/3", done, synced)
	}
	if got := countMessages(t, s, campfireID); got != 3 {
		t.Fatalf("after chunk 1: store has %d, want 3 (bounded)", got)
	}

	// Chunk 2: next 3. All test messages were written within the clock-skew
	// lookback window, so this also exercises the strict-cursor progress
	// guard (a page entirely inside the window must not stall the cursor).
	done, synced, err = protocol.SyncFilesystemChunk(s, campfireID, cfDir, 3)
	if err != nil {
		t.Fatalf("chunk 2: %v", err)
	}
	if done || synced != 3 {
		t.Fatalf("chunk 2: done=%v synced=%d, want false/3 (no progress = lookback stall, campfireagent-6d3)", done, synced)
	}

	// Chunk 3: final message → done.
	done, synced, err = protocol.SyncFilesystemChunk(s, campfireID, cfDir, 3)
	if err != nil {
		t.Fatalf("chunk 3: %v", err)
	}
	if !done || synced != 1 {
		t.Fatalf("chunk 3: done=%v synced=%d, want true/1", done, synced)
	}
	if got := countMessages(t, s, campfireID); got != 7 {
		t.Fatalf("after chunk 3: store has %d, want 7", got)
	}

	// Chunk 4: nothing left, stays done, idempotent.
	done, synced, err = protocol.SyncFilesystemChunk(s, campfireID, cfDir, 3)
	if err != nil || !done || synced != 0 {
		t.Fatalf("chunk 4: done=%v synced=%d err=%v, want true/0/nil", done, synced, err)
	}
}

// TestSyncFilesystemChunk_SkipsUnverifiedAndAdvances plants a message with a
// corrupted signature; the chunked sync must skip it (not store it) and the
// cursor must advance past it so it is not re-read forever.
func TestSyncFilesystemChunk_SkipsUnverifiedAndAdvances(t *testing.T) {
	campfireID, cfDir := newChunkTestCampfire(t, 2)
	tr := fs.ForDir(cfDir)

	// Forge a message with a broken signature, written after the valid ones.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	forged, err := message.NewMessage(message.MustNewEd25519Signer(priv, pub), []byte("forged"), nil, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	forged.Signature[0] ^= 0xFF
	if err := tr.WriteMessage(campfireID, forged); err != nil {
		t.Fatalf("WriteMessage(forged): %v", err)
	}

	s := openChunkTestStore(t)
	done, synced, err := protocol.SyncFilesystemChunk(s, campfireID, cfDir, 0)
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if !done || synced != 2 {
		t.Fatalf("done=%v synced=%d, want true/2 (forged message skipped)", done, synced)
	}
	if has, _ := s.HasMessage(forged.ID); has {
		t.Fatal("forged message was stored")
	}

	// The cursor must be past the forged leaf: a re-sync reads nothing.
	done, synced, err = protocol.SyncFilesystemChunk(s, campfireID, cfDir, 0)
	if err != nil || !done || synced != 0 {
		t.Fatalf("re-sync: done=%v synced=%d err=%v, want true/0/nil (cursor advanced past unverifiable message)", done, synced, err)
	}
}

// TestSyncFilesystem_MultiChunkHistory verifies the chunked SyncFilesystem
// reimplementation drains a history larger than one internal chunk (1000).
func TestSyncFilesystem_MultiChunkHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("writes >1000 messages; skipped in -short")
	}
	const n = 1100
	campfireID, cfDir := newChunkTestCampfire(t, n)
	s := openChunkTestStore(t)

	if err := protocol.SyncFilesystem(s, campfireID, cfDir); err != nil {
		t.Fatalf("SyncFilesystem: %v", err)
	}
	if got := countMessages(t, s, campfireID); got != n {
		t.Fatalf("store has %d, want %d", got, n)
	}
}

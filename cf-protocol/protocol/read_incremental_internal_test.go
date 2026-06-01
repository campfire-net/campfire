package protocol

// Regression test for campfireagent-b1e: the SDK no-Syncer read path
// (syncIfFilesystem) must be incremental, mirroring the cmd StoreSyncer path
// (campfireagent-e58). Before the fix it re-read and re-verified the entire
// on-disk history on every Read — O(total) per call for SDK consumers (e.g.
// convention servers) reading filesystem campfires directly via protocol.Client.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/internal/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/internal/encoding"
	"github.com/campfire-net/campfire/cf-protocol/internal/message"
	"github.com/campfire-net/campfire/cf-protocol/internal/store"
	"github.com/campfire-net/campfire/cf-protocol/internal/transport/fs"
	"github.com/campfire-net/campfire/pkg/identity"
)

// countingStoreFS wraps a store.Store and counts AddMessage calls.
type countingStoreFS struct {
	store.Store
	addCalls int64
}

func (c *countingStoreFS) AddMessage(m store.MessageRecord) (bool, error) {
	atomic.AddInt64(&c.addCalls, 1)
	return c.Store.AddMessage(m)
}

func (c *countingStoreFS) calls() int64 { return atomic.LoadInt64(&c.addCalls) }

func TestSyncIfFilesystem_Incremental(t *testing.T) {
	// Strict cursor (no skew lookback) for a crisp assertion that steady-state
	// reads import zero. The lookback's skew-recovery is covered in the fs package.
	orig := fsSyncLookback
	fsSyncLookback = 0
	defer func() { fsSyncLookback = orig }()

	transportBaseDir := t.TempDir()
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()
	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	state := &campfire.CampfireState{
		PublicKey:    cfID.PublicKey,
		PrivateKey:   cfID.PrivateKey,
		JoinProtocol: "open",
		CreatedAt:    time.Now().UnixNano(),
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	tr := fs.New(transportBaseDir)
	base, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer base.Close()
	if err := base.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          "full",
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("add membership: %v", err)
	}
	cs := &countingStoreFS{Store: base}
	client := New(cs, nil)

	writeMsg := func(i int) {
		sender, err := identity.Generate()
		if err != nil {
			t.Fatalf("sender id: %v", err)
		}
		msg, err := message.NewMessage(message.MustNewEd25519Signer(sender.PrivateKey, sender.PublicKey),
			[]byte(fmt.Sprintf("msg %d", i)), []string{"status"}, []string{})
		if err != nil {
			t.Fatalf("new message %d: %v", i, err)
		}
		if err := msg.AddHop(cfID.PrivateKey, cfID.PublicKey, []byte("h"), 1, "open", []string{}, "full"); err != nil {
			t.Fatalf("add hop %d: %v", i, err)
		}
		if err := tr.WriteMessage(campfireID, msg); err != nil {
			t.Fatalf("write message %d: %v", i, err)
		}
		time.Sleep(time.Millisecond) // unique leaf
	}

	for i := 0; i < 3; i++ {
		writeMsg(i)
	}

	// First read imports all 3.
	if _, err := client.Read(ReadRequest{CampfireID: campfireID}); err != nil {
		t.Fatalf("first Read: %v", err)
	}
	if got := cs.calls(); got != 3 {
		t.Fatalf("first read AddMessage calls = %d, want 3", got)
	}

	// Steady-state read with no new messages must import zero (the regression).
	before := cs.calls()
	if _, err := client.Read(ReadRequest{CampfireID: campfireID}); err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if delta := cs.calls() - before; delta != 0 {
		t.Fatalf("steady-state read imported %d, want 0 (full-rescan regression)", delta)
	}

	// One new message imports exactly one.
	writeMsg(99)
	before = cs.calls()
	if _, err := client.Read(ReadRequest{CampfireID: campfireID}); err != nil {
		t.Fatalf("third Read: %v", err)
	}
	if delta := cs.calls() - before; delta != 1 {
		t.Fatalf("read after 1 new message imported %d, want 1", delta)
	}
}

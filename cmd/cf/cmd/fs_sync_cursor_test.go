package cmd

// Regression test for campfireagent-e58: incremental filesystem sync.
//
// Before the fix, syncFromFilesystem read, re-verified (Ed25519), and
// re-AddMessage'd the campfire's ENTIRE on-disk history on every Read / Await /
// Subscribe poll — O(total messages) per operation. On a multi-thousand-message
// campfire (e.g. an rd workspace) that cost ~4s per `rd create`. The mallcop-pro
// bakeoff measured a 25-pp pass-rate gap traced to this.
//
// The fix adds a leaf-filename sync cursor: each sync imports only messages
// written after the last imported leaf. This test proves incrementality by
// counting AddMessage calls: a steady-state sync with no new on-disk messages
// must perform ZERO AddMessage calls (not re-import the whole history), and a
// sync after one new message must import exactly one.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/campfire-net/campfire/pkg/identity"
)

// countingStore wraps a store.Store and counts AddMessage invocations so a test
// can assert that incremental sync only imports new messages.
type countingStore struct {
	store.Store
	addCalls int64
}

func (c *countingStore) AddMessage(m store.MessageRecord) (bool, error) {
	atomic.AddInt64(&c.addCalls, 1)
	return c.Store.AddMessage(m)
}

func (c *countingStore) calls() int64 { return atomic.LoadInt64(&c.addCalls) }

// TestLoadFSSyncLookback verifies the CF_FS_SYNC_LOOKBACK_MS env override
// (shared loader in the fs package, used by both sync paths).
func TestLoadFSSyncLookback(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", 2 * time.Second}, // unset → default
		{"0", 0},              // strict cursor
		{"500", 500 * time.Millisecond},
		{"-5", 2 * time.Second},         // negative → default (invalid)
		{"notanumber", 2 * time.Second}, // garbage → default
	}
	for _, c := range cases {
		t.Setenv("CF_FS_SYNC_LOOKBACK_MS", c.env)
		if c.env == "" {
			os.Unsetenv("CF_FS_SYNC_LOOKBACK_MS")
		}
		if got := fs.SyncLookbackFromEnv(); got != c.want {
			t.Errorf("CF_FS_SYNC_LOOKBACK_MS=%q: got %v, want %v", c.env, got, c.want)
		}
	}
}

// TestSyncFromFilesystem_Incremental verifies the cursor-based sync imports each
// message exactly once across repeated syncs.
func TestSyncFromFilesystem_Incremental(t *testing.T) {
	// Disable the clock-skew lookback for a crisp end-to-end assertion that the
	// persisted cursor makes steady-state syncs import zero and a single new
	// message import exactly one. The lookback window (production: 2s, anchored at
	// the cursor) intentionally re-reads the most recent messages each poll; its
	// skew-recovery behaviour is proven separately at the mechanism level in the
	// fs package (TestLookbackCursor_RecoversBackwardClockStep). Both together
	// show old history is never re-scanned — the campfireagent-e58 regression.
	origLookback := fsSyncLookback
	fsSyncLookback = 0
	defer func() { fsSyncLookback = origLookback }()

	transportBaseDir := t.TempDir()

	// Build an open-protocol filesystem campfire on disk.
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

	creator, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	tr := fs.New(transportBaseDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: creator.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("write member: %v", err)
	}

	// writeMsg appends one signed message (with a campfire provenance hop, so it
	// passes VerifyProvenance) to the transport.
	writeMsg := func(i int) {
		payload := []byte(fmt.Sprintf(`{"text":"message %d"}`, i))
		msg, err := message.NewMessage(message.MustNewEd25519Signer(creator.PrivateKey, creator.PublicKey), payload, []string{"test:msg"}, nil)
		if err != nil {
			t.Fatalf("new message %d: %v", i, err)
		}
		if err := msg.AddHop(cfID.PrivateKey, cfID.PublicKey, nil, 1, "open", []string{}, ""); err != nil {
			t.Fatalf("add hop %d: %v", i, err)
		}
		if err := tr.WriteMessage(campfireID, msg); err != nil {
			t.Fatalf("write message %d: %v", i, err)
		}
		time.Sleep(time.Millisecond) // unique nanos prefix
	}

	// Seed 3 messages on disk.
	for i := 0; i < 3; i++ {
		writeMsg(i)
	}

	base, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer base.Close()
	if err := base.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: tr.CampfireDir(campfireID),
		JoinProtocol: "open",
		Role:         campfire.RoleFull,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("add membership: %v", err)
	}
	cs := &countingStore{Store: base}

	transportDir := tr.CampfireDir(campfireID)

	// First sync: imports all 3.
	if err := syncFromFilesystem(campfireID, transportDir, cs); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := cs.calls(); got != 3 {
		t.Fatalf("first sync AddMessage calls = %d, want 3", got)
	}

	// Second sync with NO new on-disk messages: the core regression. Before the
	// fix this re-imported all 3; now it must import zero.
	before := cs.calls()
	if err := syncFromFilesystem(campfireID, transportDir, cs); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if delta := cs.calls() - before; delta != 0 {
		t.Fatalf("steady-state sync imported %d messages, want 0 (full re-sync regression)", delta)
	}

	// Write one more, sync: exactly one import.
	writeMsg(99)
	before = cs.calls()
	if err := syncFromFilesystem(campfireID, transportDir, cs); err != nil {
		t.Fatalf("third sync: %v", err)
	}
	if delta := cs.calls() - before; delta != 1 {
		t.Fatalf("sync after 1 new message imported %d, want 1", delta)
	}

	// All 4 messages must be present in the store.
	msgs, err := base.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("store has %d messages, want 4", len(msgs))
	}
}

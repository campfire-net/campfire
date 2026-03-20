package fs

// fs_test.go — test coverage for the filesystem transport (workspace-w6o).
//
// Tests cover:
//   - Init: creates directory structure and writes campfire state
//   - WriteMember / ListMembers / RemoveMember: round-trip member records
//   - ReadState: round-trip campfire state
//   - WriteMessage / ListMessages: round-trip message records, chronological order
//   - Remove: deletes the entire campfire directory
//   - atomicWriteCBOR: temp-file-then-rename produces consistent on-disk state
//   - ListMessages: returns nil (not error) when the messages dir is absent
//   - ListMessages: skips non-.cbor files without erroring

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
)

// newTestTransport creates a Transport backed by a fresh temp directory.
func newTestTransport(t *testing.T) *Transport {
	t.Helper()
	dir := t.TempDir()
	return New(dir)
}

// newTestCampfire creates a minimal Campfire for testing.
func newTestCampfire(t *testing.T) *campfire.Campfire {
	t.Helper()
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New() error: %v", err)
	}
	return cf
}

// newTestMessage creates a signed message for testing.
func newTestMessage(t *testing.T) *message.Message {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ed25519 key: %v", err)
	}
	msg, err := message.NewMessage(priv, pub, []byte("hello"), []string{"tag1"}, nil)
	if err != nil {
		t.Fatalf("message.NewMessage() error: %v", err)
	}
	return msg
}

// newTestIdentity creates a test identity.
func newTestIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate() error: %v", err)
	}
	return id
}

// pubKey returns the ed25519.PublicKey from an Identity.
func pubKey(id *identity.Identity) []byte {
	return id.PublicKey
}

// TestInit_CreatesDirectoryStructure verifies Init creates members/ and messages/ subdirs.
func TestInit_CreatesDirectoryStructure(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)

	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	cfDir := tr.CampfireDir(cf.PublicKeyHex())
	for _, sub := range []string{"members", "messages"} {
		path := filepath.Join(cfDir, sub)
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Errorf("expected %s to be a directory after Init", path)
		}
	}
}

// TestInit_WritesCampfireState verifies the campfire.cbor state file is written.
func TestInit_WritesCampfireState(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)

	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	state, err := tr.ReadState(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ReadState() error: %v", err)
	}
	if string(state.PublicKey) != string(cf.PublicKey) {
		t.Errorf("state.PublicKey mismatch")
	}
	if state.JoinProtocol != cf.JoinProtocol {
		t.Errorf("state.JoinProtocol = %q, want %q", state.JoinProtocol, cf.JoinProtocol)
	}
}

// TestReadState_NotFound verifies ReadState returns an error for a missing file.
func TestReadState_NotFound(t *testing.T) {
	tr := newTestTransport(t)
	_, err := tr.ReadState("nonexistent-campfire-id")
	if err == nil {
		t.Error("expected error reading state for nonexistent campfire")
	}
}

// TestWriteMember_ListMembers_RoundTrip verifies member records survive a write/list cycle.
func TestWriteMember_ListMembers_RoundTrip(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	id := newTestIdentity(t)
	rec := campfire.MemberRecord{
		PublicKey: pubKey(id),
		JoinedAt:  time.Now().UnixNano(),
		Role:      "full",
	}

	if err := tr.WriteMember(cf.PublicKeyHex(), rec); err != nil {
		t.Fatalf("WriteMember() error: %v", err)
	}

	members, err := tr.ListMembers(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ListMembers() error: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("got %d members, want 1", len(members))
	}
	if string(members[0].PublicKey) != string(rec.PublicKey) {
		t.Errorf("member public key mismatch")
	}
	if members[0].Role != rec.Role {
		t.Errorf("member role = %q, want %q", members[0].Role, rec.Role)
	}
}

// TestRemoveMember removes a member record and verifies it is gone.
func TestRemoveMember(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	id := newTestIdentity(t)
	rec := campfire.MemberRecord{PublicKey: pubKey(id), JoinedAt: 1}
	if err := tr.WriteMember(cf.PublicKeyHex(), rec); err != nil {
		t.Fatalf("WriteMember() error: %v", err)
	}

	if err := tr.RemoveMember(cf.PublicKeyHex(), pubKey(id)); err != nil {
		t.Fatalf("RemoveMember() error: %v", err)
	}

	members, err := tr.ListMembers(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ListMembers() after remove error: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("got %d members after remove, want 0", len(members))
	}
}

// TestRemoveMember_Idempotent verifies removing a non-existent member doesn't error.
func TestRemoveMember_Idempotent(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	id := newTestIdentity(t)
	// Never wrote this member — remove should be a no-op.
	if err := tr.RemoveMember(cf.PublicKeyHex(), pubKey(id)); err != nil {
		t.Errorf("RemoveMember() on nonexistent member should not error: %v", err)
	}
}

// TestWriteMessage_ListMessages_RoundTrip verifies messages survive a write/list cycle.
func TestWriteMessage_ListMessages_RoundTrip(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	msg := newTestMessage(t)
	if err := tr.WriteMessage(cf.PublicKeyHex(), msg); err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}

	msgs, err := tr.ListMessages(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ListMessages() error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].ID != msg.ID {
		t.Errorf("message ID = %q, want %q", msgs[0].ID, msg.ID)
	}
	if string(msgs[0].Payload) != string(msg.Payload) {
		t.Errorf("message payload mismatch")
	}
	if string(msgs[0].Signature) != string(msg.Signature) {
		t.Errorf("message signature mismatch")
	}
}

// TestListMessages_ChronologicalOrder verifies messages are returned in timestamp order.
func TestListMessages_ChronologicalOrder(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	// Write three messages with a brief sleep between each to guarantee different
	// UnixNano timestamps (filename prefix determines sort order).
	var ids []string
	for i := 0; i < 3; i++ {
		msg, err := message.NewMessage(priv, pub, []byte{byte(i)}, nil, nil)
		if err != nil {
			t.Fatalf("NewMessage() error: %v", err)
		}
		if err := tr.WriteMessage(cf.PublicKeyHex(), msg); err != nil {
			t.Fatalf("WriteMessage() error: %v", err)
		}
		ids = append(ids, msg.ID)
		time.Sleep(time.Millisecond) // ensure unique nano timestamps
	}

	msgs, err := tr.ListMessages(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ListMessages() error: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	for i, id := range ids {
		if msgs[i].ID != id {
			t.Errorf("msgs[%d].ID = %q, want %q (not in write order)", i, msgs[i].ID, id)
		}
	}
}

// TestListMessages_EmptyDir returns nil slice (not error) when messages dir exists but is empty.
func TestListMessages_EmptyDir(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	msgs, err := tr.ListMessages(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ListMessages() on empty dir error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("got %d messages from empty dir, want 0", len(msgs))
	}
}

// TestListMessages_MissingDir returns nil/nil when the messages directory doesn't exist.
func TestListMessages_MissingDir(t *testing.T) {
	tr := newTestTransport(t)
	msgs, err := tr.ListMessages("campfire-without-dir")
	if err != nil {
		t.Fatalf("ListMessages() on missing dir should return nil error, got: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil slice for missing dir, got %v", msgs)
	}
}

// TestListMessages_SkipsNonCBOR verifies that non-.cbor files in messages/ are ignored.
func TestListMessages_SkipsNonCBOR(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	// Write a garbage file that is not a .cbor file.
	junkPath := filepath.Join(tr.CampfireDir(cf.PublicKeyHex()), "messages", "junk.txt")
	if err := os.WriteFile(junkPath, []byte("not cbor"), 0644); err != nil {
		t.Fatalf("writing junk file: %v", err)
	}

	// Write a real message.
	msg := newTestMessage(t)
	if err := tr.WriteMessage(cf.PublicKeyHex(), msg); err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}

	msgs, err := tr.ListMessages(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ListMessages() error: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("got %d messages (expected junk.txt to be skipped), want 1", len(msgs))
	}
}

// TestRemove deletes the entire campfire directory.
func TestRemove(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	if err := tr.Remove(cf.PublicKeyHex()); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}

	if _, err := os.Stat(tr.CampfireDir(cf.PublicKeyHex())); !os.IsNotExist(err) {
		t.Errorf("campfire dir should not exist after Remove")
	}
}

// TestDefaultBaseDir verifies the fallback path and CF_TRANSPORT_DIR env override.
func TestDefaultBaseDir(t *testing.T) {
	// Default.
	dir := DefaultBaseDir()
	if dir != "/tmp/campfire" {
		// Only fail if the env var is also not set.
		if os.Getenv("CF_TRANSPORT_DIR") == "" {
			t.Errorf("DefaultBaseDir() = %q, want /tmp/campfire", dir)
		}
	}

	// Override via env var.
	t.Setenv("CF_TRANSPORT_DIR", "/custom/transport")
	if got := DefaultBaseDir(); got != "/custom/transport" {
		t.Errorf("DefaultBaseDir() with env = %q, want /custom/transport", got)
	}
}

// TestAtomicWriteCBOR_NoPartialFile verifies the temp-rename write leaves no partial file on success.
func TestAtomicWriteCBOR_NoPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.cbor")

	type payload struct {
		Value string `cbor:"1,keyasint"`
	}
	if err := atomicWriteCBOR(path, payload{Value: "hello"}); err != nil {
		t.Fatalf("atomicWriteCBOR() error: %v", err)
	}

	// Final file must exist.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected final file to exist: %v", err)
	}

	// No tmp files must remain.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "test.cbor" {
			t.Errorf("unexpected leftover file: %s", e.Name())
		}
	}
}

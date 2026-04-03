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
//   - AddPushSubscriber / RemovePushSubscriber / ListPushSubscribers: push subscriber management
//   - WriteMessage: delivers message to all push subscribers' inbox dirs
//   - WriteMessage: remove subscriber stops delivery
//   - WriteMessage: missing inbox dir is non-fatal, other subscribers still receive
//   - copyFile: deduplicates by filename (O_EXCL — second write is a no-op)

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
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

// TestNewPathRooted_CampfireDirIgnoresID verifies path-rooted mode returns the
// fixed directory regardless of the campfire ID argument.
func TestNewPathRooted_CampfireDirIgnoresID(t *testing.T) {
	dir := t.TempDir()
	tr := NewPathRooted(dir)

	if !tr.IsPathRooted() {
		t.Fatal("expected IsPathRooted() = true")
	}
	if got := tr.CampfireDir("any-id"); got != dir {
		t.Errorf("CampfireDir() = %q, want %q", got, dir)
	}
	if got := tr.CampfireDir("different-id"); got != dir {
		t.Errorf("CampfireDir() = %q, want %q", got, dir)
	}
}

// TestNewPathRooted_FullLifecycle verifies Init, WriteMember, WriteMessage, ReadState,
// ListMembers, ListMessages, and Remove all work in project-rooted mode.
func TestNewPathRooted_FullLifecycle(t *testing.T) {
	dir := t.TempDir()
	tr := NewPathRooted(dir)
	cf := newTestCampfire(t)

	// Init creates directory structure in the project dir.
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	for _, sub := range []string{"members", "messages"} {
		if info, err := os.Stat(filepath.Join(dir, sub)); err != nil || !info.IsDir() {
			t.Errorf("expected %s subdirectory in project dir", sub)
		}
	}

	// ReadState works.
	state, err := tr.ReadState(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ReadState() error: %v", err)
	}
	if state.JoinProtocol != cf.JoinProtocol {
		t.Errorf("JoinProtocol = %q, want %q", state.JoinProtocol, cf.JoinProtocol)
	}

	// WriteMember + ListMembers round-trip.
	id := newTestIdentity(t)
	rec := campfire.MemberRecord{PublicKey: pubKey(id), JoinedAt: 1, Role: "full"}
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

	// WriteMessage + ListMessages round-trip.
	msg := newTestMessage(t)
	if err := tr.WriteMessage(cf.PublicKeyHex(), msg); err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}
	msgs, err := tr.ListMessages(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ListMessages() error: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != msg.ID {
		t.Errorf("message round-trip failed: got %d msgs", len(msgs))
	}

	// Remove deletes the project directory contents.
	if err := tr.Remove(cf.PublicKeyHex()); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("project dir should not exist after Remove")
	}
}

// TestMultiLevel_SiblingPathRooted verifies two path-rooted campfires in sibling
// directories are fully independent — messages and members don't leak.
func TestMultiLevel_SiblingPathRooted(t *testing.T) {
	root := t.TempDir()
	dirA := filepath.Join(root, "project-a", ".campfire")
	dirB := filepath.Join(root, "project-b", ".campfire")
	os.MkdirAll(dirA, 0755)
	os.MkdirAll(dirB, 0755)

	trA := NewPathRooted(dirA)
	trB := NewPathRooted(dirB)
	cfA := newTestCampfire(t)
	cfB := newTestCampfire(t)

	if err := trA.Init(cfA); err != nil {
		t.Fatalf("Init A: %v", err)
	}
	if err := trB.Init(cfB); err != nil {
		t.Fatalf("Init B: %v", err)
	}

	// Write a message to A only.
	msgA := newTestMessage(t)
	if err := trA.WriteMessage(cfA.PublicKeyHex(), msgA); err != nil {
		t.Fatalf("WriteMessage A: %v", err)
	}

	// Write a member to B only.
	idB := newTestIdentity(t)
	recB := campfire.MemberRecord{PublicKey: pubKey(idB), JoinedAt: 1, Role: "full"}
	if err := trB.WriteMember(cfB.PublicKeyHex(), recB); err != nil {
		t.Fatalf("WriteMember B: %v", err)
	}

	// A has 1 message, 0 members (besides what Init wrote).
	msgsA, _ := trA.ListMessages(cfA.PublicKeyHex())
	if len(msgsA) != 1 {
		t.Errorf("A messages: got %d, want 1", len(msgsA))
	}
	membersA, _ := trA.ListMembers(cfA.PublicKeyHex())
	if len(membersA) != 0 {
		t.Errorf("A members: got %d, want 0", len(membersA))
	}

	// B has 0 messages, 1 member.
	msgsB, _ := trB.ListMessages(cfB.PublicKeyHex())
	if len(msgsB) != 0 {
		t.Errorf("B messages: got %d, want 0", len(msgsB))
	}
	membersB, _ := trB.ListMembers(cfB.PublicKeyHex())
	if len(membersB) != 1 {
		t.Errorf("B members: got %d, want 1", len(membersB))
	}

	// Removing A doesn't affect B.
	if err := trA.Remove(cfA.PublicKeyHex()); err != nil {
		t.Fatalf("Remove A: %v", err)
	}
	stateB, err := trB.ReadState(cfB.PublicKeyHex())
	if err != nil {
		t.Fatalf("ReadState B after removing A: %v", err)
	}
	if stateB.JoinProtocol != cfB.JoinProtocol {
		t.Errorf("B state corrupted after removing A")
	}
}

// TestMultiLevel_BaseDirMultipleCampfires verifies a standard base-dir transport
// hosts multiple campfires independently alongside a path-rooted one.
func TestMultiLevel_BaseDirMultipleCampfires(t *testing.T) {
	baseDir := t.TempDir()
	trBase := New(baseDir)

	// Two campfires in the same base-dir transport.
	cf1 := newTestCampfire(t)
	cf2 := newTestCampfire(t)
	if err := trBase.Init(cf1); err != nil {
		t.Fatalf("Init cf1: %v", err)
	}
	if err := trBase.Init(cf2); err != nil {
		t.Fatalf("Init cf2: %v", err)
	}

	// A third campfire is path-rooted elsewhere.
	pathDir := filepath.Join(t.TempDir(), ".campfire")
	os.MkdirAll(pathDir, 0755)
	trPath := NewPathRooted(pathDir)
	cf3 := newTestCampfire(t)
	if err := trPath.Init(cf3); err != nil {
		t.Fatalf("Init cf3: %v", err)
	}

	// Write messages to each.
	msg1 := newTestMessage(t)
	msg2 := newTestMessage(t)
	msg3 := newTestMessage(t)
	if err := trBase.WriteMessage(cf1.PublicKeyHex(), msg1); err != nil {
		t.Fatalf("WriteMessage cf1: %v", err)
	}
	if err := trBase.WriteMessage(cf2.PublicKeyHex(), msg2); err != nil {
		t.Fatalf("WriteMessage cf2: %v", err)
	}
	if err := trPath.WriteMessage(cf3.PublicKeyHex(), msg3); err != nil {
		t.Fatalf("WriteMessage cf3: %v", err)
	}

	// Each campfire sees only its own message.
	for _, tc := range []struct {
		name string
		tr   *Transport
		cfID string
		want string
	}{
		{"cf1", trBase, cf1.PublicKeyHex(), msg1.ID},
		{"cf2", trBase, cf2.PublicKeyHex(), msg2.ID},
		{"cf3", trPath, cf3.PublicKeyHex(), msg3.ID},
	} {
		msgs, err := tc.tr.ListMessages(tc.cfID)
		if err != nil {
			t.Fatalf("ListMessages %s: %v", tc.name, err)
		}
		if len(msgs) != 1 {
			t.Errorf("%s: got %d messages, want 1", tc.name, len(msgs))
			continue
		}
		if msgs[0].ID != tc.want {
			t.Errorf("%s: got message ID %q, want %q", tc.name, msgs[0].ID, tc.want)
		}
	}

	// Removing cf1 from base-dir doesn't affect cf2 or cf3.
	if err := trBase.Remove(cf1.PublicKeyHex()); err != nil {
		t.Fatalf("Remove cf1: %v", err)
	}
	if _, err := trBase.ReadState(cf2.PublicKeyHex()); err != nil {
		t.Errorf("cf2 state missing after removing cf1: %v", err)
	}
	if _, err := trPath.ReadState(cf3.PublicKeyHex()); err != nil {
		t.Errorf("cf3 state missing after removing cf1: %v", err)
	}
}

// TestMultiLevel_NestedPathRooted verifies a parent directory and a child directory
// can each have their own path-rooted campfire without interference.
func TestMultiLevel_NestedPathRooted(t *testing.T) {
	root := t.TempDir()
	parentCF := filepath.Join(root, ".campfire")
	childCF := filepath.Join(root, "subdir", ".campfire")
	os.MkdirAll(parentCF, 0755)
	os.MkdirAll(childCF, 0755)

	trParent := NewPathRooted(parentCF)
	trChild := NewPathRooted(childCF)
	cfParent := newTestCampfire(t)
	cfChild := newTestCampfire(t)

	if err := trParent.Init(cfParent); err != nil {
		t.Fatalf("Init parent: %v", err)
	}
	if err := trChild.Init(cfChild); err != nil {
		t.Fatalf("Init child: %v", err)
	}

	// Write messages to each.
	msgP := newTestMessage(t)
	msgC := newTestMessage(t)
	if err := trParent.WriteMessage(cfParent.PublicKeyHex(), msgP); err != nil {
		t.Fatalf("WriteMessage parent: %v", err)
	}
	if err := trChild.WriteMessage(cfChild.PublicKeyHex(), msgC); err != nil {
		t.Fatalf("WriteMessage child: %v", err)
	}

	// Parent sees only its message.
	msgsP, _ := trParent.ListMessages(cfParent.PublicKeyHex())
	if len(msgsP) != 1 || msgsP[0].ID != msgP.ID {
		t.Errorf("parent: expected 1 message with ID %q, got %d", msgP.ID, len(msgsP))
	}

	// Child sees only its message.
	msgsC, _ := trChild.ListMessages(cfChild.PublicKeyHex())
	if len(msgsC) != 1 || msgsC[0].ID != msgC.ID {
		t.Errorf("child: expected 1 message with ID %q, got %d", msgC.ID, len(msgsC))
	}

	// Write a member to parent, verify child doesn't see it.
	idP := newTestIdentity(t)
	recP := campfire.MemberRecord{PublicKey: pubKey(idP), JoinedAt: 1, Role: "full"}
	if err := trParent.WriteMember(cfParent.PublicKeyHex(), recP); err != nil {
		t.Fatalf("WriteMember parent: %v", err)
	}
	membersC, _ := trChild.ListMembers(cfChild.PublicKeyHex())
	if len(membersC) != 0 {
		t.Errorf("child has %d members, want 0 (leaked from parent)", len(membersC))
	}

	// State reads are independent.
	stateP, _ := trParent.ReadState(cfParent.PublicKeyHex())
	stateC, _ := trChild.ReadState(cfChild.PublicKeyHex())
	if string(stateP.PublicKey) == string(stateC.PublicKey) {
		t.Error("parent and child have identical public keys — test is broken")
	}

	// Removing child doesn't affect parent.
	if err := trChild.Remove(cfChild.PublicKeyHex()); err != nil {
		t.Fatalf("Remove child: %v", err)
	}
	if _, err := trParent.ReadState(cfParent.PublicKeyHex()); err != nil {
		t.Errorf("parent state missing after removing child: %v", err)
	}
	msgsP2, _ := trParent.ListMessages(cfParent.PublicKeyHex())
	if len(msgsP2) != 1 {
		t.Errorf("parent lost messages after removing child: got %d", len(msgsP2))
	}
}

// TestMultiLevel_PathRootedAndBaseDirSameParent verifies a path-rooted campfire and
// a base-dir campfire sharing the same parent directory don't collide.
func TestMultiLevel_PathRootedAndBaseDirSameParent(t *testing.T) {
	root := t.TempDir()

	// Path-rooted campfire at root/.campfire
	pathDir := filepath.Join(root, ".campfire")
	os.MkdirAll(pathDir, 0755)
	trPath := NewPathRooted(pathDir)
	cfPath := newTestCampfire(t)
	if err := trPath.Init(cfPath); err != nil {
		t.Fatalf("Init path-rooted: %v", err)
	}

	// Base-dir transport also rooted at root/ — campfire dirs are root/<id>
	trBase := New(root)
	cfBase := newTestCampfire(t)
	if err := trBase.Init(cfBase); err != nil {
		t.Fatalf("Init base-dir: %v", err)
	}

	// They resolve to different directories.
	dirPath := trPath.CampfireDir(cfPath.PublicKeyHex())
	dirBase := trBase.CampfireDir(cfBase.PublicKeyHex())
	if dirPath == dirBase {
		t.Fatal("path-rooted and base-dir resolved to same directory")
	}

	// Write to each, verify isolation.
	msgPath := newTestMessage(t)
	msgBase := newTestMessage(t)
	trPath.WriteMessage(cfPath.PublicKeyHex(), msgPath)
	trBase.WriteMessage(cfBase.PublicKeyHex(), msgBase)

	msgsPath, _ := trPath.ListMessages(cfPath.PublicKeyHex())
	msgsBase, _ := trBase.ListMessages(cfBase.PublicKeyHex())
	if len(msgsPath) != 1 || msgsPath[0].ID != msgPath.ID {
		t.Errorf("path-rooted: wrong messages")
	}
	if len(msgsBase) != 1 || msgsBase[0].ID != msgBase.ID {
		t.Errorf("base-dir: wrong messages")
	}
}

// TestNew_IsNotPathRooted verifies the standard constructor is not path-rooted.
func TestNew_IsNotPathRooted(t *testing.T) {
	tr := New(t.TempDir())
	if tr.IsPathRooted() {
		t.Error("expected IsPathRooted() = false for New()")
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

// TestAtomicWriteCBOR_RandFallback verifies that when crypto/rand fails, the timestamp
// fallback produces a non-empty temp filename and the write still succeeds.
func TestAtomicWriteCBOR_RandFallback(t *testing.T) {
	// Inject a failing rand reader to force the timestamp fallback path.
	orig := randRead
	randRead = func(b []byte) (int, error) {
		return 0, fmt.Errorf("injected rand failure")
	}
	defer func() { randRead = orig }()

	dir := t.TempDir()
	path := filepath.Join(dir, "fallback.cbor")

	type payload struct {
		Value string `cbor:"1,keyasint"`
	}
	if err := atomicWriteCBOR(path, payload{Value: "fallback"}); err != nil {
		t.Fatalf("atomicWriteCBOR() with rand failure should succeed via fallback, got error: %v", err)
	}

	// Final file must exist and have non-zero size.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected final file to exist after fallback write: %v", err)
	}
	if info.Size() == 0 {
		t.Error("expected non-empty file after fallback write")
	}

	// No tmp files must remain (rename cleaned up).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "fallback.cbor" {
			t.Errorf("unexpected leftover temp file after fallback write: %s", e.Name())
		}
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

// --- Push subscriber tests ---

// TestPushSubscriber_BasicDelivery verifies that a message written to a campfire
// is copied to a registered subscriber's inbox directory.
func TestPushSubscriber_BasicDelivery(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	inboxDir := t.TempDir()
	id := newTestIdentity(t)

	if err := tr.AddPushSubscriber(cf.PublicKeyHex(), pubKey(id), inboxDir); err != nil {
		t.Fatalf("AddPushSubscriber() error: %v", err)
	}

	msg := newTestMessage(t)
	if err := tr.WriteMessage(cf.PublicKeyHex(), msg); err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}

	// Inbox must contain exactly one .cbor file.
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		t.Fatalf("ReadDir(inboxDir) error: %v", err)
	}
	var cborFiles []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".cbor" {
			cborFiles = append(cborFiles, e.Name())
		}
	}
	if len(cborFiles) != 1 {
		t.Fatalf("inbox has %d .cbor files, want 1", len(cborFiles))
	}

	// The delivered file must be non-empty (CBOR bytes, verbatim copy).
	info, err := os.Stat(filepath.Join(inboxDir, cborFiles[0]))
	if err != nil {
		t.Fatalf("Stat delivered file: %v", err)
	}
	if info.Size() == 0 {
		t.Error("delivered file is empty")
	}
}

// TestPushSubscriber_RemoveStopsDelivery verifies that removing a subscriber
// prevents further deliveries.
func TestPushSubscriber_RemoveStopsDelivery(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	inboxDir := t.TempDir()
	id := newTestIdentity(t)

	if err := tr.AddPushSubscriber(cf.PublicKeyHex(), pubKey(id), inboxDir); err != nil {
		t.Fatalf("AddPushSubscriber() error: %v", err)
	}

	// First message is delivered.
	msg1 := newTestMessage(t)
	if err := tr.WriteMessage(cf.PublicKeyHex(), msg1); err != nil {
		t.Fatalf("WriteMessage() msg1 error: %v", err)
	}

	// Remove subscriber.
	if err := tr.RemovePushSubscriber(cf.PublicKeyHex(), pubKey(id)); err != nil {
		t.Fatalf("RemovePushSubscriber() error: %v", err)
	}

	// Second message must NOT be delivered.
	msg2 := newTestMessage(t)
	if err := tr.WriteMessage(cf.PublicKeyHex(), msg2); err != nil {
		t.Fatalf("WriteMessage() msg2 error: %v", err)
	}

	entries, _ := os.ReadDir(inboxDir)
	var cborFiles []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".cbor" {
			cborFiles = append(cborFiles, e.Name())
		}
	}
	if len(cborFiles) != 1 {
		t.Errorf("inbox has %d .cbor files after remove, want 1 (only pre-remove message)", len(cborFiles))
	}
}

// TestPushSubscriber_MultipleSubscribers verifies that all registered subscribers
// receive the message.
func TestPushSubscriber_MultipleSubscribers(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	inboxA := t.TempDir()
	inboxB := t.TempDir()
	inboxC := t.TempDir()

	idA := newTestIdentity(t)
	idB := newTestIdentity(t)
	idC := newTestIdentity(t)

	for _, tc := range []struct {
		id    *identity.Identity
		inbox string
	}{
		{idA, inboxA},
		{idB, inboxB},
		{idC, inboxC},
	} {
		if err := tr.AddPushSubscriber(cf.PublicKeyHex(), pubKey(tc.id), tc.inbox); err != nil {
			t.Fatalf("AddPushSubscriber() error: %v", err)
		}
	}

	msg := newTestMessage(t)
	if err := tr.WriteMessage(cf.PublicKeyHex(), msg); err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}

	for _, inbox := range []string{inboxA, inboxB, inboxC} {
		entries, err := os.ReadDir(inbox)
		if err != nil {
			t.Fatalf("ReadDir(%s) error: %v", inbox, err)
		}
		var cborFiles int
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".cbor" {
				cborFiles++
			}
		}
		if cborFiles != 1 {
			t.Errorf("inbox %s has %d .cbor files, want 1", inbox, cborFiles)
		}
	}
}

// TestPushSubscriber_MissingInboxDir verifies that if a subscriber's inbox dir
// does not exist, delivery fails gracefully — no crash, other subscribers still
// receive the message.
func TestPushSubscriber_MissingInboxDir(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	// Subscriber with a nonexistent inbox dir.
	idBad := newTestIdentity(t)
	missingDir := filepath.Join(t.TempDir(), "does-not-exist", "inbox")
	// Do NOT create missingDir — that's the point of this test.

	// Subscriber with a valid inbox dir.
	idGood := newTestIdentity(t)
	goodInbox := t.TempDir()

	if err := tr.AddPushSubscriber(cf.PublicKeyHex(), pubKey(idBad), missingDir); err != nil {
		t.Fatalf("AddPushSubscriber(bad) error: %v", err)
	}
	if err := tr.AddPushSubscriber(cf.PublicKeyHex(), pubKey(idGood), goodInbox); err != nil {
		t.Fatalf("AddPushSubscriber(good) error: %v", err)
	}

	msg := newTestMessage(t)
	// Must not crash or return an error — missing inbox is non-fatal.
	if err := tr.WriteMessage(cf.PublicKeyHex(), msg); err != nil {
		t.Fatalf("WriteMessage() returned error on missing inbox: %v", err)
	}

	// The good subscriber still received the message.
	entries, err := os.ReadDir(goodInbox)
	if err != nil {
		t.Fatalf("ReadDir(goodInbox) error: %v", err)
	}
	var cborFiles int
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".cbor" {
			cborFiles++
		}
	}
	if cborFiles != 1 {
		t.Errorf("good inbox has %d .cbor files, want 1", cborFiles)
	}
}

// TestPushSubscriber_ListPushSubscribers verifies round-trip list of subscribers.
func TestPushSubscriber_ListPushSubscribers(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	// No subscribers yet.
	subs, err := tr.ListPushSubscribers(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ListPushSubscribers() error: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("expected 0 subscribers, got %d", len(subs))
	}

	idA := newTestIdentity(t)
	idB := newTestIdentity(t)
	inboxA := t.TempDir()
	inboxB := t.TempDir()

	if err := tr.AddPushSubscriber(cf.PublicKeyHex(), pubKey(idA), inboxA); err != nil {
		t.Fatalf("AddPushSubscriber(A) error: %v", err)
	}
	if err := tr.AddPushSubscriber(cf.PublicKeyHex(), pubKey(idB), inboxB); err != nil {
		t.Fatalf("AddPushSubscriber(B) error: %v", err)
	}

	subs, err = tr.ListPushSubscribers(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ListPushSubscribers() error: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 subscribers, got %d", len(subs))
	}

	// Verify inbox paths are present.
	inboxPaths := map[string]bool{}
	for _, s := range subs {
		inboxPaths[s.InboxDir] = true
	}
	if !inboxPaths[inboxA] {
		t.Errorf("inboxA not found in subscriber list")
	}
	if !inboxPaths[inboxB] {
		t.Errorf("inboxB not found in subscriber list")
	}
}

// TestPushSubscriber_RemovePushSubscriber_Idempotent verifies removing a
// non-existent subscriber does not error.
func TestPushSubscriber_RemovePushSubscriber_Idempotent(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	id := newTestIdentity(t)
	if err := tr.RemovePushSubscriber(cf.PublicKeyHex(), pubKey(id)); err != nil {
		t.Errorf("RemovePushSubscriber() on nonexistent subscriber should not error: %v", err)
	}
}

// TestPushSubscriber_DeduplicateByFilename verifies that writing the same message
// filename twice to an inbox does not result in an error (dedup by UUID filename).
func TestPushSubscriber_DeduplicateByFilename(t *testing.T) {
	inboxDir := t.TempDir()
	src := filepath.Join(t.TempDir(), "msg.cbor")
	if err := os.WriteFile(src, []byte("data"), 0600); err != nil {
		t.Fatalf("writing src: %v", err)
	}

	dst := filepath.Join(inboxDir, "msg.cbor")

	// First copy.
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("first copyFile() error: %v", err)
	}
	// Second copy — same filename — must not error (dedup).
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("second copyFile() (dedup) error: %v", err)
	}
}

// TestInit_DirectoryPermissions verifies that Init creates campfire directories
// with mode 0700, not 0755. Campfire directories contain campfire.cbor which
// holds the campfire private key — they must not be world-readable.
func TestInit_DirectoryPermissions(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)

	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	cfDir := tr.CampfireDir(cf.PublicKeyHex())
	want := os.FileMode(0700)

	for _, sub := range []string{"", "members", "messages"} {
		dir := cfDir
		if sub != "" {
			dir = filepath.Join(cfDir, sub)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		got := info.Mode().Perm()
		if got != want {
			t.Errorf("directory %s has mode %04o, want %04o (directories holding private key material must be owner-only)", dir, got, want)
		}
	}
}

// TestAddPushSubscriber_DirectoryPermissions verifies that the push-subscribers
// directory is created with mode 0700.
func TestAddPushSubscriber_DirectoryPermissions(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	id := newTestIdentity(t)
	inboxDir := t.TempDir()
	if err := tr.AddPushSubscriber(cf.PublicKeyHex(), pubKey(id), inboxDir); err != nil {
		t.Fatalf("AddPushSubscriber() error: %v", err)
	}

	subDir := filepath.Join(tr.CampfireDir(cf.PublicKeyHex()), "push-subscribers")
	info, err := os.Stat(subDir)
	if err != nil {
		t.Fatalf("stat push-subscribers dir: %v", err)
	}
	got := info.Mode().Perm()
	want := os.FileMode(0700)
	if got != want {
		t.Errorf("push-subscribers dir has mode %04o, want %04o", got, want)
	}
}

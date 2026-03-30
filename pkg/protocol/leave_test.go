package protocol_test

// leave_test.go — veracity tests for protocol.Client.Leave().
//
// Covered bead: campfire-agent-wfm
//
// All 6 done conditions are covered:
//   1. LEAVE-THEN-SEND-REJECTED: B joins, sends (proves membership), leaves, send fails.
//   2. LEAVE REMOVES MEMBER RECORD: After B leaves, Members() does not include B.
//   3. LEAVE DOES NOT AFFECT OTHER MEMBERS: B leaves; A and C still send and read.
//   4. LEAVE IDEMPOTENT: Second Leave returns *ErrNotMember, no panic.
//   5. FILESYSTEM STATE CLEANUP: After Leave, B's .cbor file is gone from members dir.
//   6. go test ./pkg/protocol/ -run TestLeave passes.
//
// No mocks. Real filesystem dirs. Real SQLite stores.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// leaveTestEnv holds everything needed for a Leave test scenario.
type leaveTestEnv struct {
	// transportBaseDir is the base directory for fs transport (shared across agents).
	transportBaseDir string

	// campfireID is the shared campfire used throughout the test.
	campfireID string

	// A is the campfire creator.
	idA   *identity.Identity
	storeA store.Store
	clientA *protocol.Client

	// B is the member who will leave.
	idB   *identity.Identity
	storeB store.Store
	clientB *protocol.Client

	// C is an additional member (used in DC 3 only; populated by addMemberC).
	idC   *identity.Identity
	storeC store.Store
	clientC *protocol.Client
}

// setupLeaveEnv creates a campfire with A as creator, admits B via Admit+Join,
// and returns a leaveTestEnv.
func setupLeaveEnv(t *testing.T) *leaveTestEnv {
	t.Helper()

	transportBaseDir := t.TempDir()

	// A: creator
	idA, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity A: %v", err)
	}
	storeADir := t.TempDir()
	storeA, err := store.Open(filepath.Join(storeADir, "store.db"))
	if err != nil {
		t.Fatalf("opening store A: %v", err)
	}
	t.Cleanup(func() { storeA.Close() })
	clientA := protocol.New(storeA, idA)

	// A creates the campfire.
	createResult, err := clientA.Create(protocol.CreateRequest{
		JoinProtocol: "open",
		TransportDir: transportBaseDir,
		BeaconDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	campfireID := createResult.CampfireID

	// B: will join, then leave.
	idB, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity B: %v", err)
	}
	storeBDir := t.TempDir()
	storeB, err := store.Open(filepath.Join(storeBDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store B: %v", err)
	}
	t.Cleanup(func() { storeB.Close() })
	clientB := protocol.New(storeB, idB)

	// The campfire-specific dir is {transportBaseDir}/{campfireID}.
	// Join requires the campfire-specific dir (not the base), as it reads
	// campfire.cbor and members/ directly from TransportDir.
	campfireDir := filepath.Join(transportBaseDir, campfireID)

	// B joins (open campfire, no Admit needed).
	_, err = clientB.Join(protocol.JoinRequest{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("B Join: %v", err)
	}

	return &leaveTestEnv{
		transportBaseDir: transportBaseDir,
		campfireID:       campfireID,
		idA:              idA,
		storeA:           storeA,
		clientA:          clientA,
		idB:              idB,
		storeB:           storeB,
		clientB:          clientB,
	}
}

// addMemberC adds a third member C to the environment.
func (e *leaveTestEnv) addMemberC(t *testing.T) {
	t.Helper()

	idC, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity C: %v", err)
	}
	storeCDir := t.TempDir()
	storeC, err := store.Open(filepath.Join(storeCDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store C: %v", err)
	}
	t.Cleanup(func() { storeC.Close() })
	clientC := protocol.New(storeC, idC)

	campfireDir := filepath.Join(e.transportBaseDir, e.campfireID)
	_, err = clientC.Join(protocol.JoinRequest{
		CampfireID:    e.campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("C Join: %v", err)
	}

	e.idC = idC
	e.storeC = storeC
	e.clientC = clientC
}

// TestLeave_ThenSendRejected is DC 1: B sends (proves membership), leaves, second send fails.
func TestLeave_ThenSendRejected(t *testing.T) {
	env := setupLeaveEnv(t)

	// B sends before leaving — must succeed (proves membership).
	_, err := env.clientB.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte("before leave"),
	})
	if err != nil {
		t.Fatalf("B Send before Leave: %v (membership not established)", err)
	}

	// B leaves.
	if err := env.clientB.Leave(env.campfireID); err != nil {
		t.Fatalf("B Leave: %v", err)
	}

	// B sends after leaving — must fail.
	_, err = env.clientB.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte("after leave"),
	})
	if err == nil {
		t.Fatal("B Send after Leave must return an error; got nil")
	}
}

// TestLeave_RemovesMemberRecord is DC 2: After B leaves, Members() does not include B.
func TestLeave_RemovesMemberRecord(t *testing.T) {
	env := setupLeaveEnv(t)

	// Confirm B is present before leaving.
	membersBefore, err := env.clientA.Members(env.campfireID)
	if err != nil {
		t.Fatalf("Members before Leave: %v", err)
	}
	if !membersContains(membersBefore, env.idB.PublicKeyHex()) {
		t.Fatalf("B should be in members before Leave; got %v", pubKeyList(membersBefore))
	}

	// B leaves.
	if err := env.clientB.Leave(env.campfireID); err != nil {
		t.Fatalf("B Leave: %v", err)
	}

	// A queries members — B must be absent.
	membersAfter, err := env.clientA.Members(env.campfireID)
	if err != nil {
		t.Fatalf("Members after Leave: %v", err)
	}
	if membersContains(membersAfter, env.idB.PublicKeyHex()) {
		t.Fatalf("B should NOT be in members after Leave; got %v", pubKeyList(membersAfter))
	}
}

// TestLeave_DoesNotAffectOtherMembers is DC 3: B leaves; A and C still send and read.
func TestLeave_DoesNotAffectOtherMembers(t *testing.T) {
	env := setupLeaveEnv(t)
	env.addMemberC(t)

	// B leaves.
	if err := env.clientB.Leave(env.campfireID); err != nil {
		t.Fatalf("B Leave: %v", err)
	}

	// A sends a message.
	msgA, err := env.clientA.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte("from A after B left"),
	})
	if err != nil {
		t.Fatalf("A Send after B Leave: %v", err)
	}
	if msgA == nil {
		t.Fatal("A Send returned nil message")
	}

	// C sends a message.
	msgC, err := env.clientC.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte("from C after B left"),
	})
	if err != nil {
		t.Fatalf("C Send after B Leave: %v", err)
	}
	if msgC == nil {
		t.Fatal("C Send returned nil message")
	}

	// A reads — both messages present.
	msgs, err := env.clientA.Read(protocol.ReadRequest{CampfireID: env.campfireID})
	if err != nil {
		t.Fatalf("A Read after B Leave: %v", err)
	}
	found := map[string]bool{}
	for _, msg := range msgs.Messages {
		found[msg.ID] = true
	}
	if !found[msgA.ID] {
		t.Errorf("A's own message not found in Read results")
	}
	if !found[msgC.ID] {
		t.Errorf("C's message not found in A's Read results")
	}

	// C reads — both messages present.
	msgsC, err := env.clientC.Read(protocol.ReadRequest{CampfireID: env.campfireID})
	if err != nil {
		t.Fatalf("C Read after B Leave: %v", err)
	}
	foundC := map[string]bool{}
	for _, msg := range msgsC.Messages {
		foundC[msg.ID] = true
	}
	if !foundC[msgA.ID] {
		t.Errorf("A's message not found in C's Read results")
	}
	if !foundC[msgC.ID] {
		t.Errorf("C's own message not found in C's Read results")
	}
}

// TestLeave_Idempotent is DC 4: Second Leave returns *ErrNotMember, no panic.
func TestLeave_Idempotent(t *testing.T) {
	env := setupLeaveEnv(t)

	// First Leave — must succeed.
	if err := env.clientB.Leave(env.campfireID); err != nil {
		t.Fatalf("first B Leave: %v", err)
	}

	// Second Leave — must return *ErrNotMember, not panic.
	err := env.clientB.Leave(env.campfireID)
	if err == nil {
		t.Fatal("second Leave must return an error; got nil")
	}
	var notMember *protocol.ErrNotMember
	if !protocol.IsNotMemberError(err, &notMember) {
		t.Errorf("second Leave must return *ErrNotMember; got: %v (type %T)", err, err)
	}
}

// TestLeave_FilesystemCleanup is DC 5: After Leave, B's .cbor file is gone.
func TestLeave_FilesystemCleanup(t *testing.T) {
	env := setupLeaveEnv(t)

	// Compute the path of B's member record file.
	// The file is at: <transportBaseDir>/<campfireID>/members/<pubkeyHex>.cbor
	memberFilePath := filepath.Join(
		env.transportBaseDir,
		env.campfireID,
		"members",
		env.idB.PublicKeyHex()+".cbor",
	)

	// Confirm the file exists before leaving.
	if _, err := os.Stat(memberFilePath); os.IsNotExist(err) {
		t.Fatalf("B's member record file should exist before Leave: %s", memberFilePath)
	}

	// B leaves.
	if err := env.clientB.Leave(env.campfireID); err != nil {
		t.Fatalf("B Leave: %v", err)
	}

	// The file must be gone.
	if _, err := os.Stat(memberFilePath); !os.IsNotExist(err) {
		t.Errorf("B's member record file should be removed after Leave: %s (stat err: %v)", memberFilePath, err)
	}
}

// --- helpers ---

// membersContains returns true if pubKeyHex is in the list.
func membersContains(members []protocol.MemberRecord, pubKeyHex string) bool {
	for _, m := range members {
		if m.MemberPubkey == pubKeyHex {
			return true
		}
	}
	return false
}

// pubKeyList returns a slice of hex pubkeys for error reporting.
func pubKeyList(members []protocol.MemberRecord) []string {
	out := make([]string, len(members))
	for i, m := range members {
		key := m.MemberPubkey
		if len(key) > 12 {
			key = key[:12]
		}
		out[i] = key + "..."
	}
	return out
}


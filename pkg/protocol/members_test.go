package protocol_test

// members_test.go — veracity tests for protocol.Client.Members().
//
// Covered bead: campfire-agent-c4h
//
// All 6 done conditions are covered:
//   1. MEMBERS REFLECTS JOINS: A creates → Members=[A]. B joins → Members=[A,B]. C joins → Members=[A,B,C].
//   2. MEMBERS REFLECTS LEAVES: B leaves → Members=[A,C].
//   3. MEMBERS REFLECTS EVICTIONS: A evicts C → Members=[A].
//      (Evict is implemented as a minimal filesystem-transport-only operation in evict.go.)
//   4. SYNC-BEFORE-QUERY: A creates, B joins+sends (proves membership), A never explicitly
//      synced messages — A.Members() must include B (proving filesystem read, not stale cache).
//   5. MEMBER ROLES: A creates. A admits B with role="writer". Members() returns A=full, B=writer.
//   6. go test ./pkg/protocol/ -run TestMembers passes.
//
// No mocks. Real filesystem dirs. Real SQLite stores.
// Membership changes are proven real by having members send before asserting Members().

import (
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// membersTestAgent holds the state for one test participant.
type membersTestAgent struct {
	id     *identity.Identity
	st     store.Store
	client *protocol.Client
}

// newMembersTestAgent creates an agent with its own identity and store.
func newMembersTestAgent(t *testing.T, name string) *membersTestAgent {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("%s: identity.Generate: %v", name, err)
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("%s: store.Open: %v", name, err)
	}
	t.Cleanup(func() { st.Close() })
	return &membersTestAgent{
		id:     id,
		st:     st,
		client: protocol.New(st, id),
	}
}

// TestMembers_ReflectsJoins is DC 1.
// A creates. Members=[A]. B joins. Members=[A,B]. C joins. Members=[A,B,C].
func TestMembers_ReflectsJoins(t *testing.T) {
	transportBaseDir := t.TempDir()
	A := newMembersTestAgent(t, "A")
	B := newMembersTestAgent(t, "B")
	C := newMembersTestAgent(t, "C")

	// A creates.
	createResult, err := A.client.Create(protocol.CreateRequest{
		JoinProtocol: "open",
		TransportDir: transportBaseDir,
		BeaconDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("A Create: %v", err)
	}
	campfireID := createResult.CampfireID
	campfireDir := filepath.Join(transportBaseDir, campfireID)

	// Members after A creates — must include only A.
	members, err := A.client.Members(campfireID)
	if err != nil {
		t.Fatalf("Members after Create: %v", err)
	}
	assertMembersExact(t, "after A creates", members, []string{A.id.PublicKeyHex()})

	// B joins. Prove membership by sending.
	_, err = B.client.Join(protocol.JoinRequest{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("B Join: %v", err)
	}
	_, err = B.client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("B proving membership"),
	})
	if err != nil {
		t.Fatalf("B Send (prove membership): %v", err)
	}

	// Members must now include A and B.
	members, err = A.client.Members(campfireID)
	if err != nil {
		t.Fatalf("Members after B joins: %v", err)
	}
	assertMembersExact(t, "after B joins", members, []string{A.id.PublicKeyHex(), B.id.PublicKeyHex()})

	// C joins. Prove membership by sending.
	_, err = C.client.Join(protocol.JoinRequest{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("C Join: %v", err)
	}
	_, err = C.client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("C proving membership"),
	})
	if err != nil {
		t.Fatalf("C Send (prove membership): %v", err)
	}

	// Members must now include A, B, and C.
	members, err = A.client.Members(campfireID)
	if err != nil {
		t.Fatalf("Members after C joins: %v", err)
	}
	assertMembersExact(t, "after C joins", members, []string{A.id.PublicKeyHex(), B.id.PublicKeyHex(), C.id.PublicKeyHex()})
}

// TestMembers_ReflectsLeaves is DC 2.
// A creates, B and C join. B leaves. Members=[A,C].
func TestMembers_ReflectsLeaves(t *testing.T) {
	transportBaseDir := t.TempDir()
	A := newMembersTestAgent(t, "A")
	B := newMembersTestAgent(t, "B")
	C := newMembersTestAgent(t, "C")

	createResult, err := A.client.Create(protocol.CreateRequest{
		JoinProtocol: "open",
		TransportDir: transportBaseDir,
		BeaconDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("A Create: %v", err)
	}
	campfireID := createResult.CampfireID
	campfireDir := filepath.Join(transportBaseDir, campfireID)

	for name, agent := range map[string]*membersTestAgent{"B": B, "C": C} {
		_, err = agent.client.Join(protocol.JoinRequest{
			CampfireID:    campfireID,
			TransportDir:  campfireDir,
			TransportType: "filesystem",
		})
		if err != nil {
			t.Fatalf("%s Join: %v", name, err)
		}
		_, err = agent.client.Send(protocol.SendRequest{
			CampfireID: campfireID,
			Payload:    []byte(name + " proving membership"),
		})
		if err != nil {
			t.Fatalf("%s Send (prove membership): %v", name, err)
		}
	}

	// Confirm A, B, C all present before leave.
	members, err := A.client.Members(campfireID)
	if err != nil {
		t.Fatalf("Members before B leaves: %v", err)
	}
	assertMembersExact(t, "before B leaves", members, []string{A.id.PublicKeyHex(), B.id.PublicKeyHex(), C.id.PublicKeyHex()})

	// B leaves.
	if err := B.client.Leave(campfireID); err != nil {
		t.Fatalf("B Leave: %v", err)
	}

	// Members must be [A, C].
	members, err = A.client.Members(campfireID)
	if err != nil {
		t.Fatalf("Members after B leaves: %v", err)
	}
	assertMembersExact(t, "after B leaves", members, []string{A.id.PublicKeyHex(), C.id.PublicKeyHex()})
	if membersContainsPubKey(members, B.id.PublicKeyHex()) {
		t.Errorf("B must NOT appear in members after Leave; got %v", pubKeyList(members))
	}
}

// TestMembers_ReflectsEvictions is DC 3.
// A creates, C joins. A evicts C. Members=[A].
//
// Evict is implemented as a minimal filesystem-transport-only operation
// (creator removes the target's member record from the shared transport dir).
// It does NOT revoke cryptographic access or P2P HTTP sessions — those require
// rekeying, which is out of scope for this bead. The gap is documented here.
func TestMembers_ReflectsEvictions(t *testing.T) {
	transportBaseDir := t.TempDir()
	A := newMembersTestAgent(t, "A")
	C := newMembersTestAgent(t, "C")

	createResult, err := A.client.Create(protocol.CreateRequest{
		JoinProtocol: "open",
		TransportDir: transportBaseDir,
		BeaconDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("A Create: %v", err)
	}
	campfireID := createResult.CampfireID
	campfireDir := filepath.Join(transportBaseDir, campfireID)

	_, err = C.client.Join(protocol.JoinRequest{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("C Join: %v", err)
	}
	// C sends to prove membership before eviction.
	_, err = C.client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("C proving membership before eviction"),
	})
	if err != nil {
		t.Fatalf("C Send (prove membership): %v", err)
	}

	// Confirm C is present before eviction.
	members, err := A.client.Members(campfireID)
	if err != nil {
		t.Fatalf("Members before evict: %v", err)
	}
	if !membersContainsPubKey(members, C.id.PublicKeyHex()) {
		t.Fatalf("C must be in members before eviction; got %v", pubKeyList(members))
	}

	// A evicts C.
	_, err = A.client.Evict(protocol.EvictRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: C.id.PublicKeyHex(),
	})
	if err != nil {
		t.Fatalf("A Evict C: %v", err)
	}

	// Members must be [A] — C evicted.
	members, err = A.client.Members(campfireID)
	if err != nil {
		t.Fatalf("Members after evict: %v", err)
	}
	assertMembersExact(t, "after C evicted", members, []string{A.id.PublicKeyHex()})
}

// TestMembers_SyncBeforeQuery is DC 4.
// A creates (filesystem). B joins and sends a message (proving B is a real member).
// A has NOT called Read or any explicit sync. A calls Members() — must include B.
//
// This proves Members() reads from the shared filesystem transport directly,
// not from A's local (unsynced) store. The transport is the source of truth for membership.
func TestMembers_SyncBeforeQuery(t *testing.T) {
	transportBaseDir := t.TempDir()
	A := newMembersTestAgent(t, "A")
	B := newMembersTestAgent(t, "B")

	createResult, err := A.client.Create(protocol.CreateRequest{
		JoinProtocol: "open",
		TransportDir: transportBaseDir,
		BeaconDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("A Create: %v", err)
	}
	campfireID := createResult.CampfireID
	campfireDir := filepath.Join(transportBaseDir, campfireID)

	// B joins and sends (proving real membership in shared transport).
	_, err = B.client.Join(protocol.JoinRequest{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("B Join: %v", err)
	}
	_, err = B.client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("B proving membership via send"),
	})
	if err != nil {
		t.Fatalf("B Send: %v", err)
	}

	// A calls Members() WITHOUT having called Read (no explicit sync).
	// Members() must read from the transport dir directly and return B.
	members, err := A.client.Members(campfireID)
	if err != nil {
		t.Fatalf("A.Members() after B joined without A syncing: %v", err)
	}
	if !membersContainsPubKey(members, B.id.PublicKeyHex()) {
		t.Errorf("Members() must include B even without explicit sync by A; got %v", pubKeyList(members))
	}
	if !membersContainsPubKey(members, A.id.PublicKeyHex()) {
		t.Errorf("Members() must include A (creator); got %v", pubKeyList(members))
	}
}

// TestMembers_Roles is DC 5.
// A creates. A admits B with role="writer". B joins. Members() returns A=full, B=writer.
func TestMembers_Roles(t *testing.T) {
	transportBaseDir := t.TempDir()
	A := newMembersTestAgent(t, "A")
	B := newMembersTestAgent(t, "B")

	createResult, err := A.client.Create(protocol.CreateRequest{
		JoinProtocol: "invite-only",
		TransportDir: transportBaseDir,
		BeaconDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("A Create (invite-only): %v", err)
	}
	campfireID := createResult.CampfireID
	campfireDir := filepath.Join(transportBaseDir, campfireID)

	// A pre-admits B with role=writer.
	err = A.client.Admit(protocol.AdmitRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: B.id.PublicKeyHex(),
		Role:            "writer",
		TransportDir:    campfireDir,
	})
	if err != nil {
		t.Fatalf("A Admit B as writer: %v", err)
	}

	// B joins (invite-only, pre-admitted).
	_, err = B.client.Join(protocol.JoinRequest{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("B Join (invite-only): %v", err)
	}
	// B sends (writer can send non-system messages).
	_, err = B.client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("B proving writer membership"),
	})
	if err != nil {
		t.Fatalf("B Send as writer: %v", err)
	}

	// Members() must return correct roles.
	members, err := A.client.Members(campfireID)
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members; got %d: %v", len(members), pubKeyList(members))
	}

	roleFor := func(pubKeyHex string) string {
		for _, m := range members {
			if m.MemberPubkey == pubKeyHex {
				return m.Role
			}
		}
		return "<not found>"
	}

	aRole := roleFor(A.id.PublicKeyHex())
	bRole := roleFor(B.id.PublicKeyHex())

	if aRole != "full" {
		t.Errorf("A role: want %q, got %q", "full", aRole)
	}
	if bRole != "writer" {
		t.Errorf("B role: want %q, got %q", "writer", bRole)
	}
}

// --- helpers ---

// assertMembersExact checks that the members list contains exactly the given pubkeys
// (no more, no fewer).
func assertMembersExact(t *testing.T, when string, members []protocol.MemberRecord, wantKeys []string) {
	t.Helper()
	want := make(map[string]bool, len(wantKeys))
	for _, k := range wantKeys {
		want[k] = true
	}
	got := make(map[string]bool, len(members))
	for _, m := range members {
		got[m.MemberPubkey] = true
	}

	for _, k := range wantKeys {
		if !got[k] {
			t.Errorf("%s: expected member %s...%s to be present; got %v", when, k[:8], k[len(k)-4:], pubKeyList(members))
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("%s: unexpected member %s...%s in list; got %v", when, k[:8], k[len(k)-4:], pubKeyList(members))
		}
	}
}

// membersContainsPubKey returns true if pubKeyHex is in the members list.
func membersContainsPubKey(members []protocol.MemberRecord, pubKeyHex string) bool {
	for _, m := range members {
		if m.MemberPubkey == pubKeyHex {
			return true
		}
	}
	return false
}

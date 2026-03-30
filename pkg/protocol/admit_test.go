package protocol_test

// Tests for protocol.Client.Admit() — campfire-agent-ksb.
//
// Done conditions:
// 1. ADMIT-THEN-JOIN: A creates with joinProtocol="invite-only". A calls Admit(B.pubkey, role="full"). B joins. B sends. A reads B's message.
// 2. ADMIT WITH ROLE: A creates. A admits B with role="writer". B joins. B sends normal message (succeeds). B sends campfire:* system tag message (fails with RoleError).
// 3. ADMIT WITHOUT PRIOR ADMIT REJECTED: A creates invite-only. B joins without being admitted — must fail.
// 4. DUPLICATE ADMIT IDEMPOTENT: A admits B twice. Members() shows B exactly once.
// 5. MEMBER RECORD ON DISK: After Admit(), B's member record file exists in transport dir with correct pubkey and role.
// 6. go test ./pkg/protocol/ -run TestAdmit passes.
//
// No mocks. Real filesystem dirs, real SQLite stores, real identities.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// TestAdmit runs all Admit sub-tests.
func TestAdmit(t *testing.T) {
	t.Run("AdmitThenJoin", testAdmitThenJoin)
	t.Run("AdmitWithRole", testAdmitWithRole)
	t.Run("AdmitWithoutPriorAdmitRejected", testAdmitWithoutPriorAdmitRejected)
	t.Run("DuplicateAdmitIdempotent", testDuplicateAdmitIdempotent)
	t.Run("MemberRecordOnDisk", testMemberRecordOnDisk)
}

// testAdmitThenJoin: A creates with joinProtocol="invite-only". A calls Admit(B.pubkey, role="full").
// B joins. B sends. A reads B's message.
// Done condition 1.
func testAdmitThenJoin(t *testing.T) {
	t.Helper()

	clientA := newJoinClient(t)
	campfireID, campfireDir := createFSCampfire(t, clientA, "invite-only")

	clientB := newJoinClient(t)

	// A admits B with role full.
	if err := clientA.Admit(protocol.AdmitRequest{
		Transport: &protocol.FilesystemTransport{Dir: campfireDir},
		CampfireID:      campfireID,
		MemberPubKeyHex: clientB.PublicKeyHex(),
		Role:            campfire.RoleFull,
	}); err != nil {
		t.Fatalf("A.Admit(B): %v", err)
	}

	// B joins — must succeed because A admitted them.
	_, err := clientB.Join(protocol.JoinRequest{
		Transport: &protocol.FilesystemTransport{Dir: campfireDir},
		CampfireID:    campfireID,
	})
	if err != nil {
		t.Fatalf("B.Join after Admit: %v", err)
	}

	// B sends a message.
	want := "hello from admitted B (invite-only)"
	_, err = clientB.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(want),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("B.Send after admit+join: %v", err)
	}

	// A reads and sees B's message.
	result, err := clientA.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("A.Read: %v", err)
	}
	assertContainsPayload(t, result.Messages, want, "A.Read after admit+join")
}

// testAdmitWithRole: A creates. A admits B with role="writer". B joins.
// B can send normal messages. B cannot send campfire:* system tag messages.
// Done condition 2.
func testAdmitWithRole(t *testing.T) {
	t.Helper()

	clientA := newJoinClient(t)
	campfireID, campfireDir := createFSCampfire(t, clientA, "open")

	clientB := newJoinClient(t)

	// A admits B with role writer.
	if err := clientA.Admit(protocol.AdmitRequest{
		Transport: &protocol.FilesystemTransport{Dir: campfireDir},
		CampfireID:      campfireID,
		MemberPubKeyHex: clientB.PublicKeyHex(),
		Role:            campfire.RoleWriter,
	}); err != nil {
		t.Fatalf("A.Admit(B, writer): %v", err)
	}

	// B joins.
	_, err := clientB.Join(protocol.JoinRequest{
		Transport: &protocol.FilesystemTransport{Dir: campfireDir},
		CampfireID:    campfireID,
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	// B can send a normal message (non-system tag).
	want := "normal message from writer B"
	_, err = clientB.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(want),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("B.Send normal message as writer: %v", err)
	}

	// B cannot send a campfire:* system tag message.
	_, err = clientB.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("should fail"),
		Tags:       []string{"campfire:compact"},
	})
	if err == nil {
		t.Fatal("expected role error for writer sending system tag, got nil")
	}
	var roleErr *protocol.RoleError
	if !protocol.IsRoleError(err, &roleErr) {
		t.Errorf("expected *RoleError for writer sending campfire:* tag, got: %v", err)
	}
}

// testAdmitWithoutPriorAdmitRejected: A creates invite-only. B joins without
// being admitted first — must fail.
// Done condition 3.
func testAdmitWithoutPriorAdmitRejected(t *testing.T) {
	t.Helper()

	clientA := newJoinClient(t)
	campfireID, campfireDir := createFSCampfire(t, clientA, "invite-only")
	_ = clientA // suppress unused

	clientB := newJoinClient(t)
	_, err := clientB.Join(protocol.JoinRequest{
		Transport: &protocol.FilesystemTransport{Dir: campfireDir},
		CampfireID:    campfireID,
	})
	if err == nil {
		t.Fatal("expected error joining invite-only campfire without Admit, got nil")
	}
}

// testDuplicateAdmitIdempotent: A admits B twice. The members/ directory
// must contain exactly one record for B.
// Done condition 4.
func testDuplicateAdmitIdempotent(t *testing.T) {
	t.Helper()

	clientA := newJoinClient(t)
	campfireID, campfireDir := createFSCampfire(t, clientA, "open")

	clientB := newJoinClient(t)
	bPubKey := clientB.PublicKeyHex()

	admitReq := protocol.AdmitRequest{
		Transport: &protocol.FilesystemTransport{Dir: campfireDir},
		CampfireID:      campfireID,
		MemberPubKeyHex: bPubKey,
		Role:            campfire.RoleFull,
	}

	// Admit twice.
	if err := clientA.Admit(admitReq); err != nil {
		t.Fatalf("first Admit: %v", err)
	}
	if err := clientA.Admit(admitReq); err != nil {
		t.Fatalf("second Admit: %v", err)
	}

	// Verify B appears exactly once in the member list.
	tr := fs.ForDir(campfireDir)
	members, err := tr.ListMembers(campfireID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}

	count := 0
	for _, m := range members {
		if fmt.Sprintf("%x", m.PublicKey) == bPubKey {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected B to appear exactly once in members list, got %d occurrences (total members: %d)", count, len(members))
	}
}

// testMemberRecordOnDisk: After Admit(), B's member record file exists in the
// transport dir (members/{bPubKey}.cbor) with the correct public key and role.
// Done condition 5.
func testMemberRecordOnDisk(t *testing.T) {
	t.Helper()

	clientA := newJoinClient(t)
	campfireID, campfireDir := createFSCampfire(t, clientA, "open")

	clientB := newJoinClient(t)
	bPubKey := clientB.PublicKeyHex()

	if err := clientA.Admit(protocol.AdmitRequest{
		Transport: &protocol.FilesystemTransport{Dir: campfireDir},
		CampfireID:      campfireID,
		MemberPubKeyHex: bPubKey,
		Role:            campfire.RoleWriter,
	}); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	// Check the file exists.
	memberFile := filepath.Join(campfireDir, "members", bPubKey+".cbor")
	data, err := os.ReadFile(memberFile)
	if err != nil {
		t.Fatalf("member record file not found at %s: %v", memberFile, err)
	}

	// Decode and verify pubkey and role.
	var rec campfire.MemberRecord
	if err := cfencoding.Unmarshal(data, &rec); err != nil {
		t.Fatalf("decoding member record: %v", err)
	}

	gotPubKey := fmt.Sprintf("%x", rec.PublicKey)
	if gotPubKey != bPubKey {
		t.Errorf("member record pubkey mismatch: want %s, got %s", bPubKey, gotPubKey)
	}
	if rec.Role != campfire.RoleWriter {
		t.Errorf("member record role mismatch: want %q, got %q", campfire.RoleWriter, rec.Role)
	}
}

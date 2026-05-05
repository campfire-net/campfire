package http_test

// Tests for handleAdmit creator-only check (campfireagent-7b6).
//
// Security invariant: only the campfire creator may admit new members.
// Any authenticated non-creator member gets 403 Forbidden. The check
// mirrors the evict handler guard at handler_message.go:943.
//
// Four cases:
//  1. Creator admits a new member → 200 OK (happy path).
//  2. Admitted member (non-creator) tries to admit a third member → 403 Forbidden.
//  3. GetMembership DB error → 500 Internal Server Error (fail-closed).
//  4. Empty CreatorPubkey (legacy record) → 403 Forbidden (fail-closed; cannot verify).

import (
	"os"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/internal/store"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/internal/transport/http"
)

// TestAdmitCreatorCanAdmit verifies that the campfire creator can admit a new member.
// Uses real HTTP transport, real ed25519 identities, and a real SQLite store.
// SetSelfInfo makes idCreator the node's self identity so authMiddleware accepts
// requests signed by idCreator without requiring a separate peer endpoint entry.
func TestAdmitCreatorCanAdmit(t *testing.T) {
	campfireID := "test-admit-creator"
	idCreator := tempIdentity(t)
	idNewMember := tempIdentity(t)

	s := tempStore(t)
	addMembershipWithCreator(t, s, campfireID, idCreator.PublicKeyHex())

	tr := cfhttp.New("127.0.0.1:0", s)
	tr.SetSelfInfo(idCreator.PublicKeyHex(), "")
	if err := tr.Start(); err != nil {
		t.Fatalf("start transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	ep := epOf(tr)

	if err := cfhttp.AdmitOnRelay(ep, campfireID, idNewMember.PublicKeyHex(), "full", idCreator); err != nil {
		t.Errorf("creator admit should succeed, got error: %v", err)
	}
}

// TestAdmitNonCreatorCannotAdmit verifies that a non-creator member is rejected
// with an error when trying to admit a third party. This is the regression test
// for the SEC-HIGH finding: any member could previously admit arbitrarily.
func TestAdmitNonCreatorCannotAdmit(t *testing.T) {
	campfireID := "test-admit-noncreator"
	idCreator := tempIdentity(t)
	idMember := tempIdentity(t)
	idThirdParty := tempIdentity(t)

	s := tempStore(t)
	addMembershipWithCreator(t, s, campfireID, idCreator.PublicKeyHex())
	// idMember must be a known peer so they pass authMiddleware (membership check),
	// but they are NOT the creator — testing the creator check, not membership.
	addPeerEndpoint(t, s, campfireID, idMember.PublicKeyHex())

	// Use SetSelfInfo so the creator passes authMiddleware as the self key.
	tr := cfhttp.New("127.0.0.1:0", s)
	tr.SetSelfInfo(idCreator.PublicKeyHex(), "")
	if err := tr.Start(); err != nil {
		t.Fatalf("start transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	ep := epOf(tr)

	// Step 1: creator admits idMember (must succeed — verifies creator path works).
	if err := cfhttp.AdmitOnRelay(ep, campfireID, idMember.PublicKeyHex(), "full", idCreator); err != nil {
		t.Fatalf("creator admit of member should succeed, got: %v", err)
	}

	// Step 2: idMember attempts to admit idThirdParty — must be rejected with error.
	// idMember is in the peer list so they pass authMiddleware, but the creator check
	// must reject them with 403.
	err := cfhttp.AdmitOnRelay(ep, campfireID, idThirdParty.PublicKeyHex(), "full", idMember)
	if err == nil {
		t.Error("expected non-creator admit to be rejected (403), got nil error")
	}
}

// TestAdmitRejectedWhenStoreErrors verifies fail-closed: if GetMembership errors
// (e.g. DB closed after transport start), the admit must be rejected.
// SetSelfInfo makes idCreator the node's self identity so authMiddleware's
// membership check passes purely from the self-key comparison (no DB lookup
// needed), ensuring GetMembership in handleAdmit is the failure point under test.
func TestAdmitRejectedWhenStoreErrors(t *testing.T) {
	campfireID := "test-admit-dberror"
	idCreator := tempIdentity(t)
	idNewMember := tempIdentity(t)

	s := tempStore(t)
	addMembershipWithCreator(t, s, campfireID, idCreator.PublicKeyHex())

	tr := cfhttp.New("127.0.0.1:0", s)
	tr.SetSelfInfo(idCreator.PublicKeyHex(), "")
	if err := tr.Start(); err != nil {
		t.Fatalf("start transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	ep := epOf(tr)

	// Close the DB after starting the transport — GetMembership will error on next request.
	s.Close()

	err := cfhttp.AdmitOnRelay(ep, campfireID, idNewMember.PublicKeyHex(), "full", idCreator)
	if err == nil {
		t.Error("expected admit to fail when store errors (fail-closed), got nil error")
	}
}

// TestAdmitRejectedWhenCreatorPubkeyEmpty verifies fail-closed: if the membership
// record has an empty CreatorPubkey (legacy campfire), admission must be REJECTED
// because we cannot verify the creator identity.
func TestAdmitRejectedWhenCreatorPubkeyEmpty(t *testing.T) {
	campfireID := "test-admit-legacy"
	idAny := tempIdentity(t)
	idNewMember := tempIdentity(t)

	s := tempStore(t)
	// Legacy record: no CreatorPubkey set.
	err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
		// CreatorPubkey intentionally omitted.
	})
	if err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	// idAny must be a known peer so they pass authMiddleware.
	addPeerEndpoint(t, s, campfireID, idAny.PublicKeyHex())

	_tr := startTransport(t, "127.0.0.1:0", s)
	ep := epOf(_tr)

	// Fail-closed: empty CreatorPubkey means we cannot verify — admit must be rejected.
	if err := cfhttp.AdmitOnRelay(ep, campfireID, idNewMember.PublicKeyHex(), "full", idAny); err == nil {
		t.Error("admit with empty CreatorPubkey must be rejected (fail-closed), got nil error")
	}
}

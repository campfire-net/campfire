package http_test

// Tests for handleMembership evict auth — fail-closed creator check.
// Four cases:
//  1. GetMembership returns an error (DB closed) → evict must be REJECTED (fail closed).
//  2. CreatorPubkey is empty (legacy record) → evict REJECTED (fail-closed; cannot verify creator).
//  3. Non-creator with a set CreatorPubkey → evict rejected with 403.
//  4. Creator with a set CreatorPubkey → evict allowed (happy path).

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// addMembershipWithCreator inserts a membership with an explicit CreatorPubkey.
func addMembershipWithCreator(t *testing.T, s store.Store, campfireID, creatorPubkey string) {
	t.Helper()
	err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  os.TempDir(),
		JoinProtocol:  "open",
		Role:          "creator",
		JoinedAt:      time.Now().UnixNano(),
		CreatorPubkey: creatorPubkey,
	})
	if err != nil {
		t.Fatalf("addMembershipWithCreator: %v", err)
	}
}

// TestEvictRejectedWhenStoreErrors verifies fail-closed: if GetMembership errors
// (e.g. DB closed after transport start), the evict must be rejected.
func TestEvictRejectedWhenStoreErrors(t *testing.T) {
	campfireID := "test-evict-dberror"
	idCreator := tempIdentity(t)
	idNonCreator := tempIdentity(t)
	idVictim := tempIdentity(t)

	s := tempStore(t)
	addMembershipWithCreator(t, s, campfireID, idCreator.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+160)
	startTransport(t, addr, s)
	ep := fmt.Sprintf("http://%s", addr)

	// Close the DB after starting the transport so GetMembership will error on the next request.
	s.Close()

	evictEvent := cfhttp.MembershipEvent{
		Event:  "evict",
		Member: idVictim.PublicKeyHex(),
	}

	// Any sender should get an error back (fail closed); non-creator or creator both rejected.
	err := cfhttp.NotifyMembership(ep, campfireID, evictEvent, idNonCreator)
	if err == nil {
		t.Error("expected evict to fail when store errors (fail-closed), got nil error")
	}
}

// TestEvictRejectedWhenCreatorPubkeyEmpty verifies fail-closed: if the membership
// record has an empty CreatorPubkey (legacy campfire), eviction must be REJECTED
// because we cannot verify the creator identity.
func TestEvictRejectedWhenCreatorPubkeyEmpty(t *testing.T) {
	campfireID := "test-evict-legacy"
	idAny := tempIdentity(t)
	idVictim := tempIdentity(t)

	s := tempStore(t)
	// Legacy record: no CreatorPubkey set.
	err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
		// CreatorPubkey intentionally omitted (empty string = legacy).
	})
	if err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Sender must be a known peer for auth middleware to pass.
	addPeerEndpoint(t, s, campfireID, idAny.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+161)
	startTransport(t, addr, s)
	ep := fmt.Sprintf("http://%s", addr)

	evictEvent := cfhttp.MembershipEvent{
		Event:  "evict",
		Member: idVictim.PublicKeyHex(),
	}

	// Fail-closed: empty CreatorPubkey means we cannot verify — evict must be rejected.
	if err := cfhttp.NotifyMembership(ep, campfireID, evictEvent, idAny); err == nil {
		t.Error("evict with empty CreatorPubkey must be rejected (fail-closed), got nil error")
	}
}

// TestEvictRejectedForNonCreator verifies that when CreatorPubkey is set, only the
// creator can evict — a non-creator gets 403.
func TestEvictRejectedForNonCreator(t *testing.T) {
	campfireID := "test-evict-noncreator"
	idCreator := tempIdentity(t)
	idNonCreator := tempIdentity(t)
	idVictim := tempIdentity(t)

	s := tempStore(t)
	addMembershipWithCreator(t, s, campfireID, idCreator.PublicKeyHex())
	// Add non-creator as peer so they pass auth middleware — test the creator check, not membership.
	addPeerEndpoint(t, s, campfireID, idNonCreator.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+162)
	startTransport(t, addr, s)
	ep := fmt.Sprintf("http://%s", addr)

	evictEvent := cfhttp.MembershipEvent{
		Event:  "evict",
		Member: idVictim.PublicKeyHex(),
	}

	// Non-creator tries to evict — must get an error (403).
	err := cfhttp.NotifyMembership(ep, campfireID, evictEvent, idNonCreator)
	if err == nil {
		t.Error("expected non-creator evict to be rejected with 403, got nil error")
	}
}

// TestEvictAllowedForCreator verifies the happy path: creator can evict a member.
func TestEvictAllowedForCreator(t *testing.T) {
	campfireID := "test-evict-creator"
	idCreator := tempIdentity(t)
	idVictim := tempIdentity(t)

	s := tempStore(t)
	addMembershipWithCreator(t, s, campfireID, idCreator.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+163)
	tr := cfhttp.New(addr, s)
	tr.SetSelfInfo(idCreator.PublicKeyHex(), fmt.Sprintf("http://%s", addr))
	if err := tr.Start(); err != nil {
		t.Fatalf("start transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	ep := fmt.Sprintf("http://%s", addr)

	// Pre-add victim as a peer so removal is meaningful.
	tr.AddPeer(campfireID, idVictim.PublicKeyHex(), "http://victim:9999")

	evictEvent := cfhttp.MembershipEvent{
		Event:  "evict",
		Member: idVictim.PublicKeyHex(),
	}

	if err := cfhttp.NotifyMembership(ep, campfireID, evictEvent, idCreator); err != nil {
		t.Errorf("creator evict should succeed, got error: %v", err)
	}

	// Verify victim was removed from peer list.
	peers := tr.Peers(campfireID)
	for _, p := range peers {
		if p.PubKeyHex == idVictim.PublicKeyHex() {
			t.Error("victim still in peer list after creator evict")
		}
	}
}

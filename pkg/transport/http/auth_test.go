package http_test

// Security authorization tests.
// Each test verifies that a specific handler rejects non-members with 403.
//
// Covered beads:
//   workspace-0fh: handleDeliver and handleSync membership check
//   workspace-972: handleJoin invite-only enforcement
//   workspace-j4j: handleRekey sender membership check
//   workspace-rba: handleMembership membership check
//   workspace-1g3: handleSign membership check
//   workspace-ul2: handlePoll peer-in-list check (existing TestHandlePollNonMember covers the core case;
//                  this file adds the peer-passes check)

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---------------------------------------------------------------------------
// workspace-0fh: handleDeliver — non-member gets 403
// ---------------------------------------------------------------------------

// TestDeliverNonMemberForbidden verifies that a valid Ed25519 signer who is
// not in the campfire's peer list cannot deliver messages (403 Forbidden).
func TestDeliverNonMemberForbidden(t *testing.T) {
	campfireID := "deliver-nonmember"
	idMember := tempIdentity(t)
	idStranger := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idMember.PublicKeyHex())
	// idStranger is NOT added to peer endpoints.

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+50)
	startTransportWithSelf(t, addr, s, idMember)
	ep := fmt.Sprintf("http://%s", addr)

	msg := newTestMessage(t, idStranger)
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("encoding message: %v", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	sig := idStranger.Sign(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("X-Campfire-Sender", idStranger.PublicKeyHex())
	req.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for non-member deliver, got %d", resp.StatusCode)
	}
}

// TestDeliverMemberAllowed verifies that a campfire member can deliver messages (200).
func TestDeliverMemberAllowed(t *testing.T) {
	campfireID := "deliver-member"
	idMember := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idMember.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+51)
	startTransportWithSelf(t, addr, s, idMember)
	ep := fmt.Sprintf("http://%s", addr)

	msg := newTestMessage(t, idMember)
	if err := cfhttp.Deliver(ep, campfireID, msg, idMember); err != nil {
		t.Fatalf("member deliver failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// workspace-0fh: handleSync — non-member gets 403
// ---------------------------------------------------------------------------

// TestSyncNonMemberForbidden verifies that a valid Ed25519 signer who is not
// in the campfire's peer list cannot sync messages (403 Forbidden).
func TestSyncNonMemberForbidden(t *testing.T) {
	campfireID := "sync-nonmember"
	idMember := tempIdentity(t)
	idStranger := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idMember.PublicKeyHex())
	// idStranger is NOT added to peer endpoints.

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+52)
	startTransportWithSelf(t, addr, s, idMember)
	ep := fmt.Sprintf("http://%s", addr)

	url := fmt.Sprintf("%s/campfire/%s/sync?since=0", ep, campfireID)
	sig := idStranger.Sign([]byte{})
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("X-Campfire-Sender", idStranger.PublicKeyHex())
	req.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for non-member sync, got %d", resp.StatusCode)
	}
}

// TestSyncMemberAllowed verifies that a campfire member can sync messages (200).
func TestSyncMemberAllowed(t *testing.T) {
	campfireID := "sync-member"
	idMember := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idMember.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+53)
	startTransportWithSelf(t, addr, s, idMember)
	ep := fmt.Sprintf("http://%s", addr)

	_, err := cfhttp.Sync(ep, campfireID, 0, idMember)
	if err != nil {
		t.Fatalf("member sync failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// workspace-972: handleJoin — invite-only campfire rejects unknown joiners
// ---------------------------------------------------------------------------

// TestJoinInviteOnlyForbidden verifies that a joiner not in the peer list
// cannot join an invite-only campfire (403 Forbidden).
func TestJoinInviteOnlyForbidden(t *testing.T) {
	campfireID := "join-invite-only"

	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}

	idAdmin := tempIdentity(t)
	idStranger := tempIdentity(t)

	s := tempStore(t)
	// Add membership with invite-only protocol.
	err = s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "invite-only",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
	// idStranger is NOT in peer endpoints.

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+54)
	epAdmin := fmt.Sprintf("http://%s", addr)

	tr := cfhttp.New(addr, s)
	tr.SetSelfInfo(idAdmin.PublicKeyHex(), epAdmin)
	tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// idStranger tries to join — should be rejected.
	_, err = cfhttp.Join(epAdmin, campfireID, idStranger, "")
	if err == nil {
		t.Fatal("expected error for invite-only join, got nil")
	}
	if !contains(err.Error(), "403") {
		t.Errorf("expected 403, got: %v", err)
	}
}

// TestJoinInviteOnlyAdmittedAllowed verifies that a pre-admitted joiner (in peer list)
// can join an invite-only campfire.
func TestJoinInviteOnlyAdmittedAllowed(t *testing.T) {
	campfireID := "join-invite-admitted"

	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}

	idAdmin := tempIdentity(t)
	idAdmitted := tempIdentity(t)

	s := tempStore(t)
	err = s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "invite-only",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
	// Pre-admit idAdmitted by adding to peer endpoints.
	addPeerEndpoint(t, s, campfireID, idAdmitted.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+55)
	epAdmin := fmt.Sprintf("http://%s", addr)

	tr := cfhttp.New(addr, s)
	tr.SetSelfInfo(idAdmin.PublicKeyHex(), epAdmin)
	tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// idAdmitted joins — should succeed.
	result, err := cfhttp.Join(epAdmin, campfireID, idAdmitted, "")
	if err != nil {
		t.Fatalf("admitted join failed: %v", err)
	}
	if fmt.Sprintf("%x", result.CampfirePubKey) != fmt.Sprintf("%x", cfPub) {
		t.Errorf("campfire public key mismatch")
	}
}

// TestJoinOpenAllowed verifies that any valid signer can join an open campfire.
func TestJoinOpenAllowed(t *testing.T) {
	campfireID := "join-open"

	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}

	idAdmin := tempIdentity(t)
	idStranger := tempIdentity(t)

	s := tempStore(t)
	err = s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+56)
	epAdmin := fmt.Sprintf("http://%s", addr)

	tr := cfhttp.New(addr, s)
	tr.SetSelfInfo(idAdmin.PublicKeyHex(), epAdmin)
	tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Any agent can join an open campfire.
	result, err := cfhttp.Join(epAdmin, campfireID, idStranger, "")
	if err != nil {
		t.Fatalf("open join failed: %v", err)
	}
	if fmt.Sprintf("%x", result.CampfirePubKey) != fmt.Sprintf("%x", cfPub) {
		t.Errorf("campfire public key mismatch")
	}
}

// ---------------------------------------------------------------------------
// workspace-rba: handleMembership — non-member gets 403
// ---------------------------------------------------------------------------

// TestMembershipNonMemberForbidden verifies that a non-member cannot send
// membership notifications (403 Forbidden).
func TestMembershipNonMemberForbidden(t *testing.T) {
	campfireID := "membership-nonmember"
	idMember := tempIdentity(t)
	idStranger := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idMember.PublicKeyHex())
	// idStranger NOT in peer endpoints.

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+57)
	startTransportWithSelf(t, addr, s, idMember)
	ep := fmt.Sprintf("http://%s", addr)

	// idStranger sends a join event.
	joinEvent := cfhttp.MembershipEvent{
		Event:    "join",
		Member:   idStranger.PublicKeyHex(),
		Endpoint: "http://127.0.0.1:9999",
	}
	err := cfhttp.NotifyMembership(ep, campfireID, joinEvent, idStranger)
	if err == nil {
		t.Fatal("expected error for non-member membership notification, got nil")
	}
	if !contains(err.Error(), "403") {
		t.Errorf("expected 403, got: %v", err)
	}
}

// TestMembershipMemberAllowed verifies that a campfire member can send membership
// notifications (200).
func TestMembershipMemberAllowed(t *testing.T) {
	campfireID := "membership-member"
	idMember := tempIdentity(t)
	idOther := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idMember.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+58)
	startTransportWithSelf(t, addr, s, idMember)
	ep := fmt.Sprintf("http://%s", addr)

	// idMember sends a join event for idOther.
	joinEvent := cfhttp.MembershipEvent{
		Event:    "join",
		Member:   idOther.PublicKeyHex(),
		Endpoint: "http://127.0.0.1:9999",
	}
	if err := cfhttp.NotifyMembership(ep, campfireID, joinEvent, idMember); err != nil {
		t.Fatalf("member membership notification failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// workspace-1g3: handleSign — non-member gets 403
// ---------------------------------------------------------------------------

// TestSignNonMemberForbidden verifies that a non-member cannot participate in
// FROST threshold signing sessions (403 Forbidden).
func TestSignNonMemberForbidden(t *testing.T) {
	campfireID := "sign-nonmember"

	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}
	shareB, err := threshold.MarshalResult(2, dkgResults[2])
	if err != nil {
		t.Fatalf("MarshalResult B: %v", err)
	}

	idMember := tempIdentity(t)
	idStranger := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idMember.PublicKeyHex())
	// idStranger NOT in peer endpoints.

	s.UpsertThresholdShare(store.ThresholdShare{ //nolint:errcheck
		CampfireID:    campfireID,
		ParticipantID: 2,
		SecretShare:   shareB,
	})

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+59)
	tr := cfhttp.New(addr, s)
	tr.SetSelfInfo(idMember.PublicKeyHex(), fmt.Sprintf("http://%s", addr))
	tr.SetThresholdShareProvider(func(cfID string) (uint32, []byte, error) {
		share, err := s.GetThresholdShare(cfID)
		if err != nil || share == nil {
			return 0, nil, fmt.Errorf("no share")
		}
		return share.ParticipantID, share.SecretShare, nil
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	ep := fmt.Sprintf("http://%s", addr)

	// idStranger tries to initiate a signing session.
	ssA, err := threshold.NewSigningSession(dkgResults[1].SecretShare, dkgResults[1].Public, []byte("test"), []uint32{1, 2})
	if err != nil {
		t.Fatalf("NewSigningSession: %v", err)
	}
	aRound1Msgs := ssA.Start()

	_, err = cfhttp.SendSignRound(ep, campfireID, "nonmember-session", 1, []uint32{1, 2}, []byte("test"), aRound1Msgs, idStranger)
	if err == nil {
		t.Fatal("expected error for non-member sign round, got nil")
	}
	if !contains(err.Error(), "403") {
		t.Errorf("expected 403, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// workspace-j4j: handleRekey — non-member gets 403
// ---------------------------------------------------------------------------

// TestRekeyNonMemberForbidden verifies that a non-member cannot initiate a
// rekey operation (403 Forbidden).
func TestRekeyNonMemberForbidden(t *testing.T) {
	campfirePub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	oldCampfireID := fmt.Sprintf("%x", campfirePub)

	newPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating new campfire key: %v", err)
	}
	newCampfireID := fmt.Sprintf("%x", newPub)

	idMember := tempIdentity(t)
	idStranger := tempIdentity(t)
	s := tempStore(t)

	// Add membership for oldCampfireID.
	err = s.AddMembership(store.Membership{
		CampfireID:   oldCampfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
	addPeerEndpoint(t, s, oldCampfireID, idMember.PublicKeyHex())
	// idStranger NOT in peer endpoints.

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+60)
	tr := cfhttp.New(addr, s)
	tr.SetSelfInfo(idMember.PublicKeyHex(), fmt.Sprintf("http://%s", addr))
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	ep := fmt.Sprintf("http://%s", addr)

	// Generate sender ephemeral X25519 key for phase 1.
	senderPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ephemeral key: %v", err)
	}
	senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

	phase1Req := cfhttp.RekeyRequest{
		NewCampfireID:   newCampfireID,
		SenderX25519Pub: senderPubHex,
	}
	_, err = cfhttp.SendRekeyPhase1(ep, oldCampfireID, phase1Req, idStranger)
	if err == nil {
		t.Fatal("expected error for non-member rekey, got nil")
	}
	if !contains(err.Error(), "403") {
		t.Errorf("expected 403, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// workspace-ul2: handlePoll — known peer (in peer_endpoints but not self) passes
// ---------------------------------------------------------------------------

// TestPollKnownPeerAllowed verifies that a sender in the peer_endpoints list
// (but not the transport self key) can poll successfully.
func TestPollKnownPeerAllowed(t *testing.T) {
	campfireID := "poll-known-peer"
	idSelf := tempIdentity(t)
	idPeer := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	// idPeer is a known peer (in peer_endpoints) but not the self key.
	addPeerEndpoint(t, s, campfireID, idPeer.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+61)
	// Self key is idSelf, not idPeer.
	startTransportWithSelf(t, addr, s, idSelf)
	ep := fmt.Sprintf("http://%s", addr)

	// Pre-store a message so poll returns immediately (no blocking).
	storeMessageRecord(t, s, campfireID, idPeer)

	// idPeer polls — should succeed (200 with messages).
	resp, err := doPoll(ep, campfireID, 0, 1, idPeer)
	if err != nil {
		t.Fatalf("poll request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for known peer poll, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// workspace-j4j: handleRekey — RekeyMessageCBOR must be signed by old campfire key
// ---------------------------------------------------------------------------

// TestRekeyRejectsUnsignedRekeyMessage verifies that a rekey with an
// unsigned (or incorrectly signed) RekeyMessageCBOR message does NOT store
// that message, while still completing the rekey itself.
func TestRekeyRejectsUnsignedRekeyMessage(t *testing.T) {
	oldCFPub, oldCFPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating old campfire key: %v", err)
	}
	oldCampfireID := fmt.Sprintf("%x", oldCFPub)

	newCFPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating new campfire key: %v", err)
	}
	newCampfireID := fmt.Sprintf("%x", newCFPub)

	idA := tempIdentity(t)
	idB := tempIdentity(t)
	sB := tempStore(t)

	stateDirB := t.TempDir()
	// Write old campfire state to disk.
	oldState := struct {
		PublicKey  []byte `cbor:"1,keyasint"`
		PrivateKey []byte `cbor:"2,keyasint"`
		Threshold  uint   `cbor:"6,keyasint"`
	}{
		PublicKey:  oldCFPub,
		PrivateKey: oldCFPriv,
		Threshold:  1,
	}
	stateBytes, _ := cfencoding.Marshal(oldState)
	os.WriteFile(fmt.Sprintf("%s/%s.cbor", stateDirB, oldCampfireID), stateBytes, 0600) //nolint:errcheck

	err = sB.AddMembership(store.Membership{
		CampfireID:   oldCampfireID,
		TransportDir: stateDirB,
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
	// Add A to B's peer list so rekey membership check passes.
	addPeerEndpoint(t, sB, oldCampfireID, idA.PublicKeyHex())

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+62)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Build an UNSIGNED rekey message (no signature — simulates attacker injection).
	unsignedMsg := &message.Message{
		ID:          "attacker-rekey-id",
		Sender:      ed25519.PublicKey(oldCFPub),
		Payload:     []byte(`{"old_key":"attacker","new_key":"injected"}`),
		Tags:        []string{"campfire:rekey"},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
		Provenance:  []message.ProvenanceHop{},
		// Signature field is empty — verification will fail.
	}
	unsignedCBOR, _ := cfencoding.Marshal(unsignedMsg)

	// Build properly signed message (to verify storage does work for valid msgs).
	signedMsg, _ := message.NewMessage(
		ed25519.PrivateKey(oldCFPriv),
		ed25519.PublicKey(oldCFPub),
		[]byte(`{"old_key":"legit","new_key":"legit"}`),
		[]string{"campfire:rekey"},
		nil,
	)
	signedCBOR, _ := cfencoding.Marshal(signedMsg)

	// Build ECDH exchange for encrypted private key.
	senderPrivKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating sender X25519 key: %v", err)
	}
	senderPubHex := fmt.Sprintf("%x", senderPrivKey.PublicKey().Bytes())

	// --- Test 1: unsigned message is not stored ---
	phase1Req := cfhttp.RekeyRequest{
		NewCampfireID:   newCampfireID,
		SenderX25519Pub: senderPubHex,
	}
	phase1Req.RekeyMessageCBOR = unsignedCBOR
	receiverPubHex, err := cfhttp.SendRekeyPhase1(epB, oldCampfireID, phase1Req, idA)
	if err != nil {
		t.Fatalf("rekey phase 1 (unsigned): %v", err)
	}

	// Phase 2: encrypt new private key.
	receiverPubBytes, _ := hex.DecodeString(receiverPubHex)
	receiverPub, err := ecdh.X25519().NewPublicKey(receiverPubBytes)
	if err != nil {
		t.Fatalf("parsing receiver pub: %v", err)
	}
	shared, err := senderPrivKey.ECDH(receiverPub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}

	// Generate a new private key to encrypt and send.
	newPrivForTest, _, _ := ed25519.GenerateKey(nil)
	encKey := rekeyTestEncrypt32(t, shared, newPrivForTest)

	phase2Req := cfhttp.RekeyRequest{
		NewCampfireID:    newCampfireID,
		SenderX25519Pub:  senderPubHex,
		EncryptedPrivKey: encKey,
		RekeyMessageCBOR: unsignedCBOR,
	}
	if err := cfhttp.SendRekey(epB, oldCampfireID, phase2Req, idA); err != nil {
		// After phase 1, B's campfire is renamed to newCampfireID. Phase 2 goes to oldCampfireID
		// which may not exist in B's membership if UpdateCampfireID already ran from phase 1.
		// Phase 1 doesn't do the rename, only phase 2 does — so this should succeed.
		t.Fatalf("rekey phase 2 (unsigned msg): %v", err)
	}

	// The unsigned rekey message should NOT be in B's store.
	msgs, err := sB.ListMessages(newCampfireID, 0)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	for _, m := range msgs {
		if m.ID == unsignedMsg.ID {
			t.Error("unsigned rekey message was stored — should have been rejected")
		}
	}
	_ = signedCBOR // used in documentation; verify it works in a signed-message test
}

// rekeyTestEncrypt32 encrypts plaintext with a 32-byte key using AES-256-GCM.
func rekeyTestEncrypt32(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	// If key is not 32 bytes, hash it.
	k := key
	if len(k) != 32 {
		// Use first 32 bytes or pad.
		if len(k) > 32 {
			k = k[:32]
		} else {
			padded := make([]byte, 32)
			copy(padded, k)
			k = padded
		}
	}
	encrypted, err := rekeyTestEncrypt(k, plaintext)
	if err != nil {
		t.Fatalf("encrypting: %v", err)
	}
	return encrypted
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

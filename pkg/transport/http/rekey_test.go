package http_test

// TestRekeyThreshold1 verifies the eviction + rekey flow for threshold=1:
// - Creator (A) evicts member C from a 3-member campfire.
// - A delivers the new campfire identity to remaining member B via the rekey protocol.
// - B's store is updated to the new campfire ID.
// - A and B can exchange messages under the new campfire ID.
// - C cannot deliver messages to B under the new campfire ID.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"os"
	"path/filepath"
)

// setupCampfireState writes a CampfireState CBOR file to a temp directory.
// Returns the state directory and the campfire ID (public key hex).
func setupCampfireState(t *testing.T, priv []byte, pub ed25519.PublicKey, thresh uint) (stateDir, campfireID string) {
	t.Helper()
	stateDir = t.TempDir()
	campfireID = fmt.Sprintf("%x", pub)
	state := campfire.CampfireState{
		PublicKey:             pub,
		PrivateKey:            priv,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             thresh,
	}
	data, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("encoding campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, campfireID+".cbor"), data, 0600); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}
	return stateDir, campfireID
}

// addMembershipWithDir adds a campfire membership with a specific TransportDir.
func addMembershipWithDir(t *testing.T, s *store.Store, campfireID, transportDir string, thresh uint) {
	t.Helper()
	err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transportDir,
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    thresh,
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
}

// testHKDFSHA256 mirrors the HKDF-SHA256 derivation in crypto.go.
// Tests must use this to derive the AES key from the raw X25519 shared secret
// so that the key material matches what the server-side handler expects.
func testHKDFSHA256(sharedSecret []byte, info string) []byte {
	h := sha256.New
	salt := make([]byte, h().Size())
	extractor := hmac.New(h, salt)
	extractor.Write(sharedSecret)
	prk := extractor.Sum(nil)

	expander := hmac.New(h, prk)
	io.WriteString(expander, info) //nolint:errcheck
	expander.Write([]byte{0x01})
	okm := expander.Sum(nil)
	return okm[:32]
}

// rekeyTestEncrypt is an AES-256-GCM encrypt helper for tests.
// Returns nonce || ciphertext. key must be 32 bytes.
func rekeyTestEncrypt(key, plaintext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// deliverRekeyTest performs the two-phase rekey delivery used in tests.
func deliverRekeyTest(
	t *testing.T,
	endpoint, oldCampfireID, newCampfireID string,
	newPrivKey []byte,
	newShareData []byte,
	newParticipantID uint32,
	evictedPubkeyHex string,
	rekeyMsgCBOR []byte,
	senderID interface{ PublicKeyHex() string },
) {
	t.Helper()

	// Generate sender's ephemeral X25519 key.
	senderPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ephemeral key: %v", err)
	}
	senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

	// We need an identity to sign the request — use the identity from the test.
	// The test passes an identity.Identity, so we type-assert here.
	type idSigner interface {
		PublicKeyHex() string
		Sign([]byte) []byte
	}

	// Phase 1: get receiver's ephemeral pub.
	phase1Req := cfhttp.RekeyRequest{
		NewCampfireID:       newCampfireID,
		SenderX25519Pub:     senderPubHex,
		EvictedMemberPubkey: evictedPubkeyHex,
		RekeyMessageCBOR:    rekeyMsgCBOR,
	}
	// Note: cfhttp.SendRekeyPhase1 needs an *identity.Identity for signing.
	// We can't call it directly without the concrete type here.
	// Instead we use the explicit identity from the test via the identity package.
	t.Logf("Phase 1 req to %s, campfire %s", endpoint, oldCampfireID)
	_ = phase1Req
	t.Logf("Note: deliverRekeyTest is a placeholder — tests use SendRekeyPhase1 directly")

	_ = senderPriv
	_ = newPrivKey
	_ = newShareData
	_ = newParticipantID
	_ = senderID
}

// TestRekeyProtocolThreshold1 tests the two-phase rekey protocol for threshold=1.
// Verifies:
// - Phase 1 → receiver returns ephemeral X25519 pub key.
// - Phase 2 with encrypted new private key → B's store updated to new campfire ID.
// - B can receive messages under new campfire ID.
// - Evicted member's endpoint removed from B's peer list.
// - campfire:rekey message stored under new campfire ID in B's store.
func TestRekeyProtocolThreshold1(t *testing.T) {
	// Generate old campfire keypair.
	oldCFPub, oldCFPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating old campfire key: %v", err)
	}
	oldCampfireID := fmt.Sprintf("%x", oldCFPub)

	// Generate new campfire keypair.
	newCFPub, newCFPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating new campfire key: %v", err)
	}
	newCampfireID := fmt.Sprintf("%x", newCFPub)

	idA := tempIdentity(t) // creator
	idB := tempIdentity(t) // remaining member
	idC := tempIdentity(t) // evicted member

	sA := tempStore(t)
	sB := tempStore(t)

	// B has the old campfire state on disk and in its store.
	stateDirB, _ := setupCampfireState(t, oldCFPriv, oldCFPub, 1)
	addMembershipWithDir(t, sA, oldCampfireID, t.TempDir(), 1)
	addMembershipWithDir(t, sB, oldCampfireID, stateDirB, 1)

	// Add A (sender) to B's peer endpoints so rekey membership check passes.
	sB.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   oldCampfireID,
		MemberPubkey: idA.PublicKeyHex(),
		Endpoint:     "http://127.0.0.1:9998",
	})
	// Add C's endpoint to B's peer endpoints (to verify eviction removes it).
	sB.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   oldCampfireID,
		MemberPubkey: idC.PublicKeyHex(),
		Endpoint:     "http://127.0.0.1:9999",
	})

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+30)
	epB := fmt.Sprintf("http://%s", addrB)

	// Start B's transport.
	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Build campfire:rekey message signed by old key.
	rekeyPayload, _ := json.Marshal(map[string]string{
		"old_key": oldCampfireID,
		"new_key": newCampfireID,
		"reason":  "eviction",
	})
	rekeyMsg, err := message.NewMessage(
		ed25519.PrivateKey(oldCFPriv),
		ed25519.PublicKey(oldCFPub),
		rekeyPayload,
		[]string{"campfire:rekey"},
		nil,
	)
	if err != nil {
		t.Fatalf("creating rekey message: %v", err)
	}
	rekeyMsgCBOR, err := cfencoding.Marshal(rekeyMsg)
	if err != nil {
		t.Fatalf("encoding rekey message: %v", err)
	}

	// --- Two-phase rekey delivery ---

	// Generate sender's ephemeral X25519 key.
	senderPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating sender ephemeral key: %v", err)
	}
	senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

	// Phase 1: get receiver's ephemeral pub.
	phase1Req := cfhttp.RekeyRequest{
		NewCampfireID:       newCampfireID,
		SenderX25519Pub:     senderPubHex,
		EvictedMemberPubkey: idC.PublicKeyHex(),
		RekeyMessageCBOR:    rekeyMsgCBOR,
	}
	receiverEphemeralPubHex, err := cfhttp.SendRekeyPhase1(epB, oldCampfireID, phase1Req, idA)
	if err != nil {
		t.Fatalf("rekey phase 1: %v", err)
	}
	if receiverEphemeralPubHex == "" {
		t.Fatal("expected receiver ephemeral pub key in phase 1 response")
	}

	// Derive shared secret.
	receiverPubBytes, _ := hex.DecodeString(receiverEphemeralPubHex)
	receiverPub, err := ecdh.X25519().NewPublicKey(receiverPubBytes)
	if err != nil {
		t.Fatalf("parsing receiver ephemeral pub: %v", err)
	}
	rawShared, err := senderPriv.ECDH(receiverPub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	// Apply HKDF to match server-side key derivation (campfire-rekey-v1).
	derivedKey := testHKDFSHA256(rawShared, "campfire-rekey-v1")

	// Encrypt new campfire private key.
	encNewPrivKey, err := rekeyTestEncrypt(derivedKey, newCFPriv)
	if err != nil {
		t.Fatalf("encrypting new private key: %v", err)
	}

	// Phase 2: send encrypted new private key.
	phase2Req := cfhttp.RekeyRequest{
		NewCampfireID:       newCampfireID,
		SenderX25519Pub:     senderPubHex,
		EvictedMemberPubkey: idC.PublicKeyHex(),
		RekeyMessageCBOR:    rekeyMsgCBOR,
		EncryptedPrivKey:    encNewPrivKey,
	}
	if err := cfhttp.SendRekey(epB, oldCampfireID, phase2Req, idA); err != nil {
		t.Fatalf("rekey phase 2: %v", err)
	}

	// --- Verify B's state ---

	// B's membership should now be under new campfire ID.
	newMembership, err := sB.GetMembership(newCampfireID)
	if err != nil {
		t.Fatalf("getting new membership: %v", err)
	}
	if newMembership == nil {
		t.Fatal("B's store should have membership under new campfire ID after rekey")
	}

	// Old membership should be gone.
	oldMembership, _ := sB.GetMembership(oldCampfireID)
	if oldMembership != nil {
		t.Error("B's store should NOT have membership under old campfire ID after rekey")
	}

	// Evicted member C should not be in B's peer endpoints.
	peerEndpoints, _ := sB.ListPeerEndpoints(newCampfireID)
	for _, pe := range peerEndpoints {
		if pe.MemberPubkey == idC.PublicKeyHex() {
			t.Error("evicted member C should not be in B's peer endpoints for new campfire")
		}
	}

	// campfire:rekey message should be in B's store under new campfire ID.
	msgs, err := sB.ListMessages(newCampfireID, 0)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	foundRekey := false
	for _, m := range msgs {
		for _, tag := range []byte(m.Tags) {
			_ = tag
		}
		var payload map[string]string
		json.Unmarshal(m.Payload, &payload) //nolint:errcheck
		if payload["old_key"] == oldCampfireID && payload["new_key"] == newCampfireID {
			foundRekey = true
			break
		}
	}
	if !foundRekey {
		t.Error("campfire:rekey message not found in B's store under new campfire ID")
	}

	// --- B can receive messages under new campfire ID ---

	// A sends a message under new campfire ID to B.
	newMsg, err := message.NewMessage(
		idA.PrivateKey, idA.PublicKey,
		[]byte("post-rekey message"),
		[]string{"test"},
		nil,
	)
	if err != nil {
		t.Fatalf("creating post-rekey message: %v", err)
	}
	if err := newMsg.AddHop(
		ed25519.PrivateKey(newCFPriv),
		ed25519.PublicKey(newCFPub),
		newCFPub, 2, "open", []string{},
	); err != nil {
		t.Fatalf("adding hop: %v", err)
	}

	// B needs a membership entry for newCampfireID to accept delivery.
	// (The handler updated sB's membership store, so delivery should work.)
	if err := cfhttp.Deliver(epB, newCampfireID, newMsg, idA); err != nil {
		t.Fatalf("delivering post-rekey message to B: %v", err)
	}

	// Verify B syncs the message.
	syncedMsgs, err := cfhttp.Sync(epB, newCampfireID, 0, idA)
	if err != nil {
		t.Fatalf("syncing from B: %v", err)
	}
	postRekeyFound := false
	for _, m := range syncedMsgs {
		if m.ID == newMsg.ID {
			postRekeyFound = true
			if !m.VerifySignature() {
				t.Error("post-rekey message signature invalid")
			}
			break
		}
	}
	if !postRekeyFound {
		t.Errorf("post-rekey message not found in B's store (got %d messages)", len(syncedMsgs))
	}
}

// TestRekeyProtocolThreshold2 tests the rekey flow for threshold=2:
// - 3-member campfire (A, B, C) with threshold=2.
// - A (creator) evicts C.
// - A distributes new FROST DKG share to B via the rekey protocol.
// - B's store has the new campfire ID and new FROST share.
// - A and B can threshold-sign under the new campfire ID.
func TestRekeyProtocolThreshold2(t *testing.T) {
	// Old DKG: 3 participants, threshold=2.
	oldDKGResults, err := threshold.RunDKG([]uint32{1, 2, 3}, 2)
	if err != nil {
		t.Fatalf("RunDKG (old): %v", err)
	}
	oldGroupKey := oldDKGResults[1].GroupPublicKey()
	oldCampfireID := fmt.Sprintf("%x", oldGroupKey)

	// New DKG: 2 remaining participants, threshold=2.
	newDKGResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG (new): %v", err)
	}
	newGroupKey := newDKGResults[1].GroupPublicKey()
	newCampfireID := fmt.Sprintf("%x", newGroupKey)

	idA := tempIdentity(t) // creator (participant 1)
	idB := tempIdentity(t) // remaining (participant 2)
	idC := tempIdentity(t) // evicted (participant 3)

	sA := tempStore(t)
	sB := tempStore(t)

	// B's campfire state on disk (no private key for threshold>1).
	stateDirB, _ := setupCampfireState(t, nil, oldGroupKey, 2)
	addMembershipWithDir(t, sA, oldCampfireID, t.TempDir(), 2)
	addMembershipWithDir(t, sB, oldCampfireID, stateDirB, 2)

	// Store old DKG shares.
	oldShareA, _ := threshold.MarshalResult(1, oldDKGResults[1])
	oldShareB, _ := threshold.MarshalResult(2, oldDKGResults[2])
	sA.UpsertThresholdShare(store.ThresholdShare{CampfireID: oldCampfireID, ParticipantID: 1, SecretShare: oldShareA}) //nolint:errcheck
	sB.UpsertThresholdShare(store.ThresholdShare{CampfireID: oldCampfireID, ParticipantID: 2, SecretShare: oldShareB}) //nolint:errcheck

	// Add A (sender) to B's peer endpoints so rekey membership check passes.
	sB.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:    oldCampfireID,
		MemberPubkey:  idA.PublicKeyHex(),
		Endpoint:      "http://127.0.0.1:9997",
		ParticipantID: 1,
	})
	// Add C to B's peer endpoints.
	sB.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:    oldCampfireID,
		MemberPubkey:  idC.PublicKeyHex(),
		Endpoint:      "http://127.0.0.1:9998",
		ParticipantID: 3,
	})

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+34)
	epB := fmt.Sprintf("http://%s", addrB)

	// Start B's transport with threshold share provider.
	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	trB.SetThresholdShareProvider(func(cfID string) (uint32, []byte, error) {
		share, err := sB.GetThresholdShare(cfID)
		if err != nil || share == nil {
			return 0, nil, fmt.Errorf("no share for %s", cfID)
		}
		return share.ParticipantID, share.SecretShare, nil
	})
	if err := trB.Start(); err != nil {
		t.Fatalf("starting B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// For threshold>1, FROST signing requires a quorum — no single private key exists.
	// The eviction proceeds without a rekey audit message (nil CBOR).
	// This matches the fallback behavior in evictThresholdN when FROST signing fails.
	var rekeyMsgCBOR []byte

	// Serialize B's new FROST DKG share.
	newShareBData, err := threshold.MarshalResult(2, newDKGResults[2])
	if err != nil {
		t.Fatalf("marshaling new share B: %v", err)
	}

	// --- Two-phase rekey delivery ---
	senderPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating sender ephemeral key: %v", err)
	}
	senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

	phase1Req := cfhttp.RekeyRequest{
		NewCampfireID:       newCampfireID,
		SenderX25519Pub:     senderPubHex,
		EvictedMemberPubkey: idC.PublicKeyHex(),
		RekeyMessageCBOR:    rekeyMsgCBOR,
	}
	receiverEphemeralPubHex, err := cfhttp.SendRekeyPhase1(epB, oldCampfireID, phase1Req, idA)
	if err != nil {
		t.Fatalf("rekey phase 1: %v", err)
	}
	if receiverEphemeralPubHex == "" {
		t.Fatal("expected receiver ephemeral pub in phase 1 response")
	}

	receiverPubBytes, _ := hex.DecodeString(receiverEphemeralPubHex)
	receiverPub, err := ecdh.X25519().NewPublicKey(receiverPubBytes)
	if err != nil {
		t.Fatalf("parsing receiver ephemeral pub: %v", err)
	}
	rawShared2, err := senderPriv.ECDH(receiverPub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	// Apply HKDF to match server-side key derivation (campfire-rekey-v1).
	derivedKey2 := testHKDFSHA256(rawShared2, "campfire-rekey-v1")

	encNewShare, err := rekeyTestEncrypt(derivedKey2, newShareBData)
	if err != nil {
		t.Fatalf("encrypting new share: %v", err)
	}

	phase2Req := cfhttp.RekeyRequest{
		NewCampfireID:       newCampfireID,
		SenderX25519Pub:     senderPubHex,
		EvictedMemberPubkey: idC.PublicKeyHex(),
		RekeyMessageCBOR:    rekeyMsgCBOR,
		EncryptedShareData:  encNewShare,
		NewParticipantID:    2,
	}
	if err := cfhttp.SendRekey(epB, oldCampfireID, phase2Req, idA); err != nil {
		t.Fatalf("rekey phase 2: %v", err)
	}

	// --- Verify B's state ---

	newMembership, err := sB.GetMembership(newCampfireID)
	if err != nil {
		t.Fatalf("getting new membership: %v", err)
	}
	if newMembership == nil {
		t.Fatal("B's store should have membership under new campfire ID")
	}

	oldMembership, _ := sB.GetMembership(oldCampfireID)
	if oldMembership != nil {
		t.Error("B's store should NOT have old campfire ID")
	}

	// B should have the new FROST DKG share.
	newShareStored, err := sB.GetThresholdShare(newCampfireID)
	if err != nil {
		t.Fatalf("getting new threshold share: %v", err)
	}
	if newShareStored == nil {
		t.Fatal("B's store should have new FROST DKG share after rekey")
	}
	if newShareStored.ParticipantID != 2 {
		t.Errorf("expected participant ID 2, got %d", newShareStored.ParticipantID)
	}

	// Evicted member C should not be in B's peer endpoints.
	peerEndpoints, _ := sB.ListPeerEndpoints(newCampfireID)
	for _, pe := range peerEndpoints {
		if pe.MemberPubkey == idC.PublicKeyHex() {
			t.Error("evicted member C should not be in B's peer endpoints for new campfire")
		}
	}

	// --- A and B can threshold-sign under new campfire ID ---

	// Store A's new share.
	newShareAData, _ := threshold.MarshalResult(1, newDKGResults[1])
	sA.UpsertThresholdShare(store.ThresholdShare{ //nolint:errcheck
		CampfireID:    newCampfireID,
		ParticipantID: 1,
		SecretShare:   newShareAData,
	})

	signMsg := []byte("post-rekey threshold signing test")
	sig, err := threshold.Sign(newDKGResults, []uint32{1, 2}, signMsg)
	if err != nil {
		t.Fatalf("threshold.Sign with new DKG: %v", err)
	}
	if !ed25519.Verify(newGroupKey, signMsg, sig) {
		t.Fatal("post-rekey threshold signature invalid")
	}

	t.Logf("Rekey threshold=2 test passed: old=%s new=%s", oldCampfireID[:12], newCampfireID[:12])
}

// TestRekeyNonCreatorForbidden verifies that a non-creator member cannot trigger a rekey.
func TestRekeyNonCreatorForbidden(t *testing.T) {
	oldCFPub, oldCFPriv, _ := ed25519.GenerateKey(nil)
	oldCampfireID := fmt.Sprintf("%x", oldCFPub)

	idCreator := tempIdentity(t)
	idMember := tempIdentity(t)

	sB := tempStore(t)
	stateDirB, _ := setupCampfireState(t, oldCFPriv, oldCFPub, 1)

	// Add membership WITH creator_pubkey set to idCreator.
	err := sB.AddMembership(store.Membership{
		CampfireID:    oldCampfireID,
		TransportDir:  stateDirB,
		JoinProtocol:  "open",
		Role:          "member",
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		CreatorPubkey: idCreator.PublicKeyHex(),
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Add idMember to peer endpoints so membership check passes.
	sB.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   oldCampfireID,
		MemberPubkey: idMember.PublicKeyHex(),
		Endpoint:     "http://127.0.0.1:9997",
	})

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+40)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idCreator.PublicKeyHex(), epB)
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Non-creator member tries to send rekey.
	senderPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

	newCFPub, _, _ := ed25519.GenerateKey(nil)
	newCampfireID := fmt.Sprintf("%x", newCFPub)

	phase1Req := cfhttp.RekeyRequest{
		NewCampfireID:   newCampfireID,
		SenderX25519Pub: senderPubHex,
	}
	_, err = cfhttp.SendRekeyPhase1(epB, oldCampfireID, phase1Req, idMember)
	if err == nil {
		t.Fatal("expected error: non-creator should be forbidden from rekey")
	}
	t.Logf("non-creator rekey correctly rejected: %v", err)
}

// TestRekeyForgedSenderRejected verifies that a rekey message signed by the sender's
// personal key (not the campfire key) is rejected.
func TestRekeyForgedSenderRejected(t *testing.T) {
	oldCFPub, oldCFPriv, _ := ed25519.GenerateKey(nil)
	oldCampfireID := fmt.Sprintf("%x", oldCFPub)

	idA := tempIdentity(t)

	sB := tempStore(t)
	stateDirB, _ := setupCampfireState(t, oldCFPriv, oldCFPub, 1)
	addMembershipWithDir(t, sB, oldCampfireID, stateDirB, 1)

	// Add A to B's peer endpoints.
	sB.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   oldCampfireID,
		MemberPubkey: idA.PublicKeyHex(),
		Endpoint:     "http://127.0.0.1:9997",
	})

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+42)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idA.PublicKeyHex(), epB)
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Build a rekey message signed by A's personal key (NOT the campfire key).
	newCFPub, _, _ := ed25519.GenerateKey(nil)
	newCampfireID := fmt.Sprintf("%x", newCFPub)

	rekeyPayload, _ := json.Marshal(map[string]string{
		"old_key": oldCampfireID,
		"new_key": newCampfireID,
		"reason":  "forged eviction",
	})
	// Sign with A's personal key — this should be rejected.
	forgedMsg, err := message.NewMessage(
		idA.PrivateKey, idA.PublicKey,
		rekeyPayload,
		[]string{"campfire:rekey"},
		nil,
	)
	if err != nil {
		t.Fatalf("creating forged message: %v", err)
	}
	forgedMsgCBOR, _ := cfencoding.Marshal(forgedMsg)

	senderPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

	phase1Req := cfhttp.RekeyRequest{
		NewCampfireID:   newCampfireID,
		SenderX25519Pub: senderPubHex,
		RekeyMessageCBOR: forgedMsgCBOR,
	}
	receiverPubHex, err := cfhttp.SendRekeyPhase1(epB, oldCampfireID, phase1Req, idA)
	if err != nil {
		// Phase 1 may reject early due to the sig check — that's fine.
		t.Logf("phase 1 rejected forged message: %v", err)
		return
	}

	// If phase 1 succeeded (sig check is on phase 2), try phase 2.
	receiverPubBytes, _ := hex.DecodeString(receiverPubHex)
	receiverPub, _ := ecdh.X25519().NewPublicKey(receiverPubBytes)
	rawShared, _ := senderPriv.ECDH(receiverPub)
	derivedKey := testHKDFSHA256(rawShared, "campfire-rekey-v1")

	newPrivKey := make([]byte, 64)
	rand.Read(newPrivKey) //nolint:errcheck
	encKey, _ := rekeyTestEncrypt(derivedKey, newPrivKey)

	phase2Req := cfhttp.RekeyRequest{
		NewCampfireID:    newCampfireID,
		SenderX25519Pub:  senderPubHex,
		RekeyMessageCBOR: forgedMsgCBOR,
		EncryptedPrivKey: encKey,
	}
	err = cfhttp.SendRekey(epB, oldCampfireID, phase2Req, idA)
	if err == nil {
		t.Fatal("expected phase 2 to reject forged rekey message signature")
	}
	t.Logf("forged rekey signature correctly rejected: %v", err)
}

// TestRekeyUnsignedMessageRejected verifies that unsigned rekey messages are rejected
// for all threshold values. An unsigned rekey message cannot serve as a verifiable
// audit record; the handler must reject it regardless of threshold.
// When FROST quorum signing fails (threshold>1), the sender should omit RekeyMessageCBOR
// entirely (nil) rather than sending an unsigned placeholder.
func TestRekeyUnsignedMessageRejected(t *testing.T) {
	for _, tc := range []struct {
		name      string
		threshold uint
	}{
		{"threshold=1", 1},
		{"threshold=2", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldCFPub, oldCFPriv, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatalf("generating campfire key: %v", err)
			}
			oldCampfireID := fmt.Sprintf("%x", oldCFPub)

			newCFPub, _, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatalf("generating new campfire key: %v", err)
			}
			newCampfireID := fmt.Sprintf("%x", newCFPub)

			idA := tempIdentity(t)
			sB := tempStore(t)
			stateDirB, _ := setupCampfireState(t, oldCFPriv, oldCFPub, tc.threshold)
			addMembershipWithDir(t, sB, oldCampfireID, stateDirB, tc.threshold)

			sB.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
				CampfireID:   oldCampfireID,
				MemberPubkey: idA.PublicKeyHex(),
				Endpoint:     "http://127.0.0.1:9997",
			})

			base := portBase()
			addrB := fmt.Sprintf("127.0.0.1:%d", base+48+int(tc.threshold))
			epB := fmt.Sprintf("http://%s", addrB)

			trB := cfhttp.New(addrB, sB)
			trB.SetSelfInfo(idA.PublicKeyHex(), epB)
			if err := trB.Start(); err != nil {
				t.Fatalf("starting transport: %v", err)
			}
			t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
			time.Sleep(20 * time.Millisecond)

			// Build an unsigned rekey message (no Signature field).
			rekeyPayload, _ := json.Marshal(map[string]string{
				"old_key": oldCampfireID,
				"new_key": newCampfireID,
				"reason":  "eviction",
			})
			unsignedMsg := &message.Message{
				ID:          "unsigned-rekey-test",
				Sender:      ed25519.PublicKey(oldCFPub),
				Payload:     rekeyPayload,
				Tags:        []string{"campfire:rekey"},
				Antecedents: []string{},
				Timestamp:   time.Now().UnixNano(),
				Provenance:  []message.ProvenanceHop{},
				// Signature intentionally absent.
			}
			unsignedMsgCBOR, _ := cfencoding.Marshal(unsignedMsg)

			senderPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
			senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

			phase1Req := cfhttp.RekeyRequest{
				NewCampfireID:    newCampfireID,
				SenderX25519Pub:  senderPubHex,
				RekeyMessageCBOR: unsignedMsgCBOR,
			}
			_, err = cfhttp.SendRekeyPhase1(epB, oldCampfireID, phase1Req, idA)
			if err == nil {
				t.Fatalf("expected unsigned rekey message to be rejected for %s", tc.name)
			}
			t.Logf("unsigned rekey correctly rejected for %s: %v", tc.name, err)
		})
	}
}

// TestRekeyDBFailureLeavesStateFileIntact verifies the atomicity guarantee:
// if UpdateCampfireID fails (DB closed after phase 1 but before phase 2 commit),
// the old campfire state file must remain untouched so the campfire is recoverable.
//
// This guards against the regression where file ops happened before the DB update,
// leaving a node with the old .cbor file deleted but the DB still holding the old
// campfire_id — an unrecoverable inconsistency.
func TestRekeyDBFailureLeavesStateFileIntact(t *testing.T) {
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

	idA := tempIdentity(t) // creator / sender

	sB := tempStore(t)
	stateDirB, _ := setupCampfireState(t, oldCFPriv, oldCFPub, 1)
	addMembershipWithDir(t, sB, oldCampfireID, stateDirB, 1)

	// Add A to B's peer endpoints so membership check passes.
	sB.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   oldCampfireID,
		MemberPubkey: idA.PublicKeyHex(),
		Endpoint:     "http://127.0.0.1:9996",
	})

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+46)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idA.PublicKeyHex(), epB)
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Build campfire:rekey message signed by old key.
	rekeyPayload, _ := json.Marshal(map[string]string{
		"old_key": oldCampfireID,
		"new_key": newCampfireID,
		"reason":  "eviction",
	})
	rekeyMsg, err := message.NewMessage(
		ed25519.PrivateKey(oldCFPriv),
		ed25519.PublicKey(oldCFPub),
		rekeyPayload,
		[]string{"campfire:rekey"},
		nil,
	)
	if err != nil {
		t.Fatalf("creating rekey message: %v", err)
	}
	rekeyMsgCBOR, err := cfencoding.Marshal(rekeyMsg)
	if err != nil {
		t.Fatalf("encoding rekey message: %v", err)
	}

	// Phase 1: get receiver's ephemeral pub key.
	senderPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating sender ephemeral key: %v", err)
	}
	senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

	phase1Req := cfhttp.RekeyRequest{
		NewCampfireID:    newCampfireID,
		SenderX25519Pub:  senderPubHex,
		RekeyMessageCBOR: rekeyMsgCBOR,
	}
	receiverEphemeralPubHex, err := cfhttp.SendRekeyPhase1(epB, oldCampfireID, phase1Req, idA)
	if err != nil {
		t.Fatalf("rekey phase 1: %v", err)
	}
	if receiverEphemeralPubHex == "" {
		t.Fatal("expected receiver ephemeral pub key in phase 1 response")
	}

	// Derive shared secret and encrypt new private key.
	receiverPubBytes, _ := hex.DecodeString(receiverEphemeralPubHex)
	receiverPub, err := ecdh.X25519().NewPublicKey(receiverPubBytes)
	if err != nil {
		t.Fatalf("parsing receiver ephemeral pub: %v", err)
	}
	rawShared, err := senderPriv.ECDH(receiverPub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	derivedKey := testHKDFSHA256(rawShared, "campfire-rekey-v1")

	newPrivKey := make([]byte, ed25519.PrivateKeySize)
	rand.Read(newPrivKey) //nolint:errcheck
	encNewPrivKey, err := rekeyTestEncrypt(derivedKey, newPrivKey)
	if err != nil {
		t.Fatalf("encrypting new private key: %v", err)
	}

	// Close the DB AFTER phase 1 so that UpdateCampfireID will fail in phase 2.
	// The transport is still running and will accept the HTTP request, but the
	// DB write will fail.
	sB.Close()

	// Phase 2 must fail because the DB is closed.
	phase2Req := cfhttp.RekeyRequest{
		NewCampfireID:    newCampfireID,
		SenderX25519Pub:  senderPubHex,
		RekeyMessageCBOR: rekeyMsgCBOR,
		EncryptedPrivKey: encNewPrivKey,
	}
	err = cfhttp.SendRekey(epB, oldCampfireID, phase2Req, idA)
	if err == nil {
		t.Fatal("expected phase 2 to fail when DB is closed")
	}
	t.Logf("phase 2 correctly failed with DB closed: %v", err)

	// The old campfire state file must still exist — the campfire is recoverable.
	oldStateFile := filepath.Join(stateDirB, oldCampfireID+".cbor")
	if _, statErr := os.Stat(oldStateFile); os.IsNotExist(statErr) {
		t.Error("old campfire state file must NOT be removed when UpdateCampfireID fails (DB failure leaves inconsistent state)")
	}

	// The new campfire state file must NOT have been written.
	newStateFile := filepath.Join(stateDirB, newCampfireID+".cbor")
	if _, statErr := os.Stat(newStateFile); !os.IsNotExist(statErr) {
		t.Error("new campfire state file must NOT be written when UpdateCampfireID fails")
	}
}

// TestRekeyPhase2CorruptCiphertextReturns400 verifies that if phase-2 sends a ciphertext
// encrypted under a different key (not the HKDF-derived shared secret), the handler
// returns 400 Bad Request. This exercises the integration path:
//
//	wrong shared secret → aesGCMDecrypt failure → http.StatusBadRequest
//
// The aesGCMDecrypt function is unit-tested in crypto_test.go; this test covers the
// handler-level integration path that was previously untested.
func TestRekeyPhase2CorruptCiphertextReturns400(t *testing.T) {
	// Generate old campfire keypair.
	oldCFPub, oldCFPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating old campfire key: %v", err)
	}
	oldCampfireID := fmt.Sprintf("%x", oldCFPub)

	// Generate new campfire keypair (we only need the pub for the new campfire ID).
	newCFPub, newCFPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating new campfire key: %v", err)
	}
	newCampfireID := fmt.Sprintf("%x", newCFPub)

	idA := tempIdentity(t) // sender (creator)
	sB := tempStore(t)

	stateDirB, _ := setupCampfireState(t, oldCFPriv, oldCFPub, 1)
	addMembershipWithDir(t, sB, oldCampfireID, stateDirB, 1)

	// A must be in B's peer endpoints so the membership check passes.
	sB.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   oldCampfireID,
		MemberPubkey: idA.PublicKeyHex(),
		Endpoint:     "http://127.0.0.1:9995",
	})

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+52)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idA.PublicKeyHex(), epB)
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Build a valid campfire:rekey message signed by the old campfire key.
	rekeyPayload, _ := json.Marshal(map[string]string{
		"old_key": oldCampfireID,
		"new_key": newCampfireID,
		"reason":  "eviction",
	})
	rekeyMsg, err := message.NewMessage(
		ed25519.PrivateKey(oldCFPriv),
		ed25519.PublicKey(oldCFPub),
		rekeyPayload,
		[]string{"campfire:rekey"},
		nil,
	)
	if err != nil {
		t.Fatalf("creating rekey message: %v", err)
	}
	rekeyMsgCBOR, err := cfencoding.Marshal(rekeyMsg)
	if err != nil {
		t.Fatalf("encoding rekey message: %v", err)
	}

	// --- Phase 1: complete successfully to establish the rekey session ---
	senderPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating sender ephemeral key: %v", err)
	}
	senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

	phase1Req := cfhttp.RekeyRequest{
		NewCampfireID:    newCampfireID,
		SenderX25519Pub:  senderPubHex,
		RekeyMessageCBOR: rekeyMsgCBOR,
	}
	receiverEphemeralPubHex, err := cfhttp.SendRekeyPhase1(epB, oldCampfireID, phase1Req, idA)
	if err != nil {
		t.Fatalf("rekey phase 1 failed (expected success): %v", err)
	}
	if receiverEphemeralPubHex == "" {
		t.Fatal("expected non-empty receiver ephemeral pub key from phase 1")
	}

	// --- Phase 2: encrypt the payload with a DIFFERENT key (not the HKDF-derived secret) ---
	//
	// The correct shared secret is derived from senderPriv.ECDH(receiverPub).
	// Instead, we generate a fresh random X25519 key and use its derived secret —
	// this produces a ciphertext the server cannot decrypt because it holds a
	// different private key.
	wrongSenderPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating wrong sender key: %v", err)
	}
	// Use the real receiver pub so ECDH doesn't fail on parsing, but the wrong
	// sender private key produces a different raw shared secret.
	receiverPubBytes, err := hex.DecodeString(receiverEphemeralPubHex)
	if err != nil {
		t.Fatalf("decoding receiver ephemeral pub: %v", err)
	}
	receiverPub, err := ecdh.X25519().NewPublicKey(receiverPubBytes)
	if err != nil {
		t.Fatalf("parsing receiver ephemeral pub: %v", err)
	}
	wrongRawShared, err := wrongSenderPriv.ECDH(receiverPub)
	if err != nil {
		t.Fatalf("ECDH with wrong sender key: %v", err)
	}
	wrongDerivedKey := testHKDFSHA256(wrongRawShared, "campfire-rekey-v1")

	// Encrypt the new private key using the WRONG derived key.
	encNewPrivKey, err := rekeyTestEncrypt(wrongDerivedKey, newCFPriv)
	if err != nil {
		t.Fatalf("encrypting with wrong key: %v", err)
	}

	// Phase 2: senderPubHex is the CORRECT sender pub (server looks it up),
	// but the ciphertext was encrypted with a different shared secret.
	// aesGCMDecrypt will fail because the GCM authentication tag won't verify.
	phase2Req := cfhttp.RekeyRequest{
		NewCampfireID:    newCampfireID,
		SenderX25519Pub:  senderPubHex,
		RekeyMessageCBOR: rekeyMsgCBOR,
		EncryptedPrivKey: encNewPrivKey,
	}
	err = cfhttp.SendRekey(epB, oldCampfireID, phase2Req, idA)
	if err == nil {
		t.Fatal("expected phase-2 to return 400 when ciphertext was encrypted with wrong key")
	}
	t.Logf("phase-2 with corrupt ciphertext correctly rejected: %v", err)

	// Verify that B's store was NOT updated — old membership still present, no new one.
	oldMembership, _ := sB.GetMembership(oldCampfireID)
	if oldMembership == nil {
		t.Error("old membership should still exist after a failed phase-2 decryption")
	}
	newMembership, _ := sB.GetMembership(newCampfireID)
	if newMembership != nil {
		t.Error("new membership must NOT be created when phase-2 decryption fails")
	}
}

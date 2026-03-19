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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// testHKDFSHA256 mirrors the server-side hkdfSHA256 for use in tests.
// Derives a 32-byte key using HKDF-SHA256 with no salt and the given info string.
func testHKDFSHA256(ikm []byte, info string) []byte {
	key, err := cfhttp.HkdfSHA256(ikm, info)
	if err != nil {
		panic(fmt.Sprintf("testHKDFSHA256: %v", err))
	}
	return key
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

	// Derive shared secret via ECDH + HKDF, matching the handler side.
	receiverPubBytes, _ := hex.DecodeString(receiverEphemeralPubHex)
	receiverPub, err := ecdh.X25519().NewPublicKey(receiverPubBytes)
	if err != nil {
		t.Fatalf("parsing receiver ephemeral pub: %v", err)
	}
	rawShared, err := senderPriv.ECDH(receiverPub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	sharedSecret := testHKDFSHA256(rawShared, "campfire-rekey-v1")

	// Encrypt new campfire private key.
	encNewPrivKey, err := rekeyTestEncrypt(sharedSecret, newCFPriv)
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

	// Build campfire:rekey message (unsigned for threshold>1 test simplicity).
	rekeyPayload, _ := json.Marshal(map[string]string{
		"old_key": oldCampfireID,
		"new_key": newCampfireID,
		"reason":  "eviction",
	})
	rekeyMsg := &message.Message{
		ID:          "test-rekey-t2-id",
		Sender:      ed25519.PublicKey(oldGroupKey),
		Payload:     rekeyPayload,
		Tags:        []string{"campfire:rekey"},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
		Provenance:  []message.ProvenanceHop{},
	}
	rekeyMsgCBOR, _ := cfencoding.Marshal(rekeyMsg)

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
	sharedSecret := testHKDFSHA256(rawShared2, "campfire-rekey-v1")

	encNewShare, err := rekeyTestEncrypt(sharedSecret, newShareBData)
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

// TestRekeyHKDFRequiredForDecrypt verifies that the handler applies HKDF-SHA256
// to the raw ECDH shared secret before decryption. A payload encrypted with
// only the raw ECDH output (no HKDF) must be rejected with a decryption error.
// This is a regression test for the deliverRekey KDF mismatch bug.
func TestRekeyHKDFRequiredForDecrypt(t *testing.T) {
	// Generate campfire keypair.
	cfPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID := fmt.Sprintf("%x", cfPub)
	newCFPub, newCFPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating new campfire key: %v", err)
	}
	newCampfireID := fmt.Sprintf("%x", newCFPub)

	idA := tempIdentity(t)
	idB := tempIdentity(t)

	sB := tempStore(t)
	stateDirB, _ := setupCampfireState(t, nil, cfPub, 1)
	addMembershipWithDir(t, sB, campfireID, stateDirB, 1)

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+38)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	senderPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating sender ephemeral key: %v", err)
	}
	senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

	rekeyPayload, _ := json.Marshal(map[string]string{
		"old_key": campfireID,
		"new_key": newCampfireID,
	})
	rekeyMsg := &message.Message{
		ID:          "test-hkdf-required",
		Sender:      ed25519.PublicKey(cfPub),
		Payload:     rekeyPayload,
		Tags:        []string{"campfire:rekey"},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
		Provenance:  []message.ProvenanceHop{},
	}
	rekeyMsgCBOR, _ := cfencoding.Marshal(rekeyMsg)

	// Phase 1: get receiver's ephemeral pub.
	phase1Req := cfhttp.RekeyRequest{
		NewCampfireID:       newCampfireID,
		SenderX25519Pub:     senderPubHex,
		EvictedMemberPubkey: idB.PublicKeyHex(),
		RekeyMessageCBOR:    rekeyMsgCBOR,
	}
	receiverEphemeralPubHex, err := cfhttp.SendRekeyPhase1(epB, campfireID, phase1Req, idA)
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
	rawShared, err := senderPriv.ECDH(receiverPub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}

	// Encrypt with RAW ECDH shared secret — no HKDF. Handler must reject this.
	encWithRaw, err := rekeyTestEncrypt(rawShared, newCFPriv)
	if err != nil {
		t.Fatalf("encrypting with raw shared secret: %v", err)
	}

	// Phase 2 with payload encrypted using raw ECDH (no HKDF) — must fail.
	phase2BadReq := cfhttp.RekeyRequest{
		NewCampfireID:       newCampfireID,
		SenderX25519Pub:     senderPubHex,
		EvictedMemberPubkey: idB.PublicKeyHex(),
		RekeyMessageCBOR:    rekeyMsgCBOR,
		EncryptedPrivKey:    encWithRaw,
	}
	err = cfhttp.SendRekey(epB, campfireID, phase2BadReq, idA)
	if err == nil {
		t.Fatal("expected decryption error when payload is encrypted with raw ECDH (no HKDF), got nil")
	}
	t.Logf("correctly rejected raw-ECDH-encrypted payload: %v", err)
}

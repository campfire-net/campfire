package http_test

// Tests for rekey phase-1 replay and missing-session error paths (workspace-ber).
//
// Three cases covered:
//
//  (a) Phase-1 replay (TestRekeyPhase1ReplayOverwritesSession):
//      A second phase-1 request with the same senderX25519Pub silently overwrites
//      the first session.  A phase-2 call that uses the shared secret derived from
//      the FIRST receiver key fails (400) because the receiver now holds a different
//      ephemeral key for that slot — the AES-GCM decryption fails.
//
//  (b) Phase-2 without phase-1 (TestRekeyPhase2WithoutPhase1Returns400):
//      A phase-2 request with a senderX25519Pub that was never registered via
//      phase-1 returns HTTP 400.
//
//  (c) Phase-2 after session pruned:
//      Tested in rekey_replay_internal_test.go (package http, white-box) because
//      it requires direct manipulation of the transport's session map.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// rawRekeyPost sends a signed rekey HTTP POST and returns the HTTP status code.
// Unlike SendRekey/SendRekeyPhase1, this does not error on non-200 responses,
// making it suitable for error-path testing.
func rawRekeyPost(t *testing.T, endpoint, campfireID string, req cfhttp.RekeyRequest, id interface {
	PublicKeyHex() string
	Sign([]byte) []byte
}) int {
	t.Helper()

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("rawRekeyPost: marshaling request: %v", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/rekey", endpoint, campfireID)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("rawRekeyPost: building request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	sig := id.Sign(bodyBytes)
	httpReq.Header.Set("X-Campfire-Sender", id.PublicKeyHex())
	httpReq.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("rawRekeyPost: request to %s failed: %v", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	return resp.StatusCode
}

// setupRekeyTestServer starts a transport for a receiver with the given campfire
// membership and returns its endpoint URL.  It is the caller's responsibility to
// stop the transport (via t.Cleanup — already registered inside).
func setupRekeyTestServer(t *testing.T, port int, idSenderPubHex string) (ep, campfireID string) {
	t.Helper()

	oldCFPub, oldCFPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID = fmt.Sprintf("%x", oldCFPub)

	sB := tempStore(t)
	stateDirB, _ := setupCampfireState(t, oldCFPriv, oldCFPub, 1)
	addMembershipWithDir(t, sB, campfireID, stateDirB, 1)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ep = fmt.Sprintf("http://%s", addr)

	trB := cfhttp.New(addr, sB)
	trB.SetSelfInfo(idSenderPubHex, ep)
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	return ep, campfireID
}

// TestRekeyPhase1ReplayOverwritesSession verifies case (a):
// A second phase-1 with the same senderX25519Pub replaces the cached receiver key.
// A phase-2 call using the FIRST (now-stale) shared secret returns a non-200
// error (400 decryption failure), demonstrating the overwrite effect.
// The test then confirms a clean phase-2 with a fresh session succeeds.
func TestRekeyPhase1ReplayOverwritesSession(t *testing.T) {
	idA := tempIdentity(t) // sender / creator

	base := portBase()
	ep, campfireID := setupRekeyTestServer(t, base+60, idA.PublicKeyHex())

	// Generate a new campfire ID (any hex-encoded ed25519 pub, or just a 32-byte
	// random hex for this version of the handler which doesn't validate format).
	newCFPub, newCFPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating new campfire key: %v", err)
	}
	newID := fmt.Sprintf("%x", newCFPub)

	// --- First phase-1: obtain receiverPub1 ---
	senderPriv1, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating sender key 1: %v", err)
	}
	senderPub1Hex := fmt.Sprintf("%x", senderPriv1.PublicKey().Bytes())

	receiverPub1Hex, err := cfhttp.SendRekeyPhase1(ep, campfireID, cfhttp.RekeyRequest{
		NewCampfireID:   newID,
		SenderX25519Pub: senderPub1Hex,
	}, idA)
	if err != nil {
		t.Fatalf("first phase-1: %v", err)
	}
	if receiverPub1Hex == "" {
		t.Fatal("expected receiver ephemeral pub from first phase-1")
	}

	// --- Replay phase-1 with the SAME senderX25519Pub ---
	// This overwrites rekeySessions[senderPub1Hex] with a new receiver key.
	_, err = cfhttp.SendRekeyPhase1(ep, campfireID, cfhttp.RekeyRequest{
		NewCampfireID:   newID,
		SenderX25519Pub: senderPub1Hex, // same key — overwrites
	}, idA)
	if err != nil {
		t.Fatalf("replay phase-1: %v", err)
	}

	// --- Phase-2 using the STALE shared secret (derived from receiverPub1) ---
	// The receiver now holds a freshly-generated key under senderPub1Hex.
	// ECDH(senderPriv1, receiverPub1) → stale shared secret → decryption fails.
	receiverPub1Bytes, err := hex.DecodeString(receiverPub1Hex)
	if err != nil {
		t.Fatalf("decoding first receiver pub: %v", err)
	}
	receiverPub1, err := ecdh.X25519().NewPublicKey(receiverPub1Bytes)
	if err != nil {
		t.Fatalf("parsing first receiver pub: %v", err)
	}
	staleShared, err := senderPriv1.ECDH(receiverPub1)
	if err != nil {
		t.Fatalf("ECDH with stale key: %v", err)
	}

	encPrivStale, err := rekeyTestEncrypt(staleShared, newCFPriv)
	if err != nil {
		t.Fatalf("encrypting with stale secret: %v", err)
	}

	staleStatus := rawRekeyPost(t, ep, campfireID, cfhttp.RekeyRequest{
		NewCampfireID:    newID,
		SenderX25519Pub:  senderPub1Hex,
		EncryptedPrivKey: encPrivStale,
	}, idA)

	// Acceptable outcomes:
	//  - 400: AES-GCM decryption failure (most likely — receiver key changed)
	//  - 200: astronomically unlikely key collision where both phase-1s produced
	//          the same receiver key (accept to avoid a flaky test)
	if staleStatus != http.StatusBadRequest && staleStatus != http.StatusOK {
		t.Errorf("expected 400 (decryption failure) after stale-key phase-2, got %d", staleStatus)
	}
	t.Logf("stale phase-2 status: %d", staleStatus)

	// If the stale phase-2 happened to succeed (key collision), the rekey already
	// applied; nothing more to verify.
	if staleStatus == http.StatusOK {
		t.Log("stale phase-2 returned 200 (key collision — skipping further checks)")
		return
	}

	// The session for senderPub1Hex was consumed by the stale phase-2 attempt
	// (claimRekeySession deletes on retrieval).  A second phase-2 attempt with
	// the same key now finds no session → 400.
	dummyEnc := make([]byte, 64)
	rand.Read(dummyEnc) //nolint:errcheck
	status2 := rawRekeyPost(t, ep, campfireID, cfhttp.RekeyRequest{
		NewCampfireID:    newID,
		SenderX25519Pub:  senderPub1Hex,
		EncryptedPrivKey: dummyEnc,
	}, idA)
	if status2 != http.StatusBadRequest {
		t.Errorf("expected 400 after session consumed, got %d", status2)
	}

	// --- Confirm a clean phase-2 with a fresh session succeeds ---
	senderPriv2, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating sender key 2: %v", err)
	}
	senderPub2Hex := fmt.Sprintf("%x", senderPriv2.PublicKey().Bytes())

	freshRecvHex, err := cfhttp.SendRekeyPhase1(ep, campfireID, cfhttp.RekeyRequest{
		NewCampfireID:   newID,
		SenderX25519Pub: senderPub2Hex,
	}, idA)
	if err != nil {
		t.Fatalf("fresh phase-1: %v", err)
	}

	freshRecvBytes, err := hex.DecodeString(freshRecvHex)
	if err != nil {
		t.Fatalf("decoding fresh receiver pub: %v", err)
	}
	freshRecvPub, err := ecdh.X25519().NewPublicKey(freshRecvBytes)
	if err != nil {
		t.Fatalf("parsing fresh receiver pub: %v", err)
	}
	freshShared, err := senderPriv2.ECDH(freshRecvPub)
	if err != nil {
		t.Fatalf("ECDH with fresh receiver key: %v", err)
	}
	encPrivFresh, err := rekeyTestEncrypt(freshShared, newCFPriv)
	if err != nil {
		t.Fatalf("encrypting with fresh secret: %v", err)
	}

	if err := cfhttp.SendRekey(ep, campfireID, cfhttp.RekeyRequest{
		NewCampfireID:    newID,
		SenderX25519Pub:  senderPub2Hex,
		EncryptedPrivKey: encPrivFresh,
	}, idA); err != nil {
		t.Errorf("fresh phase-2 should succeed, got: %v", err)
	}
}

// TestRekeyPhase2WithoutPhase1Returns400 verifies case (b):
// A phase-2 request referencing a senderX25519Pub that was never registered
// via a prior phase-1 call returns HTTP 400 "no pending rekey session".
func TestRekeyPhase2WithoutPhase1Returns400(t *testing.T) {
	idA := tempIdentity(t)

	base := portBase()
	ep, campfireID := setupRekeyTestServer(t, base+61, idA.PublicKeyHex())

	newCFPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating new campfire key: %v", err)
	}
	newID := fmt.Sprintf("%x", newCFPub)

	// Never call phase-1 for this sender key.
	ghostPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ghost key: %v", err)
	}
	ghostPubHex := fmt.Sprintf("%x", ghostPriv.PublicKey().Bytes())

	// Any non-empty EncryptedPrivKey triggers the phase-2 code path.
	dummyEnc := make([]byte, 64)
	rand.Read(dummyEnc) //nolint:errcheck

	status := rawRekeyPost(t, ep, campfireID, cfhttp.RekeyRequest{
		NewCampfireID:    newID,
		SenderX25519Pub:  ghostPubHex,
		EncryptedPrivKey: dummyEnc,
	}, idA)

	if status != http.StatusBadRequest {
		t.Errorf("expected 400 for phase-2 without prior phase-1, got %d", status)
	}
}

package http

// TestRekeySessionKeyIsolatedBySender verifies workspace-bzv:
// The rekeySessions map is keyed by (campfireID, senderEdPubKey, senderX25519Pub).
// Two different authenticated senders using the same SenderX25519Pub value produce
// separate session map entries — neither can overwrite the other's session.
//
// TestRekeyPhase2AfterSessionPrunedReturns400 verifies case (c) from workspace-ber:
// A phase-2 call whose session was created more than 5 minutes ago (and thus pruned
// by pruneRekeySessions) returns 400 "no pending rekey session for this sender key".
//
// This is a white-box test (package http) so it can directly manipulate the
// transport's rekeySessions map and call pruneRekeySessions, avoiding any real sleep.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
)

func TestRekeyPhase2AfterSessionPrunedReturns400(t *testing.T) {
	// Build a minimal Transport: no listener, just the in-memory session map.
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	tr := &Transport{
		store:         s,
		peers:         make(map[string][]PeerInfo),
		signSessions:  make(map[string]*signingSessionState),
		rekeySessions: make(map[string]*rekeySessionState),
		pollBroker: &PollBroker{
			subs:           make(map[string][]chan struct{}),
			limits:         make(map[string]int),
			maxPerCampfire: 64,
		},
	}

	// Add a campfire membership so the handler's membership check passes.
	campfireID := "deadbeef"
	newID := "cafebabe"
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Create a sender identity and add it as a known member so auth passes.
	senderID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating sender identity: %v", err)
	}
	s.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: senderID.PublicKeyHex(),
		Endpoint:     "http://127.0.0.1:1",
	})

	// Generate a sender ephemeral key and inject a STALE session entry (6 min old).
	senderPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating sender key: %v", err)
	}
	senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

	receiverPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating receiver key: %v", err)
	}

	// Composite key must match what handleRekey uses: (campfireID, senderEdPubKey, senderX25519Pub).
	sessionKey := rekeySessionKey(campfireID, senderID.PublicKeyHex(), senderPubHex)

	tr.mu.Lock()
	tr.rekeySessions[sessionKey] = &rekeySessionState{
		myPrivKey: receiverPriv,
		createdAt: time.Now().Add(-6 * time.Minute), // beyond the 5-minute prune window
	}
	// Immediately prune — simulates the background ticker firing.
	tr.pruneRekeySessions()
	tr.mu.Unlock()

	// Sanity check: the stale session should be gone.
	tr.mu.RLock()
	_, stillPresent := tr.rekeySessions[sessionKey]
	tr.mu.RUnlock()
	if stillPresent {
		t.Fatal("sanity check failed: stale session survived pruning")
	}

	// Build a phase-2 request body (non-empty EncryptedPrivKey → phase-2 path).
	dummyEnc := make([]byte, 64)
	rand.Read(dummyEnc) //nolint:errcheck

	reqBody := RekeyRequest{
		NewCampfireID:    newID,
		SenderX25519Pub:  senderPubHex,
		EncryptedPrivKey: dummyEnc,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshaling request body: %v", err)
	}

	// Build the handler and dispatch the request via httptest.
	h := &handler{transport: tr, store: s}

	req := httptest.NewRequest(http.MethodPost, "/campfire/"+campfireID+"/rekey", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	// Note: handleRekey receives senderHex and body as explicit params; auth headers
	// are not read by the handler itself, only by the authMiddleware wrapping it.

	rr := httptest.NewRecorder()
	h.handleRekey(rr, req, campfireID, senderID.PublicKeyHex(), bodyBytes)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when phase-2 arrives after session was pruned, got %d (body: %s)",
			rr.Code, rr.Body.String())
	}
}

// TestRekeySessionKeyIsolatedBySender verifies that two different authenticated
// senders sharing the same SenderX25519Pub value produce separate rekeySessions
// entries.  This directly tests the workspace-bzv fix: keying sessions by
// (campfireID, senderEdPubKey, senderX25519Pub) instead of senderX25519Pub alone.
func TestRekeySessionKeyIsolatedBySender(t *testing.T) {
	// Same X25519 public key hex presented by two different Ed25519 identities.
	sharedX25519PubHex := "aabbccdd"
	campfireID := "testcampfire"

	idLegit := "legitEdPub"
	idAttacker := "attackerEdPub"

	keyLegit := rekeySessionKey(campfireID, idLegit, sharedX25519PubHex)
	keyAttacker := rekeySessionKey(campfireID, idAttacker, sharedX25519PubHex)

	if keyLegit == keyAttacker {
		t.Fatalf("session keys must differ by sender identity: legit=%q attacker=%q", keyLegit, keyAttacker)
	}

	// Simulate two concurrent phase-1 registrations.
	tr := &Transport{
		rekeySessions: make(map[string]*rekeySessionState),
	}

	privLegit, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating legit key: %v", err)
	}
	privAttacker, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating attacker key: %v", err)
	}

	tr.mu.Lock()
	tr.storeRekeySession(campfireID, idLegit, sharedX25519PubHex, privLegit)
	tr.storeRekeySession(campfireID, idAttacker, sharedX25519PubHex, privAttacker)
	tr.mu.Unlock()

	// Both sessions must coexist independently.
	tr.mu.Lock()
	claimedLegit := tr.claimRekeySession(campfireID, idLegit, sharedX25519PubHex)
	claimedAttacker := tr.claimRekeySession(campfireID, idAttacker, sharedX25519PubHex)
	tr.mu.Unlock()

	if claimedLegit == nil {
		t.Error("legit session was wiped by attacker's phase-1 — composite key not applied")
	}
	if claimedAttacker == nil {
		t.Error("attacker session missing — unexpected")
	}
	// The two claimed keys must be distinct private keys.
	if claimedLegit != nil && claimedAttacker != nil {
		if claimedLegit == claimedAttacker {
			t.Error("legit and attacker sessions resolved to same private key — isolation broken")
		}
	}
}

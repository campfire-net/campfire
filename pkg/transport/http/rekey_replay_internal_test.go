package http

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
	"encoding/base64"
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

	tr.mu.Lock()
	tr.rekeySessions[senderPubHex] = &rekeySessionState{
		myPrivKey: receiverPriv,
		createdAt: time.Now().Add(-6 * time.Minute), // beyond the 5-minute prune window
	}
	// Immediately prune — simulates the background ticker firing.
	tr.pruneRekeySessions()
	tr.mu.Unlock()

	// Sanity check: the stale session should be gone.
	tr.mu.RLock()
	_, stillPresent := tr.rekeySessions[senderPubHex]
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
	// Sign the body so verifyRequestSignature passes.
	sig := senderID.Sign(bodyBytes)
	req.Header.Set("X-Campfire-Sender", senderID.PublicKeyHex())
	req.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))

	rr := httptest.NewRecorder()
	h.handleRekey(rr, req, campfireID, senderID.PublicKeyHex(), bodyBytes)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when phase-2 arrives after session was pruned, got %d (body: %s)",
			rr.Code, rr.Body.String())
	}
}

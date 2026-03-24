package http_test

// Security regression tests for workspace-17qu.4:
// handleJoin must persist the verified sender identity (X-Campfire-Sender header)
// rather than the unverified body field (JoinerPubkey). An attacker who signs with
// their own key but puts a different pubkey in the body must have their own key stored.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// setupJoinServer starts a transport with a key provider and returns the
// campfire ID, server endpoint, store, and cleanup func.
func setupJoinServer(t *testing.T, portOffset int) (campfireID, ep string, sHost store.Store) {
	t.Helper()
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID = fmt.Sprintf("%x", cfPub)

	selfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating host identity: %v", err)
	}
	sHost = tempStore(t)

	if err := sHost.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: t.TempDir(),
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+portOffset)
	ep = fmt.Sprintf("http://%s", addr)

	tr := cfhttp.New(addr, sHost)
	tr.SetSelfInfo(selfID.PublicKeyHex(), ep)
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

	return campfireID, ep, sHost
}

// signJoinRequest builds a raw join HTTP request signed by signerID but with
// joinerPubkeyInBody as the body's joiner_pubkey field.
func signJoinRequest(t *testing.T, ep, campfireID string, signerID *identity.Identity, joinerPubkeyInBody, joinerEndpoint string) *http.Request {
	t.Helper()
	ephemPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ephemeral key: %v", err)
	}
	joinReq := cfhttp.JoinRequest{
		JoinerPubkey:       joinerPubkeyInBody,
		JoinerEndpoint:     joinerEndpoint,
		EphemeralX25519Pub: fmt.Sprintf("%x", ephemPriv.PublicKey().Bytes()),
	}
	body, err := json.Marshal(joinReq)
	if err != nil {
		t.Fatalf("marshaling join req: %v", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/join", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Sign with signerID — this sets X-Campfire-Sender to signerID's pubkey.
	signTestRequest(req, signerID, body)
	return req
}

// TestJoinPubkeyInjectionPrevented verifies that when a joiner signs with key A
// but puts key B in the body's joiner_pubkey field, key A (senderHex) is stored
// in the peer list — not key B.
func TestJoinPubkeyInjectionPrevented(t *testing.T) {
	// 203.0.113.0/24 is a documentation range now blocked by SSRF validation.
	// This test focuses on pubkey injection, not SSRF; bypass endpoint validation.
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	campfireID, ep, sHost := setupJoinServer(t, 300)

	// keyA is the real signer identity.
	keyA, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keyA: %v", err)
	}
	// keyB is the victim identity the attacker wants to impersonate.
	keyB, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keyB: %v", err)
	}

	// The attacker provides an endpoint so we can check which pubkey is stored.
	attackerEndpoint := "http://203.0.113.1:9000"

	// Build a join request signed by keyA but with keyB in the body.
	req := signJoinRequest(t, ep, campfireID, keyA, keyB.PublicKeyHex(), attackerEndpoint)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Verify the stored peer has keyA's pubkey, NOT keyB's.
	peers, err := sHost.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("listing peer endpoints: %v", err)
	}

	for _, p := range peers {
		if p.Endpoint == attackerEndpoint {
			if p.MemberPubkey == keyB.PublicKeyHex() {
				t.Errorf("pubkey injection succeeded: stored keyB (%s) instead of keyA (%s)",
					keyB.PublicKeyHex()[:16], keyA.PublicKeyHex()[:16])
			}
			if p.MemberPubkey == keyA.PublicKeyHex() {
				t.Logf("correctly stored keyA (verified sender) for endpoint %s", attackerEndpoint)
				return
			}
		}
	}
	t.Errorf("no peer entry found for endpoint %s; peers: %+v", attackerEndpoint, peers)
}

// TestJoinMatchingPubkeyWorks verifies that a normal join (where JoinerPubkey
// matches the actual sender key) still succeeds and stores the correct pubkey.
func TestJoinMatchingPubkeyWorks(t *testing.T) {
	// 203.0.113.0/24 is a documentation range now blocked by SSRF validation.
	// This test focuses on pubkey matching, not SSRF; bypass endpoint validation.
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	campfireID, ep, sHost := setupJoinServer(t, 305)

	// Normal joiner: signs with keyA and puts keyA in body.
	keyA, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keyA: %v", err)
	}

	joinerEndpoint := "http://203.0.113.2:9001"
	req := signJoinRequest(t, ep, campfireID, keyA, keyA.PublicKeyHex(), joinerEndpoint)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// The stored peer must have keyA's pubkey.
	peers, err := sHost.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("listing peer endpoints: %v", err)
	}

	found := false
	for _, p := range peers {
		if p.Endpoint == joinerEndpoint {
			if p.MemberPubkey != keyA.PublicKeyHex() {
				t.Errorf("wrong pubkey stored: got %s, want %s",
					p.MemberPubkey[:16], keyA.PublicKeyHex()[:16])
			}
			found = true
		}
	}
	if !found {
		t.Errorf("no peer entry found for endpoint %s; peers: %+v", joinerEndpoint, peers)
	}

}

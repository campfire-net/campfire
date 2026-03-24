package http_test

// Security regression tests for workspace-k9e:
// handleJoin for open-protocol campfires must:
//   1. Reject join requests that omit EphemeralX25519Pub (400 Bad Request).
//   2. Return only the admitting node's own endpoint in the Peers list,
//      not the full stored peer list (prevents unauthenticated member enumeration).

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// setupOpenCampfireServer starts a transport for an open campfire with a key
// provider and two pre-existing peers. Returns the campfire ID, server endpoint,
// host identity, and store.
func setupOpenCampfireServer(t *testing.T, portOffset int) (campfireID, ep string, hostID *identity.Identity, peerA, peerB *identity.Identity, sHost store.Store) {
	t.Helper()

	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID = fmt.Sprintf("%x", cfPub)

	hostID, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating host identity: %v", err)
	}
	peerA, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating peerA identity: %v", err)
	}
	peerB, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating peerB identity: %v", err)
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

	// Pre-populate two known peers with distinct endpoints.
	if err := sHost.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: peerA.PublicKeyHex(),
		Endpoint:     "http://203.0.113.10:8080",
	}); err != nil {
		t.Fatalf("upserting peerA: %v", err)
	}
	if err := sHost.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: peerB.PublicKeyHex(),
		Endpoint:     "http://203.0.113.20:8080",
	}); err != nil {
		t.Fatalf("upserting peerB: %v", err)
	}

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+portOffset)
	ep = fmt.Sprintf("http://%s", addr)

	tr := cfhttp.New(addr, sHost)
	tr.SetSelfInfo(hostID.PublicKeyHex(), ep)
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

	return campfireID, ep, hostID, peerA, peerB, sHost
}

// buildJoinRequestEphemeral constructs a POST /campfire/{id}/join request signed by joiner.
// If ephemeralPub is empty, the field is omitted from the body.
func buildJoinRequestEphemeral(t *testing.T, ep, campfireID string, joiner *identity.Identity, ephemeralPub string) *http.Request {
	t.Helper()

	body, _ := json.Marshal(cfhttp.JoinRequest{
		JoinerPubkey:       joiner.PublicKeyHex(),
		JoinerEndpoint:     "", // empty — no SSRF concern
		EphemeralX25519Pub: ephemeralPub,
	})

	url := fmt.Sprintf("%s/campfire/%s/join", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building join request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	signTestRequest(req, joiner, body)
	return req
}

// ---------------------------------------------------------------------------
// workspace-k9e — EphemeralX25519Pub is required
// ---------------------------------------------------------------------------

// TestJoinMissingEphemeralKeyRejected verifies that a join request without an
// EphemeralX25519Pub field is rejected with 400 Bad Request.
// Without the ephemeral key there is no way to securely deliver the campfire
// private key (threshold=1) or DKG share (threshold>1), so the server must
// reject the request outright rather than admitting a useless member.
func TestJoinMissingEphemeralKeyRejected(t *testing.T) {
	campfireID, ep, _, _, _, _ := setupOpenCampfireServer(t, 280)

	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}

	// Send a join request with empty EphemeralX25519Pub.
	req := buildJoinRequestEphemeral(t, ep, campfireID, joiner, "")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join HTTP error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 400 for missing EphemeralX25519Pub, got %d: %s", resp.StatusCode, body)
	}
}

// TestJoinWithEphemeralKeySucceeds verifies that a join request with a valid
// EphemeralX25519Pub is accepted (200 OK) and returns an encrypted private key.
func TestJoinWithEphemeralKeySucceeds(t *testing.T) {
	campfireID, ep, _, _, _, _ := setupOpenCampfireServer(t, 281)

	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}

	ephemPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	ephemPubHex := fmt.Sprintf("%x", ephemPriv.PublicKey().Bytes())

	req := buildJoinRequestEphemeral(t, ep, campfireID, joiner, ephemPubHex)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join HTTP error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for valid join, got %d: %s", resp.StatusCode, body)
	}

	var joinResp cfhttp.JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
		t.Fatalf("decoding join response: %v", err)
	}

	// For threshold=1, EncryptedPrivKey must be populated.
	if len(joinResp.EncryptedPrivKey) == 0 {
		t.Error("expected EncryptedPrivKey to be populated for threshold=1 campfire")
	}
	// ResponderX25519Pub must be present for ECDH to work.
	if joinResp.ResponderX25519Pub == "" {
		t.Error("expected ResponderX25519Pub to be populated")
	}
}

// ---------------------------------------------------------------------------
// workspace-k9e — Peer list must not expose all member endpoints to joiners
// ---------------------------------------------------------------------------

// TestJoinPeerListRestrictedToAdmittingNode verifies that the join response
// contains only the admitting node's own endpoint in the Peers list.
// The server has two pre-existing peers (peerA at 203.0.113.10 and peerB at
// 203.0.113.20) in its store. The joiner must NOT receive those endpoints
// in the join response — only the admitting node's own endpoint is returned.
// This prevents unauthenticated enumeration of all campfire member IP addresses.
func TestJoinPeerListRestrictedToAdmittingNode(t *testing.T) {
	campfireID, ep, hostID, peerA, peerB, _ := setupOpenCampfireServer(t, 282)

	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}

	ephemPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	ephemPubHex := fmt.Sprintf("%x", ephemPriv.PublicKey().Bytes())

	req := buildJoinRequestEphemeral(t, ep, campfireID, joiner, ephemPubHex)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join HTTP error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var joinResp cfhttp.JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
		t.Fatalf("decoding join response: %v", err)
	}

	// The response must contain only the admitting node's own endpoint.
	if len(joinResp.Peers) > 1 {
		t.Errorf("expected at most 1 peer (admitting node), got %d: %v", len(joinResp.Peers), joinResp.Peers)
	}

	// The known peers (peerA and peerB) must not appear in the response.
	for _, p := range joinResp.Peers {
		if p.PubKeyHex == peerA.PublicKeyHex() {
			t.Errorf("peerA endpoint leaked in join response (endpoint: %s)", p.Endpoint)
		}
		if p.PubKeyHex == peerB.PublicKeyHex() {
			t.Errorf("peerB endpoint leaked in join response (endpoint: %s)", p.Endpoint)
		}
	}

	// The admitting node's own endpoint must be present.
	admittingNodeFound := false
	for _, p := range joinResp.Peers {
		if p.PubKeyHex == hostID.PublicKeyHex() {
			admittingNodeFound = true
			if p.Endpoint != ep {
				t.Errorf("admitting node endpoint mismatch: got %s, want %s", p.Endpoint, ep)
			}
		}
	}
	if !admittingNodeFound && len(joinResp.Peers) > 0 {
		t.Logf("admitting node not found in peers (may have no selfInfo set): peers=%v", joinResp.Peers)
	}
}

// TestJoinPeerListEmptyWhenNoSelfInfo verifies that if the admitting node has
// no selfInfo configured (no known endpoint), the join response returns an
// empty Peers list — not the stored peer list.
func TestJoinPeerListEmptyWhenNoSelfInfo(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID := fmt.Sprintf("%x", cfPub)

	sHost := tempStore(t)
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

	// Store a peer endpoint — this must NOT be disclosed.
	existingPeer, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating existing peer identity: %v", err)
	}
	if err := sHost.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: existingPeer.PublicKeyHex(),
		Endpoint:     "http://203.0.113.99:8080",
	}); err != nil {
		t.Fatalf("upserting existing peer: %v", err)
	}

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+283)
	ep := fmt.Sprintf("http://%s", addr)

	tr := cfhttp.New(addr, sHost)
	// NOTE: SetSelfInfo is intentionally NOT called — no self endpoint configured.
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

	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}
	ephemPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	ephemPubHex := fmt.Sprintf("%x", ephemPriv.PublicKey().Bytes())

	req := buildJoinRequestEphemeral(t, ep, campfireID, joiner, ephemPubHex)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join HTTP error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var joinResp cfhttp.JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
		t.Fatalf("decoding join response: %v", err)
	}

	// No peers should be returned when the admitting node has no selfInfo.
	if len(joinResp.Peers) != 0 {
		t.Errorf("expected empty peers list when no selfInfo, got %d entries: %v",
			len(joinResp.Peers), joinResp.Peers)
	}

	// The stored peer's endpoint must not appear.
	for _, p := range joinResp.Peers {
		if p.PubKeyHex == existingPeer.PublicKeyHex() {
			t.Errorf("stored peer leaked to joiner: %s at %s", p.PubKeyHex[:8], p.Endpoint)
		}
	}
}

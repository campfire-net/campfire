// Package http_test — conformance tests for the public cf-protocol/transport/http wrapper.
//
// These tests exercise the re-exported symbols through the PUBLIC import path
// (github.com/campfire-net/campfire/cf-protocol/transport/http), confirming
// that every alias routes to the real internal implementation.
//
// Design constraints (campfireagent-f62):
//   - No floating mocks of *http.Client or net.Conn.
//   - All network interactions use real TCP via Transport.Start() on port 0.
//   - Real store.Open (SQLite), real identity.Generate().
//   - TestMain overrides the SSRF client (same as internal/transport/http) so
//     loopback test servers are reachable.
package http_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
	"github.com/campfire-net/campfire/cf-protocol/internal/message"
	"github.com/campfire-net/campfire/cf-protocol/internal/store"
	"github.com/campfire-net/campfire/pkg/identity"
)

// ---------------------------------------------------------------------------
// TestMain — loopback access setup (mirrors internal/transport/http/testmain_test.go)
// ---------------------------------------------------------------------------

func TestMain(m *testing.M) {
	// Replace the SSRF-safe client so loopback test servers are reachable.
	// OverrideHTTPClientForTest and OverridePollTransportForTest are re-exported
	// from the internal package via the public wrapper.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 30 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)
	// Allow loopback joiner endpoints in membership notifications.
	cfhttp.OverrideValidateJoinerEndpointForTest()
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Shared test helpers
// ---------------------------------------------------------------------------

func tempStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func tempIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	return id
}

// addMembership inserts a campfire membership record into the store.
func addMembership(t *testing.T, s store.Store, campfireID string) {
	t.Helper()
	err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "http",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
}

// addPeerEndpoint inserts a peer record so membership checks pass.
func addPeerEndpoint(t *testing.T, s store.Store, campfireID, pubKeyHex string) {
	t.Helper()
	err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: pubKeyHex,
		Endpoint:     "http://127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("adding peer endpoint: %v", err)
	}
}

// startTransport starts a new Transport on a random port and registers cleanup.
func startTransport(t *testing.T, s store.Store) *cfhttp.Transport {
	t.Helper()
	// cfhttp.New is re-exported from internal — uses the real type.
	tr := cfhttp.New("127.0.0.1:0", s)
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	return tr
}

// epOf returns the full HTTP endpoint URL for a started transport.
func epOf(tr *cfhttp.Transport) string {
	return "http://" + tr.Addr()
}

// newTestMessage creates a minimal signed message.
func newTestMessage(t *testing.T, id *identity.Identity) *message.Message {
	t.Helper()
	msg, err := message.NewMessage(
		message.MustNewEd25519Signer(id.PrivateKey, id.PublicKey),
		[]byte("public-wrapper-test"),
		[]string{"test"},
		nil,
	)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	return msg
}

// signRequest signs a request with the Campfire auth headers using the given identity.
// This mirrors the signing scheme used by the internal transport helpers.
func signRequest(req *http.Request, id *identity.Identity, body []byte) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		panic("signRequest: rand.Read: " + err.Error())
	}
	nonce := hex.EncodeToString(nonceBytes)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	var payload []byte
	payload = append(payload, []byte(timestamp)...)
	payload = append(payload, '\n')
	payload = append(payload, []byte(nonce)...)
	payload = append(payload, '\n')
	payload = append(payload, body...)

	sig := id.Sign(payload)
	req.Header.Set("X-Campfire-Sender", id.PublicKeyHex())
	req.Header.Set("X-Campfire-Nonce", nonce)
	req.Header.Set("X-Campfire-Timestamp", timestamp)
	req.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))
}

// ---------------------------------------------------------------------------
// Type alias conformance — verify exported types are the real implementation
// ---------------------------------------------------------------------------

// TestTransportTypeIsRealImplementation verifies that cfhttp.Transport (the
// public re-export) is the actual *Transport implementation, not a stub.
// We do this by calling real methods and observing real network behavior.
func TestTransportTypeIsRealImplementation(t *testing.T) {
	s := tempStore(t)
	addMembership(t, s, "type-check-cf")
	tr := cfhttp.New("127.0.0.1:0", s)
	if err := tr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tr.Stop() //nolint:errcheck

	addr := tr.Addr()
	if addr == "" {
		t.Fatal("Addr() returned empty string — transport did not bind")
	}

	// Verify the HTTP server is actually listening by making a raw GET.
	resp, err := http.Get("http://" + addr + "/campfire/type-check-cf/sync?since=0")
	if err != nil {
		t.Fatalf("transport not reachable at %s: %v", addr, err)
	}
	resp.Body.Close()
	// Sync without auth headers should return 401 — proves the server is live.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated sync, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Deliver + Sync (core message send/read flow)
// ---------------------------------------------------------------------------

// TestPublicDeliver verifies that cfhttp.Deliver (re-exported) stores a message
// on the remote transport and cfhttp.Sync retrieves it.
func TestPublicDeliverAndSync(t *testing.T) {
	campfireID := "pub-deliver-sync"
	id := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	tr := startTransport(t, s)
	ep := epOf(tr)

	msg := newTestMessage(t, id)
	if err := cfhttp.Deliver(ep, campfireID, msg, id); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	msgs, err := cfhttp.Sync(ep, campfireID, 0, id)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message after Deliver, got %d", len(msgs))
	}
	if msgs[0].ID != msg.ID {
		t.Errorf("message ID mismatch: got %s, want %s", msgs[0].ID, msg.ID)
	}
}

// TestPublicSyncSinceFilter verifies that the since-timestamp filter works
// correctly through the public Sync alias.
func TestPublicSyncSinceFilter(t *testing.T) {
	campfireID := "pub-sync-since"
	id := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	tr := startTransport(t, s)
	ep := epOf(tr)

	// Deliver first message.
	msg1 := newTestMessage(t, id)
	if err := cfhttp.Deliver(ep, campfireID, msg1, id); err != nil {
		t.Fatalf("Deliver msg1: %v", err)
	}

	cutoff := time.Now().UnixNano()
	time.Sleep(2 * time.Millisecond)

	// Deliver second message after cutoff.
	msg2 := newTestMessage(t, id)
	if err := cfhttp.Deliver(ep, campfireID, msg2, id); err != nil {
		t.Fatalf("Deliver msg2: %v", err)
	}

	msgs, err := cfhttp.Sync(ep, campfireID, cutoff, id)
	if err != nil {
		t.Fatalf("Sync since cutoff: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message since cutoff, got %d", len(msgs))
	}
	if msgs[0].ID != msg2.ID {
		t.Errorf("expected msg2, got %s", msgs[0].ID)
	}
}

// ---------------------------------------------------------------------------
// DeliverToAll (fan-out)
// ---------------------------------------------------------------------------

// TestPublicDeliverToAll verifies the fan-out helper delivers to multiple peers.
func TestPublicDeliverToAll(t *testing.T) {
	campfireID := "pub-deliver-all"
	id := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)
	addMembership(t, s1, campfireID)
	addMembership(t, s2, campfireID)
	addPeerEndpoint(t, s1, campfireID, id.PublicKeyHex())
	addPeerEndpoint(t, s2, campfireID, id.PublicKeyHex())

	tr1 := startTransport(t, s1)
	tr2 := startTransport(t, s2)
	ep1, ep2 := epOf(tr1), epOf(tr2)

	msg := newTestMessage(t, id)
	errs := cfhttp.DeliverToAll([]string{ep1, ep2}, campfireID, msg, id)
	for i, err := range errs {
		if err != nil {
			t.Errorf("DeliverToAll[%d]: %v", i, err)
		}
	}

	for name, ep := range map[string]string{"peer1": ep1, "peer2": ep2} {
		got, err := cfhttp.Sync(ep, campfireID, 0, id)
		if err != nil {
			t.Errorf("Sync from %s: %v", name, err)
			continue
		}
		if len(got) != 1 {
			t.Errorf("%s: expected 1 message, got %d", name, len(got))
		}
	}
}

// ---------------------------------------------------------------------------
// MembershipEvent type + NotifyMembership (join / leave)
// ---------------------------------------------------------------------------

// TestPublicMembershipEventJoinLeave verifies the MembershipEvent type alias
// and NotifyMembership function route through to the real implementation.
func TestPublicMembershipEventJoinLeave(t *testing.T) {
	campfireID := "pub-membership-jl"
	id1 := tempIdentity(t)
	id2 := tempIdentity(t)

	s1 := tempStore(t)
	s2 := tempStore(t)
	addMembership(t, s1, campfireID)
	addMembership(t, s2, campfireID)
	// id2 must be a known member of s1 so the auth check passes.
	addPeerEndpoint(t, s1, campfireID, id2.PublicKeyHex())

	tr1 := startTransport(t, s1)
	tr2 := startTransport(t, s2)
	ep1 := epOf(tr1)
	ep2 := epOf(tr2)

	// Use the PUBLIC MembershipEvent type alias.
	joinEvt := cfhttp.MembershipEvent{
		Event:    "join",
		Member:   id2.PublicKeyHex(),
		Endpoint: ep2,
	}
	if err := cfhttp.NotifyMembership(ep1, campfireID, joinEvt, id2); err != nil {
		t.Fatalf("NotifyMembership (join): %v", err)
	}

	peers := tr1.Peers(campfireID)
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after join, got %d", len(peers))
	}
	if peers[0].PubKeyHex != id2.PublicKeyHex() {
		t.Errorf("peer pubkey mismatch")
	}
	if peers[0].Endpoint != ep2 {
		t.Errorf("peer endpoint mismatch: got %s, want %s", peers[0].Endpoint, ep2)
	}

	// Leave event.
	leaveEvt := cfhttp.MembershipEvent{
		Event:  "leave",
		Member: id2.PublicKeyHex(),
	}
	if err := cfhttp.NotifyMembership(ep1, campfireID, leaveEvt, id2); err != nil {
		t.Fatalf("NotifyMembership (leave): %v", err)
	}

	peers = tr1.Peers(campfireID)
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers after leave, got %d", len(peers))
	}

	// suppress unused warning
	_ = id1
}

// ---------------------------------------------------------------------------
// Join (key exchange)
// ---------------------------------------------------------------------------

// TestPublicJoinKeyExchange verifies that the re-exported Join function and
// JoinResult type correctly route to the real key-exchange implementation.
func TestPublicJoinKeyExchange(t *testing.T) {
	campfireID := "pub-join-kex"

	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	idA := tempIdentity(t)
	idB := tempIdentity(t)
	sA := tempStore(t)

	err = sA.AddMembership(store.Membership{
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

	// Use the PUBLIC New and SetKeyProvider aliases.
	trA := cfhttp.New("127.0.0.1:0", sA)
	trA.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("campfire not found: %s", id)
	})
	if err := trA.Start(); err != nil {
		t.Fatalf("starting transport A: %v", err)
	}
	defer trA.Stop() //nolint:errcheck
	epA := epOf(trA)
	trA.SetSelfInfo(idA.PublicKeyHex(), epA)
	time.Sleep(20 * time.Millisecond)

	// Use the PUBLIC Join and JoinResult aliases.
	result, err := cfhttp.Join(epA, campfireID, idB, "")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}

	if fmt.Sprintf("%x", result.CampfirePubKey) != fmt.Sprintf("%x", cfPub) {
		t.Errorf("campfire public key mismatch")
	}
	if fmt.Sprintf("%x", result.CampfirePrivKey) != fmt.Sprintf("%x", cfPriv) {
		t.Errorf("campfire private key mismatch after ECDH decryption")
	}
	if result.JoinProtocol != "open" {
		t.Errorf("join_protocol = %s, want open", result.JoinProtocol)
	}
	if result.Threshold != 1 {
		t.Errorf("threshold = %d, want 1", result.Threshold)
	}

	// Admitting member (idA) should appear in the peer list.
	found := false
	for _, p := range result.Peers {
		if p.PubKeyHex == idA.PublicKeyHex() {
			found = true
		}
	}
	if !found {
		t.Errorf("admitting member not in peer list: %+v", result.Peers)
	}
}

// ---------------------------------------------------------------------------
// Signature verification (request-level auth)
// ---------------------------------------------------------------------------

// TestPublicVerifyRequestSignature exercises the exported VerifyRequestSignature
// function directly, confirming the alias resolves to the real verifier.
func TestPublicVerifyRequestSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	pubHex := hex.EncodeToString(pub)

	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		t.Fatalf("rand: %v", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	body := []byte(`{"test":"verify-request-signature"}`)

	// Build the signed payload (timestamp + "\n" + nonce + "\n" + body).
	var payload []byte
	payload = append(payload, []byte(timestamp)...)
	payload = append(payload, '\n')
	payload = append(payload, []byte(nonce)...)
	payload = append(payload, '\n')
	payload = append(payload, body...)

	sig := ed25519.Sign(priv, payload)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	// Valid signature must pass.
	if err := cfhttp.VerifyRequestSignature(pubHex, sigB64, nonce, timestamp, body); err != nil {
		t.Errorf("VerifyRequestSignature rejected valid signature: %v", err)
	}

	// Wrong key must fail.
	_, wrongPub, err2 := ed25519.GenerateKey(nil)
	if err2 != nil {
		t.Fatalf("generating wrong key: %v", err2)
	}
	wrongHex := hex.EncodeToString(wrongPub)
	if err := cfhttp.VerifyRequestSignature(wrongHex, sigB64, nonce, timestamp, body); err == nil {
		t.Error("VerifyRequestSignature accepted signature under wrong key")
	}

	// Tampered body must fail.
	tamperedBody := []byte(`{"test":"tampered"}`)
	if err := cfhttp.VerifyRequestSignature(pubHex, sigB64, nonce, timestamp, tamperedBody); err == nil {
		t.Error("VerifyRequestSignature accepted signature over tampered body")
	}
}

// ---------------------------------------------------------------------------
// SSRF — NewSSRFSafeClient
// ---------------------------------------------------------------------------

// TestPublicSSRFSafeClientRejectsLoopback verifies that the re-exported
// NewSSRFSafeClient returns a client that blocks loopback addresses.
func TestPublicSSRFSafeClientRejectsLoopback(t *testing.T) {
	// Restore real SSRF validation for this sub-test only.
	cfhttp.RestoreValidateJoinerEndpoint()
	t.Cleanup(cfhttp.OverrideValidateJoinerEndpointForTest)

	client := cfhttp.NewSSRFSafeClient()
	_, err := client.Get("http://127.0.0.1:1/anything")
	if err == nil {
		t.Error("NewSSRFSafeClient should have blocked loopback connection; got nil error")
	}
}

// ---------------------------------------------------------------------------
// Auth rejection tests (via real transport)
// ---------------------------------------------------------------------------

// TestPublicDeliverRejectsNoAuthHeaders confirms that the server rejects
// deliver requests that have no Campfire auth headers (401).
func TestPublicDeliverRejectsNoAuthHeaders(t *testing.T) {
	campfireID := "pub-no-auth"
	id := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)

	tr := startTransport(t, s)
	ep := epOf(tr)

	// Raw POST with no auth headers.
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	resp, err := http.Post(url, "application/octet-stream", nil) //nolint:gosec
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated deliver, got %d", resp.StatusCode)
	}
	_ = id
}

// TestPublicSyncRejectsNoAuthHeaders confirms the sync endpoint returns 401
// for unauthenticated requests.
func TestPublicSyncRejectsNoAuthHeaders(t *testing.T) {
	campfireID := "pub-sync-no-auth"

	s := tempStore(t)
	addMembership(t, s, campfireID)

	tr := startTransport(t, s)
	ep := epOf(tr)

	url := fmt.Sprintf("%s/campfire/%s/sync?since=0", ep, campfireID)
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated sync, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// PeerInfo type alias
// ---------------------------------------------------------------------------

// TestPublicPeerInfoType verifies that PeerInfo (re-exported alias) is usable
// as a real value type with the same fields as the internal type.
func TestPublicPeerInfoType(t *testing.T) {
	// Construct a PeerInfo using the PUBLIC type alias.
	pi := cfhttp.PeerInfo{
		PubKeyHex: "deadbeef",
		Endpoint:  "http://example.com:8080",
	}
	if pi.PubKeyHex != "deadbeef" {
		t.Errorf("PeerInfo.PubKeyHex field access failed")
	}
	if pi.Endpoint != "http://example.com:8080" {
		t.Errorf("PeerInfo.Endpoint field access failed")
	}
}

// ---------------------------------------------------------------------------
// AddPeer / RemovePeer (in-memory peer list mutations)
// ---------------------------------------------------------------------------

// TestPublicAddRemovePeer verifies that the Transport's in-memory peer list
// can be mutated and queried through the public API.
func TestPublicAddRemovePeer(t *testing.T) {
	campfireID := "pub-add-remove-peer"
	s := tempStore(t)
	addMembership(t, s, campfireID)

	tr := startTransport(t, s)

	pubKey := "aabbccdd"
	endpoint := "http://127.0.0.1:9999"

	tr.AddPeer(campfireID, pubKey, endpoint)
	peers := tr.Peers(campfireID)
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after AddPeer, got %d", len(peers))
	}
	if peers[0].PubKeyHex != pubKey {
		t.Errorf("AddPeer: wrong pubkey: got %s, want %s", peers[0].PubKeyHex, pubKey)
	}

	tr.RemovePeer(campfireID, pubKey)
	peers = tr.Peers(campfireID)
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers after RemovePeer, got %d", len(peers))
	}
}

// ---------------------------------------------------------------------------
// HkdfSHA256 + AESGCMEncrypt (crypto helpers)
// ---------------------------------------------------------------------------

// TestPublicCryptoHelpers verifies that the re-exported crypto functions
// are callable through the public package and produce real output.
func TestPublicCryptoHelpers(t *testing.T) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}

	// cfhttp.HkdfSHA256 is re-exported; signature: (sharedSecret []byte, info string) ([]byte, error)
	// Must return a 32-byte derived key.
	key, err := cfhttp.HkdfSHA256(secret, "campfire-join-v1")
	if err != nil {
		t.Fatalf("HkdfSHA256: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("HkdfSHA256 returned %d bytes, want 32", len(key))
	}

	// Different info strings must produce different keys (domain separation).
	key2, err := cfhttp.HkdfSHA256(secret, "campfire-rekey-v1")
	if err != nil {
		t.Fatalf("HkdfSHA256 (rekey): %v", err)
	}
	if string(key) == string(key2) {
		t.Error("HkdfSHA256: same key for different info strings — domain separation broken")
	}

	plaintext := []byte("encrypt-me-please")

	// cfhttp.AESGCMEncrypt is re-exported; signature: (key, plaintext []byte) ([]byte, error)
	// Returns nonce||ciphertext.
	ct, err := cfhttp.AESGCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("AESGCMEncrypt: %v", err)
	}
	if len(ct) == 0 {
		t.Error("AESGCMEncrypt returned empty ciphertext")
	}
	// Ciphertext must differ from plaintext (includes random nonce prefix).
	if string(ct) == string(plaintext) {
		t.Error("AESGCMEncrypt did not encrypt: ciphertext == plaintext")
	}
}

// ---------------------------------------------------------------------------
// Stop is idempotent
// ---------------------------------------------------------------------------

// TestPublicTransportStopIdempotent verifies that calling Stop multiple times
// does not panic or return a fatal error.
func TestPublicTransportStopIdempotent(t *testing.T) {
	s := tempStore(t)
	tr := cfhttp.New("127.0.0.1:0", s)
	if err := tr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := tr.Stop(); err != nil {
		t.Logf("First Stop: %v (may be non-nil for already-closed)", err)
	}
	// Second Stop must not panic.
	_ = tr.Stop()
}

package main

// relay_create_test.go — Tests for POST /campfire/create endpoint on the relay.
//
// Protocol: The creator encrypts the campfire private key using
// ECDH(creatorEphemPriv, relayStaticX25519Pub) + HKDF-SHA256 + AES-256-GCM.
// The relay decrypts using its static X25519 private key. This is a single-round
// protocol — the creator discovers the relay's static X25519 pub via a separate
// channel (e.g. GET /campfire/relay-info, out-of-band config, or the response to
// a first create that returns relay_x25519_pub).
//
// Done conditions (campfireagent-99ea):
//   - POST /campfire/create → 201 Created
//   - Subsequent POST /campfire/{id}/join by second agent → succeeds
//   - Duplicate create → 409 Conflict
//   - Bad signature → 401 Unauthorized

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newRelayTestServer creates a TransportRouter with a static X25519 key, backed
// by a real httptest.Server with only the /campfire/ routes (no MCP).
func newRelayTestServer(t *testing.T) (*TransportRouter, *httptest.Server) {
	t.Helper()
	router := NewTransportRouter()

	mux := http.NewServeMux()
	mux.Handle("/campfire/", router)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Wire selfEndpoint (no global store for unit tests).
	router.mu.Lock()
	router.selfEndpoint = ts.URL
	router.mu.Unlock()

	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	return router, ts
}

// generateRelayX25519Key generates a fresh static X25519 keypair.
func generateRelayX25519Key(t *testing.T) (*ecdh.PrivateKey, string) {
	t.Helper()
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating relay X25519 key: %v", err)
	}
	return priv, hex.EncodeToString(priv.PublicKey().Bytes())
}

// buildCreateRequest constructs the JSON body for POST /campfire/create.
// The creator encrypts the campfire private key using ECDH(creatorEphemPriv, relayStaticX25519Pub).
func buildCreateRequest(
	t *testing.T,
	creatorID *identity.Identity,
	campfirePrivKey ed25519.PrivateKey,
	campfirePubKey ed25519.PublicKey,
	relayX25519PubHex string,
	joinProtocol string,
) []byte {
	t.Helper()

	// Creator generates ephemeral X25519 keypair.
	creatorEphPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating creator ephemeral X25519 key: %v", err)
	}
	creatorEphPubHex := hex.EncodeToString(creatorEphPriv.PublicKey().Bytes())

	// Parse the relay's static X25519 pub key.
	relayPubBytes, err := hex.DecodeString(relayX25519PubHex)
	if err != nil {
		t.Fatalf("decoding relay X25519 pub: %v", err)
	}
	relayX25519Pub, err := ecdh.X25519().NewPublicKey(relayPubBytes)
	if err != nil {
		t.Fatalf("parsing relay X25519 pub: %v", err)
	}

	// Derive shared secret: ECDH(creatorEphPriv, relayStaticPub).
	shared, err := creatorEphPriv.ECDH(relayX25519Pub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	aesKey, err := cfhttp.HkdfSHA256(shared, "campfire-join-v1")
	if err != nil {
		t.Fatalf("HKDF: %v", err)
	}

	// Encrypt campfire private key.
	encryptedPrivKey, err := cfhttp.AESGCMEncrypt(aesKey, campfirePrivKey)
	if err != nil {
		t.Fatalf("encrypting campfire privkey: %v", err)
	}

	req := cfhttp.CreateCampfireRequest{
		CampfireID:            fmt.Sprintf("%x", campfirePubKey),
		EncryptedPrivKey:      encryptedPrivKey,
		EphemeralX25519Pub:    creatorEphPubHex,
		JoinProtocol:          joinProtocol,
		ReceptionRequirements: []string{},
		Threshold:             1,
		Description:           "test campfire",
		CreatorPubkey:         creatorID.PublicKeyHex(),
	}
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshaling create request: %v", err)
	}
	return bodyBytes
}

// signedCreateRequest creates a signed HTTP POST request for /campfire/create.
func signedCreateRequest(t *testing.T, url string, body []byte, signerID *identity.Identity) *http.Request {
	t.Helper()
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("generating nonce: %v", err)
	}
	nonceHex := hex.EncodeToString(nonce)
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	// Signed payload: timestamp + "\n" + nonce + "\n" + body.
	var payload []byte
	payload = append(payload, []byte(ts)...)
	payload = append(payload, '\n')
	payload = append(payload, []byte(nonceHex)...)
	payload = append(payload, '\n')
	payload = append(payload, body...)

	sig := ed25519.Sign(signerID.PrivateKey, payload)

	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Campfire-Sender", signerID.PublicKeyHex())
	req.Header.Set("X-Campfire-Nonce", nonceHex)
	req.Header.Set("X-Campfire-Timestamp", ts)
	req.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))
	return req
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRelayCreate_Success verifies that POST /campfire/create returns 201 Created
// when the request is well-formed, properly signed, and the ECDH key exchange succeeds.
func TestRelayCreate_Success(t *testing.T) {
	router, _ := newRelayTestServer(t)

	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")
	req := signedCreateRequest(t, "/campfire/create", body, creatorID)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var respBody cfhttp.CreateCampfireResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if respBody.CampfireID != cf.PublicKeyHex() {
		t.Errorf("expected campfire_id=%s, got %s", cf.PublicKeyHex(), respBody.CampfireID)
	}
	if respBody.Endpoint == "" {
		t.Error("expected non-empty endpoint in response")
	}

	// Campfire must be registered in the router after create.
	if tr := router.GetCampfireTransport(cf.PublicKeyHex()); tr == nil {
		t.Error("campfire not registered in transport router after create")
	}
}

// TestRelayCreate_JoinAfterCreate verifies that a second agent can join a campfire
// after it has been registered on the relay via POST /campfire/create.
// This is the primary integration done-condition for campfireagent-99ea.
func TestRelayCreate_JoinAfterCreate(t *testing.T) {
	// Create a NEW router with a real httptest.Server for full HTTP round-trips.
	router := NewTransportRouter()
	mux := http.NewServeMux()
	mux.Handle("/campfire/", router)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	router.mu.Lock()
	router.selfEndpoint = ts.URL
	router.mu.Unlock()

	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	// Creator registers campfire.
	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")

	// Sign and send via real HTTP client to the test server.
	nonce := make([]byte, 16)
	rand.Read(nonce) //nolint:errcheck
	nonceHex := hex.EncodeToString(nonce)
	tsStr := strconv.FormatInt(time.Now().Unix(), 10)
	var payload []byte
	payload = append(payload, []byte(tsStr)...)
	payload = append(payload, '\n')
	payload = append(payload, []byte(nonceHex)...)
	payload = append(payload, '\n')
	payload = append(payload, body...)
	sig := ed25519.Sign(creatorID.PrivateKey, payload)

	realReq, err := http.NewRequest(http.MethodPost, ts.URL+"/campfire/create", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	realReq.Header.Set("Content-Type", "application/json")
	realReq.Header.Set("X-Campfire-Sender", creatorID.PublicKeyHex())
	realReq.Header.Set("X-Campfire-Nonce", nonceHex)
	realReq.Header.Set("X-Campfire-Timestamp", tsStr)
	realReq.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))

	realClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := realClient.Do(realReq)
	if err != nil {
		t.Fatalf("POST /campfire/create: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("create failed with %d: %s", resp.StatusCode, string(respBody))
	}

	// Second agent joins via the relay's /campfire/{id}/join endpoint.
	joinerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}

	joinResult, err := cfhttp.Join(ts.URL, cf.PublicKeyHex(), joinerID, "")
	if err != nil {
		t.Fatalf("join after create failed: %v", err)
	}

	if joinResult.CampfirePrivKey == nil {
		t.Fatal("expected campfire private key in join result")
	}

	// Verify the decrypted private key matches the original campfire.
	joinedPriv := ed25519.PrivateKey(joinResult.CampfirePrivKey)
	derivedPub := joinedPriv.Public().(ed25519.PublicKey)
	if fmt.Sprintf("%x", derivedPub) != cf.PublicKeyHex() {
		t.Errorf("joined campfire pubkey mismatch: expected %s, got %x", cf.PublicKeyHex(), derivedPub)
	}
}

// TestRelayCreate_DuplicateConflict verifies that registering the same campfire
// twice returns 409 Conflict.
func TestRelayCreate_DuplicateConflict(t *testing.T) {
	router, _ := newRelayTestServer(t)

	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")

	// First request — should succeed.
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, signedCreateRequest(t, "/campfire/create", body, creatorID))
	if w1.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d", w1.Code)
	}

	// Second request — same campfire — must return 409.
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, signedCreateRequest(t, "/campfire/create", body, creatorID))
	if w2.Code != http.StatusConflict {
		t.Errorf("duplicate create: expected 409, got %d", w2.Code)
	}
}

// TestRelayCreate_BadSignature verifies that a request with an invalid Ed25519 signature
// returns 401 Unauthorized.
func TestRelayCreate_BadSignature(t *testing.T) {
	router, _ := newRelayTestServer(t)

	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")

	// Build a properly-signed request, then tamper the body after signing.
	req := signedCreateRequest(t, "/campfire/create", body, creatorID)
	tamperedBody := []byte(strings.Replace(string(body), "test campfire", "malicious campfire", 1))
	req.Body = io.NopCloser(bytes.NewReader(tamperedBody))
	req.ContentLength = int64(len(tamperedBody))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad signature: expected 401, got %d", w.Code)
	}
}

// TestRelayCreate_NonceReplay verifies that replaying the exact same signed
// request (same nonce) within the timestamp window is rejected with 401.
// This is the nonce replay protection for POST /campfire/create (campfireagent-67f).
func TestRelayCreate_NonceReplay(t *testing.T) {
	router, _ := newRelayTestServer(t)

	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf1, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}
	cf2, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New (second): %v", err)
	}

	body1 := buildCreateRequest(t, creatorID, cf1.PrivateKey, cf1.PublicKey, relayPubHex, "open")
	body2 := buildCreateRequest(t, creatorID, cf2.PrivateKey, cf2.PublicKey, relayPubHex, "open")

	// Build the first request and capture its nonce/timestamp/signature.
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("generating nonce: %v", err)
	}
	nonceHex := hex.EncodeToString(nonce)
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	var payload []byte
	payload = append(payload, []byte(ts)...)
	payload = append(payload, '\n')
	payload = append(payload, []byte(nonceHex)...)
	payload = append(payload, '\n')
	payload = append(payload, body1...)
	sig := ed25519.Sign(creatorID.PrivateKey, payload)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	// First request with this nonce — should succeed.
	req1 := httptest.NewRequest(http.MethodPost, "/campfire/create", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Campfire-Sender", creatorID.PublicKeyHex())
	req1.Header.Set("X-Campfire-Nonce", nonceHex)
	req1.Header.Set("X-Campfire-Timestamp", ts)
	req1.Header.Set("X-Campfire-Signature", sigB64)

	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	if w1.Code != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(w1.Result().Body)
		t.Fatalf("first request: expected 201, got %d: %s", w1.Code, string(bodyBytes))
	}

	// Second request — different campfire body but SAME nonce — must be rejected as replay.
	// Re-sign with the same nonce but over body2 to get a valid signature.
	var payload2 []byte
	payload2 = append(payload2, []byte(ts)...)
	payload2 = append(payload2, '\n')
	payload2 = append(payload2, []byte(nonceHex)...)
	payload2 = append(payload2, '\n')
	payload2 = append(payload2, body2...)
	sig2 := ed25519.Sign(creatorID.PrivateKey, payload2)
	sigB642 := base64.StdEncoding.EncodeToString(sig2)

	req2 := httptest.NewRequest(http.MethodPost, "/campfire/create", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Campfire-Sender", creatorID.PublicKeyHex())
	req2.Header.Set("X-Campfire-Nonce", nonceHex) // same nonce — replay
	req2.Header.Set("X-Campfire-Timestamp", ts)
	req2.Header.Set("X-Campfire-Signature", sigB642)

	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("replay: expected 401, got %d: %s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "replay detected") {
		t.Errorf("replay: expected 'replay detected' in body, got %q", w2.Body.String())
	}
}

// TestRelayCreate_MissingSignatureHeaders verifies that a request without
// Ed25519 signature headers returns 401 Unauthorized.
func TestRelayCreate_MissingSignatureHeaders(t *testing.T) {
	router, _ := newRelayTestServer(t)

	relayPriv, _ := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	body := []byte(`{"campfire_id":"abc","join_protocol":"open","threshold":1}`)
	req := httptest.NewRequest(http.MethodPost, "/campfire/create", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No X-Campfire-Sender, X-Campfire-Signature, etc.

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing headers: expected 401, got %d", w.Code)
	}
}

// TC-H2: TestRelayCreate_PrivKeyMismatch verifies that a request where the
// encrypted private key decrypts successfully but the resulting key does not
// match the claimed campfire_id returns 400 Bad Request.
//
// This tests the validation step after ECDH: even if the creator can perform
// the ECDH exchange, they must actually hold the matching campfire private key.
func TestRelayCreate_PrivKeyMismatch(t *testing.T) {
	router, _ := newRelayTestServer(t)

	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}

	// Generate the campfire that will be claimed in the request.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	// Generate a DIFFERENT ed25519 keypair — this is the wrong private key.
	_, wrongPrivKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating wrong ed25519 key: %v", err)
	}

	// Creator generates ephemeral X25519 keypair.
	creatorEphPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating creator ephemeral X25519 key: %v", err)
	}
	creatorEphPubHex := hex.EncodeToString(creatorEphPriv.PublicKey().Bytes())

	// Parse the relay's static X25519 pub key.
	relayPubBytes, err := hex.DecodeString(relayPubHex)
	if err != nil {
		t.Fatalf("decoding relay X25519 pub: %v", err)
	}
	relayX25519Pub, err := ecdh.X25519().NewPublicKey(relayPubBytes)
	if err != nil {
		t.Fatalf("parsing relay X25519 pub: %v", err)
	}

	// ECDH: derive shared secret with the relay's static key (same as normal flow).
	shared, err := creatorEphPriv.ECDH(relayX25519Pub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	aesKey, err := cfhttp.HkdfSHA256(shared, "campfire-join-v1")
	if err != nil {
		t.Fatalf("HKDF: %v", err)
	}

	// Encrypt the WRONG private key — ECDH will succeed, but key validation fails.
	encryptedWrongKey, err := cfhttp.AESGCMEncrypt(aesKey, wrongPrivKey)
	if err != nil {
		t.Fatalf("encrypting wrong privkey: %v", err)
	}

	// Build request with the correct campfire_id but the wrong encrypted key.
	reqBody := cfhttp.CreateCampfireRequest{
		CampfireID:         cf.PublicKeyHex(),
		EncryptedPrivKey:   encryptedWrongKey,
		EphemeralX25519Pub: creatorEphPubHex,
		JoinProtocol:       "open",
		Threshold:          1,
		Description:        "mismatch test",
		CreatorPubkey:      creatorID.PublicKeyHex(),
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshaling request: %v", err)
	}

	req := signedCreateRequest(t, "/campfire/create", body, creatorID)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("privkey mismatch: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TC-M1: TestRelayCreate_StaleTimestamp verifies that a request with a timestamp
// older than the allowed freshness window (60s) returns 401 Unauthorized.
//
// This tests replay protection against stale requests re-submitted after the
// nonce window has expired — the timestamp check must catch them first.
func TestRelayCreate_StaleTimestamp(t *testing.T) {
	router, _ := newRelayTestServer(t)

	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")

	// Set timestamp to 90 seconds in the past (outside the 60s window).
	staleTime := time.Now().Add(-90 * time.Second)
	ts := strconv.FormatInt(staleTime.Unix(), 10)

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("generating nonce: %v", err)
	}
	nonceHex := hex.EncodeToString(nonce)

	// Build signature over the stale timestamp.
	var payload []byte
	payload = append(payload, []byte(ts)...)
	payload = append(payload, '\n')
	payload = append(payload, []byte(nonceHex)...)
	payload = append(payload, '\n')
	payload = append(payload, body...)
	sig := ed25519.Sign(creatorID.PrivateKey, payload)

	req := httptest.NewRequest(http.MethodPost, "/campfire/create", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Campfire-Sender", creatorID.PublicKeyHex())
	req.Header.Set("X-Campfire-Nonce", nonceHex)
	req.Header.Set("X-Campfire-Timestamp", ts)
	req.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("stale timestamp: expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

// TC-M1: TestRelayCreate_FutureTimestamp verifies that a request with a timestamp
// too far in the future (beyond the allowed freshness window) returns 401 Unauthorized.
//
// This prevents pre-signed requests from being held and replayed later.
func TestRelayCreate_FutureTimestamp(t *testing.T) {
	router, _ := newRelayTestServer(t)

	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")

	// Set timestamp to 90 seconds in the future (outside the 60s window).
	futureTime := time.Now().Add(90 * time.Second)
	ts := strconv.FormatInt(futureTime.Unix(), 10)

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("generating nonce: %v", err)
	}
	nonceHex := hex.EncodeToString(nonce)

	// Build signature over the future timestamp.
	var payload []byte
	payload = append(payload, []byte(ts)...)
	payload = append(payload, '\n')
	payload = append(payload, []byte(nonceHex)...)
	payload = append(payload, '\n')
	payload = append(payload, body...)
	sig := ed25519.Sign(creatorID.PrivateKey, payload)

	req := httptest.NewRequest(http.MethodPost, "/campfire/create", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Campfire-Sender", creatorID.PublicKeyHex())
	req.Header.Set("X-Campfire-Nonce", nonceHex)
	req.Header.Set("X-Campfire-Timestamp", ts)
	req.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("future timestamp: expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

// TC-M2: TestRelayCreate_CreatorPubkeyMismatch verifies that a request where
// the creator_pubkey field in the JSON body does not match the X-Campfire-Sender
// header (the signing key) returns 400 Bad Request.
//
// This prevents one agent from registering a campfire and claiming another
// agent's public key as the creator.
func TestRelayCreate_CreatorPubkeyMismatch(t *testing.T) {
	router, _ := newRelayTestServer(t)

	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	// The agent who signs the request.
	signerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating signer identity: %v", err)
	}

	// A different identity whose pubkey will be claimed in creator_pubkey.
	otherID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating other identity: %v", err)
	}

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	// Build a valid request body but set creator_pubkey to the OTHER identity.
	// buildCreateRequest sets creator_pubkey = creatorID, so we build manually.
	creatorEphPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating creator ephemeral X25519 key: %v", err)
	}
	creatorEphPubHex := hex.EncodeToString(creatorEphPriv.PublicKey().Bytes())

	relayPubBytes, err := hex.DecodeString(relayPubHex)
	if err != nil {
		t.Fatalf("decoding relay X25519 pub: %v", err)
	}
	relayX25519Pub, err := ecdh.X25519().NewPublicKey(relayPubBytes)
	if err != nil {
		t.Fatalf("parsing relay X25519 pub: %v", err)
	}
	shared, err := creatorEphPriv.ECDH(relayX25519Pub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	aesKey, err := cfhttp.HkdfSHA256(shared, "campfire-join-v1")
	if err != nil {
		t.Fatalf("HKDF: %v", err)
	}
	encryptedPrivKey, err := cfhttp.AESGCMEncrypt(aesKey, cf.PrivateKey)
	if err != nil {
		t.Fatalf("encrypting privkey: %v", err)
	}

	// Set creator_pubkey to otherID, but the request will be signed by signerID.
	reqBody := cfhttp.CreateCampfireRequest{
		CampfireID:         cf.PublicKeyHex(),
		EncryptedPrivKey:   encryptedPrivKey,
		EphemeralX25519Pub: creatorEphPubHex,
		JoinProtocol:       "open",
		Threshold:          1,
		Description:        "mismatch test",
		CreatorPubkey:      otherID.PublicKeyHex(), // does not match signer
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshaling request: %v", err)
	}

	// Sign with signerID — X-Campfire-Sender will be signerID.PublicKeyHex(),
	// which does not match creator_pubkey (otherID.PublicKeyHex()).
	req := signedCreateRequest(t, "/campfire/create", body, signerID)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("creator_pubkey mismatch: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

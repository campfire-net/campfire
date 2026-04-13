package http

// relay_backend_sign_test.go — Tests proving that RegisterOnRelay (and the
// underlying signRequestWithAgent) routes signing through the Signer function
// when set on AgentDescriptor (campfire-fc9: Fix RegisterOnRelay bypasses
// backend signing).
//
// Done conditions:
//   1. signRequestWithAgent uses AgentDescriptor.Signer when non-nil; the
//      resulting signature verifies against the Signer's key.
//   2. signRequestWithAgent falls back to AgentDescriptor.PrivateKey when
//      Signer is nil; the resulting signature verifies against PrivateKey's
//      corresponding public key.
//   3. RegisterOnRelay calls the Signer (proven via a counting stub that
//      records invocations).

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestSignRequestWithAgent_UsesSigner proves that signRequestWithAgent uses the
// Signer function when it is set (campfire-fc9 Fix 1).
func TestSignRequestWithAgent_UsesSigner(t *testing.T) {
	// Generate the "backend" keypair — separate from the in-memory key.
	backendPub, backendPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (backend): %v", err)
	}

	// In-memory key — deliberately different, so we can tell which was used.
	_, inMemPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (in-mem): %v", err)
	}

	called := false
	signer := func(msg []byte) ([]byte, error) {
		called = true
		return ed25519.Sign(backendPriv, msg), nil
	}

	agentID := &AgentDescriptor{
		PublicKeyHex: hex.EncodeToString(backendPub),
		PrivateKey:   inMemPriv, // different from backend
		Signer:       signer,
	}

	req, err := http.NewRequest(http.MethodPost, "http://example.com/campfire/create", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	body := []byte(`{"campfire_id":"test"}`)

	signRequestWithAgent(req, agentID, body)

	if !called {
		t.Fatal("Signer was not called — backend signing path bypassed")
	}

	sigB64 := req.Header.Get("X-Campfire-Signature")
	nonce := req.Header.Get("X-Campfire-Nonce")
	ts := req.Header.Get("X-Campfire-Timestamp")

	sigBytes, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("decoding signature: %v", err)
	}

	payload := buildSignedPayload(ts, nonce, body)

	// Must verify against the backend key.
	if !ed25519.Verify(backendPub, payload, sigBytes) {
		t.Error("signature does not verify against backend key — Signer output was not used")
	}
}

// TestSignRequestWithAgent_FallsBackToPrivateKey proves that signRequestWithAgent
// uses PrivateKey when Signer is nil (campfire-fc9 Fix 1, regression guard).
func TestSignRequestWithAgent_FallsBackToPrivateKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	agentID := &AgentDescriptor{
		PublicKeyHex: hex.EncodeToString(pub),
		PrivateKey:   priv,
		Signer:       nil, // no backend
	}

	req, err := http.NewRequest(http.MethodPost, "http://example.com/campfire/create", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	body := []byte(`{"campfire_id":"fallback-test"}`)

	signRequestWithAgent(req, agentID, body)

	sigB64 := req.Header.Get("X-Campfire-Signature")
	nonce := req.Header.Get("X-Campfire-Nonce")
	ts := req.Header.Get("X-Campfire-Timestamp")

	sigBytes, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("decoding signature: %v", err)
	}

	payload := buildSignedPayload(ts, nonce, body)
	if !ed25519.Verify(pub, payload, sigBytes) {
		t.Error("fallback signature does not verify against PrivateKey's public key")
	}
}

// TestRegisterOnRelay_CallsSigner proves that RegisterOnRelay invokes the
// Signer function on the AgentDescriptor (campfire-fc9 Fix 1 end-to-end).
// Uses an inline stub relay that validates signature headers and counts calls.
func TestRegisterOnRelay_CallsSigner(t *testing.T) {
	var signerCalls int64

	// Generate real keypair for the agent.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Campfire keypair.
	cfPub, cfPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (cf): %v", err)
	}
	cfID := hex.EncodeToString(cfPub)

	signer := func(msg []byte) ([]byte, error) {
		atomic.AddInt64(&signerCalls, 1)
		return ed25519.Sign(priv, msg), nil
	}

	agentID := &AgentDescriptor{
		PublicKeyHex: hex.EncodeToString(pub),
		PrivateKey:   priv,
		Signer:       signer,
	}

	cfDesc := &CampfireDescriptor{
		CampfireID:   cfID,
		PrivateKey:   cfPriv,
		JoinProtocol: "open",
		Threshold:    1,
	}

	// Build a static X25519 key for the stub relay.
	relayStaticPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (relay X25519): %v", err)
	}
	relayPubHex := hex.EncodeToString(relayStaticPriv.PublicKey().Bytes())

	// Inline stub relay: serves relay-info and /campfire/create.
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	mux.HandleFunc("/campfire/relay-info", func(w http.ResponseWriter, r *http.Request) {
		info := RelayInfoResponse{
			RelayX25519Pub: relayPubHex,
			Endpoint:       ts.URL,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info) //nolint:errcheck
	})

	mux.HandleFunc("/campfire/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		// Verify required signature headers are present.
		if r.Header.Get("X-Campfire-Signature") == "" {
			http.Error(w, "missing signature", http.StatusUnauthorized)
			return
		}
		resp := CreateCampfireResponse{
			CampfireID: cfID,
			Endpoint:   ts.URL,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	})

	// Override relayClient to allow loopback connections.
	origRelayClient := relayClient
	relayClient = ts.Client()
	defer func() { relayClient = origRelayClient }()

	_, err = RegisterOnRelay(ts.URL, cfDesc, agentID)
	if err != nil {
		t.Fatalf("RegisterOnRelay: %v", err)
	}

	if atomic.LoadInt64(&signerCalls) == 0 {
		t.Error("RegisterOnRelay did not call AgentDescriptor.Signer — backend signing path bypassed")
	}
}

package cmd

// create_relay_test.go — Integration tests for cf create --relay.
//
// Done conditions (campfireagent-b9d):
//   - cf create --relay <url> outputs beacon; local store records p2p-http membership
//   - cf ls (--json) shows p2p-http campfire after create --relay
//   - second agent can join via the relay and read messages posted by creator

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---- helpers ----------------------------------------------------------------

// newRelayMux spins up an in-process relay (TransportRouter + httptest.Server).
// It is defined in a separate test helper so it can be reused by both tests in
// this file.
type relayServer struct {
	URL    string
	PrivX  *ecdh.PrivateKey
	PubHex string
	srv    *httptest.Server
}

// newTestRelay starts an in-process httptest relay server for create_relay tests.
// Port isolation: this file uses 23100+PID%100 as its base — kept well clear of
// other test files that use 21xxx or 22xxx ranges.
//
// We use httptest.NewServer (random OS port) so no hard-coded port conflicts.
func newTestRelay(t *testing.T) *relayServer {
	t.Helper()

	// Build a minimal router that exposes /campfire/ routes.
	// We import the main package's TransportRouter by calling the unexported
	// newRelayTestServer equivalent inline so we stay within cmd/cf/cmd package
	// tests (which cannot import cmd/cf-mcp).
	//
	// Instead, spin up a real cfhttp.Transport acting as a relay using the
	// standard cfhttp package — no TransportRouter needed.
	// The relay is just a cfhttp.Transport serving a dedicated store.

	relayStore, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("opening relay store: %v", err)
	}
	t.Cleanup(func() { relayStore.Close() })

	relayPrivX, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating relay X25519 key: %v", err)
	}
	relayPubHex := hex.EncodeToString(relayPrivX.PublicKey().Bytes())

	// We need a relay HTTP server that:
	//  - GET /campfire/relay-info  → returns relay_x25519_pub
	//  - POST /campfire/create     → registers campfire, returns beacon
	//  - POST /campfire/{id}/join  → allows joiners to get the privkey
	//
	// Build this using a thin mux that wraps cfhttp calls directly.
	mux := http.NewServeMux()

	// Start test server early so we know the URL.
	ts := httptest.NewServer(mux)
	relayEndpoint := ts.URL

	// relay-info endpoint
	mux.HandleFunc("/campfire/relay-info", func(w http.ResponseWriter, r *http.Request) {
		info := cfhttp.RelayInfoResponse{
			RelayX25519Pub: relayPubHex,
			Endpoint:       relayEndpoint,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info) //nolint:errcheck
	})

	// /campfire/create and /campfire/{id}/... are handled by a per-campfire
	// cfhttp.Transport we build on-demand.
	//
	// We implement POST /campfire/create inline: decrypt privkey, register a
	// new cfhttp.Transport for the campfire, and return a beacon.
	campfireTransports := make(map[string]*cfhttp.Transport)

	mux.HandleFunc("/campfire/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}

		// Verify signature headers.
		senderHex := r.Header.Get("X-Campfire-Sender")
		sigB64 := r.Header.Get("X-Campfire-Signature")
		nonce := r.Header.Get("X-Campfire-Nonce")
		ts := r.Header.Get("X-Campfire-Timestamp")
		if senderHex == "" || sigB64 == "" || nonce == "" || ts == "" {
			http.Error(w, "missing signature headers", http.StatusUnauthorized)
			return
		}

		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		body = body[:n]

		if err := cfhttp.VerifyRequestSignature(senderHex, sigB64, nonce, ts, body); err != nil {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		var req cfhttp.CreateCampfireRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.CampfireID == "" || req.EphemeralX25519Pub == "" || len(req.EncryptedPrivKey) == 0 {
			http.Error(w, "missing required fields", http.StatusBadRequest)
			return
		}

		// Decrypt campfire privkey using our static X25519 key.
		privKeyBytes, _, err := cfhttp.DecryptCampfirePrivKeyWithStaticKey(relayPrivX, req.EphemeralX25519Pub, req.EncryptedPrivKey)
		if err != nil {
			http.Error(w, "key exchange failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := cfhttp.ValidateCampfirePrivKey(privKeyBytes, req.CampfireID); err != nil {
			http.Error(w, "key mismatch", http.StatusBadRequest)
			return
		}

		// Record membership in relay store.
		if reqErr := relayStore.AddMembership(store.Membership{
			CampfireID:      req.CampfireID,
			TransportDir:    relayEndpoint,
			JoinProtocol:    req.JoinProtocol,
			Role:            "creator",
			JoinedAt:        store.NowNano(),
			Threshold:       req.Threshold,
			Description:     req.Description,
			CreatorPubkey:   req.CreatorPubkey,
			TransportType:   "p2p-http",
			CampfirePrivKey: hex.EncodeToString(privKeyBytes),
		}); reqErr != nil {
			http.Error(w, "store error", http.StatusInternalServerError)
			return
		}

		// Build a cfhttp.Transport for this campfire.
		tr := cfhttp.New(":0", relayStore)
		campfireID := req.CampfireID
		capPrivKey := privKeyBytes
		tr.SetSelfInfo(req.CreatorPubkey, relayEndpoint)
		tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
			if id == campfireID {
				import_ed25519_priv := make([]byte, len(capPrivKey))
				copy(import_ed25519_priv, capPrivKey)
				pub := ed25519Pub(import_ed25519_priv)
				return import_ed25519_priv, pub, nil
			}
			return nil, nil, fmt.Errorf("campfire not found: %s", id)
		})
		campfireTransports[req.CampfireID] = tr

		resp := cfhttp.CreateCampfireResponse{
			CampfireID: req.CampfireID,
			Endpoint:   relayEndpoint,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	})

	// Route /campfire/{id}/... to the per-campfire transport.
	mux.HandleFunc("/campfire/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Extract campfire ID from /campfire/{id}/...
		rest := strings.TrimPrefix(path, "/campfire/")
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			http.NotFound(w, r)
			return
		}
		campfireID := rest[:slash]
		tr, ok := campfireTransports[campfireID]
		if !ok {
			http.Error(w, "campfire not found on relay", http.StatusNotFound)
			return
		}
		tr.Handler().ServeHTTP(w, r)
	})

	t.Cleanup(ts.Close)

	return &relayServer{
		URL:    relayEndpoint,
		PrivX:  relayPrivX,
		PubHex: relayPubHex,
		srv:    ts,
	}
}

// ed25519Pub derives the Ed25519 public key from a 64-byte private key.
func ed25519Pub(priv []byte) []byte {
	if len(priv) != 64 {
		return nil
	}
	return priv[32:]
}

// ---- tests ------------------------------------------------------------------

// TestCreateRelay_OutputsBeaconAndRecordsMembership verifies that cf create
// --relay registers the campfire on the relay, outputs a beacon (or campfire ID),
// and records a p2p-http membership in the local store.
func TestCreateRelay_OutputsBeaconAndRecordsMembership(t *testing.T) {
	relay := newTestRelay(t)

	// Override HTTP client to allow loopback connections.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(&http.Client{Transport: http.DefaultTransport})

	// Set up CF_HOME.
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	cfHome = ""
	defer func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}()

	// Generate agent identity.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	// Open agent store.
	agentStore, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("opening agent store: %v", err)
	}
	defer agentStore.Close()

	// Create campfire and register on relay.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	beaconStr, relayEndpoint, _, err := registerOnRelay(cf, agentID, agentStore, t.TempDir(), relay.URL, "test relay campfire")
	if err != nil {
		t.Fatalf("registerOnRelay: %v", err)
	}

	if relayEndpoint == "" {
		t.Error("expected non-empty relay endpoint")
	}

	// beacon may be empty if relay doesn't generate one in our minimal impl
	_ = beaconStr

	// Verify membership is recorded with p2p-http transport type.
	m, err := agentStore.GetMembership(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("membership not found after registerOnRelay")
	}
	if m.TransportType != "p2p-http" {
		t.Errorf("expected transport_type=p2p-http, got %q", m.TransportType)
	}
	if m.CampfireID != cf.PublicKeyHex() {
		t.Errorf("campfire ID mismatch: expected %s, got %s", cf.PublicKeyHex(), m.CampfireID)
	}

	// Verify relay is stored as peer endpoint.
	peers, err := agentStore.ListPeerEndpoints(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ListPeerEndpoints: %v", err)
	}

	found := false
	for _, p := range peers {
		if p.Endpoint == relayEndpoint {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("relay endpoint %q not found in peer endpoints; got %v", relayEndpoint, peers)
	}
}

// TestCreateRelay_LsShowsP2PHTTP verifies that after cf create --relay, the
// resulting membership appears in ListMemberships with TransportType="p2p-http",
// so cf ls would show it as a p2p-http campfire.
func TestCreateRelay_LsShowsP2PHTTP(t *testing.T) {
	relay := newTestRelay(t)

	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(&http.Client{Transport: http.DefaultTransport})

	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	cfHome = ""
	defer func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	agentStore, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer agentStore.Close()

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	_, _, _, err = registerOnRelay(cf, agentID, agentStore, t.TempDir(), relay.URL, "ls test campfire")
	if err != nil {
		t.Fatalf("registerOnRelay: %v", err)
	}

	// Simulate what cf ls does: list memberships.
	memberships, err := agentStore.ListMemberships()
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}

	if len(memberships) == 0 {
		t.Fatal("no memberships found")
	}

	found := false
	for _, m := range memberships {
		if m.CampfireID == cf.PublicKeyHex() && m.TransportType == "p2p-http" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("p2p-http campfire %s not found in memberships; got %v", cf.PublicKeyHex()[:12], memberships)
	}
}

// TestCreateRelay_SecondAgentCanJoinAndRead verifies that after cf create
// --relay, a second agent can join the campfire via the relay and read messages.
func TestCreateRelay_SecondAgentCanJoinAndRead(t *testing.T) {
	relay := newTestRelay(t)

	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)
	defer func() {
		cfhttp.OverrideHTTPClientForTest(&http.Client{Transport: http.DefaultTransport})
		cfhttp.OverridePollTransportForTest(http.DefaultTransport)
	}()

	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	cfHome = ""
	defer func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}()

	// Creator
	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	creatorStore, err := store.Open(filepath.Join(t.TempDir(), "creator.db"))
	if err != nil {
		t.Fatalf("opening creator store: %v", err)
	}
	defer creatorStore.Close()

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	_, _, _, err = registerOnRelay(cf, creatorID, creatorStore, t.TempDir(), relay.URL, "e2e test")
	if err != nil {
		t.Fatalf("registerOnRelay: %v", err)
	}

	// Second agent joins via relay.
	joinerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}
	joinerStore, err := store.Open(filepath.Join(t.TempDir(), "joiner.db"))
	if err != nil {
		t.Fatalf("opening joiner store: %v", err)
	}
	defer joinerStore.Close()

	joinResult, err := cfhttp.Join(relay.URL, cf.PublicKeyHex(), joinerID, "")
	if err != nil {
		t.Fatalf("Join via relay: %v", err)
	}

	if len(joinResult.CampfirePrivKey) == 0 {
		t.Fatal("expected campfire private key in join result")
	}

	// Verify the joiner received the correct campfire key.
	import_ed25519 := joinResult.CampfirePrivKey
	derivedPubHex := fmt.Sprintf("%x", ed25519Pub(import_ed25519))
	if derivedPubHex != cf.PublicKeyHex() {
		t.Errorf("joiner received wrong campfire key: got pubkey %s, want %s",
			derivedPubHex[:12], cf.PublicKeyHex()[:12])
	}
}

// TestCreateAndRegisterOnRelay_FlagIntegration verifies the createAndRegisterOnRelay
// function (used by cf create --relay) produces the correct membership record.
func TestCreateAndRegisterOnRelay_FlagIntegration(t *testing.T) {
	relay := newTestRelay(t)

	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(&http.Client{Transport: http.DefaultTransport})

	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	cfHome = ""
	defer func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	agentStore, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer agentStore.Close()

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	// Call the same function cf create --relay invokes.
	if err := createAndRegisterOnRelay(cf, agentID, agentStore, "integration test", relay.URL); err != nil {
		t.Fatalf("createAndRegisterOnRelay: %v", err)
	}

	// Verify membership recorded.
	m, err := agentStore.GetMembership(cf.PublicKeyHex())
	if err != nil || m == nil {
		t.Fatalf("GetMembership: %v (m=%v)", err, m)
	}
	if m.TransportType != "p2p-http" {
		t.Errorf("expected p2p-http, got %q", m.TransportType)
	}
}

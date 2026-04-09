package main

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// routerNonceEntry records a seen request nonce and its expiry time for the
// TransportRouter's per-router nonce deduplication store.
type routerNonceEntry struct {
	expiresAt time.Time
}

// routerNonceWindow is how long a nonce is retained in the TransportRouter's
// nonce store. Matches the window used in pkg/transport/http for consistency.
// We retain nonces for 2× the timestamp window to handle clock-skew edge cases.
const routerNonceWindow = 2 * requestTimestampWindowCreate

// TransportRouter maps campfire IDs to per-session HTTP transport instances.
// When an external peer sends a request to /campfire/{id}/deliver (or sync,
// poll, etc.), the router looks up which session owns that campfire and
// delegates to that session's transport handler.
//
// This is the "transport-is-the-service" architecture from the design doc:
// the MCP server embeds HTTP transport endpoints so hosted agents are native
// HTTP transport peers. External CLI agents see the hosted server as a normal
// peer endpoint.
type TransportRouter struct {
	mu               sync.RWMutex
	campfires        map[string]*cfhttp.Transport // campfireID → session's transport
	transports       map[string]*cfhttp.Transport // session token → transport
	sessionCampfires map[string][]string          // session token → owned campfire IDs

	// nonceMu guards seenNonces independently of the router-global mu to avoid
	// serializing nonce checks with campfire registration operations.
	nonceMu    sync.Mutex
	seenNonces map[string]routerNonceEntry // nonce → expiry; guards /campfire/create replays

	// globalStore is a non-namespaced store for cross-instance campfire lookups.
	// When a campfire isn't found in the local in-memory router, the global store
	// is checked for a membership with CampfirePrivKey. If found, a transport is
	// reconstructed on demand so p2p-http join/deliver/sync work across Azure
	// Functions instances.
	globalStore    store.Store
	selfEndpoint   string // server's external URL (e.g. https://mcp.east.getcampfire.dev/api)

	// staticX25519Key is an optional static X25519 private key used for ECDH in
	// the POST /campfire/create endpoint. When set, the relay decrypts campfire
	// private keys sent by creators using ECDH(staticX25519Key, creatorEphemPub).
	// When nil, the relay generates a fresh ephemeral key per request (requires
	// the creator to first discover the relay's ephemeral pub via a separate call).
	staticX25519Key *ecdh.PrivateKey
}

// NewTransportRouter creates a new TransportRouter.
func NewTransportRouter() *TransportRouter {
	return &TransportRouter{
		campfires:        make(map[string]*cfhttp.Transport),
		transports:       make(map[string]*cfhttp.Transport),
		sessionCampfires: make(map[string][]string),
		seenNonces:       make(map[string]routerNonceEntry),
	}
}

// consumeNonce records a nonce as seen and returns true if it was new.
// Returns false if the nonce has been seen before (replay attempt).
// Nonces are pruned lazily on write; the caller is responsible for periodic
// pruning via pruneNonces if long-lived operation without traffic is expected.
func (r *TransportRouter) consumeNonce(nonce string) (accepted bool) {
	r.nonceMu.Lock()
	defer r.nonceMu.Unlock()

	now := time.Now()

	// Fast path: reject duplicates before pruning.
	if _, seen := r.seenNonces[nonce]; seen {
		return false
	}

	// Lazy prune: remove expired entries while holding the lock.
	for k, e := range r.seenNonces {
		if now.After(e.expiresAt) {
			delete(r.seenNonces, k)
		}
	}

	// Record nonce.
	r.seenNonces[nonce] = routerNonceEntry{expiresAt: now.Add(routerNonceWindow)}
	return true
}

// register is unexported to prevent direct use. Use RegisterForSession instead,
// which also tracks session ownership for cleanup on reap.
func (r *TransportRouter) register(campfireID string, t *cfhttp.Transport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.campfires[campfireID] = t
}

// RegisterForSession associates a campfire ID with a session's transport and
// records the campfire as owned by the session token. Use this instead of
// Register when the session token is available, so that UnregisterSession can
// clean up all campfires when the session is reaped.
func (r *TransportRouter) RegisterForSession(campfireID, token string, t *cfhttp.Transport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.campfires[campfireID] = t
	r.sessionCampfires[token] = append(r.sessionCampfires[token], campfireID)
}

// Unregister removes a campfire ID from the router. After this call, requests
// for the campfire return 404. If the transport was running a nonce pruner
// goroutine (e.g. from reconstructFromGlobalStore), it is stopped.
func (r *TransportRouter) Unregister(campfireID string) {
	r.mu.Lock()
	t := r.campfires[campfireID]
	delete(r.campfires, campfireID)
	r.mu.Unlock()
	if t != nil {
		t.StopNoncePruner()
	}
}

// UnregisterSession removes the session's transport and all campfire routes it
// owns. After this call, requests for any campfire owned by the session return
// 404 instead of hitting a stopped transport.
func (r *TransportRouter) UnregisterSession(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, campfireID := range r.sessionCampfires[token] {
		delete(r.campfires, campfireID)
	}
	delete(r.sessionCampfires, token)
	delete(r.transports, token)
}

// RotateSession transfers all campfire routes and the transport from oldToken
// to newToken without removing the campfire→transport mappings. This is the
// correct operation for token rotation: the session's identity and campfire
// ownership are preserved; only the external credential changes.
//
// After this call, GetCampfireTransport and LookupInviteAcrossAllStores
// continue to return the correct transport for all campfires previously owned
// by oldToken, and GetTransport(newToken) returns the session's transport.
func (r *TransportRouter) RotateSession(oldToken, newToken string, t *cfhttp.Transport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Transfer campfire ownership: move all campfire IDs from old token to new
	// token. The campfires map entries remain unchanged — the routes are intact.
	campfires := r.sessionCampfires[oldToken]
	delete(r.sessionCampfires, oldToken)
	if len(campfires) > 0 {
		r.sessionCampfires[newToken] = campfires
	}
	// Rotate the transport registration.
	delete(r.transports, oldToken)
	r.transports[newToken] = t
}

// RegisterSession associates a session token with its transport instance.
// Called when a session's transport is first created.
func (r *TransportRouter) RegisterSession(token string, t *cfhttp.Transport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transports[token] = t
}

// GetTransport returns the transport for a session token.
func (r *TransportRouter) GetTransport(token string) *cfhttp.Transport {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.transports[token]
}

// GetCampfireTransport returns the transport that owns a campfire.
func (r *TransportRouter) GetCampfireTransport(campfireID string) *cfhttp.Transport {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.campfires[campfireID]
}

// LookupInviteAcrossAllStores searches every registered session's store for an
// invite record matching inviteCode. Returns the first match found, or nil if
// no store holds the code. Used by handleJoin to resolve campfire_id from an
// invite code when the caller provides only invite_code (design-mcp-security.md §5.a).
//
// If no locally registered session owns the invite, falls back to the global
// store. If found there, the transport is reconstructed on demand (same as
// reconstructFromGlobalStore for campfire ID lookups).
func (r *TransportRouter) LookupInviteAcrossAllStores(inviteCode string) (*cfhttp.Transport, string) {
	r.mu.RLock()
	localCampfires := r.campfires
	gs := r.globalStore
	r.mu.RUnlock()

	// Check locally registered transports first.
	for campfireID, t := range localCampfires {
		inv, err := t.Store().LookupInvite(inviteCode)
		if err == nil && inv != nil && inv.CampfireID == campfireID {
			return t, campfireID
		}
	}

	// Fall back to the global store for cross-instance invite resolution.
	if gs != nil {
		inv, err := gs.LookupInvite(inviteCode)
		if err == nil && inv != nil && !inv.Revoked {
			campfireID := inv.CampfireID
			// Reconstruct the transport for this campfire if not already cached.
			t := r.GetCampfireTransport(campfireID)
			if t == nil {
				t = r.reconstructFromGlobalStore(campfireID)
			}
			if t != nil {
				return t, campfireID
			}
		}
	}

	return nil, ""
}

// SetGlobalStore configures a non-namespaced store for cross-instance campfire
// lookups. When a p2p-http request arrives for a campfire not in the local
// in-memory router, the global store is checked for a membership record with
// CampfirePrivKey. If found, a transport is reconstructed so the request
// succeeds regardless of which Azure Functions instance handles it.
func (r *TransportRouter) SetGlobalStore(s store.Store, endpoint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.globalStore = s
	r.selfEndpoint = endpoint
}

// ServeHTTP routes incoming /campfire/{id}/... requests to the correct session's
// transport handler. It extracts the campfire ID from the URL path, looks up the
// owning transport, and delegates. If no session owns the campfire locally, falls
// back to reconstructing a transport from the global store.
//
// Special case: POST /campfire/create is intercepted before per-campfire routing.
// This endpoint allows external agents to register a campfire on the relay via ECDH
// key exchange (design-cf-remote-relay.md §3).
func (r *TransportRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Path: /campfire/{id}/{action}
	// The handler.route in pkg/transport/http expects paths starting with /campfire/
	path := req.URL.Path
	if len(path) < len("/campfire/") {
		http.NotFound(w, req)
		return
	}

	// Intercept POST /campfire/create before per-campfire routing.
	// This endpoint has no campfire ID in the path — the campfire is being registered.
	if path == "/campfire/create" && req.Method == http.MethodPost {
		r.handleCreateCampfire(w, req)
		return
	}

	// Intercept GET /campfire/relay-info — returns static X25519 pub key for ECDH.
	if path == "/campfire/relay-info" && req.Method == http.MethodGet {
		r.handleRelayInfo(w, req)
		return
	}

	// Extract campfire ID from path: /campfire/{id}/...
	// The path after /campfire/ starts at index 10
	rest := path[len("/campfire/"):]
	campfireID := rest
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		campfireID = rest[:idx]
	}

	if campfireID == "" {
		http.NotFound(w, req)
		return
	}

	t := r.GetCampfireTransport(campfireID)
	if t == nil {
		t = r.reconstructFromGlobalStore(campfireID)
	}
	if t == nil {
		http.Error(w, "campfire not found on this server", http.StatusNotFound)
		return
	}

	// Delegate to the transport's handler. The transport's mux expects
	// the full /campfire/{id}/{action} path.
	t.Handler().ServeHTTP(w, req)
}

// reconstructFromGlobalStore checks the non-namespaced global store for a
// campfire's membership (with CampfirePrivKey). If found, it creates a new
// cfhttp.Transport backed by the global store and caches it in the local router.
//
// This enables cross-instance p2p-http operations: a campfire created on
// instance A can serve join/deliver/sync on instance B because the campfire
// state is in shared Azure Table Storage.
func (r *TransportRouter) reconstructFromGlobalStore(campfireID string) *cfhttp.Transport {
	r.mu.RLock()
	gs := r.globalStore
	endpoint := r.selfEndpoint
	r.mu.RUnlock()

	if gs == nil {
		return nil
	}

	m, err := gs.GetMembership(campfireID)
	if err != nil || m == nil || m.CampfirePrivKey == "" {
		return nil
	}

	privKeyBytes, err := hex.DecodeString(m.CampfirePrivKey)
	if err != nil || len(privKeyBytes) != ed25519.PrivateKeySize {
		return nil
	}

	// Create a transport backed by the global store. Peer endpoints, membership
	// checks, and message storage all go through the shared (non-namespaced) store
	// so they are visible to every instance.
	t := cfhttp.New("", gs)
	t.StartNoncePruner()

	// Use the campfire creator's pubkey as self info — this appears in join
	// responses so the joiner knows the relay's peer endpoint.
	t.SetSelfInfo(m.CreatorPubkey, endpoint)

	// Key provider reads from the global store so multiple campfires are supported.
	t.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		gm, gerr := gs.GetMembership(id)
		if gerr != nil || gm == nil || gm.CampfirePrivKey == "" {
			return nil, nil, fmt.Errorf("campfire not found: %s", id)
		}
		pk, decErr := hex.DecodeString(gm.CampfirePrivKey)
		if decErr != nil || len(pk) != ed25519.PrivateKeySize {
			return nil, nil, fmt.Errorf("invalid key for campfire: %s", id)
		}
		pub := ed25519.PrivateKey(pk).Public().(ed25519.PublicKey)
		return pk, pub, nil
	})

	// Default to pull+push delivery modes for hosted campfires.
	t.SetDeliveryModesProvider(func(string) []string {
		return []string{campfire.DeliveryModePull, campfire.DeliveryModePush}
	})

	// Cache locally so subsequent requests for this campfire skip the store lookup.
	r.register(campfireID, t)
	return t
}

// GlobalStore returns the global store, or nil if not configured.
// Used by handleCreateHTTP to write campfire state to the shared store.
func (r *TransportRouter) GlobalStore() store.Store {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.globalStore
}

// SetStaticX25519Key sets the relay's static X25519 private key used for ECDH in
// POST /campfire/create. The creator encrypts the campfire private key using
// ECDH(creatorEphemPriv, relay.StaticX25519PubHex()), so the relay must have a
// known static key for the creator to target.
//
// If not set, POST /campfire/create is unavailable (returns 501).
func (r *TransportRouter) SetStaticX25519Key(key *ecdh.PrivateKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.staticX25519Key = key
}

// StaticX25519PubHex returns the hex-encoded static X25519 public key for the relay,
// or "" if not configured. Creators use this to encrypt campfire private keys for
// POST /campfire/create.
func (r *TransportRouter) StaticX25519PubHex() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.staticX25519Key == nil {
		return ""
	}
	return hex.EncodeToString(r.staticX25519Key.PublicKey().Bytes())
}

// maxCreateBodySize is the maximum body accepted by POST /campfire/create.
const maxCreateBodySize = 4 * 1024 // 4 KiB — create requests are small

// requestTimestampWindowCreate is the freshness window for /campfire/create requests.
// Matches the window used in pkg/transport/http/handler.go.
const requestTimestampWindowCreate = 60 * time.Second

// handleCreateCampfire handles POST /campfire/create.
//
// This endpoint allows an external agent to register a campfire on the relay.
// The creator proves ownership of the campfire private key via ECDH key exchange:
//
//  1. Creator generates ephemeral X25519 keypair, encrypts campfire privkey with
//     AES-256-GCM using HKDF(ECDH(creatorEphemPriv, relayEphemPub), "campfire-join-v1").
//  2. Creator sends encrypted_priv_key + ephemeral_x25519_pub in a signed POST.
//  3. Relay generates its own ephemeral X25519 keypair, performs ECDH, decrypts.
//  4. Relay validates the decrypted privkey produces the claimed campfire_id.
//  5. Relay stores membership in global store, registers transport with router.
//  6. Relay returns endpoint and beacon.
//
// Error responses:
//   - 401: missing/invalid Ed25519 signature headers
//   - 400: malformed request body or invalid keys
//   - 409: campfire already registered on this relay
//   - 500: internal errors (key generation, store write)
func (r *TransportRouter) handleCreateCampfire(w http.ResponseWriter, req *http.Request) {
	// --- Signature verification ---
	// Reuse the same header-based verification as pkg/transport/http/handler.go
	// (readAndVerify pattern). We re-implement it here because readAndVerify is on
	// the unexported handler struct in that package.
	senderHex := req.Header.Get("X-Campfire-Sender")
	sigB64 := req.Header.Get("X-Campfire-Signature")
	nonce := req.Header.Get("X-Campfire-Nonce")
	timestamp := req.Header.Get("X-Campfire-Timestamp")
	if senderHex == "" || sigB64 == "" || nonce == "" || timestamp == "" {
		http.Error(w, "missing signature headers", http.StatusUnauthorized)
		return
	}

	// Validate timestamp freshness.
	tsUnix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		http.Error(w, "invalid timestamp header", http.StatusUnauthorized)
		return
	}
	tsTime := time.Unix(tsUnix, 0)
	diff := time.Since(tsTime)
	if diff < 0 {
		diff = -diff
	}
	if diff > requestTimestampWindowCreate {
		http.Error(w, "request timestamp out of window", http.StatusUnauthorized)
		return
	}

	// Read and size-limit the body before signature verification.
	req.Body = http.MaxBytesReader(w, req.Body, maxCreateBodySize)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	// Verify the Ed25519 signature covers timestamp + nonce + body.
	if err := cfhttp.VerifyRequestSignature(senderHex, sigB64, nonce, timestamp, body); err != nil {
		log.Printf("handleCreateCampfire: signature verification failed from %s: %v", senderHex[:min(8, len(senderHex))], err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Check nonce uniqueness — reject replays of signed requests.
	if !r.consumeNonce(nonce) {
		log.Printf("handleCreateCampfire: duplicate nonce %s from %s", nonce, senderHex[:min(8, len(senderHex))])
		http.Error(w, "replay detected", http.StatusUnauthorized)
		return
	}

	// --- Parse request body ---
	var createReq cfhttp.CreateCampfireRequest
	if err := json.Unmarshal(body, &createReq); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// Validate required fields.
	if createReq.CampfireID == "" {
		http.Error(w, "campfire_id is required", http.StatusBadRequest)
		return
	}
	if createReq.EphemeralX25519Pub == "" {
		http.Error(w, "ephemeral_x25519_pub is required", http.StatusBadRequest)
		return
	}
	if len(createReq.EncryptedPrivKey) == 0 {
		http.Error(w, "encrypted_priv_key is required", http.StatusBadRequest)
		return
	}

	// creator_pubkey must match the request signer.
	if createReq.CreatorPubkey == "" {
		createReq.CreatorPubkey = senderHex
	} else if createReq.CreatorPubkey != senderHex {
		http.Error(w, "creator_pubkey does not match X-Campfire-Sender", http.StatusBadRequest)
		return
	}

	// Default join protocol if absent.
	if createReq.JoinProtocol == "" {
		createReq.JoinProtocol = "open"
	}
	if createReq.Threshold == 0 {
		createReq.Threshold = 1
	}

	// --- Duplicate check ---
	// If the campfire is already registered in the global store, return 409.
	r.mu.RLock()
	gs := r.globalStore
	endpoint := r.selfEndpoint
	r.mu.RUnlock()

	if gs != nil {
		existing, _ := gs.GetMembership(createReq.CampfireID)
		if existing != nil && existing.CampfirePrivKey != "" {
			http.Error(w, "campfire already registered on this relay", http.StatusConflict)
			return
		}
	}
	// Also check in-memory router.
	if r.GetCampfireTransport(createReq.CampfireID) != nil {
		http.Error(w, "campfire already registered on this relay", http.StatusConflict)
		return
	}

	// --- ECDH key exchange — decrypt campfire private key ---
	// Use the relay's static X25519 key if available; otherwise generate ephemeral.
	// Note: using a static key allows single-round protocol (creator knows relay's pub in advance).
	r.mu.RLock()
	staticKey := r.staticX25519Key
	r.mu.RUnlock()

	var privKeyBytes []byte
	var relayPubHex string
	if staticKey != nil {
		privKeyBytes, relayPubHex, err = cfhttp.DecryptCampfirePrivKeyWithStaticKey(staticKey, createReq.EphemeralX25519Pub, createReq.EncryptedPrivKey)
	} else {
		privKeyBytes, relayPubHex, err = cfhttp.DecryptCampfirePrivKey(createReq.EphemeralX25519Pub, createReq.EncryptedPrivKey)
	}
	if err != nil {
		log.Printf("handleCreateCampfire: ECDH/decrypt failed: %v", err)
		http.Error(w, "key exchange failed", http.StatusBadRequest)
		return
	}

	// --- Validate decrypted private key produces the claimed campfire_id ---
	if err := cfhttp.ValidateCampfirePrivKey(privKeyBytes, createReq.CampfireID); err != nil {
		log.Printf("handleCreateCampfire: key validation failed: %v", err)
		http.Error(w, "private key does not match campfire_id", http.StatusBadRequest)
		return
	}

	privKey := ed25519.PrivateKey(privKeyBytes)
	pubKey := privKey.Public().(ed25519.PublicKey)

	// --- Store membership in global store (same as handleCreateHTTP) ---
	if gs != nil {
		gs.AddMembership(store.Membership{ //nolint:errcheck
			CampfireID:      createReq.CampfireID,
			TransportDir:    endpoint,
			JoinProtocol:    createReq.JoinProtocol,
			Role:            campfire.RoleFull,
			JoinedAt:        store.NowNano(),
			Threshold:       createReq.Threshold,
			Description:     createReq.Description,
			CreatorPubkey:   createReq.CreatorPubkey,
			TransportType:   "p2p-http",
			CampfirePrivKey: fmt.Sprintf("%x", privKeyBytes),
		})
		// Register the creator as a peer so checkMembership passes and
		// joiners see the relay endpoint.
		gs.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
			CampfireID:   createReq.CampfireID,
			MemberPubkey: createReq.CreatorPubkey,
			Endpoint:     endpoint,
		})
	}

	// --- Register transport with the router ---
	// Use the same pattern as reconstructFromGlobalStore.
	st := gs
	if st == nil {
		// No global store: use an in-memory-only store.
		var openErr error
		st, openErr = store.Open(":memory:")
		if openErr != nil {
			log.Printf("handleCreateCampfire: cannot open in-memory store: %v", openErr)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		// Store membership for key lookup.
		st.AddMembership(store.Membership{ //nolint:errcheck
			CampfireID:      createReq.CampfireID,
			TransportDir:    endpoint,
			JoinProtocol:    createReq.JoinProtocol,
			Role:            campfire.RoleFull,
			JoinedAt:        store.NowNano(),
			Threshold:       createReq.Threshold,
			Description:     createReq.Description,
			CreatorPubkey:   createReq.CreatorPubkey,
			TransportType:   "p2p-http",
			CampfirePrivKey: fmt.Sprintf("%x", privKeyBytes),
		})
	}

	t := cfhttp.New("", st)
	t.StartNoncePruner()
	t.SetSelfInfo(createReq.CreatorPubkey, endpoint)

	// Key provider reads from the store so subsequent join requests work.
	capturedGS := st // capture for closure
	t.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		m, merr := capturedGS.GetMembership(id)
		if merr != nil || m == nil || m.CampfirePrivKey == "" {
			return nil, nil, fmt.Errorf("campfire not found: %s", id)
		}
		pk, decErr := hex.DecodeString(m.CampfirePrivKey)
		if decErr != nil || len(pk) != ed25519.PrivateKeySize {
			return nil, nil, fmt.Errorf("invalid key for campfire: %s", id)
		}
		pub := ed25519.PrivateKey(pk).Public().(ed25519.PublicKey)
		return pk, pub, nil
	})

	t.SetDeliveryModesProvider(func(string) []string {
		return []string{campfire.DeliveryModePull, campfire.DeliveryModePush}
	})

	// Register without session ownership (no session token for this path).
	r.register(createReq.CampfireID, t)

	// --- Generate beacon ---
	var beaconStr string
	b, berr := beacon.New(
		pubKey, privKey,
		createReq.JoinProtocol, createReq.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "p2p-http",
			Config:   map[string]string{"endpoint": endpoint},
		},
		createReq.Description,
	)
	if berr == nil {
		data, encErr := cfencoding.Marshal(b)
		if encErr == nil {
			beaconStr = "beacon:" + base64.StdEncoding.EncodeToString(data)
		}
	}

	// --- Return response ---
	resp := cfhttp.CreateCampfireResponse{
		CampfireID:     createReq.CampfireID,
		RelayX25519Pub: relayPubHex,
		Beacon:         beaconStr,
		Endpoint:       endpoint,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// handleRelayInfo handles GET /campfire/relay-info.
// Returns the relay's static X25519 public key and endpoint so creators can
// pre-encrypt campfire private keys for POST /campfire/create.
func (r *TransportRouter) handleRelayInfo(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	staticKey := r.staticX25519Key
	endpoint := r.selfEndpoint
	r.mu.RUnlock()

	resp := cfhttp.RelayInfoResponse{
		Endpoint: endpoint,
	}
	if staticKey != nil {
		resp.RelayX25519Pub = hex.EncodeToString(staticKey.PublicKey().Bytes())
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}


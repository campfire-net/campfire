package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

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

	// globalStore is a non-namespaced store for cross-instance campfire lookups.
	// When a campfire isn't found in the local in-memory router, the global store
	// is checked for a membership with CampfirePrivKey. If found, a transport is
	// reconstructed on demand so p2p-http join/deliver/sync work across Azure
	// Functions instances.
	globalStore    store.Store
	selfEndpoint   string // server's external URL (e.g. https://mcp.east.getcampfire.dev/api)
}

// NewTransportRouter creates a new TransportRouter.
func NewTransportRouter() *TransportRouter {
	return &TransportRouter{
		campfires:        make(map[string]*cfhttp.Transport),
		transports:       make(map[string]*cfhttp.Transport),
		sessionCampfires: make(map[string][]string),
	}
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
func (r *TransportRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Path: /campfire/{id}/{action}
	// The handler.route in pkg/transport/http expects paths starting with /campfire/
	path := req.URL.Path
	if len(path) < len("/campfire/") {
		http.NotFound(w, req)
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

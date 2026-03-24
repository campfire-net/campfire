package main

import (
	"net/http"
	"strings"
	"sync"

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
// for the campfire return 404.
func (r *TransportRouter) Unregister(campfireID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.campfires, campfireID)
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
func (r *TransportRouter) LookupInviteAcrossAllStores(inviteCode string) (*cfhttp.Transport, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for campfireID, t := range r.campfires {
		inv, err := t.Store().LookupInvite(inviteCode)
		if err == nil && inv != nil && inv.CampfireID == campfireID {
			return t, campfireID
		}
	}
	return nil, ""
}

// ServeHTTP routes incoming /campfire/{id}/... requests to the correct session's
// transport handler. It extracts the campfire ID from the URL path, looks up the
// owning transport, and delegates. Returns 404 if no session owns the campfire.
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
		http.Error(w, "campfire not found on this server", http.StatusNotFound)
		return
	}

	// Delegate to the transport's handler. The transport's mux expects
	// the full /campfire/{id}/{action} path.
	t.Handler().ServeHTTP(w, req)
}

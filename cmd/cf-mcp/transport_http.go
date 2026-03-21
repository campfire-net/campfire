package main

import (
	"net/http"
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
	mu         sync.RWMutex
	campfires  map[string]*cfhttp.Transport // campfireID → session's transport
	transports map[string]*cfhttp.Transport // session token → transport
}

// NewTransportRouter creates a new TransportRouter.
func NewTransportRouter() *TransportRouter {
	return &TransportRouter{
		campfires:  make(map[string]*cfhttp.Transport),
		transports: make(map[string]*cfhttp.Transport),
	}
}

// Register associates a campfire ID with a session's transport instance.
// Called when a hosted agent creates a campfire via the MCP campfire_create tool.
func (r *TransportRouter) Register(campfireID string, t *cfhttp.Transport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.campfires[campfireID] = t
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
	if idx := indexOf(rest, '/'); idx >= 0 {
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

// indexOf returns the index of the first occurrence of sep in s, or -1.
func indexOf(s string, sep byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return i
		}
	}
	return -1
}

package http

import (
	"net/http"
	"strings"

	"github.com/campfire-net/campfire/pkg/store"
)

// MembershipEvent represents a membership change notification.
type MembershipEvent struct {
	Event    string `json:"event"`    // "join", "leave", or "evict"
	Member   string `json:"member"`   // hex public key
	Endpoint string `json:"endpoint"` // HTTP endpoint URL (may be empty for leave/evict)
}

// CampfireKeyProvider returns the campfire private key for a given campfire ID.
// Returns an error if the campfire is not found on this node.
type CampfireKeyProvider func(campfireID string) (privKey []byte, pubKey []byte, err error)

type handler struct {
	store     *store.Store
	transport *Transport
	// keyProvider is read from transport.keyProvider at call time.
	// Kept here for backward-compat test construction; transport takes precedence.
	keyProvider CampfireKeyProvider
}

// checkMembership verifies that senderHex is a member of campfireID on this node.
// Returns true if the sender is the local self key or appears in the stored peer list.
// Returns false with a ready-to-send 403 if not a member.
func (h *handler) checkMembership(w http.ResponseWriter, campfireID, senderHex string) (ok bool) {
	selfPubKeyHex, _ := h.transport.SelfInfo()
	isMember := senderHex == selfPubKeyHex
	if !isMember {
		peers, err := h.store.ListPeerEndpoints(campfireID)
		if err == nil {
			for _, p := range peers {
				if p.MemberPubkey == senderHex {
					isMember = true
					break
				}
			}
		}
	}
	if !isMember {
		http.Error(w, "not a campfire member", http.StatusForbidden)
		return false
	}
	return true
}

// route dispatches requests under /campfire/{id}/...
// Endpoint implementations live in handler_message.go, handler_join.go,
// handler_sign.go, and handler_rekey.go.
func (h *handler) route(w http.ResponseWriter, r *http.Request) {
	// Path: /campfire/{id}/{action}
	path := strings.TrimPrefix(r.URL.Path, "/campfire/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	campfireID := parts[0]
	action := parts[1]

	switch {
	case action == "deliver" && r.Method == http.MethodPost:
		h.handleDeliver(w, r, campfireID)
	case action == "sync" && r.Method == http.MethodGet:
		h.handleSync(w, r, campfireID)
	case action == "poll" && r.Method == http.MethodGet:
		h.handlePoll(w, r, campfireID)
	case action == "membership" && r.Method == http.MethodPost:
		h.handleMembership(w, r, campfireID)
	case action == "join" && r.Method == http.MethodPost:
		h.handleJoin(w, r, campfireID)
	case action == "sign" && r.Method == http.MethodPost:
		h.handleSign(w, r, campfireID)
	case action == "rekey" && r.Method == http.MethodPost:
		h.handleRekey(w, r, campfireID)
	default:
		http.NotFound(w, r)
	}
}

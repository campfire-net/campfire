package http

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/campfire-net/campfire/cf-protocol/store"
)

// AdmitRequest is the JSON body for POST /campfire/{id}/admit.
type AdmitRequest struct {
	MemberPubkey string `json:"member_pubkey"` // hex-encoded ed25519 public key
	Role         string `json:"role,omitempty"` // "observer", "writer", or "full" (default: "full")
}

// handleAdmit adds a member to an invite-only campfire's peer list so they
// can pass the join gate. Only existing members (verified by authMiddleware)
// can admit new members.
func (h *handler) handleAdmit(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte) {
	var req AdmitRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.MemberPubkey == "" {
		http.Error(w, "member_pubkey is required", http.StatusBadRequest)
		return
	}
	if len(req.MemberPubkey) != 64 {
		http.Error(w, "member_pubkey must be 64 hex chars (32 bytes)", http.StatusBadRequest)
		return
	}

	role := req.Role
	if role == "" {
		role = "full"
	}

	if err := h.store.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: req.MemberPubkey,
		Endpoint:     "", // no endpoint yet — will be set when they join
		Role:         role,
	}); err != nil {
		log.Printf("handleAdmit: failed to upsert peer for campfire %s: %v", campfireID, err)
		http.Error(w, "failed to admit member", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":       "admitted",
		"campfire_id":  campfireID,
		"member":       req.MemberPubkey,
		"role":         role,
	}) //nolint:errcheck
}

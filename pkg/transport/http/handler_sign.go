package http

import (
	"encoding/json"
	"log"
	"net/http"

	frostmessages "github.com/taurusgroup/frost-ed25519/pkg/messages"

	"github.com/campfire-net/campfire/pkg/threshold"
)

// handleSign processes a FROST signing round message from the initiator.
// POST /campfire/{id}/sign
// Body: SignRoundRequest (JSON)
// Returns: SignRoundResponse (JSON)
// This endpoint is ephemeral — no messages are stored in history.
// Auth (signature + membership) is enforced by authMiddleware in route.
func (h *handler) handleSign(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte) {
	// Look up the threshold share provider.
	if h.transport == nil {
		http.Error(w, "threshold signing not supported", http.StatusNotImplemented)
		return
	}
	h.transport.mu.RLock()
	sp := h.transport.thresholdShareProvider
	h.transport.mu.RUnlock()
	if sp == nil {
		http.Error(w, "threshold share provider not configured", http.StatusNotImplemented)
		return
	}

	var req SignRoundRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}

	// Load this node's DKG share for the campfire.
	participantID, shareData, err := sp(campfireID)
	if err != nil {
		log.Printf("handleSign: failed to load threshold share for campfire %s: %v", campfireID, err)
		http.Error(w, "threshold share not found", http.StatusNotFound)
		return
	}
	_, dkgResult, err := threshold.UnmarshalResult(shareData)
	if err != nil {
		log.Printf("handleSign: failed to deserialize threshold share for campfire %s: %v", campfireID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var outRaw [][]byte

	if req.Round == 1 {
		// Round 1: create a new signing session and return this participant's commitments.
		// Acquire global lock only to find/create the session state.
		h.transport.mu.Lock()
		sessionState, err := h.transport.getOrCreateSignSession(req.SessionID, req.SignerIDs, req.MessageToSign, dkgResult, participantID)
		h.transport.mu.Unlock()
		if err != nil {
			log.Printf("handleSign: failed to create signing session %s for campfire %s: %v", req.SessionID, campfireID, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		// Acquire per-session lock for Start()+Deliver() — prevents concurrent
		// round-1 requests from racing on the same SigningSession state machine.
		sessionState.mu.Lock()
		ss := sessionState.session

		// Start generates this participant's round-1 commitment messages.
		commitMsgs := ss.Start()

		// Deliver inbound round-1 messages from the initiator.
		for _, raw := range req.Messages {
			var msg frostmessages.Message
			if err := msg.UnmarshalBinary(raw); err != nil {
				continue
			}
			ss.Deliver(&msg) //nolint:errcheck
		}
		sessionState.mu.Unlock()

		// Return this participant's commitment messages (the initiator needs them).
		for _, m := range commitMsgs {
			b, err := m.MarshalBinary()
			if err != nil {
				continue
			}
			outRaw = append(outRaw, b)
		}
	} else {
		// Round 2: look up the existing session under write lock to prevent concurrent pruning.
		// We hold the write lock for the duration of the round-2 state mutation and cleanup
		// so that pruneSignSessions cannot delete the session out from under us.
		h.transport.mu.Lock()
		sessionState, ok := h.transport.signSessions[req.SessionID]
		if !ok {
			h.transport.mu.Unlock()
			http.Error(w, "signing session not found (round 2 without round 1?)", http.StatusBadRequest)
			return
		}
		ss := sessionState.session

		// Advance state machine: after all round-1 messages were delivered in round 1,
		// ProcessAll now produces this participant's round-2 share messages.
		sharesMsgs := ss.ProcessAll()

		// Deliver inbound round-2 messages from the initiator.
		for _, raw := range req.Messages {
			var msg frostmessages.Message
			if err := msg.UnmarshalBinary(raw); err != nil {
				continue
			}
			ss.Deliver(&msg) //nolint:errcheck
		}

		// Advance again to process any newly deliverable state.
		additionalMsgs := ss.ProcessAll()

		// Clean up after round 2 (while still holding the write lock).
		delete(h.transport.signSessions, req.SessionID)
		h.transport.mu.Unlock()

		// Return all outbound messages: own shares + any additional output.
		allOut := append(sharesMsgs, additionalMsgs...)
		for _, m := range allOut {
			b, err := m.MarshalBinary()
			if err != nil {
				continue
			}
			outRaw = append(outRaw, b)
		}
	}

	resp := SignRoundResponse{Messages: outRaw}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

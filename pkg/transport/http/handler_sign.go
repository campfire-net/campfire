package http

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	frostmessages "github.com/taurusgroup/frost-ed25519/pkg/messages"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/threshold"
)

// validateMessageToSign checks that msg is a CBOR-encoded HopSignInput or
// MessageSignInput. Both are the only payload types that the campfire protocol
// legitimately threshold-signs. Arbitrary bytes are rejected to prevent
// signing-oracle abuse by campfire members.
//
// Returns nil if msg is a valid HopSignInput (non-empty MessageID + non-empty
// CampfireID) or a valid MessageSignInput (non-empty ID field). Returns an
// error if msg cannot be decoded as either.
func validateMessageToSign(msg []byte) error {
	if len(msg) == 0 {
		return fmt.Errorf("message_to_sign is empty")
	}

	// Try HopSignInput first: requires MessageID (field 1) and CampfireID (field 2).
	var hop message.HopSignInput
	if err := cfencoding.Unmarshal(msg, &hop); err == nil {
		if hop.MessageID != "" && len(hop.CampfireID) > 0 {
			return nil
		}
	}

	// Try MessageSignInput: requires ID (field 1).
	var msi message.MessageSignInput
	if err := cfencoding.Unmarshal(msg, &msi); err == nil {
		if msi.ID != "" {
			return nil
		}
	}

	return fmt.Errorf("message_to_sign is not a valid HopSignInput or MessageSignInput")
}

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

	// Round 1 carries the MessageToSign that this node will co-sign. Validate
	// it is a known canonical type (HopSignInput or MessageSignInput) before
	// any FROST state is created. This prevents a campfire member from using
	// this node as a signing oracle for arbitrary bytes.
	if req.Round == 1 {
		if err := validateMessageToSign(req.MessageToSign); err != nil {
			log.Printf("handleSign: rejected invalid message_to_sign for campfire %s session %s: %v", campfireID, req.SessionID, err)
			http.Error(w, "message_to_sign must be a CBOR-encoded HopSignInput or MessageSignInput", http.StatusBadRequest)
			return
		}
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
		sessionState, err := h.transport.getOrCreateSignSession(campfireID, req.SessionID, req.SignerIDs, req.MessageToSign, dkgResult, participantID)
		h.transport.mu.Unlock()
		if err != nil {
			if err == errSignSessionCapExceeded {
				log.Printf("handleSign: sign session cap exceeded for campfire %s (max %d)", campfireID, maxSignSessionsPerCampfire)
				http.Error(w, "too many concurrent sign sessions for this campfire", http.StatusTooManyRequests)
				return
			}
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
		// Round 2: acquire the global write lock only long enough to extract the session
		// pointer and prevent pruneSignSessions from racing. Release before any FROST crypto.
		h.transport.mu.Lock()
		sessionState, ok := h.transport.signSessions[req.SessionID]
		h.transport.mu.Unlock()
		if !ok {
			http.Error(w, "signing session not found (round 2 without round 1?)", http.StatusBadRequest)
			return
		}

		// Hold the per-session lock for the duration of FROST crypto operations so that
		// concurrent requests on the same session do not race on the state machine.
		sessionState.mu.Lock()
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
		sessionState.mu.Unlock()

		// Re-acquire the global write lock briefly to remove the completed session.
		h.transport.removeSignSession(req.SessionID)

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

package http

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// handleDeliver receives a CBOR-encoded Message from a peer.
// POST /campfire/{id}/deliver
// Auth (signature + membership) is enforced by authMiddleware in route.
func (h *handler) handleDeliver(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte) {
	// Decode message
	var msg message.Message
	if err := cfencoding.Unmarshal(body, &msg); err != nil {
		http.Error(w, "invalid CBOR body", http.StatusBadRequest)
		return
	}

	// Verify the inner message signature (prevents tampered message content).
	if !msg.VerifySignature() {
		log.Printf("handleDeliver: message signature invalid for campfire %s", campfireID)
		http.Error(w, "invalid message signature", http.StatusBadRequest)
		return
	}

	// Verify that msg.Sender matches the authenticated senderHex from the request headers.
	// This prevents a member from delivering a message attributed to a different member
	// (e.g., M1 sending a message where msg.Sender == M2's pubkey).
	if msg.SenderHex() != senderHex {
		log.Printf("handleDeliver: sender mismatch for campfire %s: header=%s msg=%s", campfireID, senderHex, msg.SenderHex())
		http.Error(w, "sender mismatch", http.StatusBadRequest)
		return
	}

	// Server-side role enforcement.
	// Self (the local node) is always allowed — it is the creator/admitting member.
	// For peers, look up their stored role and enforce restrictions:
	//   - observer: cannot deliver any messages.
	//   - writer:   cannot deliver campfire:* system messages.
	//   - member / creator: no restrictions.
	selfPubKeyHex, _ := h.transport.SelfInfo()
	if senderHex != selfPubKeyHex {
		role, err := h.store.GetPeerRole(campfireID, senderHex)
		if err != nil {
			log.Printf("handleDeliver: failed to look up role for sender %s in campfire %s: %v", senderHex, campfireID, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		switch role {
		case "observer":
			log.Printf("handleDeliver: observer %s attempted to deliver message to campfire %s", senderHex, campfireID)
			http.Error(w, "observers cannot deliver messages", http.StatusForbidden)
			return
		case "writer":
			for _, tag := range msg.Tags {
				if strings.HasPrefix(tag, "campfire:") {
					log.Printf("handleDeliver: writer %s attempted to deliver system message (tag %q) to campfire %s", senderHex, tag, campfireID)
					http.Error(w, "writers cannot deliver campfire system messages", http.StatusForbidden)
					return
				}
			}
		}
	}

	// Store in local SQLite
	if _, err := h.store.AddMessage(store.MessageRecordFromMessage(campfireID, &msg, store.NowNano())); err != nil {
		log.Printf("handleDeliver: failed to store message for campfire %s: %v", campfireID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Wake any long-polling goroutines waiting for new messages.
	if h.transport != nil && h.transport.pollBroker != nil {
		h.transport.pollBroker.Notify(campfireID)
	}

	w.WriteHeader(http.StatusOK)
}

// handleSync serves messages from the local store newer than the given timestamp.
// GET /campfire/{id}/sync?since={nanosecond-timestamp}
// Auth (signature + membership) is enforced by authMiddleware in route.
func (h *handler) handleSync(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte) {
	sinceStr := r.URL.Query().Get("since")
	var since int64
	if sinceStr != "" {
		var err error
		since, err = strconv.ParseInt(sinceStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid since parameter", http.StatusBadRequest)
			return
		}
	}

	records, err := h.store.ListMessages(campfireID, since)
	if err != nil {
		log.Printf("handleSync: failed to query messages for campfire %s: %v", campfireID, err)
		http.Error(w, "failed to query messages", http.StatusInternalServerError)
		return
	}

	// Convert store records back to message.Message for wire format
	msgs := make([]message.Message, 0, len(records))
	for _, rec := range records {
		msg, err := recordToMessage(rec)
		if err != nil {
			continue
		}
		msgs = append(msgs, msg)
	}

	data, err := cfencoding.Marshal(msgs)
	if err != nil {
		log.Printf("handleSync: failed to encode response for campfire %s: %v", campfireID, err)
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/cbor")
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// handlePoll implements long-polling: sync-then-block semantics.
// GET /campfire/{id}/poll?since={ns}&timeout={s}
//
// Behaviour:
//  1. Auth check (401 on failure) — enforced by authMiddleware in route.
//  2. Membership check (403 if sender not a member) — enforced by authMiddleware.
//  3. Parse query params (400 on bad since; timeout default=30, cap=50).
//  4. Subscribe to PollBroker (503 if limit exceeded).
//  5. Initial sync: if records exist → 200 with CBOR body + X-Campfire-Cursor.
//  6. Block on channel or timeout.
//  7. Post-wait sync: if records exist → 200; else → 204 + X-Campfire-Cursor=since.
func (h *handler) handlePoll(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte) {
	// Null-broker guard.
	if h.transport == nil || h.transport.pollBroker == nil {
		http.Error(w, "long poll not supported", http.StatusNotImplemented)
		return
	}

	// Parse query params.
	sinceStr := r.URL.Query().Get("since")
	var since int64
	if sinceStr != "" {
		var err error
		since, err = strconv.ParseInt(sinceStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid since parameter", http.StatusBadRequest)
			return
		}
	}

	timeoutSec := 30
	if timeoutStr := r.URL.Query().Get("timeout"); timeoutStr != "" {
		t, err := strconv.Atoi(timeoutStr)
		if err != nil {
			http.Error(w, "invalid timeout parameter", http.StatusBadRequest)
			return
		}
		timeoutSec = t
	}
	if timeoutSec > 50 {
		timeoutSec = 50 // cap below server WriteTimeout (60s) to avoid killed connections
	}
	if timeoutSec < 1 {
		timeoutSec = 1 // enforce minimum 1s to prevent zero-duration busy-loop DoS
	}

	// Subscribe to PollBroker.
	ch, dereg, err := h.transport.pollBroker.Subscribe(campfireID)
	if err != nil {
		http.Error(w, "too many active pollers", http.StatusServiceUnavailable)
		return
	}
	defer dereg()

	// Helper: encode and send records as CBOR 200 with cursor header.
	respondWithRecords := func(records []store.MessageRecord) {
		msgs := make([]message.Message, 0, len(records))
		for _, rec := range records {
			msg, err := recordToMessage(rec)
			if err != nil {
				continue
			}
			msgs = append(msgs, msg)
		}
		data, err := cfencoding.Marshal(msgs)
		if err != nil {
			log.Printf("handlePoll: failed to encode response for campfire %s: %v", campfireID, err)
			http.Error(w, "failed to encode response", http.StatusInternalServerError)
			return
		}
		cursor := strconv.FormatInt(records[len(records)-1].ReceivedAt, 10)
		w.Header().Set("Content-Type", "application/cbor")
		w.Header().Set("X-Campfire-Cursor", cursor)
		w.WriteHeader(http.StatusOK)
		w.Write(data) //nolint:errcheck
	}

	// The poll cursor is a received_at nanosecond timestamp. Filter by received_at
	// so cursor and filter use the same field, preventing message loss when sender
	// clocks are skewed relative to the server. (Fix for workspace-d68.)
	pollFilter := store.MessageFilter{AfterReceivedAt: since}

	// Initial sync: return immediately if messages already exist.
	records, err := h.store.ListMessages(campfireID, 0, pollFilter)
	if err != nil {
		log.Printf("handlePoll: failed to query messages for campfire %s: %v", campfireID, err)
		http.Error(w, "failed to query messages", http.StatusInternalServerError)
		return
	}
	if len(records) > 0 {
		respondWithRecords(records)
		return
	}

	// Block until notification or timeout.
	select {
	case <-ch:
	case <-time.After(time.Duration(timeoutSec) * time.Second):
	}

	// Post-wait sync.
	records, err = h.store.ListMessages(campfireID, 0, pollFilter)
	if err != nil {
		log.Printf("handlePoll: failed to query messages (post-wait) for campfire %s: %v", campfireID, err)
		http.Error(w, "failed to query messages", http.StatusInternalServerError)
		return
	}
	if len(records) > 0 {
		respondWithRecords(records)
		return
	}

	// No messages: 204 with cursor = since.
	w.Header().Set("X-Campfire-Cursor", strconv.FormatInt(since, 10))
	w.WriteHeader(http.StatusNoContent)
}

// handleMembership receives a membership change notification.
// POST /campfire/{id}/membership
// Auth (signature + membership) is enforced by authMiddleware in route.
func (h *handler) handleMembership(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte) {
	var event MembershipEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// Update local peer list based on event.
	// Identity validation: the sender (verified via signature) must match
	// the member field for join/leave events to prevent identity injection.
	switch event.Event {
	case "join":
		// A node can only announce its own join.
		if event.Member != senderHex {
			http.Error(w, "join member must match sender", http.StatusBadRequest)
			return
		}
		if event.Endpoint != "" {
			if err := validateJoinerEndpoint(event.Endpoint); err != nil {
				http.Error(w, "invalid endpoint: "+err.Error(), http.StatusBadRequest)
				return
			}
			h.transport.AddPeer(campfireID, senderHex, event.Endpoint)
		}
	case "leave":
		// A node can only announce its own departure.
		if event.Member != senderHex {
			http.Error(w, "leave member must match sender", http.StatusBadRequest)
			return
		}
		h.transport.RemovePeer(campfireID, senderHex)
	case "evict":
		// Eviction is issued by the creator on behalf of another member.
		// Fail-closed: if we can't verify the creator, reject the eviction.
		membership, err := h.store.GetMembership(campfireID)
		if err != nil {
			log.Printf("handleMembership: GetMembership failed for campfire %s: %v", campfireID, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if membership != nil && membership.CreatorPubkey != "" && senderHex != membership.CreatorPubkey {
			http.Error(w, "only the campfire creator may evict members", http.StatusForbidden)
			return
		}
		h.transport.RemovePeer(campfireID, event.Member)
	default:
		http.Error(w, "unknown event type", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// verifyRequestSignature checks the Ed25519 signature header.
// The signature covers: timestamp (8 bytes, big-endian Unix seconds) || nonce (32 bytes) || body.
// This construction prevents replay attacks: each request has a unique nonce and a
// bounded timestamp, so captured requests cannot be re-submitted.
func verifyRequestSignature(senderHex, sigB64, nonce, timestamp string, body []byte) error {
	pubKeyBytes, err := hex.DecodeString(senderHex)
	if err != nil {
		return fmt.Errorf("decoding sender public key: %w", err)
	}
	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key length: %d", len(pubKeyBytes))
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("decoding signature: %w", err)
	}
	// Build the signed payload: timestamp || nonce || body.
	signedPayload := buildSignedPayload(timestamp, nonce, body)
	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), signedPayload, sig) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

// buildSignedPayload constructs the canonical bytes that are signed for a request.
// Format: timestamp (as ASCII decimal string) + "\n" + nonce + "\n" + body.
// Using ASCII strings avoids endianness ambiguity and is trivially debuggable.
func buildSignedPayload(timestamp, nonce string, body []byte) []byte {
	// pre-allocate: len(timestamp) + 1 + len(nonce) + 1 + len(body)
	n := len(timestamp) + 1 + len(nonce) + 1 + len(body)
	out := make([]byte, 0, n)
	out = append(out, []byte(timestamp)...)
	out = append(out, '\n')
	out = append(out, []byte(nonce)...)
	out = append(out, '\n')
	out = append(out, body...)
	return out
}

// recordToMessage converts a store.MessageRecord to a message.Message.
func recordToMessage(rec store.MessageRecord) (message.Message, error) {
	senderBytes, err := hex.DecodeString(rec.Sender)
	if err != nil {
		return message.Message{}, fmt.Errorf("decoding sender: %w", err)
	}

	var tags []string
	if err := json.Unmarshal([]byte(rec.Tags), &tags); err != nil {
		tags = []string{}
	}
	var antecedents []string
	if err := json.Unmarshal([]byte(rec.Antecedents), &antecedents); err != nil {
		antecedents = []string{}
	}
	var provenance []message.ProvenanceHop
	if err := json.Unmarshal([]byte(rec.Provenance), &provenance); err != nil {
		provenance = []message.ProvenanceHop{}
	}

	return message.Message{
		ID:          rec.ID,
		Sender:      senderBytes,
		Payload:     rec.Payload,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   rec.Timestamp,
		Signature:   rec.Signature,
		Provenance:  provenance,
		Instance:    rec.Instance,
	}, nil
}

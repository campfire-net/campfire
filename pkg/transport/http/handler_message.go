package http

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// handleDeliver receives a CBOR-encoded Message from a peer.
// POST /campfire/{id}/deliver
func (h *handler) handleDeliver(w http.ResponseWriter, r *http.Request, campfireID string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	// Verify sender signature headers
	senderHex := r.Header.Get("X-Campfire-Sender")
	sigB64 := r.Header.Get("X-Campfire-Signature")
	if senderHex == "" || sigB64 == "" {
		http.Error(w, "missing signature headers", http.StatusUnauthorized)
		return
	}
	if err := verifyRequestSignature(senderHex, sigB64, body); err != nil {
		log.Printf("handleDeliver: signature verification failed: %v", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Membership check: sender must be a known member of this campfire.
	if !h.checkMembership(w, campfireID, senderHex) {
		return
	}

	// Decode message
	var msg message.Message
	if err := cfencoding.Unmarshal(body, &msg); err != nil {
		http.Error(w, "invalid CBOR body", http.StatusBadRequest)
		return
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
func (h *handler) handleSync(w http.ResponseWriter, r *http.Request, campfireID string) {
	// Verify sender signature (query params don't have body — sign empty body)
	senderHex := r.Header.Get("X-Campfire-Sender")
	sigB64 := r.Header.Get("X-Campfire-Signature")
	if senderHex == "" || sigB64 == "" {
		http.Error(w, "missing signature headers", http.StatusUnauthorized)
		return
	}
	if err := verifyRequestSignature(senderHex, sigB64, []byte{}); err != nil {
		log.Printf("handleSync: signature verification failed: %v", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Membership check: sender must be a known member of this campfire.
	if !h.checkMembership(w, campfireID, senderHex) {
		return
	}

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
//  1. Auth check (401 on failure).
//  2. Membership check (403 if sender not a member).
//  3. Parse query params (400 on bad since; timeout default=30, cap=120).
//  4. Subscribe to PollBroker (503 if limit exceeded).
//  5. Initial sync: if records exist → 200 with CBOR body + X-Campfire-Cursor.
//  6. Block on channel or timeout.
//  7. Post-wait sync: if records exist → 200; else → 204 + X-Campfire-Cursor=since.
func (h *handler) handlePoll(w http.ResponseWriter, r *http.Request, campfireID string) {
	// Null-broker guard.
	if h.transport == nil || h.transport.pollBroker == nil {
		http.Error(w, "long poll not supported", http.StatusNotImplemented)
		return
	}

	// Verify auth (empty body, same pattern as handleSync).
	senderHex := r.Header.Get("X-Campfire-Sender")
	sigB64 := r.Header.Get("X-Campfire-Signature")
	if senderHex == "" || sigB64 == "" {
		http.Error(w, "missing signature headers", http.StatusUnauthorized)
		return
	}
	if err := verifyRequestSignature(senderHex, sigB64, []byte{}); err != nil {
		log.Printf("handlePoll: signature verification failed: %v", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Membership check: sender must be in peer_endpoints or be this node's self key.
	if !h.checkMembership(w, campfireID, senderHex) {
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
	if timeoutSec > 120 {
		timeoutSec = 120
	}
	if timeoutSec < 0 {
		timeoutSec = 0
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
func (h *handler) handleMembership(w http.ResponseWriter, r *http.Request, campfireID string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	// Verify sender signature
	senderHex := r.Header.Get("X-Campfire-Sender")
	sigB64 := r.Header.Get("X-Campfire-Signature")
	if senderHex == "" || sigB64 == "" {
		http.Error(w, "missing signature headers", http.StatusUnauthorized)
		return
	}
	if err := verifyRequestSignature(senderHex, sigB64, body); err != nil {
		log.Printf("handleMembership: signature verification failed: %v", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Membership check: sender must be a known member of this campfire.
	if !h.checkMembership(w, campfireID, senderHex) {
		return
	}

	var event MembershipEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// Update local peer list based on event
	switch event.Event {
	case "join":
		if event.Endpoint != "" {
			h.transport.AddPeer(campfireID, event.Member, event.Endpoint)
		}
	case "leave", "evict":
		h.transport.RemovePeer(campfireID, event.Member)
	default:
		http.Error(w, "unknown event type", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// verifyRequestSignature checks the Ed25519 signature header.
// The signature covers the request body bytes.
func verifyRequestSignature(senderHex, sigB64 string, body []byte) error {
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
	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), body, sig) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
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

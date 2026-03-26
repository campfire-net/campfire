package http

import (
	"bytes"
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
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
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

	// Server-side role enforcement on the deliverer (the HTTP request sender).
	// Self (the local node) is always allowed — it is the creator/admitting member.
	// For peers, look up their stored role and enforce restrictions:
	//   - observer: cannot deliver any messages.
	//   - writer:   cannot deliver campfire:* system messages.
	//   - full (and backward-compat aliases "member", "creator", ""): no restrictions.
	//
	// When msg.Sender != senderHex (relay case), the deliverer acts on behalf of the
	// original author. The message signature (VerifySignature above) proves the content
	// is authentic; we only need to verify the deliverer has delivery rights.
	//
	// campfire.EffectiveRole normalizes legacy/unknown values ("member", "creator",
	// empty string) to campfire.RoleFull so the switch only needs to handle the
	// three canonical roles. Without this normalization a peer whose role was stored
	// as "member" (the pre-enforcement default) would fall through the switch without
	// restriction — correct behaviour, but relying on implicit fallthrough rather than
	// explicit semantics.
	selfPubKeyHex, _ := h.transport.SelfInfo()
	if senderHex != selfPubKeyHex {
		rawRole, err := h.store.GetPeerRole(campfireID, senderHex)
		if err != nil {
			log.Printf("handleDeliver: failed to look up role for sender %s in campfire %s: %v", senderHex, campfireID, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		switch campfire.EffectiveRole(rawRole) {
		case campfire.RoleObserver:
			log.Printf("handleDeliver: observer %s attempted to deliver message to campfire %s", senderHex, campfireID)
			http.Error(w, "observers cannot deliver messages", http.StatusForbidden)
			return
		case campfire.RoleWriter:
			for _, tag := range msg.Tags {
				if strings.HasPrefix(tag, "campfire:") {
					log.Printf("handleDeliver: writer %s attempted to deliver system message (tag %q) to campfire %s", senderHex, tag, campfireID)
					http.Error(w, "writers cannot deliver campfire system messages", http.StatusForbidden)
					return
				}
			}
		}
		// campfire.RoleFull and any other normalized value: no restrictions.
	}

	// Sender-match check: when the HTTP deliverer differs from the message author,
	// this is a relay. Relay is permitted for members with delivery rights (RoleFull or
	// RoleWriter), which was verified above. Non-members are already rejected by
	// authMiddleware before this point.
	if msg.SenderHex() != senderHex {
		log.Printf("handleDeliver: relay for campfire %s: deliverer=%s author=%s", campfireID, senderHex, msg.SenderHex())
	}

	// Dedup check (spec §7.3): if message ID already seen, drop silently.
	// Check dedup BEFORE storing — a duplicate should not be re-stored or re-forwarded.
	// Return 200 so the sender doesn't retry.
	if h.transport != nil && h.transport.dedup != nil {
		if h.transport.dedup.See(msg.ID) {
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// Max hops check (spec §7.5): if provenance chain length >= MaxHops, drop.
	if len(msg.Provenance) >= MaxHops {
		log.Printf("handleDeliver: message %s for campfire %s exceeds max_hops (%d), dropping", msg.ID, campfireID, MaxHops)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Store in local SQLite
	if _, err := h.store.AddMessage(store.MessageRecordFromMessage(campfireID, &msg, store.NowNano())); err != nil {
		log.Printf("handleDeliver: failed to store message for campfire %s: %v", campfireID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Process routing:beacon and routing:withdraw tags for routing table updates.
	if h.transport != nil {
		for _, tag := range msg.Tags {
			switch tag {
			case "routing:beacon":
				if err := h.transport.routingTable.HandleBeacon(msg.Payload, campfireID); err != nil {
					log.Printf("handleDeliver: routing:beacon processing failed for campfire %s: %v", campfireID, err)
				}
			case "routing:withdraw":
				if err := h.transport.routingTable.HandleWithdraw(msg.Payload); err != nil {
					log.Printf("handleDeliver: routing:withdraw processing failed for campfire %s: %v", campfireID, err)
				}
			}
		}
	}

	// Forward the message to other peers (router forwarding, spec §7.2).
	if h.transport != nil {
		h.forwardMessage(campfireID, senderHex, &msg)
	}

	// Wake any long-polling goroutines waiting for new messages.
	if h.transport != nil && h.transport.pollBroker != nil {
		h.transport.pollBroker.Notify(campfireID)
	}

	w.WriteHeader(http.StatusOK)
}

// forwardMessage forwards a message to peers for the given campfire, excluding the sender.
// It appends a provenance hop signed by the campfire key before forwarding.
//
// Forwarding policy (spec §11.2):
//   - Default: forward only if this instance has the campfire key (locally-hosted campfire).
//   - Relay mode: forward for any campfire in the routing table (opt-in).
//
// If the keyProvider is not set, forwarding is skipped (no campfire key to sign hops).
func (h *handler) forwardMessage(campfireID, senderHex string, msg *message.Message) {
	kp := h.keyProvider
	if kp == nil && h.transport != nil {
		h.transport.mu.RLock()
		kp = h.transport.keyProvider
		h.transport.mu.RUnlock()
	}
	if kp == nil {
		// No key provider: cannot sign provenance hops; skip forwarding.
		return
	}

	privKeyBytes, pubKeyBytes, err := kp(campfireID)
	if err != nil {
		// Not a locally-hosted campfire; check relay mode.
		h.transport.mu.RLock()
		relayMode := h.transport.relayMode
		h.transport.mu.RUnlock()

		if !relayMode {
			// Default policy: only forward for locally-hosted campfires.
			return
		}
		// Relay mode: no campfire key, cannot sign hops. Skip.
		log.Printf("forwardMessage: relay mode enabled for campfire %s but no key available: %v", campfireID, err)
		return
	}

	campfirePriv := ed25519.PrivateKey(privKeyBytes)
	campfirePub := ed25519.PublicKey(pubKeyBytes)

	// Get campfire membership for provenance hop metadata.
	membership, err := h.store.GetMembership(campfireID)
	if err != nil || membership == nil {
		// Log but continue — we can still forward without membership metadata.
		log.Printf("forwardMessage: GetMembership failed for campfire %s: %v", campfireID, err)
	}

	var joinProtocol string
	if membership != nil {
		joinProtocol = membership.JoinProtocol
	}

	// Make a copy of the message to add provenance hop without mutating the original.
	fwdMsg := *msg
	fwdMsg.Provenance = make([]message.ProvenanceHop, len(msg.Provenance))
	copy(fwdMsg.Provenance, msg.Provenance)

	// Add provenance hop signed by campfire key (spec §7.4).
	if err := fwdMsg.AddHop(campfirePriv, campfirePub, nil, 0, joinProtocol, nil, ""); err != nil {
		log.Printf("forwardMessage: AddHop failed for campfire %s: %v", campfireID, err)
		return
	}

	// Build the forwarder identity from campfire keys.
	// The router signs requests as the campfire, not as an individual agent.
	fwdIdentity := &identity.Identity{
		PublicKey:  campfirePub,
		PrivateKey: campfirePriv,
	}

	// Collect forwarding targets: first check routing table, then fall back to local peers.
	var targetEndpoints []string
	routes := h.transport.routingTable.Lookup(campfireID)
	if len(routes) > 0 {
		for _, route := range routes {
			if route.Endpoint != "" {
				targetEndpoints = append(targetEndpoints, route.Endpoint)
			}
		}
	}

	// Also include locally known peers (from membership events).
	h.transport.mu.RLock()
	localPeers := make([]PeerInfo, len(h.transport.peers[campfireID]))
	copy(localPeers, h.transport.peers[campfireID])
	h.transport.mu.RUnlock()

	for _, peer := range localPeers {
		if peer.Endpoint == "" {
			continue
		}
		// Exclude the sender (prevent echo — spec §7.2 step 4).
		if peer.PubKeyHex == senderHex {
			continue
		}
		// Exclude already-targeted endpoints (from routing table).
		alreadyTargeted := false
		for _, ep := range targetEndpoints {
			if ep == peer.Endpoint {
				alreadyTargeted = true
				break
			}
		}
		if !alreadyTargeted {
			targetEndpoints = append(targetEndpoints, peer.Endpoint)
		}
	}

	if len(targetEndpoints) == 0 {
		return
	}

	// Forward in parallel (fire-and-forget, errors are logged not fatal).
	for _, ep := range targetEndpoints {
		go func(endpoint string) {
			if err := deliverMessage(endpoint, campfireID, &fwdMsg, fwdIdentity); err != nil {
				log.Printf("forwardMessage: deliver to %s for campfire %s failed: %v", endpoint, campfireID, err)
			}
		}(ep)
	}
}

// deliverMessage delivers a message to a peer endpoint, signing the request.
// This is the internal version that accepts raw ed25519 keys wrapped in identity.Identity.
func deliverMessage(endpoint, campfireID string, msg *message.Message, id *identity.Identity) error {
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encoding message: %w", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/deliver", endpoint, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	signRequest(req, id, body)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("peer returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
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
		if membership == nil || membership.CreatorPubkey == "" || senderHex != membership.CreatorPubkey {
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
// The signature covers: timestamp (as ASCII decimal Unix seconds string) || newline || nonce (hex string) || newline || body.
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
// Tags, Antecedents, and Provenance are already typed Go values on MessageRecord
// (JSON deserialization happens at the store boundary), so no unmarshaling is needed here.
func recordToMessage(rec store.MessageRecord) (message.Message, error) {
	senderBytes, err := hex.DecodeString(rec.Sender)
	if err != nil {
		return message.Message{}, fmt.Errorf("decoding sender: %w", err)
	}

	return message.Message{
		ID:          rec.ID,
		Sender:      senderBytes,
		Payload:     rec.Payload,
		Tags:        rec.Tags,
		Antecedents: rec.Antecedents,
		Timestamp:   rec.Timestamp,
		Signature:   rec.Signature,
		Provenance:  rec.Provenance,
		Instance:    rec.Instance,
	}, nil
}

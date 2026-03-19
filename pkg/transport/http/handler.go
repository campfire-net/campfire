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
	"os"
	"strconv"
	"strings"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	frostmessages "github.com/taurusgroup/frost-ed25519/pkg/messages"
)

// JoinRequest is the body sent by a joiner to POST /campfire/{id}/join.
type JoinRequest struct {
	// JoinerPubkey is the hex-encoded Ed25519 public key of the joining agent.
	JoinerPubkey string `json:"joiner_pubkey"`
	// JoinerEndpoint is the HTTP endpoint URL where the joiner listens (may be empty).
	JoinerEndpoint string `json:"joiner_endpoint"`
	// EphemeralX25519Pub is the joiner's ephemeral X25519 public key (hex) for key exchange.
	// The admitting member uses this to derive a shared secret and encrypt the campfire private key.
	EphemeralX25519Pub string `json:"ephemeral_x25519_pub"`
}

// PeerEntry is one member's pubkey + endpoint in the JoinResponse.
type PeerEntry struct {
	PubKeyHex     string `json:"pubkey"`
	Endpoint      string `json:"endpoint"`
	ParticipantID uint32 `json:"participant_id,omitempty"` // FROST participant ID (0 = unknown / threshold=1)
}

// JoinResponse is returned by the admitting member on success.
type JoinResponse struct {
	// EncryptedPrivKey is the campfire private key encrypted with AES-256-GCM.
	// The encryption key is derived via ECDH (joiner ephemeral X25519 + responder X25519).
	// Nil for threshold>1 (use ThresholdShareData instead).
	EncryptedPrivKey []byte `json:"encrypted_priv_key,omitempty"`
	// ResponderX25519Pub is the admitting member's ephemeral X25519 public key (hex).
	// The joiner uses this with its ephemeral private key to derive the same shared secret.
	ResponderX25519Pub string `json:"responder_x25519_pub,omitempty"`
	// CampfirePubKey is the campfire's Ed25519 public key (hex).
	CampfirePubKey string `json:"campfire_pub_key"`
	// JoinProtocol is the campfire's join protocol ("open" or "invite-only").
	JoinProtocol string `json:"join_protocol"`
	// ReceptionRequirements lists the required tags for messages.
	ReceptionRequirements []string `json:"reception_requirements"`
	// Threshold is the campfire's signing threshold.
	Threshold uint `json:"threshold"`
	// Peers is the list of known peer endpoints (including the admitting member).
	Peers []PeerEntry `json:"peers"`
	// ThresholdShareData is the joiner's FROST DKG share (threshold>1).
	// Serialized with threshold.MarshalResult. Encrypted with AES-256-GCM via ECDH.
	ThresholdShareData []byte `json:"threshold_share_data,omitempty"`
	// JoinerParticipantID is the FROST participant ID assigned to the joiner (threshold>1).
	JoinerParticipantID uint32 `json:"joiner_participant_id,omitempty"`
}

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

// route dispatches requests under /campfire/{id}/...
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

// handleDeliver receives a CBOR-encoded Message from a peer.
// POST /campfire/{id}/deliver
func (h *handler) handleDeliver(w http.ResponseWriter, r *http.Request, campfireID string) {
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

// handleJoin processes a join request from a new member.
// POST /campfire/{id}/join
// Body: JoinRequest (JSON)
// Returns: JoinResponse (JSON) — includes encrypted campfire private key + peer list.
func (h *handler) handleJoin(w http.ResponseWriter, r *http.Request, campfireID string) {
	// Prefer transport's key provider (set via SetKeyProvider), fall back to handler's.
	kp := h.keyProvider
	if h.transport != nil {
		h.transport.mu.RLock()
		if h.transport.keyProvider != nil {
			kp = h.transport.keyProvider
		}
		h.transport.mu.RUnlock()
	}
	if kp == nil {
		http.Error(w, "join not supported on this node", http.StatusNotImplemented)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	// Verify joiner's Ed25519 signature over the request body.
	senderHex := r.Header.Get("X-Campfire-Sender")
	sigB64 := r.Header.Get("X-Campfire-Signature")
	if senderHex == "" || sigB64 == "" {
		http.Error(w, "missing signature headers", http.StatusUnauthorized)
		return
	}
	if err := verifyRequestSignature(senderHex, sigB64, body); err != nil {
		log.Printf("handleJoin: signature verification failed: %v", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var req JoinRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// Fetch campfire private key from this node.
	privKey, pubKey, err := kp(campfireID)
	if err != nil {
		log.Printf("handleJoin: campfire %s not found: %v", campfireID, err)
		http.Error(w, "campfire not found", http.StatusNotFound)
		return
	}

	// Fetch campfire membership record for metadata.
	membership, err := h.store.GetMembership(campfireID)
	if err != nil || membership == nil {
		http.Error(w, "campfire membership not found", http.StatusNotFound)
		return
	}

	// Build response.
	resp := JoinResponse{
		CampfirePubKey:        fmt.Sprintf("%x", pubKey),
		JoinProtocol:          membership.JoinProtocol,
		ReceptionRequirements: []string{},
		Threshold:             membership.Threshold,
	}

	// Derive shared secret for key material encryption (used for both threshold=1 and threshold>1).
	var sharedSecret []byte
	if req.EphemeralX25519Pub != "" {
		joinerX25519PubHex, err := hex.DecodeString(req.EphemeralX25519Pub)
		if err != nil {
			http.Error(w, "invalid ephemeral X25519 public key", http.StatusBadRequest)
			return
		}
		joinerX25519, err := parseX25519PublicKey(joinerX25519PubHex)
		if err != nil {
			log.Printf("handleJoin: failed to parse joiner X25519 key: %v", err)
			http.Error(w, "invalid ephemeral X25519 public key", http.StatusBadRequest)
			return
		}
		respPriv, err := generateX25519Key()
		if err != nil {
			log.Printf("handleJoin: key generation failed: %v", err)
			http.Error(w, "key generation failed", http.StatusInternalServerError)
			return
		}
		respPub := respPriv.PublicKey()
		shared, err := respPriv.ECDH(joinerX25519)
		if err != nil {
			log.Printf("handleJoin: ECDH failed: %v", err)
			http.Error(w, "ECDH failed", http.StatusInternalServerError)
			return
		}
		sharedSecret = shared
		resp.ResponderX25519Pub = fmt.Sprintf("%x", respPub.Bytes())
	}

	// For threshold=1: encrypt and transmit the campfire private key.
	if membership.Threshold == 1 && sharedSecret != nil {
		encrypted, err := aesGCMEncrypt(sharedSecret, privKey)
		if err != nil {
			log.Printf("handleJoin: encryption failed for campfire %s: %v", campfireID, err)
			http.Error(w, "encryption failed", http.StatusInternalServerError)
			return
		}
		resp.EncryptedPrivKey = encrypted
	}

	// For threshold>1: distribute a pending DKG share to this joiner.
	var joinerParticipantID uint32
	if membership.Threshold > 1 && sharedSecret != nil {
		pid, shareData, err := h.store.ClaimPendingThresholdShare(campfireID)
		if err != nil {
			log.Printf("handleJoin: failed to claim threshold share for campfire %s: %v", campfireID, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if shareData != nil {
			encrypted, err := aesGCMEncrypt(sharedSecret, shareData)
			if err != nil {
				log.Printf("handleJoin: failed to encrypt threshold share for campfire %s: %v", campfireID, err)
				http.Error(w, "encrypting threshold share failed", http.StatusInternalServerError)
				return
			}
			resp.ThresholdShareData = encrypted
			resp.JoinerParticipantID = pid
			joinerParticipantID = pid
		}
	}

	// Add peer endpoints from persistent store (includes participant IDs).
	storedPeers, _ := h.store.ListPeerEndpoints(campfireID)
	for _, p := range storedPeers {
		resp.Peers = append(resp.Peers, PeerEntry{
			PubKeyHex:     p.MemberPubkey,
			Endpoint:      p.Endpoint,
			ParticipantID: p.ParticipantID,
		})
	}

	// Also add the admitting member's own endpoint if known.
	selfPubHex, selfEndpoint := h.transport.SelfInfo()
	if selfEndpoint != "" && selfPubHex != "" {
		// Avoid duplicate if already stored.
		found := false
		for _, p := range resp.Peers {
			if p.PubKeyHex == selfPubHex {
				found = true
				break
			}
		}
		if !found {
			// Admitting member is participant 1.
			selfPID := uint32(1)
			if membership.Threshold <= 1 {
				selfPID = 0
			}
			resp.Peers = append(resp.Peers, PeerEntry{
				PubKeyHex:     selfPubHex,
				Endpoint:      selfEndpoint,
				ParticipantID: selfPID,
			})
		}
	}

	// Persist the joiner's endpoint, including participant ID for threshold>1.
	if req.JoinerEndpoint != "" {
		h.store.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
			CampfireID:    campfireID,
			MemberPubkey:  req.JoinerPubkey,
			Endpoint:      req.JoinerEndpoint,
			ParticipantID: joinerParticipantID,
		})
		h.transport.AddPeer(campfireID, req.JoinerPubkey, req.JoinerEndpoint)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// handleSign processes a FROST signing round message from the initiator.
// POST /campfire/{id}/sign
// Body: SignRoundRequest (JSON)
// Returns: SignRoundResponse (JSON)
// This endpoint is ephemeral — no messages are stored in history.
func (h *handler) handleSign(w http.ResponseWriter, r *http.Request, campfireID string) {
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

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	// Verify sender signature.
	senderHex := r.Header.Get("X-Campfire-Sender")
	sigB64 := r.Header.Get("X-Campfire-Signature")
	if senderHex == "" || sigB64 == "" {
		http.Error(w, "missing signature headers", http.StatusUnauthorized)
		return
	}
	if err := verifyRequestSignature(senderHex, sigB64, body); err != nil {
		log.Printf("handleSign: signature verification failed: %v", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
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
		// The session key includes the sessionID to be unique per signing protocol.
		h.transport.mu.Lock()
		ss, err := h.transport.getOrCreateSignSession(req.SessionID, req.SignerIDs, req.MessageToSign, dkgResult, participantID)
		h.transport.mu.Unlock()
		if err != nil {
			log.Printf("handleSign: failed to create signing session %s for campfire %s: %v", req.SessionID, campfireID, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

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

		// Return this participant's commitment messages (the initiator needs them).
		for _, m := range commitMsgs {
			b, err := m.MarshalBinary()
			if err != nil {
				continue
			}
			outRaw = append(outRaw, b)
		}
	} else {
		// Round 2: look up the existing session, advance state, deliver inbound share messages.
		h.transport.mu.RLock()
		sessionState, ok := h.transport.signSessions[req.SessionID]
		h.transport.mu.RUnlock()
		if !ok {
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

		// Return all outbound messages: own shares + any additional output.
		allOut := append(sharesMsgs, additionalMsgs...)
		for _, m := range allOut {
			b, err := m.MarshalBinary()
			if err != nil {
				continue
			}
			outRaw = append(outRaw, b)
		}

		// Clean up after round 2.
		h.transport.removeSignSession(req.SessionID)
	}

	resp := SignRoundResponse{Messages: outRaw}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
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

// RekeyRequest is the body for POST /campfire/{old-id}/rekey.
// Two-phase protocol (same endpoint, distinguished by presence of encrypted payload):
//
//	Phase 1: EncryptedPrivKey and EncryptedShareData are both empty.
//	         Receiver generates an ephemeral X25519 keypair, caches it keyed by
//	         SenderX25519Pub, and responds with RekeyResponse{EphemeralX25519Pub}.
//
//	Phase 2: EncryptedPrivKey or EncryptedShareData is non-empty.
//	         Receiver looks up its cached ephemeral key by SenderX25519Pub,
//	         derives shared = ECDH(receiver_priv, sender_pub), decrypts,
//	         updates state, and responds 200 OK with no body.
type RekeyRequest struct {
	// NewCampfireID is the new campfire public key (hex).
	NewCampfireID string `json:"new_campfire_id"`
	// SenderX25519Pub is the sender's ephemeral X25519 public key (hex).
	// The receiver uses this in ECDH with its own ephemeral key.
	SenderX25519Pub string `json:"sender_x25519_pub"`
	// EncryptedPrivKey is the new campfire private key, AES-256-GCM encrypted.
	// Used for threshold=1. Empty in phase 1.
	EncryptedPrivKey []byte `json:"encrypted_priv_key,omitempty"`
	// EncryptedShareData is the new FROST DKG share for this peer, AES-256-GCM encrypted.
	// Used for threshold>1. Empty in phase 1.
	EncryptedShareData []byte `json:"encrypted_share_data,omitempty"`
	// NewParticipantID is the FROST participant ID for the new DKG (threshold>1).
	NewParticipantID uint32 `json:"new_participant_id,omitempty"`
	// RekeyMessageCBOR is the CBOR-encoded campfire:rekey system message,
	// signed by the OLD campfire key.
	RekeyMessageCBOR []byte `json:"rekey_message_cbor"`
	// EvictedMemberPubkey is the hex public key of the evicted member.
	EvictedMemberPubkey string `json:"evicted_member_pubkey"`
}

// RekeyResponse is returned during phase 1 of the rekey protocol.
type RekeyResponse struct {
	// EphemeralX25519Pub is the receiver's ephemeral X25519 public key (hex).
	// The sender uses this with its own ephemeral private key to derive the shared secret.
	EphemeralX25519Pub string `json:"ephemeral_x25519_pub,omitempty"`
}

// handleRekey implements the two-phase rekey protocol.
// POST /campfire/{old-id}/rekey
//
// Phase 1 (no encrypted payload): generate receiver ephemeral key, cache it, return pub key.
// Phase 2 (with encrypted payload): look up cached key, derive shared secret, decrypt, update state.
func (h *handler) handleRekey(w http.ResponseWriter, r *http.Request, oldCampfireID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	// Verify sender signature.
	senderHex := r.Header.Get("X-Campfire-Sender")
	sigB64 := r.Header.Get("X-Campfire-Signature")
	if senderHex == "" || sigB64 == "" {
		http.Error(w, "missing signature headers", http.StatusUnauthorized)
		return
	}
	if err := verifyRequestSignature(senderHex, sigB64, body); err != nil {
		log.Printf("handleRekey: signature verification failed: %v", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var req RekeyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.NewCampfireID == "" {
		http.Error(w, "missing new_campfire_id", http.StatusBadRequest)
		return
	}
	if req.SenderX25519Pub == "" {
		http.Error(w, "missing sender_x25519_pub", http.StatusBadRequest)
		return
	}

	// Verify we are a member of the old campfire.
	membership, err := h.store.GetMembership(oldCampfireID)
	if err != nil || membership == nil {
		http.Error(w, "not a member of this campfire", http.StatusNotFound)
		return
	}

	isPhase1 := len(req.EncryptedPrivKey) == 0 && len(req.EncryptedShareData) == 0

	if isPhase1 {
		// Phase 1: generate receiver ephemeral key, cache it, return pub key.
		myPriv, err := generateX25519Key()
		if err != nil {
			log.Printf("handleRekey: key generation failed: %v", err)
			http.Error(w, "key generation failed", http.StatusInternalServerError)
			return
		}

		// Cache by sender's X25519 pub key hex.
		h.transport.mu.Lock()
		h.transport.storeRekeySession(req.SenderX25519Pub, myPriv)
		h.transport.mu.Unlock()

		resp := RekeyResponse{
			EphemeralX25519Pub: fmt.Sprintf("%x", myPriv.PublicKey().Bytes()),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
		return
	}

	// Phase 2: look up cached receiver key, derive shared secret, decrypt, update state.
	h.transport.mu.Lock()
	myPriv := h.transport.claimRekeySession(req.SenderX25519Pub)
	h.transport.mu.Unlock()

	if myPriv == nil {
		http.Error(w, "no pending rekey session for this sender key (call phase 1 first)", http.StatusBadRequest)
		return
	}

	// Parse sender's X25519 pub key.
	senderPubBytes, err := hex.DecodeString(req.SenderX25519Pub)
	if err != nil {
		http.Error(w, "invalid sender_x25519_pub", http.StatusBadRequest)
		return
	}
	senderPub, err := parseX25519PublicKey(senderPubBytes)
	if err != nil {
		log.Printf("handleRekey: failed to parse sender X25519 key: %v", err)
		http.Error(w, "invalid sender_x25519_pub", http.StatusBadRequest)
		return
	}

	// Derive shared secret.
	sharedSecret, err := myPriv.ECDH(senderPub)
	if err != nil {
		log.Printf("handleRekey: ECDH failed: %v", err)
		http.Error(w, "ECDH failed", http.StatusInternalServerError)
		return
	}

	newCampfireID := req.NewCampfireID
	newPubKeyBytes, err := hex.DecodeString(newCampfireID)
	if err != nil {
		http.Error(w, "invalid new_campfire_id hex", http.StatusBadRequest)
		return
	}

	// Decrypt key material.
	var newPrivKey []byte
	var newShareData []byte

	if len(req.EncryptedPrivKey) > 0 {
		newPrivKey, err = aesGCMDecrypt(sharedSecret, req.EncryptedPrivKey)
		if err != nil {
			log.Printf("handleRekey: failed to decrypt private key for campfire %s: %v", oldCampfireID, err)
			http.Error(w, "decryption failed", http.StatusBadRequest)
			return
		}
	}
	if len(req.EncryptedShareData) > 0 {
		newShareData, err = aesGCMDecrypt(sharedSecret, req.EncryptedShareData)
		if err != nil {
			log.Printf("handleRekey: failed to decrypt share data for campfire %s: %v", oldCampfireID, err)
			http.Error(w, "decryption failed", http.StatusBadRequest)
			return
		}
	}

	// Update campfire state file.
	stateFile := membership.TransportDir + "/" + oldCampfireID + ".cbor"
	stateData, readErr := os.ReadFile(stateFile)
	if readErr == nil {
		var oldState campfireStateForRekey
		cfencoding.Unmarshal(stateData, &oldState) //nolint:errcheck

		newState := oldState
		newState.PublicKey = newPubKeyBytes
		if len(newPrivKey) > 0 {
			newState.PrivateKey = newPrivKey
		} else {
			newState.PrivateKey = nil
		}
		if newStateData, marshalErr := cfencoding.Marshal(newState); marshalErr == nil {
			newStateFile := membership.TransportDir + "/" + newCampfireID + ".cbor"
			os.WriteFile(newStateFile, newStateData, 0600) //nolint:errcheck
		}
		os.Remove(stateFile) //nolint:errcheck
	}

	// Update store: rename campfire_id in all tables.
	if err := h.store.UpdateCampfireID(oldCampfireID, newCampfireID); err != nil {
		log.Printf("handleRekey: failed to update campfire ID %s -> %s: %v", oldCampfireID, newCampfireID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Update in-memory peer list.
	h.transport.mu.Lock()
	if peers, ok := h.transport.peers[oldCampfireID]; ok {
		h.transport.peers[newCampfireID] = peers
		delete(h.transport.peers, oldCampfireID)
	}
	h.transport.mu.Unlock()

	// Remove evicted member from peer list.
	if req.EvictedMemberPubkey != "" {
		h.transport.RemovePeer(newCampfireID, req.EvictedMemberPubkey)
		h.store.DeletePeerEndpoint(newCampfireID, req.EvictedMemberPubkey) //nolint:errcheck
	}

	// Store new FROST DKG share if provided (threshold>1).
	if len(newShareData) > 0 {
		h.store.UpsertThresholdShare(store.ThresholdShare{ //nolint:errcheck
			CampfireID:    newCampfireID,
			ParticipantID: req.NewParticipantID,
			SecretShare:   newShareData,
		})
	}

	// Store the rekey system message.
	if len(req.RekeyMessageCBOR) > 0 {
		var rekeyMsg message.Message
		if cfencoding.Unmarshal(req.RekeyMessageCBOR, &rekeyMsg) == nil {
			h.store.AddMessage(store.MessageRecordFromMessage(newCampfireID, &rekeyMsg, store.NowNano())) //nolint:errcheck
		}
	}

	w.WriteHeader(http.StatusOK)
}

// campfireStateForRekey is used for reading/writing campfire state in the rekey handler
// without importing the campfire package (to avoid circular deps).
type campfireStateForRekey struct {
	PublicKey             []byte   `cbor:"1,keyasint"`
	PrivateKey            []byte   `cbor:"2,keyasint"`
	JoinProtocol          string   `cbor:"3,keyasint"`
	ReceptionRequirements []string `cbor:"4,keyasint"`
	CreatedAt             int64    `cbor:"5,keyasint"`
	Threshold             uint     `cbor:"6,keyasint"`
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

package http

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	cfencoding "github.com/3dl-dev/campfire/pkg/encoding"
	"github.com/3dl-dev/campfire/pkg/message"
	"github.com/3dl-dev/campfire/pkg/store"
	"github.com/3dl-dev/campfire/pkg/threshold"
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
	case action == "membership" && r.Method == http.MethodPost:
		h.handleMembership(w, r, campfireID)
	case action == "join" && r.Method == http.MethodPost:
		h.handleJoin(w, r, campfireID)
	case action == "sign" && r.Method == http.MethodPost:
		h.handleSign(w, r, campfireID)
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
		http.Error(w, fmt.Sprintf("invalid signature: %v", err), http.StatusUnauthorized)
		return
	}

	// Decode message
	var msg message.Message
	if err := cfencoding.Unmarshal(body, &msg); err != nil {
		http.Error(w, "invalid CBOR body", http.StatusBadRequest)
		return
	}

	// Store in local SQLite
	tagsJSON, _ := json.Marshal(msg.Tags)
	anteJSON, _ := json.Marshal(msg.Antecedents)
	provJSON, _ := json.Marshal(msg.Provenance)
	rec := store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      fmt.Sprintf("%x", msg.Sender),
		Payload:     msg.Payload,
		Tags:        string(tagsJSON),
		Antecedents: string(anteJSON),
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
		Provenance:  string(provJSON),
		ReceivedAt:  time.Now().UnixNano(),
	}
	if _, err := h.store.AddMessage(rec); err != nil {
		http.Error(w, "failed to store message", http.StatusInternalServerError)
		return
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
		http.Error(w, fmt.Sprintf("invalid signature: %v", err), http.StatusUnauthorized)
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
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/cbor")
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
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
		http.Error(w, fmt.Sprintf("invalid signature: %v", err), http.StatusUnauthorized)
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
		http.Error(w, fmt.Sprintf("invalid signature: %v", err), http.StatusUnauthorized)
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
		http.Error(w, fmt.Sprintf("campfire not found: %v", err), http.StatusNotFound)
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
			http.Error(w, fmt.Sprintf("parsing joiner X25519 key: %v", err), http.StatusBadRequest)
			return
		}
		respPriv, err := generateX25519Key()
		if err != nil {
			http.Error(w, "key generation failed", http.StatusInternalServerError)
			return
		}
		respPub := respPriv.PublicKey()
		shared, err := respPriv.ECDH(joinerX25519)
		if err != nil {
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
			http.Error(w, fmt.Sprintf("claiming threshold share: %v", err), http.StatusInternalServerError)
			return
		}
		if shareData != nil {
			encrypted, err := aesGCMEncrypt(sharedSecret, shareData)
			if err != nil {
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

	_ = joinerParticipantID // used in ThresholdShareData response

	// Persist the joiner's endpoint.
	if req.JoinerEndpoint != "" {
		h.store.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
			CampfireID:   campfireID,
			MemberPubkey: req.JoinerPubkey,
			Endpoint:     req.JoinerEndpoint,
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
		http.Error(w, fmt.Sprintf("invalid signature: %v", err), http.StatusUnauthorized)
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
		http.Error(w, fmt.Sprintf("loading threshold share: %v", err), http.StatusNotFound)
		return
	}
	_, dkgResult, err := threshold.UnmarshalResult(shareData)
	if err != nil {
		http.Error(w, fmt.Sprintf("deserializing threshold share: %v", err), http.StatusInternalServerError)
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
			http.Error(w, fmt.Sprintf("creating signing session: %v", err), http.StatusInternalServerError)
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
		// Round 2: look up the existing session and deliver inbound share messages.
		h.transport.mu.RLock()
		sessionState, ok := h.transport.signSessions[req.SessionID]
		h.transport.mu.RUnlock()
		if !ok {
			http.Error(w, "signing session not found (round 2 without round 1?)", http.StatusBadRequest)
			return
		}
		ss := sessionState.session

		// Deliver round-1 completion and generate round-2 share messages.
		// First call ProcessAll to advance from round-1 state.
		_ = ss.ProcessAll()

		// Deliver inbound round-2 messages from the initiator.
		for _, raw := range req.Messages {
			var msg frostmessages.Message
			if err := msg.UnmarshalBinary(raw); err != nil {
				continue
			}
			ss.Deliver(&msg) //nolint:errcheck
		}

		// ProcessAll generates round-2 output (share messages to initiator).
		outMsgs := ss.ProcessAll()
		for _, m := range outMsgs {
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
	}, nil
}

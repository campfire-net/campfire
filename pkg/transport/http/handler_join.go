package http

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/campfire-net/campfire/pkg/store"
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

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
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

	// Validate JoinerEndpoint to prevent SSRF: reject private IPs, non-http schemes, etc.
	if err := validateJoinerEndpoint(req.JoinerEndpoint); err != nil {
		log.Printf("handleJoin: invalid joiner endpoint %q: %v", req.JoinerEndpoint, err)
		http.Error(w, "invalid joiner_endpoint", http.StatusBadRequest)
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

	// Enforce invite-only protocol: reject unadmitted joiners server-side.
	// The joiner must already be in the peer list to be admitted.
	// (Invite-only campfires require the creator to pre-add the joiner's pubkey.)
	if membership.JoinProtocol == "invite-only" {
		admitted := false
		if peers, err := h.store.ListPeerEndpoints(campfireID); err == nil {
			for _, p := range peers {
				if p.MemberPubkey == senderHex {
					admitted = true
					break
				}
			}
		}
		// Also allow self (transport node) to admit itself.
		selfPubHex, _ := h.transport.SelfInfo()
		if senderHex == selfPubHex {
			admitted = true
		}
		if !admitted {
			http.Error(w, "campfire is invite-only", http.StatusForbidden)
			return
		}
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
		derivedKey, err := HkdfSHA256(shared, "campfire-join-v1")
		if err != nil {
			log.Printf("handleJoin: key derivation failed: %v", err)
			http.Error(w, "key derivation failed", http.StatusInternalServerError)
			return
		}
		sharedSecret = derivedKey
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
			MemberPubkey:  senderHex,
			Endpoint:      req.JoinerEndpoint,
			ParticipantID: joinerParticipantID,
		})
		h.transport.AddPeer(campfireID, senderHex, req.JoinerEndpoint)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

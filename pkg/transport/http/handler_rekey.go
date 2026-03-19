package http

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

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
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
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

	// Membership check: only current members may initiate a rekey.
	if !h.checkMembership(w, oldCampfireID, senderHex) {
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

	// Creator-only check: only the campfire creator may trigger a rekey.
	// For legacy records where CreatorPubkey is empty, skip to preserve backward compat.
	if membership.CreatorPubkey != "" && senderHex != membership.CreatorPubkey {
		log.Printf("handleRekey: sender %s is not creator %s for campfire %s", senderHex, membership.CreatorPubkey, oldCampfireID)
		http.Error(w, "only the campfire creator may trigger a rekey", http.StatusForbidden)
		return
	}

	// Verify rekey message signature against stored campfire public key (oldCampfireID),
	// NOT the attacker-controlled rekeyMsg.Sender field.
	if len(req.RekeyMessageCBOR) > 0 {
		var rekeyMsgForVerify message.Message
		if cfencoding.Unmarshal(req.RekeyMessageCBOR, &rekeyMsgForVerify) == nil && len(rekeyMsgForVerify.Signature) > 0 {
			campfirePubKeyBytes, hexErr := hex.DecodeString(oldCampfireID)
			if hexErr == nil && len(campfirePubKeyBytes) == ed25519.PublicKeySize {
				signInput := message.MessageSignInput{
					ID:          rekeyMsgForVerify.ID,
					Payload:     rekeyMsgForVerify.Payload,
					Tags:        rekeyMsgForVerify.Tags,
					Antecedents: rekeyMsgForVerify.Antecedents,
					Timestamp:   rekeyMsgForVerify.Timestamp,
				}
				signBytes, marshalErr := cfencoding.Marshal(signInput)
				if marshalErr != nil {
					log.Printf("handleRekey: failed to marshal sign input for campfire %s: %v", oldCampfireID, marshalErr)
					http.Error(w, "internal server error", http.StatusInternalServerError)
					return
				}
				if !ed25519.Verify(ed25519.PublicKey(campfirePubKeyBytes), signBytes, rekeyMsgForVerify.Signature) {
					log.Printf("handleRekey: rekey message signature invalid for campfire %s", oldCampfireID)
					http.Error(w, "rekey message signature invalid", http.StatusUnauthorized)
					return
				}
			}
		}
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

	// Derive shared secret via ECDH + HKDF (RFC 5869, SHA-256).
	// Raw X25519 output must not be used directly as an AES key.
	rawShared, err := myPriv.ECDH(senderPub)
	if err != nil {
		log.Printf("handleRekey: ECDH failed: %v", err)
		http.Error(w, "ECDH failed", http.StatusInternalServerError)
		return
	}
	sharedSecret, err := HkdfSHA256(rawShared, "campfire-rekey-v1")
	if err != nil {
		log.Printf("handleRekey: key derivation failed: %v", err)
		http.Error(w, "key derivation failed", http.StatusInternalServerError)
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

	// Update store FIRST: rename campfire_id in all tables.
	// If this fails, leave state files untouched so the campfire remains recoverable.
	if err := h.store.UpdateCampfireID(oldCampfireID, newCampfireID); err != nil {
		log.Printf("handleRekey: failed to update campfire ID %s -> %s: %v", oldCampfireID, newCampfireID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Update campfire state file only after the DB update succeeds.
	safeTransportDir, sanitizeErr := sanitizeTransportDir(membership.TransportDir)
	if sanitizeErr != nil {
		log.Printf("handleRekey: invalid transport dir for campfire %s: %v", oldCampfireID, sanitizeErr)
		http.Error(w, "invalid transport dir", http.StatusInternalServerError)
		return
	}
	stateFile := filepath.Join(safeTransportDir, oldCampfireID+".cbor")
	stateData, readErr := os.ReadFile(stateFile)
	if readErr == nil {
		var oldState campfire.CampfireState
		cfencoding.Unmarshal(stateData, &oldState) //nolint:errcheck

		newState := oldState
		newState.PublicKey = newPubKeyBytes
		if len(newPrivKey) > 0 {
			newState.PrivateKey = newPrivKey
		} else {
			newState.PrivateKey = nil
		}
		if newStateData, marshalErr := cfencoding.Marshal(newState); marshalErr == nil {
			newStateFile := filepath.Join(safeTransportDir, newCampfireID+".cbor")
			if writeErr := os.WriteFile(newStateFile, newStateData, 0600); writeErr != nil {
				log.Printf("handleRekey: failed to write new state file for %s: %v", newCampfireID, writeErr)
			} else {
				if removeErr := os.Remove(stateFile); removeErr != nil {
					log.Printf("handleRekey: failed to remove old state file for %s: %v", oldCampfireID, removeErr)
				}
			}
		}
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

	// Store the rekey system message. Signature was already verified above against
	// the stored campfire public key (not the attacker-controlled Sender field).
	if len(req.RekeyMessageCBOR) > 0 {
		var rekeyMsg message.Message
		if cfencoding.Unmarshal(req.RekeyMessageCBOR, &rekeyMsg) == nil {
			h.store.AddMessage(store.MessageRecordFromMessage(newCampfireID, &rekeyMsg, store.NowNano())) //nolint:errcheck
		}
	}

	w.WriteHeader(http.StatusOK)
}

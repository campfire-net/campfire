package http

import (
	"context"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// contextKey is the unexported type used for request-context values set by auth middleware.
type contextKey int

const (
	// ctxSenderHex is the hex-encoded Ed25519 public key of the verified sender.
	ctxSenderHex contextKey = iota
	// ctxBody is the raw request body, already read by auth middleware.
	ctxBody
)

// maxRequestBodySize is the maximum number of bytes accepted in any request body.
const maxRequestBodySize = 4 * 1024 * 1024 // 4 MiB

// validateJoinerEndpoint and isPrivateIP are defined in ssrf.go.
// sanitizeTransportDir is defined in validate.go.
// MembershipEvent and CampfireKeyProvider are defined in types.go.

type handler struct {
	store     *store.Store
	transport *Transport
	// keyProvider is read from transport.keyProvider at call time.
	// Kept here for backward-compat test construction; transport takes precedence.
	keyProvider CampfireKeyProvider
}

// checkMembership verifies that senderHex is a member of campfireID on this node.
// Returns true if the sender is the local self key or appears in the stored peer list.
// Returns false with a ready-to-send 403 if not a member.
func (h *handler) checkMembership(w http.ResponseWriter, campfireID, senderHex string) (ok bool) {
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
		return false
	}
	return true
}

// authMiddleware verifies the Ed25519 request signature and campfire membership,
// then stores the verified senderHex and (for POST/PUT methods) the body bytes
// in the request context for downstream handlers to consume without re-reading.
//
// For GET/HEAD/DELETE (no body), the signature covers an empty byte slice.
// For POST/PUT/PATCH, the body is read here and stored in context; handlers
// must retrieve it via ctxBody rather than reading r.Body again.
func (h *handler) authMiddleware(campfireID string, next func(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		senderHex, body, ok := h.readAndVerify(w, r)
		if !ok {
			return
		}
		if !h.checkMembership(w, campfireID, senderHex) {
			return
		}
		ctx := context.WithValue(r.Context(), ctxSenderHex, senderHex)
		ctx = context.WithValue(ctx, ctxBody, body)
		next(w, r.WithContext(ctx), campfireID, senderHex, body)
	}
}

// signatureOnlyMiddleware verifies the Ed25519 request signature but does NOT
// check campfire membership. Used for handleJoin: the joiner is not yet a member.
func (h *handler) signatureOnlyMiddleware(campfireID string, next func(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		senderHex, body, ok := h.readAndVerify(w, r)
		if !ok {
			return
		}
		ctx := context.WithValue(r.Context(), ctxSenderHex, senderHex)
		ctx = context.WithValue(ctx, ctxBody, body)
		next(w, r.WithContext(ctx), campfireID, senderHex, body)
	}
}

// requestTimestampWindow is the maximum allowed age (or future skew) of a signed request.
// Requests with a timestamp older than this window are rejected as stale replays.
const requestTimestampWindow = 60 * time.Second

// readAndVerify extracts and validates the X-Campfire-Sender / X-Campfire-Signature /
// X-Campfire-Nonce / X-Campfire-Timestamp headers, reads the body (for methods with a
// body), verifies the Ed25519 signature, enforces timestamp freshness, and checks nonce
// uniqueness. Returns (senderHex, body, true) on success or writes an HTTP error and
// returns ("", nil, false) on failure.
//
// Replay protection: the signature covers timestamp+nonce+body. The server rejects
// requests with timestamps outside the ±60s window and nonces it has already seen.
func (h *handler) readAndVerify(w http.ResponseWriter, r *http.Request) (senderHex string, body []byte, ok bool) {
	senderHex = r.Header.Get("X-Campfire-Sender")
	sigB64 := r.Header.Get("X-Campfire-Signature")
	nonce := r.Header.Get("X-Campfire-Nonce")
	timestamp := r.Header.Get("X-Campfire-Timestamp")
	if senderHex == "" || sigB64 == "" || nonce == "" || timestamp == "" {
		http.Error(w, "missing signature headers", http.StatusUnauthorized)
		return "", nil, false
	}

	// Validate timestamp freshness.
	tsUnix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		http.Error(w, "invalid timestamp header", http.StatusUnauthorized)
		return "", nil, false
	}
	tsTime := time.Unix(tsUnix, 0)
	now := time.Now()
	diff := now.Sub(tsTime)
	if diff < 0 {
		diff = -diff
	}
	if diff > requestTimestampWindow {
		http.Error(w, "request timestamp out of window", http.StatusUnauthorized)
		return "", nil, false
	}

	// For request methods that carry a body, read it once here.
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		body, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read body", http.StatusBadRequest)
			return "", nil, false
		}
	default:
		body = []byte{}
	}

	if err := verifyRequestSignature(senderHex, sigB64, nonce, timestamp, body); err != nil {
		log.Printf("auth: signature verification failed for %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return "", nil, false
	}

	// Check nonce uniqueness — reject replays.
	if h.transport != nil {
		if !h.transport.consumeNonce(nonce) {
			log.Printf("auth: duplicate nonce %s for %s %s", nonce, r.Method, r.URL.Path)
			http.Error(w, "duplicate request nonce", http.StatusUnauthorized)
			return "", nil, false
		}
	}

	return senderHex, body, true
}

// route dispatches requests under /campfire/{id}/...
// Endpoint implementations live in handler_message.go, handler_join.go,
// handler_sign.go, and handler_rekey.go.
//
// Auth is applied here via middleware so individual handlers do not duplicate
// the signature-verification + membership-check preamble:
//   - authMiddleware: signature + membership (deliver, sync, poll, membership, sign, rekey)
//   - signatureOnlyMiddleware: signature only (join — joiner is not yet a member)
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
		h.authMiddleware(campfireID, h.handleDeliver)(w, r)
	case action == "sync" && r.Method == http.MethodGet:
		h.authMiddleware(campfireID, h.handleSync)(w, r)
	case action == "poll" && r.Method == http.MethodGet:
		h.authMiddleware(campfireID, h.handlePoll)(w, r)
	case action == "membership" && r.Method == http.MethodPost:
		h.authMiddleware(campfireID, h.handleMembership)(w, r)
	case action == "join" && r.Method == http.MethodPost:
		h.signatureOnlyMiddleware(campfireID, h.handleJoin)(w, r)
	case action == "sign" && r.Method == http.MethodPost:
		h.authMiddleware(campfireID, h.handleSign)(w, r)
	case action == "rekey" && r.Method == http.MethodPost:
		h.authMiddleware(campfireID, h.handleRekey)(w, r)
	default:
		http.NotFound(w, r)
	}
}

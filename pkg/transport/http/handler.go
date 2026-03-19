package http

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

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

// validateJoinerEndpoint checks that a joiner-supplied endpoint URL is safe to
// contact. It rejects:
//   - non-http/https schemes (e.g. file://)
//   - bare IP addresses in private/loopback/link-local ranges
//   - hostnames that resolve exclusively to private/loopback addresses
func validateJoinerEndpoint(rawURL string) error {
	if rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("endpoint scheme %q not allowed (must be http or https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("endpoint has no host")
	}
	ip := net.ParseIP(host)
	if ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("endpoint IP %s is in a private/reserved range", ip)
		}
		return nil
	}
	// Resolve hostname and check each address.
	addrs, err := net.LookupHost(host)
	if err != nil {
		// If we cannot resolve the host, reject it — better safe than sorry.
		return fmt.Errorf("cannot resolve endpoint host %q: %w", host, err)
	}
	for _, a := range addrs {
		parsed := net.ParseIP(a)
		if parsed != nil && isPrivateIP(parsed) {
			return fmt.Errorf("endpoint host %q resolves to private/reserved address %s", host, a)
		}
	}
	return nil
}

// isPrivateIP reports whether ip is a loopback, private, link-local,
// or other address that should not be dialled by a server-side handler.
func isPrivateIP(ip net.IP) bool {
	private := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"::1/128",
		"169.254.0.0/16",
		"fe80::/10",
		"fc00::/7",
		"0.0.0.0/8",
		"100.64.0.0/10", // CGNAT
		"198.18.0.0/15", // benchmarking
		"240.0.0.0/4",   // reserved
	}
	for _, cidr := range private {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// sanitizeTransportDir validates a TransportDir value from a membership record
// and returns the cleaned absolute path. It rejects paths that are not absolute
// or that contain ".." components, defending against path traversal attacks.
func sanitizeTransportDir(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("transport dir is empty")
	}
	// Clean the path (resolves any . and .. elements).
	clean := filepath.Clean(dir)
	// After cleaning, the path must still be absolute.
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("transport dir %q is not an absolute path", dir)
	}
	// Reject if the original path contained ".." segments (pre-clean check).
	// filepath.Clean resolves them, but we want to reject stored values that
	// include traversal markers — they indicate a tampered record.
	if strings.Contains(dir, "..") {
		return "", fmt.Errorf("transport dir %q contains path traversal", dir)
	}
	return clean, nil
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

// readAndVerify extracts and validates the X-Campfire-Sender / X-Campfire-Signature
// headers, reads the body (for methods with a body), verifies the Ed25519 signature,
// and returns (senderHex, body, true) on success or writes an HTTP error and
// returns ("", nil, false) on failure.
func (h *handler) readAndVerify(w http.ResponseWriter, r *http.Request) (senderHex string, body []byte, ok bool) {
	senderHex = r.Header.Get("X-Campfire-Sender")
	sigB64 := r.Header.Get("X-Campfire-Signature")
	if senderHex == "" || sigB64 == "" {
		http.Error(w, "missing signature headers", http.StatusUnauthorized)
		return "", nil, false
	}

	// For request methods that carry a body, read it once here.
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read body", http.StatusBadRequest)
			return "", nil, false
		}
	default:
		body = []byte{}
	}

	if err := verifyRequestSignature(senderHex, sigB64, body); err != nil {
		log.Printf("auth: signature verification failed for %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return "", nil, false
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

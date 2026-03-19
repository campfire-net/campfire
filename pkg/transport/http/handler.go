package http

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/campfire-net/campfire/pkg/store"
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

// route dispatches requests under /campfire/{id}/...
// Endpoint implementations live in handler_message.go, handler_join.go,
// handler_sign.go, and handler_rekey.go.
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

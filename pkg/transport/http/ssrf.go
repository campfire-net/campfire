package http

import (
	"context"
	"fmt"
	"net"
	nethttp "net/http"
	"net/url"
	"time"
)

// validateJoinerEndpoint validates that the given endpoint URL is safe to use
// as a peer endpoint. It protects against SSRF by:
//
//  1. Parsing and validating the URL structure (must be http/https, must have a host).
//  2. Resolving the hostname and checking all resolved IPs against the private-range
//     blocklist at validation time.
//  3. Installing a custom DialContext on the shared httpClient that re-validates the
//     resolved IP at actual connection time (time-of-use check) to defeat DNS rebinding.
//
// The time-of-use guard works by wrapping the default resolver: after the OS/library
// resolves the address for an outbound connection, the DialContext hook checks the IP
// before allowing the TCP connection to proceed. This closes the TOCTOU window.
//
// If endpoint is empty, the call is a no-op (empty endpoint is allowed — peer has no
// reachable address, which is valid for receive-only members).
// validateJoinerEndpointFunc is the active validation function. Override in tests
// to allow loopback endpoints.
var validateJoinerEndpointFunc = validateJoinerEndpointImpl

func validateJoinerEndpoint(endpoint string) error {
	return validateJoinerEndpointFunc(endpoint)
}

// OverrideValidateJoinerEndpointForTest disables SSRF validation in tests.
func OverrideValidateJoinerEndpointForTest() {
	validateJoinerEndpointFunc = func(string) error { return nil }
}

// RestoreValidateJoinerEndpoint re-enables the real SSRF validation.
func RestoreValidateJoinerEndpoint() {
	validateJoinerEndpointFunc = validateJoinerEndpointImpl
}

func validateJoinerEndpointImpl(endpoint string) error {
	if endpoint == "" {
		return nil
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid endpoint scheme %q: must be http or https", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("endpoint URL has no host")
	}

	// If the host is an IP literal, check it directly — no DNS involved.
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("endpoint resolves to a private/internal address")
		}
		return nil
	}

	// Hostname: resolve now (validation-time check).
	addrs, err := net.DefaultResolver.LookupHost(context.Background(), host)
	if err != nil {
		return fmt.Errorf("cannot resolve endpoint host %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("endpoint host %q resolved to no addresses", host)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("endpoint host %q resolves to a private/internal address", host)
		}
	}
	return nil
}

// newSSRFSafeTransport returns an *http.Transport whose DialContext re-validates
// the resolved IP at connection time to close the DNS-rebinding TOCTOU window.
// Use this transport for any outbound connection to a peer-supplied endpoint.
func newSSRFSafeTransport() *nethttp.Transport {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &nethttp.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("splitting host/port: %w", err)
			}

			// Resolve the address ourselves so we can inspect the IP.
			ips, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolving %q: %w", host, err)
			}

			// Separate tracking: if we encounter any private IPs, block immediately.
			// If all IPs are public but connections fail, return the last dial error
			// (not an SSRF message) so callers can distinguish SSRF blocks from
			// genuine network failures.
			var lastDialErr error
			for _, ipStr := range ips {
				ip := net.ParseIP(ipStr)
				if ip == nil {
					continue
				}
				if isPrivateIP(ip) {
					return nil, fmt.Errorf("connection to %s blocked: resolves to private/internal IP %s", host, ip)
				}
				// Attempt connection to this non-private IP.
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				lastDialErr = dialErr
			}
			if lastDialErr != nil {
				// Had at least one public IP but all connections failed — surface the
				// actual network error, not an SSRF message.
				return nil, lastDialErr
			}
			return nil, fmt.Errorf("no connectable address resolved for %q", host)
		},
	}
}

// isPrivateIP reports whether ip is in a private, loopback, link-local, or
// reserved address range that should never be reachable from an external peer.
//
// In addition to the standard RFC1918/loopback/link-local ranges, it handles:
//   - IPv4-mapped IPv6 addresses (::ffff:192.168.x.x etc.)
//   - The unspecified address 0.0.0.0 / ::
//   - The AWS EC2 metadata address 169.254.169.254
func isPrivateIP(ip net.IP) bool {
	// Unwrap IPv4-mapped IPv6 (e.g. ::ffff:192.168.1.1) to IPv4 so the IPv4
	// range checks below apply correctly.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	// Unspecified (0.0.0.0 / ::)
	if ip.IsUnspecified() {
		return true
	}
	// Loopback (127.0.0.0/8, ::1)
	if ip.IsLoopback() {
		return true
	}
	// Link-local unicast (169.254.0.0/16, fe80::/10)
	if ip.IsLinkLocalUnicast() {
		return true
	}
	// Link-local multicast
	if ip.IsLinkLocalMulticast() {
		return true
	}
	// Private unicast ranges
	for _, cidr := range privateRanges {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateJoinerEndpoint is the exported form of validateJoinerEndpoint.
// It routes through validateJoinerEndpointFunc so that
// OverrideValidateJoinerEndpointForTest() is the single override point for
// both this var and any callers that hold a reference to it (e.g. ssrfValidateEndpoint
// in cmd/cf-mcp/main.go). No separate per-caller override is needed in tests.
var ValidateJoinerEndpoint = validateJoinerEndpoint

// NewSSRFSafeClient returns an *http.Client backed by the SSRF-safe transport.
// Exported for tests that want to verify the transport rejects private addresses.
func NewSSRFSafeClient() *nethttp.Client {
	return &nethttp.Client{
		Timeout:   30 * time.Second,
		Transport: newSSRFSafeTransport(),
	}
}

// privateRanges is the set of CIDR blocks that are considered private/internal.
var privateRanges = func() []*net.IPNet {
	ranges := []string{
		"10.0.0.0/8",         // RFC1918
		"172.16.0.0/12",      // RFC1918
		"192.168.0.0/16",     // RFC1918
		"100.64.0.0/10",      // RFC6598 shared address (CGNAT)
		"192.0.0.0/24",       // RFC6890 IETF protocol assignments
		"198.18.0.0/15",      // RFC2544 benchmarking
		"198.51.100.0/24",    // RFC5737 documentation
		"203.0.113.0/24",     // RFC5737 documentation
		"240.0.0.0/4",        // RFC1112 reserved
		"255.255.255.255/32", // broadcast
		"fc00::/7",           // RFC4193 unique local
		"::1/128",            // IPv6 loopback (belt-and-suspenders — IsLoopback covers it)
	}
	nets := make([]*net.IPNet, 0, len(ranges))
	for _, r := range ranges {
		_, cidr, err := net.ParseCIDR(r)
		if err != nil {
			panic(fmt.Sprintf("ssrf.go: invalid CIDR %q: %v", r, err))
		}
		nets = append(nets, cidr)
	}
	return nets
}()

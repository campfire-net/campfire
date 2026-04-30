package protocol

// resolve_domain.go — domain-based resolution for naming.root and naming.seeds.
//
// When a naming config value is not a 64-hex campfire ID and not a beacon: string,
// treat it as a domain name and resolve it via HTTPS .well-known/campfire beacon
// discovery. This enables UX like:
//
//   [naming]
//   root = "getcampfire.dev"
//   seeds = ["partner.dev"]

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// wellKnownBeaconResponse is the JSON structure returned by
// https://{domain}/.well-known/campfire.
// Only the campfire_id field is needed for resolution.
type wellKnownBeaconResponse struct {
	CampfireID string `json:"campfire_id"`
}

// domainResolverHTTPClient is the HTTP client used for domain resolution.
// Tests override this to inject a mock server.
var domainResolverHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	// Do not follow redirects to different hosts (SSRF mitigation).
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

// domainBeaconFetchFn is the function used to fetch a campfire ID from a domain.
// Tests override this to inject mock behavior without needing real DNS/TLS.
var domainBeaconFetchFn = fetchDomainBeacon

// resolveNamingValue resolves a naming config value (root or seed) to a
// canonical 64-hex campfire ID.
//
// Resolution order:
//  1. 64-char lowercase hex → return as-is (already a campfire ID)
//  2. "beacon:" prefix → decode beacon, extract campfire ID
//  3. Otherwise → treat as domain, fetch https://{domain}/.well-known/campfire
//
// Returns the resolved 64-hex campfire ID and an optional warning string
// (non-empty when domain resolution was performed, so the user knows what happened).
func resolveNamingValue(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("empty naming value")
	}

	// Fast path: 64-hex campfire ID.
	if hexIDRe.MatchString(s) {
		return s, "", nil
	}

	// Beacon string: delegate to existing beacon resolver.
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "beacon:") || strings.HasPrefix(lower, "cf+beacon://") {
		id, _, err := resolveInput(s, nil)
		if err != nil {
			return "", "", fmt.Errorf("resolving beacon value %q: %w", s, err)
		}
		return id, "", nil
	}

	// Domain resolution.
	if err := validateDomain(s); err != nil {
		return "", "", fmt.Errorf("invalid domain %q: %w", s, err)
	}

	id, err := domainBeaconFetchFn(s)
	if err != nil {
		return "", "", fmt.Errorf("resolving domain %q: %w", s, err)
	}

	warning := fmt.Sprintf("naming: resolved domain %q to campfire ID %s via .well-known/campfire", s, id)
	return id, warning, nil
}

// validateDomain checks that s is a valid domain name suitable for beacon resolution.
// Rejects IP addresses, localhost, and internal/reserved ranges.
func validateDomain(s string) error {
	// Reject empty.
	if s == "" {
		return fmt.Errorf("empty domain")
	}

	// Reject anything with a scheme or path component.
	if strings.Contains(s, "://") || strings.Contains(s, "/") {
		return fmt.Errorf("domain must not contain scheme or path")
	}

	// Reject port numbers.
	if strings.Contains(s, ":") {
		return fmt.Errorf("domain must not contain port number")
	}

	// Reject IP addresses (IPv4 and IPv6).
	if net.ParseIP(s) != nil {
		return fmt.Errorf("IP addresses are not allowed; use a domain name")
	}
	// Also catch bracketed IPv6 like [::1].
	if strings.HasPrefix(s, "[") {
		return fmt.Errorf("IP addresses are not allowed; use a domain name")
	}

	// Reject localhost and common internal domains.
	lower := strings.ToLower(s)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("localhost is not allowed")
	}
	if lower == "local" || strings.HasSuffix(lower, ".local") {
		return fmt.Errorf(".local domains are not allowed")
	}
	if strings.HasSuffix(lower, ".internal") {
		return fmt.Errorf(".internal domains are not allowed")
	}

	// Must contain at least one dot (bare hostnames rejected).
	if !strings.Contains(s, ".") {
		return fmt.Errorf("bare hostname %q is not allowed; use a fully qualified domain name", s)
	}

	return nil
}

// fetchDomainBeacon fetches the .well-known/campfire endpoint for the given domain
// and extracts the campfire ID.
//
// Security: HTTPS only. The campfire_id field may have an "ed25519:" prefix which
// is stripped to yield the raw hex ID.
func fetchDomainBeacon(domain string) (string, error) {
	url := "https://" + domain + "/.well-known/campfire"

	resp, err := domainResolverHTTPClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}

	// Limit response body to 64 KiB.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("reading response from %s: %w", url, err)
	}

	var beacon wellKnownBeaconResponse
	if err := json.Unmarshal(data, &beacon); err != nil {
		return "", fmt.Errorf("parsing JSON from %s: %w", url, err)
	}

	if beacon.CampfireID == "" {
		return "", fmt.Errorf("no campfire_id in response from %s", url)
	}

	// Strip "ed25519:" prefix if present (the well-known endpoint uses this format).
	id := beacon.CampfireID
	if strings.HasPrefix(id, "ed25519:") {
		id = strings.TrimPrefix(id, "ed25519:")
	}

	// Validate the extracted ID is 64-hex.
	if !hexIDRe.MatchString(id) {
		return "", fmt.Errorf("campfire_id from %s is not a valid 64-hex ID: %q", url, id)
	}

	return id, nil
}

// resolveNamingConfig resolves all domain references in a NamingConfig,
// returning the resolved config and any warnings generated.
func resolveNamingConfig(cfg *NamingConfig) ([]string, error) {
	var warnings []string

	if cfg.Root != "" {
		resolved, warn, err := resolveNamingValue(cfg.Root)
		if err != nil {
			return nil, fmt.Errorf("naming.root: %w", err)
		}
		cfg.Root = resolved
		if warn != "" {
			warnings = append(warnings, warn)
		}
	}

	for i, seed := range cfg.Seeds {
		resolved, warn, err := resolveNamingValue(seed)
		if err != nil {
			return nil, fmt.Errorf("naming.seeds[%d] (%q): %w", i, seed, err)
		}
		cfg.Seeds[i] = resolved
		if warn != "" {
			warnings = append(warnings, warn)
		}
	}

	return warnings, nil
}

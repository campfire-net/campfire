package protocol

// resolve.go — resolveInput() for unified campfire address resolution.
//
// Change 5 of Campfire 0.16 (campfire-agent-2jv).
//
// resolveInput accepts a user-supplied campfire address in any of three forms:
//  1. 64-hex campfire ID — pass-through, zero allocation
//  2. "beacon:<base64>" or "cf+beacon://<base64>" — parse, verify, extract ID + hint
//  3. "cf://<name>" — delegate to NamingResolver
//
// All 12 SDK entry points call resolveInput at the top to normalize the address.
// For non-Join methods, the caller discards the hint and uses stored transport.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/campfire-net/campfire/pkg/beacon"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
)

// ErrBeaconVerificationFailed is returned when a beacon string is syntactically
// valid but its Ed25519 signature does not verify. Distinct from not-found errors:
// callers can use errors.Is(err, ErrBeaconVerificationFailed) to distinguish a
// corrupted address from a campfire that is simply unreachable.
var ErrBeaconVerificationFailed = errors.New("beacon verification failed")

// hexIDRe matches exactly 64 lowercase hex characters (a valid campfire ID).
// Compiled once at package init for zero-allocation use in the hot path.
var hexIDRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// TransportHint carries optional transport metadata extracted from a beacon.
// Returned by resolveInput when the input was a beacon string.
//
// SECURITY: Tainted is ALWAYS true for beacon-derived hints. Callers MUST NOT
// use the hint for send/read operations on existing memberships (SSRF prevention).
// The hint is only safe for initial Join operations.
type TransportHint struct {
	// Transport is the transport protocol: "http", "fs", or "github".
	Transport string

	// Endpoint is the HTTP endpoint (only meaningful for http transport).
	Endpoint string

	// Tainted is ALWAYS true for beacon-derived transports.
	// This is a type-level invariant, not a configuration option.
	// It is never false for a TransportHint returned by resolveInput.
	Tainted bool
}

// NamingResolver resolves a cf:// URI to a campfire ID.
// Implementations should wrap pkg/naming.Resolver.
type NamingResolver interface {
	// Resolve resolves a cf:// URI and returns the canonical hex campfire ID.
	Resolve(uri string) (campfireID string, err error)
}

// resolveInput resolves a user-supplied campfire address to a canonical 64-hex ID.
//
// Accepted input formats:
//   - 64-hex campfire ID: returned as-is (zero allocation, zero latency)
//   - "beacon:<base64>" or "cf+beacon://<base64>": parsed, verified, ID extracted
//   - "cf://<name>" or "cf://<64hex>": delegated to NamingResolver
//
// Returns:
//   - campfireID: canonical 64-hex campfire public key
//   - hint: optional transport hint (non-nil only for beacon inputs); always Tainted=true
//   - err: ErrBeaconVerificationFailed for invalid beacon signatures;
//     descriptive error if resolver is nil and input is a cf:// URI
func resolveInput(s string, resolver NamingResolver) (campfireID string, hint *TransportHint, err error) {
	if s == "" {
		return "", nil, fmt.Errorf("campfire address is required")
	}

	// Fast path: 64-hex pass-through — the dominant case.
	// hexIDRe is compiled once; MatchString does not allocate.
	if hexIDRe.MatchString(s) {
		return s, nil, nil
	}

	rawLower := strings.ToLower(s)

	// Beacon string: "beacon:<base64>" or "cf+beacon://<base64>"
	if strings.HasPrefix(rawLower, "beacon:") || strings.HasPrefix(rawLower, "cf+beacon://") {
		var beaconData string
		if strings.HasPrefix(rawLower, "beacon:") {
			beaconData = s[len("beacon:"):]
		} else {
			beaconData = s[len("cf+beacon://"):]
		}
		b, decodeErr := decodeBeaconBase64(beaconData)
		if decodeErr != nil {
			return "", nil, fmt.Errorf("%w: %v", ErrBeaconVerificationFailed, decodeErr)
		}
		if !b.Verify() {
			return "", nil, fmt.Errorf("%w: signature mismatch for campfire %s",
				ErrBeaconVerificationFailed, b.CampfireIDHex())
		}
		h := &TransportHint{
			Transport: b.Transport.Protocol,
			Tainted:   true, // ALWAYS true — type-level invariant
		}
		if b.Transport.Config != nil {
			h.Endpoint = b.Transport.Config["endpoint"]
		}
		return b.CampfireIDHex(), h, nil
	}

	// cf:// URI (named, direct-hex, or alias)
	if strings.HasPrefix(rawLower, "cf://") {
		if resolver == nil {
			return "", nil, fmt.Errorf("naming resolver not configured; cf:// URIs require WithNamingResolver")
		}
		id, resolveErr := resolver.Resolve(s)
		if resolveErr != nil {
			return "", nil, fmt.Errorf("resolving cf:// URI %q: %w", s, resolveErr)
		}
		return id, nil, nil
	}

	// Unknown format.
	return "", nil, fmt.Errorf("unrecognized campfire address %q: expected 64-hex ID, beacon:<base64>, or cf:// URI", s)
}

// decodeBeaconBase64 decodes a base64-encoded beacon payload.
// Accepts URL-safe (with or without padding) and standard variants.
func decodeBeaconBase64(data string) (*beacon.Beacon, error) {
	if data == "" {
		return nil, fmt.Errorf("empty beacon data")
	}

	var raw []byte
	var decErr error
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		raw, decErr = enc.DecodeString(data)
		if decErr == nil {
			break
		}
	}
	if decErr != nil {
		return nil, fmt.Errorf("base64 decode: %w", decErr)
	}

	var b beacon.Beacon
	if err := cfencoding.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("beacon decode: %w", err)
	}
	return &b, nil
}

// WithNamingResolver injects a NamingResolver into the Client, enabling
// cf:// URI resolution in all SDK methods.
//
// The resolver is optional. When nil, SDK methods that receive a cf:// URI
// return a clear error: "naming resolver not configured; cf:// URIs require
// WithNamingResolver". 64-hex IDs and beacon strings work without a resolver.
func WithNamingResolver(r NamingResolver) Option {
	return func(o *options) {
		o.namingResolver = r
	}
}

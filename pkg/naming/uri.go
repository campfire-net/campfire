// Package naming implements the cf:// URI scheme for hierarchical campfire naming,
// resolution, caching, and service discovery per the Naming and URI Convention.
package naming

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/campfire-net/campfire/pkg/beacon"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
)

// MaxDepth is the maximum number of dot-separated segments in a campfire name.
const MaxDepth = 8

// MaxNameLength is the maximum total length of a campfire name.
const MaxNameLength = 253

// MaxSegmentLength is the maximum length of a single name segment.
const MaxSegmentLength = 63

// MaxDescriptionLength is the maximum allowed description length (sanitized).
const MaxDescriptionLength = 80

// segmentRe validates a single name segment: lowercase alphanumeric plus hyphens,
// must not start or end with a hyphen.
var segmentRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// URIKind classifies how a URI was expressed.
type URIKind int

const (
	// URIKindNamed is a standard dot-separated name (e.g. cf://aietf.social.lobby).
	URIKindNamed URIKind = iota
	// URIKindAlias is a local alias (e.g. cf://~baron).
	URIKindAlias
	// URIKindDirect is a direct 64-hex campfire ID (e.g. cf://<64 hex chars>).
	URIKindDirect
	// URIKindBeacon is a beacon URI containing a base64-encoded signed beacon.
	// Accepted formats: beacon:<base64> or cf+beacon://<base64>.
	// The campfire ID is extracted from the beacon after signature verification.
	// SECURITY: the transport hint from the beacon MUST only be used for join
	// operations -- never for send/read on existing memberships (SSRF prevention).
	URIKindBeacon
)

// URI represents a parsed cf:// URI.
type URI struct {
	// Kind classifies how the URI was expressed.
	Kind URIKind
	// Name is the dot-separated campfire name (e.g. "aietf.social.lobby").
	// Empty for alias and direct kinds.
	Name string
	// Segments is the parsed dot-separated name segments.
	// Empty for alias and direct kinds.
	Segments []string
	// Alias is the local alias name (without ~), populated for URIKindAlias.
	Alias string
	// CampfireID is the direct campfire ID, populated for URIKindDirect and
	// URIKindBeacon (extracted from the verified beacon payload).
	CampfireID string
	// BeaconData holds the raw base64-encoded beacon string for URIKindBeacon.
	// Callers that need the full beacon should decode and unmarshal this field.
	// SECURITY: transport hint from beacon is ONLY for join, not send/read.
	BeaconData string
	// Path is the slash-separated resource path (e.g. "trending").
	// Empty if the URI has no path component.
	Path string
	// Query contains URL-encoded key-value parameters.
	Query url.Values
}

// ParseURI parses a cf:// URI string into a structured URI.
// It enforces strict parsing rules per the naming convention:
//   - No URL decoding in name portion
//   - Empty segments rejected
//   - Path traversal rejected
//   - Fragments, userinfo, and port numbers rejected
//   - Null bytes rejected
//   - Non-ASCII rejected in name/path portions
//   - Canonicalized to lowercase
//
// Beacon URI formats accepted (case-insensitive prefix):
//   - beacon:<base64>      (canonical)
//   - cf+beacon://<base64> (alternative)
//
// SECURITY: for beacon URIs, the transport hint MUST only be used during join,
// not during send/read operations on existing memberships (SSRF prevention).
func ParseURI(raw string) (*URI, error) {
	// Null byte check
	if strings.ContainsRune(raw, 0) {
		return nil, fmt.Errorf("null byte in URI")
	}

	// --- Beacon URI: beacon:<base64> ---
	rawLower := strings.ToLower(raw)
	if strings.HasPrefix(rawLower, "beacon:") {
		data := raw[len("beacon:"):]
		return parseBeaconData(data)
	}
	// --- Beacon URI: cf+beacon://<base64> ---
	if strings.HasPrefix(rawLower, "cf+beacon://") {
		data := raw[len("cf+beacon://"):]
		return parseBeaconData(data)
	}

	// Canonicalize to lowercase
	raw = strings.ToLower(raw)

	// Must start with cf://
	if !strings.HasPrefix(raw, "cf://") {
		return nil, fmt.Errorf("URI must start with cf://")
	}

	rest := raw[len("cf://"):]
	if rest == "" {
		return nil, fmt.Errorf("empty URI after scheme")
	}

	// Reject fragments
	if strings.Contains(rest, "#") {
		return nil, fmt.Errorf("fragments (#) not allowed in cf:// URIs")
	}

	// Reject userinfo (@)
	if strings.Contains(rest, "@") {
		return nil, fmt.Errorf("userinfo (@) not allowed in cf:// URIs")
	}

	// Reject port numbers (:)
	// We check the name portion (before any / or ?) for colons
	namePart := rest
	if idx := strings.IndexAny(rest, "/?"); idx >= 0 {
		namePart = rest[:idx]
	}
	if strings.Contains(namePart, ":") {
		return nil, fmt.Errorf("port numbers (:) not allowed in cf:// URIs")
	}

	// Split query string
	var queryStr string
	if idx := strings.Index(rest, "?"); idx >= 0 {
		queryStr = rest[idx+1:]
		rest = rest[:idx]
	}

	// Split path
	var path string
	if idx := strings.Index(rest, "/"); idx >= 0 {
		path = rest[idx+1:]
		rest = rest[:idx]
	}

	name := rest

	// Validate name
	if name == "" {
		return nil, fmt.Errorf("empty name in URI")
	}

	// Validate path (no traversal, ASCII only)
	if path != "" {
		if err := validatePath(path); err != nil {
			return nil, err
		}
	}

	// Parse query string (URL decoding applies only to query values)
	var query url.Values
	if queryStr != "" {
		var err error
		query, err = url.ParseQuery(queryStr)
		if err != nil {
			return nil, fmt.Errorf("invalid query string: %w", err)
		}
	}

	// --- Alias URI: name starts with ~ ---
	if strings.HasPrefix(name, "~") {
		alias := name[1:]
		if alias == "" {
			return nil, fmt.Errorf("empty alias name after ~")
		}
		return &URI{
			Kind:  URIKindAlias,
			Alias: alias,
			Path:  path,
			Query: query,
		}, nil
	}

	// --- Direct URI: name is exactly 64 lowercase hex chars ---
	if hexRe.MatchString(name) {
		return &URI{
			Kind:       URIKindDirect,
			CampfireID: name,
			Path:       path,
			Query:      query,
		}, nil
	}

	// --- Named URI: standard dot-separated name ---
	// Check for non-ASCII in name
	for _, r := range name {
		if r > unicode.MaxASCII {
			return nil, fmt.Errorf("non-ASCII character in name")
		}
	}

	segments := strings.Split(name, ".")

	// Check for empty segments (double dots)
	for _, seg := range segments {
		if seg == "" {
			return nil, fmt.Errorf("empty segment in name")
		}
	}

	// Depth limit
	if len(segments) > MaxDepth {
		return nil, fmt.Errorf("name exceeds maximum depth of %d segments", MaxDepth)
	}

	// Total name length
	if len(name) > MaxNameLength {
		return nil, fmt.Errorf("name exceeds maximum length of %d characters", MaxNameLength)
	}

	// Validate each segment
	for _, seg := range segments {
		if err := ValidateSegment(seg); err != nil {
			return nil, fmt.Errorf("invalid segment %q: %w", seg, err)
		}
	}

	return &URI{
		Kind:     URIKindNamed,
		Name:     name,
		Segments: segments,
		Path:     path,
		Query:    query,
	}, nil
}

// parseBeaconData decodes and verifies a base64-encoded beacon payload,
// returning a URIKindBeacon URI with the campfire ID extracted from the beacon.
//
// SECURITY: the beacon signature MUST verify before the campfire ID is trusted.
// Tampered beacons are rejected. The transport hint in BeaconData MUST only be
// used for join operations -- never for send/read on existing memberships.
func parseBeaconData(data string) (*URI, error) {
	if data == "" {
		return nil, fmt.Errorf("empty beacon data")
	}
	// Decode base64 (URL-safe without padding is canonical for URI embedding;
	// also accept padded and standard variants for interoperability).
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		raw, err = enc.DecodeString(data)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("beacon base64 decode: %w", err)
	}

	// Unmarshal the beacon.
	var b beacon.Beacon
	if err := cfencoding.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("beacon decode: %w", err)
	}

	// SECURITY: verify the beacon signature before trusting the campfire ID.
	// A tampered beacon (modified transport, description, etc.) must be rejected.
	if !b.Verify() {
		return nil, fmt.Errorf("beacon signature verification failed")
	}

	return &URI{
		Kind:       URIKindBeacon,
		CampfireID: b.CampfireIDHex(),
		BeaconData: data,
	}, nil
}

// hexRe matches exactly 64 lowercase hex characters.
var hexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateInbound returns an error if the URI is not allowed in inbound contexts
// (e.g., alias URIs cannot be sent by remote peers).
func ValidateInbound(u *URI) error {
	if u.Kind == URIKindAlias {
		return fmt.Errorf("alias URIs (cf://~...) are local-only and not allowed in inbound contexts")
	}
	return nil
}

// ValidateSegment checks whether a single name segment is valid.
func ValidateSegment(seg string) error {
	if len(seg) > MaxSegmentLength {
		return fmt.Errorf("segment exceeds maximum length of %d characters", MaxSegmentLength)
	}
	if !segmentRe.MatchString(seg) {
		return fmt.Errorf("segment must be lowercase alphanumeric plus hyphens, not starting/ending with hyphen")
	}
	return nil
}

// validatePath checks the path component for traversal and ASCII-only.
func validatePath(path string) error {
	parts := strings.Split(path, "/")
	for _, p := range parts {
		if p == ".." {
			return fmt.Errorf("path traversal (..) not allowed")
		}
		for _, r := range p {
			if r > unicode.MaxASCII {
				return fmt.Errorf("non-ASCII character in path")
			}
		}
	}
	return nil
}

// String returns the canonical string representation of the URI.
// For beacon URIs, returns the canonical beacon:<base64> form.
func (u *URI) String() string {
	if u.Kind == URIKindBeacon {
		return "beacon:" + u.BeaconData
	}
	var sb strings.Builder
	sb.WriteString("cf://")
	switch u.Kind {
	case URIKindAlias:
		sb.WriteByte('~')
		sb.WriteString(u.Alias)
	case URIKindDirect:
		sb.WriteString(u.CampfireID)
	default:
		sb.WriteString(u.Name)
	}
	if u.Path != "" {
		sb.WriteByte('/')
		sb.WriteString(u.Path)
	}
	if len(u.Query) > 0 {
		sb.WriteByte('?')
		sb.WriteString(u.Query.Encode())
	}
	return sb.String()
}

// HasPath returns true if the URI has a path component (future invocation).
func (u *URI) HasPath() bool {
	return u.Path != ""
}

// Args returns the query parameters as a map[string]string (first value wins).
func (u *URI) Args() map[string]string {
	if len(u.Query) == 0 {
		return nil
	}
	args := make(map[string]string, len(u.Query))
	for k, v := range u.Query {
		if len(v) > 0 {
			args[k] = v[0]
		}
	}
	return args
}

// IsCampfireURI returns true if the string looks like a cf://, beacon:, or cf+beacon:// URI.
func IsCampfireURI(s string) bool {
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "cf://") ||
		strings.HasPrefix(lower, "beacon:") ||
		strings.HasPrefix(lower, "cf+beacon://")
}

// LooksLikeName returns true if s looks like a bare dot-separated campfire name
// (e.g. "aietf.social.lobby") that can be resolved without a cf:// prefix.
// A valid name has only lowercase alphanumeric + hyphen + dot chars,
// no leading/trailing dots or hyphens, and valid segments throughout.
func LooksLikeName(s string) bool {
	s = strings.ToLower(s)
	if !strings.Contains(s, ".") {
		return false
	}
	segs := strings.Split(s, ".")
	for _, seg := range segs {
		if err := ValidateSegment(seg); err != nil {
			return false
		}
	}
	return true
}

// SanitizeDescription truncates to MaxDescriptionLength and strips control characters.
func SanitizeDescription(s string) string {
	// Strip control characters and newlines
	var sb strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		sb.WriteRune(r)
	}
	s = sb.String()
	if len(s) > MaxDescriptionLength {
		s = s[:MaxDescriptionLength]
	}
	return s
}

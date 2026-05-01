package discovery

// snippet.go — Tier 1 discovery: naming:preview snippets.
//
// A snippet is a parent-signed, schema-locked summary of a child campfire published
// as a naming:preview message in the parent campfire. Snippets allow members of a
// parent namespace to browse child campfires without joining them.
//
// Spec: design v2 §3.1, cf-discovery-spec.md §1–§7.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// TagPreview is the campfire message tag for snippet messages.
const TagPreview = "naming:preview"

// snippetNameRe validates a single-segment snippet name.
// Must match ^[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?$ and must not contain a dot.
var snippetNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?$`)

// Permitted member_count_bucket values.
const (
	BucketSolo   = "1"
	BucketSmall  = "2-5"
	BucketMedium = "6-25"
	BucketLarge  = "26+"
)

var permittedBuckets = map[string]bool{
	BucketSolo: true, BucketSmall: true, BucketMedium: true, BucketLarge: true,
}

// minFreshnessWindow is the minimum allowed freshness_window duration.
const minFreshnessWindow = time.Second

// maxFreshnessWindow is the maximum allowed freshness_window duration.
const maxFreshnessWindow = 24 * time.Hour

// Snippet is the parsed form of a naming:preview message payload (cf-discovery-spec.md §1.2).
type Snippet struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	MemberCountBucket string `json:"member_count_bucket"`
	FreshnessWindow   string `json:"freshness_window"`
	ParentSignature   string `json:"parent_signature"`
	// Beacon is an optional additional field — the child's beacon string for cold-start dialing.
	// Included in signing payload if non-empty.
	Beacon string `json:"beacon,omitempty"`
}

// SnippetResult is a Snippet with freshness state computed at a point in time.
type SnippetResult struct {
	Snippet
	// Stale is true when the snippet's message timestamp + freshness_window < now.
	// Stale snippets are shown with a degradation indicator, not hidden.
	Stale bool
	// StaleReason is a human-readable explanation when Stale is true.
	StaleReason string
}

// ValidateSnippet validates a snippet's fields per cf-discovery-spec.md §2.3 steps 1-3.
// Returns nil if the snippet is valid. Does NOT verify the parent_signature (that requires
// the parent's public key — use VerifySnippet for full verification).
func ValidateSnippet(s Snippet) error {
	// Step 1: field presence
	if s.Name == "" {
		return fmt.Errorf("snippet: name is required")
	}
	if s.Description == "" {
		return fmt.Errorf("snippet: description is required")
	}
	if s.MemberCountBucket == "" {
		return fmt.Errorf("snippet: member_count_bucket is required")
	}
	if s.FreshnessWindow == "" {
		return fmt.Errorf("snippet: freshness_window is required")
	}
	if s.ParentSignature == "" {
		return fmt.Errorf("snippet: parent_signature is required")
	}

	// Step 3: field constraints
	if !snippetNameRe.MatchString(s.Name) {
		return fmt.Errorf("snippet: name %q does not match segment regex", s.Name)
	}
	// Dotted names are forbidden — a snippet represents a single-hop child only.
	for _, c := range s.Name {
		if c == '.' {
			return fmt.Errorf("snippet: name %q contains a dot — multi-segment paths are forbidden", s.Name)
		}
	}
	if !permittedBuckets[s.MemberCountBucket] {
		return fmt.Errorf("snippet: member_count_bucket %q is not a permitted value (1, 2-5, 6-25, 26+)", s.MemberCountBucket)
	}
	fw, err := time.ParseDuration(s.FreshnessWindow)
	if err != nil {
		return fmt.Errorf("snippet: freshness_window %q is not a valid Go duration: %w", s.FreshnessWindow, err)
	}
	if fw < minFreshnessWindow {
		return fmt.Errorf("snippet: freshness_window %q is below minimum of 1s", s.FreshnessWindow)
	}
	if fw > maxFreshnessWindow {
		return fmt.Errorf("snippet: freshness_window %q exceeds maximum of 24h", s.FreshnessWindow)
	}
	// Validate parent_signature is URL-safe base64 without padding, 64 bytes decoded.
	sigBytes, err := base64.RawURLEncoding.DecodeString(s.ParentSignature)
	if err != nil {
		return fmt.Errorf("snippet: parent_signature is not valid URL-safe base64: %w", err)
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return fmt.Errorf("snippet: parent_signature decoded to %d bytes, want %d", len(sigBytes), ed25519.SignatureSize)
	}
	return nil
}

// snippetSignPayload produces the canonical signing payload for a snippet per §2.2.
// Format: "naming:preview\n<canonical-json>"
// The parent_signature field is excluded. The beacon field is included if non-empty.
func snippetSignPayload(s Snippet) ([]byte, error) {
	// Canonical field order: name, description, member_count_bucket, freshness_window.
	// beacon is included after freshness_window if non-empty (per cf-discovery-spec.md §8).
	type canonical struct {
		Name              string `json:"name"`
		Description       string `json:"description"`
		MemberCountBucket string `json:"member_count_bucket"`
		FreshnessWindow   string `json:"freshness_window"`
		Beacon            string `json:"beacon,omitempty"`
	}
	body, err := json.Marshal(canonical{
		Name:              s.Name,
		Description:       s.Description,
		MemberCountBucket: s.MemberCountBucket,
		FreshnessWindow:   s.FreshnessWindow,
		Beacon:            s.Beacon,
	})
	if err != nil {
		return nil, err
	}
	payload := append([]byte("naming:preview\n"), body...)
	return payload, nil
}

// SignSnippet produces a Snippet signed by the parent campfire's private key.
// The parent_signature field is set to the URL-safe base64 (no padding) encoding
// of the Ed25519 signature over the canonical signing payload.
func SignSnippet(
	name string,
	description string,
	bucket string,
	freshnessWindow string,
	beacon string,
	parentPriv ed25519.PrivateKey,
) (*Snippet, error) {
	s := Snippet{
		Name:              name,
		Description:       description,
		MemberCountBucket: bucket,
		FreshnessWindow:   freshnessWindow,
		Beacon:            beacon,
	}
	// Validate all fields except parent_signature (not yet computed).
	// Use a 64-byte all-zeros placeholder to satisfy the signature length check.
	placeholder := base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	if err := ValidateSnippet(Snippet{
		Name: s.Name, Description: s.Description,
		MemberCountBucket: s.MemberCountBucket, FreshnessWindow: s.FreshnessWindow,
		ParentSignature: placeholder,
	}); err != nil {
		return nil, err
	}
	payload, err := snippetSignPayload(s)
	if err != nil {
		return nil, fmt.Errorf("encoding sign payload: %w", err)
	}
	sig := ed25519.Sign(parentPriv, payload)
	s.ParentSignature = base64.RawURLEncoding.EncodeToString(sig)
	return &s, nil
}

// VerifySnippet performs full snippet verification per cf-discovery-spec.md §2.3.
// Steps 1-3 are field validation; step 5 is signature verification against parentPub.
// Step 6 (freshness) is returned separately via the Stale field in SnippetResult.
// msgTimestamp is the timestamp of the enclosing campfire message (Unix seconds).
func VerifySnippet(s Snippet, parentPub ed25519.PublicKey, msgTimestamp int64, now time.Time) (*SnippetResult, error) {
	// Steps 1-3: field validation
	if err := ValidateSnippet(s); err != nil {
		return nil, err
	}

	// Step 5: signature verification
	payload, err := snippetSignPayload(s)
	if err != nil {
		return nil, fmt.Errorf("encoding sign payload for verification: %w", err)
	}
	sigBytes, _ := base64.RawURLEncoding.DecodeString(s.ParentSignature) // already validated
	if !ed25519.Verify(parentPub, payload, sigBytes) {
		return nil, fmt.Errorf("snippet: parent_signature verification failed")
	}

	// Step 6: freshness (degradation, not rejection)
	fw, _ := time.ParseDuration(s.FreshnessWindow) // already validated
	msgTime := time.Unix(msgTimestamp, 0)
	result := &SnippetResult{Snippet: s}
	if now.After(msgTime.Add(fw)) {
		result.Stale = true
		result.StaleReason = fmt.Sprintf("freshness_window %s elapsed since message timestamp %s",
			s.FreshnessWindow, msgTime.Format(time.RFC3339))
	}
	return result, nil
}

// ComposeFreshnessWindows returns the minimum freshness window across a chain of
// snippet results, implementing the multi-hop min-composition rule per cf-discovery-spec.md §3.2.
// Returns an error if any result in the chain has a malformed freshness_window.
func ComposeFreshnessWindows(chain []SnippetResult) (time.Duration, bool, error) {
	if len(chain) == 0 {
		return 0, false, fmt.Errorf("empty chain")
	}
	minWindow := maxFreshnessWindow + 1 // sentinel
	anyStale := false
	for i, r := range chain {
		fw, err := time.ParseDuration(r.FreshnessWindow)
		if err != nil {
			return 0, false, fmt.Errorf("chain[%d]: invalid freshness_window %q: %w", i, r.FreshnessWindow, err)
		}
		if fw < minWindow {
			minWindow = fw
		}
		if r.Stale {
			anyStale = true
		}
	}
	return minWindow, anyStale, nil
}

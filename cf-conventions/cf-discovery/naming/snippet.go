// Package naming — cf-discovery 1.0 snippet schema types and validation.
//
// This file contains the production snippet schema: Snippet wire-format struct,
// ValidateSnippet, ComposeFreshnessWindow, SnippetValidationError, and helpers.
//
// Resolves OPEN-014 (campfireagent-219).
package naming

import (
	"encoding/base64"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Snippet type — wire-format struct matching cf-discovery-spec.md §1.2
// ---------------------------------------------------------------------------

// Snippet is the wire-format struct for a cf-discovery Tier-1 snippet
// (naming:preview message body). All five fields are required.
//
// See docs/cf-discovery-spec.md §1.2 and §1.3.
type Snippet struct {
	Name               string `json:"name"`
	Description        string `json:"description"`
	MemberCountBucket  string `json:"member_count_bucket"`
	FreshnessWindow    string `json:"freshness_window"`
	ParentSignature    string `json:"parent_signature"`
}

// PermittedBuckets is the exhaustive set of allowed member_count_bucket values.
// cf-discovery-spec.md §1.3 (member_count_bucket).
var PermittedBuckets = map[string]bool{
	"1":    true,
	"2-5":  true,
	"6-25": true,
	"26+":  true,
}

// SnippetValidationError is a structured validation error.
type SnippetValidationError struct {
	Field  string
	Reason string
}

func (e *SnippetValidationError) Error() string {
	return "snippet validation failed — field \"" + e.Field + "\": " + e.Reason
}

// ValidateSnippet validates a Snippet against the cf-discovery 1.0 spec rules
// (§1.3, §2.3 steps 1–3). Returns nil if valid, non-nil error describing the
// first violation. Signature verification (§2.3 steps 4–5) is out of scope for
// this function — it requires the parent's public key and is handled by the
// transport layer.
//
// Note on step 6 (freshness): staleness produces degradation, not rejection.
// This function does not check the message timestamp, only that the
// freshness_window field parses correctly and is in range.
func ValidateSnippet(s *Snippet) error {
	// §2.3 step 1 — all five fields present and non-empty
	if s.Name == "" {
		return &SnippetValidationError{Field: "name", Reason: "required field is empty"}
	}
	if s.Description == "" {
		return &SnippetValidationError{Field: "description", Reason: "required field is empty"}
	}
	if s.MemberCountBucket == "" {
		return &SnippetValidationError{Field: "member_count_bucket", Reason: "required field is empty"}
	}
	if s.FreshnessWindow == "" {
		return &SnippetValidationError{Field: "freshness_window", Reason: "required field is empty"}
	}
	if s.ParentSignature == "" {
		return &SnippetValidationError{Field: "parent_signature", Reason: "required field is empty"}
	}

	// §1.3 name: valid segment, no dot (adversarial rule)
	if strings.Contains(s.Name, ".") {
		return &SnippetValidationError{Field: "name", Reason: "must not contain a dot — snippets are single-hop only"}
	}
	if !isValidSegment(s.Name) {
		return &SnippetValidationError{Field: "name", Reason: "invalid naming segment — must match ^[a-z0-9]([a-z0-9\\-]{0,61}[a-z0-9])?$"}
	}

	// §1.3 description: no embedded newlines, no null bytes
	if strings.ContainsAny(s.Description, "\n\r\x00") {
		return &SnippetValidationError{Field: "description", Reason: "must not contain newlines or null bytes"}
	}

	// §1.3 member_count_bucket: must be one of the permitted values (adversarial rule)
	if !PermittedBuckets[s.MemberCountBucket] {
		return &SnippetValidationError{
			Field:  "member_count_bucket",
			Reason: "not a permitted value — must be one of: \"1\", \"2-5\", \"6-25\", \"26+\"",
		}
	}

	// §1.3 freshness_window: must parse as duration, range [1s, 24h], positive
	d, err := time.ParseDuration(s.FreshnessWindow)
	if err != nil {
		return &SnippetValidationError{Field: "freshness_window", Reason: "does not parse as a Go duration: " + err.Error()}
	}
	if d <= 0 {
		return &SnippetValidationError{Field: "freshness_window", Reason: "must be positive (> 0)"}
	}
	if d < time.Second {
		return &SnippetValidationError{Field: "freshness_window", Reason: "below minimum of 1s"}
	}
	if d > 24*time.Hour {
		return &SnippetValidationError{Field: "freshness_window", Reason: "exceeds maximum of 24h"}
	}

	// §1.3 parent_signature: URL-safe base64 no-padding, decoded length 64 bytes
	sig, err := base64.RawURLEncoding.DecodeString(s.ParentSignature)
	if err != nil {
		return &SnippetValidationError{Field: "parent_signature", Reason: "not valid URL-safe base64 (no-padding): " + err.Error()}
	}
	if len(sig) != 64 {
		return &SnippetValidationError{Field: "parent_signature", Reason: "decoded length must be 64 bytes (Ed25519 signature)"}
	}

	return nil
}

// ComposeFreshnessWindow returns the minimum freshness_window across a chain of
// snippet windows, per cf-discovery-spec.md §3.2. Returns an error if any window
// fails to parse. All windows must already be validated (i.e., ValidateSnippet
// passes) before calling this function.
func ComposeFreshnessWindow(windows []string) (time.Duration, error) {
	if len(windows) == 0 {
		return 0, nil
	}
	min := time.Duration(0)
	for i, w := range windows {
		d, err := time.ParseDuration(w)
		if err != nil {
			return 0, err
		}
		if i == 0 || d < min {
			min = d
		}
	}
	return min, nil
}

// isValidSegment returns true if name matches the valid segment pattern.
// Pattern: ^[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?$
// Single-character names are also valid ([a-z0-9]).
func isValidSegment(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for i, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z':
			// ok
		case ch >= '0' && ch <= '9':
			// ok
		case ch == '-':
			if i == 0 || i == len(name)-1 {
				return false // no leading/trailing hyphen
			}
		default:
			return false
		}
	}
	return true
}

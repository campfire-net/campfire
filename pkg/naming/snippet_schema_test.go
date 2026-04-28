// Package naming — cf-discovery 1.0 snippet schema conformance tests.
//
// These tests validate the snippet schema as specified in docs/cf-discovery-spec.md.
// They serve as the machine-readable companion to the spec: every rule in §1–§7
// that can be stated as a predicate is tested here.
//
// Resolves OPEN-014 (campfireagent-219).
package naming

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

// SnippetValidationError is a structured validation error.
type SnippetValidationError struct {
	Field  string
	Reason string
}

func (e *SnippetValidationError) Error() string {
	return "snippet validation failed — field \"" + e.Field + "\": " + e.Reason
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

// ---------------------------------------------------------------------------
// TDD: spec-existence test (must fail before spec file is written)
// ---------------------------------------------------------------------------

// TestCFDiscoverySpecExists asserts that the cf-discovery 1.0 spec document
// exists at docs/cf-discovery-spec.md in the campfire repo. This test is the
// "failing test" per TDD requirement (§10.1): it fails before the file is
// created, and passes after.
func TestCFDiscoverySpecExists(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine source file path")
	}
	// Walk from pkg/naming up two levels to the repo root.
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..")
	specPath := filepath.Join(repoRoot, "docs", "cf-discovery-spec.md")

	info, err := os.Stat(specPath)
	if err != nil {
		t.Fatalf("cf-discovery 1.0 spec not found at %s: %v", specPath, err)
	}
	if info.IsDir() {
		t.Fatalf("expected a file at %s, got a directory", specPath)
	}
	if info.Size() == 0 {
		t.Fatalf("spec file at %s is empty", specPath)
	}
}

// TestCFDiscoverySpecContainsRequiredSections ensures the spec file contains
// all required section headers. This is a structural completeness check.
func TestCFDiscoverySpecContainsRequiredSections(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine source file path")
	}
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..")
	specPath := filepath.Join(repoRoot, "docs", "cf-discovery-spec.md")

	content, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("reading spec: %v", err)
	}
	text := string(content)

	required := []string{
		"parent_signature",
		"member_count_bucket",
		"freshness_window",
		"Adversarial",
		"Signing Rules",
		"Freshness Semantics",
		"Trust Model",
	}
	for _, section := range required {
		if !strings.Contains(text, section) {
			t.Errorf("spec missing required section/term: %q", section)
		}
	}
}

// ---------------------------------------------------------------------------
// Happy-path validation tests
// ---------------------------------------------------------------------------

// validSignature is a 64-byte value (all zeros) encoded as URL-safe base64
// without padding — used in tests that exercise field logic but do not perform
// real Ed25519 verification.
var validSignature = base64.RawURLEncoding.EncodeToString(make([]byte, 64))

func goodSnippet() *Snippet {
	return &Snippet{
		Name:              "lobby",
		Description:       "General discussion for social members",
		MemberCountBucket: "6-25",
		FreshnessWindow:   "5m",
		ParentSignature:   validSignature,
	}
}

func TestSnippetValidation_HappyPath(t *testing.T) {
	if err := ValidateSnippet(goodSnippet()); err != nil {
		t.Errorf("valid snippet rejected: %v", err)
	}
}

func TestSnippetValidation_AllBuckets(t *testing.T) {
	for _, bucket := range []string{"1", "2-5", "6-25", "26+"} {
		s := goodSnippet()
		s.MemberCountBucket = bucket
		if err := ValidateSnippet(s); err != nil {
			t.Errorf("bucket %q rejected: %v", bucket, err)
		}
	}
}

func TestSnippetValidation_FreshnessWindowBoundaries(t *testing.T) {
	cases := []struct {
		w    string
		want bool // true = valid
	}{
		{"1s", true},
		{"30s", true},
		{"5m", true},
		{"1h", true},
		{"24h", true},
		{"25h", false},
		{"999h", false},
		{"0s", false},
		{"-1m", false},
	}
	for _, tc := range cases {
		s := goodSnippet()
		s.FreshnessWindow = tc.w
		err := ValidateSnippet(s)
		if tc.want && err != nil {
			t.Errorf("freshness_window %q should be valid, got: %v", tc.w, err)
		}
		if !tc.want && err == nil {
			t.Errorf("freshness_window %q should be invalid, but passed", tc.w)
		}
	}
}

func TestSnippetValidation_ParentSignature(t *testing.T) {
	// Valid 64-byte signature
	good := base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	s := goodSnippet()
	s.ParentSignature = good
	if err := ValidateSnippet(s); err != nil {
		t.Errorf("valid parent_signature rejected: %v", err)
	}

	// 63 bytes — too short
	short := base64.RawURLEncoding.EncodeToString(make([]byte, 63))
	s.ParentSignature = short
	if err := ValidateSnippet(s); err == nil {
		t.Error("63-byte signature should be rejected")
	}

	// 65 bytes — too long
	long := base64.RawURLEncoding.EncodeToString(make([]byte, 65))
	s.ParentSignature = long
	if err := ValidateSnippet(s); err == nil {
		t.Error("65-byte signature should be rejected")
	}

	// Not base64
	s.ParentSignature = "not-valid-base64!!!"
	if err := ValidateSnippet(s); err == nil {
		t.Error("invalid base64 should be rejected")
	}
}

// ---------------------------------------------------------------------------
// Adversarial test cases (cf-discovery-spec.md §7)
// ---------------------------------------------------------------------------

// TestSnippetAdversarial_DottedName — §7.1
// A snippet with a dotted name must be rejected; it claims to vouch for a
// grandchild, which the parent campfire cannot observe.
func TestSnippetAdversarial_DottedName(t *testing.T) {
	s := goodSnippet()
	s.Name = "child.grandchild"
	err := ValidateSnippet(s)
	if err == nil {
		t.Error("adversarial: dotted name must be rejected (§7.1)")
	}
}

// TestSnippetAdversarial_InvalidBucket — §7.2
// Non-standard bucket values are rejected to prevent signal embedding.
func TestSnippetAdversarial_InvalidBucket(t *testing.T) {
	cases := []string{"10000", "0", "many", "1-999", ""}
	for _, bad := range cases {
		s := goodSnippet()
		s.MemberCountBucket = bad
		err := ValidateSnippet(s)
		if err == nil {
			t.Errorf("adversarial: bucket %q must be rejected (§7.2)", bad)
		}
	}
}

// TestSnippetAdversarial_ExcessiveFreshnessWindow — §7.3
// A hostile parent declares a very long freshness window to prevent degradation.
func TestSnippetAdversarial_ExcessiveFreshnessWindow(t *testing.T) {
	s := goodSnippet()
	s.FreshnessWindow = "999h"
	err := ValidateSnippet(s)
	if err == nil {
		t.Error("adversarial: freshness_window 999h must be rejected (§7.3)")
	}
}

// TestSnippetAdversarial_FreshnessStacking — §7.4
// Multi-hop freshness composition must use MIN, not MAX or final-hop.
// If the root has a 5m window and the leaf has a 1h window, the result is 5m.
func TestSnippetAdversarial_FreshnessStacking(t *testing.T) {
	// root→intermediate: 5m; intermediate→child: 1h
	// An attacker wants the 1h window to win. MIN must return 5m.
	windows := []string{"5m", "1h"}
	composed, err := ComposeFreshnessWindow(windows)
	if err != nil {
		t.Fatalf("ComposeFreshnessWindow failed: %v", err)
	}
	expected := 5 * time.Minute
	if composed != expected {
		t.Errorf("adversarial freshness stacking: got %v, want %v (§7.4)", composed, expected)
	}
}

// TestSnippetAdversarial_FreshnessStackingReverseOrder — §7.4 (reverse)
// MIN composition must work regardless of order.
func TestSnippetAdversarial_FreshnessStackingReverseOrder(t *testing.T) {
	windows := []string{"1h", "5m"}
	composed, err := ComposeFreshnessWindow(windows)
	if err != nil {
		t.Fatalf("ComposeFreshnessWindow failed: %v", err)
	}
	expected := 5 * time.Minute
	if composed != expected {
		t.Errorf("adversarial freshness stacking (reverse): got %v, want %v (§7.4)", composed, expected)
	}
}

// TestSnippetAdversarial_EmptyRequiredField — §7.6
// A snippet with any empty required field must be rejected.
func TestSnippetAdversarial_EmptyRequiredField(t *testing.T) {
	cases := []struct {
		field string
		mutate func(*Snippet)
	}{
		{"name", func(s *Snippet) { s.Name = "" }},
		{"description", func(s *Snippet) { s.Description = "" }},
		{"member_count_bucket", func(s *Snippet) { s.MemberCountBucket = "" }},
		{"freshness_window", func(s *Snippet) { s.FreshnessWindow = "" }},
		{"parent_signature", func(s *Snippet) { s.ParentSignature = "" }},
	}
	for _, tc := range cases {
		s := goodSnippet()
		tc.mutate(s)
		err := ValidateSnippet(s)
		if err == nil {
			t.Errorf("adversarial: empty %q must be rejected (§7.6)", tc.field)
		}
	}
}

// TestSnippetAdversarial_DescriptionNewline — description must not contain newlines
func TestSnippetAdversarial_DescriptionNewline(t *testing.T) {
	s := goodSnippet()
	s.Description = "Hello\nWorld"
	err := ValidateSnippet(s)
	if err == nil {
		t.Error("description with embedded newline must be rejected")
	}

	s.Description = "Hello\rWorld"
	err = ValidateSnippet(s)
	if err == nil {
		t.Error("description with embedded CR must be rejected")
	}
}

// ---------------------------------------------------------------------------
// JSON round-trip test — spec wire format matches struct tags
// ---------------------------------------------------------------------------

func TestSnippetJSONRoundTrip(t *testing.T) {
	orig := goodSnippet()

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify JSON keys match the spec names
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, key := range []string{"name", "description", "member_count_bucket", "freshness_window", "parent_signature"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON key %q missing from marshalled snippet", key)
		}
	}

	// Round-trip back to struct
	var got Snippet
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal to Snippet: %v", err)
	}
	if got.Name != orig.Name {
		t.Errorf("name mismatch: got %q, want %q", got.Name, orig.Name)
	}
	if got.MemberCountBucket != orig.MemberCountBucket {
		t.Errorf("member_count_bucket mismatch: got %q, want %q", got.MemberCountBucket, orig.MemberCountBucket)
	}
	if err := ValidateSnippet(&got); err != nil {
		t.Errorf("round-tripped snippet invalid: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Freshness composition — single-hop edge cases
// ---------------------------------------------------------------------------

func TestComposeFreshnessWindow_SingleHop(t *testing.T) {
	d, err := ComposeFreshnessWindow([]string{"5m"})
	if err != nil {
		t.Fatal(err)
	}
	if d != 5*time.Minute {
		t.Errorf("single-hop: got %v, want 5m", d)
	}
}

func TestComposeFreshnessWindow_Empty(t *testing.T) {
	d, err := ComposeFreshnessWindow(nil)
	if err != nil {
		t.Fatal(err)
	}
	if d != 0 {
		t.Errorf("empty chain: got %v, want 0", d)
	}
}

func TestComposeFreshnessWindow_ThreeHops(t *testing.T) {
	// Three hops: 1h, 30m, 10m — result should be 10m
	d, err := ComposeFreshnessWindow([]string{"1h", "30m", "10m"})
	if err != nil {
		t.Fatal(err)
	}
	if d != 10*time.Minute {
		t.Errorf("three-hop: got %v, want 10m", d)
	}
}

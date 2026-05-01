package discovery_test

// snippet_test.go — Tests for Tier 1 snippet validation, signing, and verification.
// Covers all adversarial cases from cf-discovery-spec.md §7.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	discovery "github.com/campfire-net/campfire/cf-conventions/cf-discovery"
)

// validSig is a 64-byte all-zeros value encoded as URL-safe base64 without padding.
// Used for structural tests that do not check signature validity.
var validSig = base64.RawURLEncoding.EncodeToString(make([]byte, 64))

func goodSnippet() discovery.Snippet {
	return discovery.Snippet{
		Name:              "lobby",
		Description:       "General discussion for social members",
		MemberCountBucket: "6-25",
		FreshnessWindow:   "5m",
		ParentSignature:   validSig,
	}
}

// ── Happy path ──────────────────────────────────────────────────────────────

func TestValidateSnippet_HappyPath(t *testing.T) {
	if err := discovery.ValidateSnippet(goodSnippet()); err != nil {
		t.Errorf("valid snippet rejected: %v", err)
	}
}

func TestValidateSnippet_AllBuckets(t *testing.T) {
	for _, b := range []string{discovery.BucketSolo, discovery.BucketSmall, discovery.BucketMedium, discovery.BucketLarge} {
		s := goodSnippet()
		s.MemberCountBucket = b
		if err := discovery.ValidateSnippet(s); err != nil {
			t.Errorf("bucket %q rejected: %v", b, err)
		}
	}
}

func TestValidateSnippet_FreshnessWindowBoundaries(t *testing.T) {
	cases := []struct {
		w    string
		want bool
	}{
		{"1s", true},
		{"5m", true},
		{"24h", true},
		{"0s", false},   // not positive
		{"999h", false}, // exceeds 24h max
		{"bad", false},  // not a valid duration
	}
	for _, tc := range cases {
		s := goodSnippet()
		s.FreshnessWindow = tc.w
		err := discovery.ValidateSnippet(s)
		if tc.want && err != nil {
			t.Errorf("freshness_window %q should be valid, got: %v", tc.w, err)
		}
		if !tc.want && err == nil {
			t.Errorf("freshness_window %q should be invalid, but passed", tc.w)
		}
	}
}

// ── Adversarial cases (cf-discovery-spec.md §7) ────────────────────────────

// §7.1 — Dotted name injection
func TestValidateSnippet_DottedName(t *testing.T) {
	s := goodSnippet()
	s.Name = "child.grandchild"
	if err := discovery.ValidateSnippet(s); err == nil {
		t.Error("§7.1: dotted name should be rejected")
	}
}

// §7.2 — Invalid bucket value
func TestValidateSnippet_InvalidBucket(t *testing.T) {
	for _, bad := range []string{"10000", "", "ALL"} {
		s := goodSnippet()
		s.MemberCountBucket = bad
		if err := discovery.ValidateSnippet(s); err == nil {
			t.Errorf("§7.2: invalid bucket %q should be rejected", bad)
		}
	}
}

// §7.3 — Out-of-range freshness window
func TestValidateSnippet_FreshnessOutOfRange(t *testing.T) {
	s := goodSnippet()
	s.FreshnessWindow = "999h"
	if err := discovery.ValidateSnippet(s); err == nil {
		t.Error("§7.3: freshness_window 999h should be rejected (exceeds 24h max)")
	}
}

// §7.6 — Empty required field
func TestValidateSnippet_EmptyFields(t *testing.T) {
	fields := []struct {
		name string
		set  func(*discovery.Snippet)
	}{
		{"name", func(s *discovery.Snippet) { s.Name = "" }},
		{"description", func(s *discovery.Snippet) { s.Description = "" }},
		{"member_count_bucket", func(s *discovery.Snippet) { s.MemberCountBucket = "" }},
		{"freshness_window", func(s *discovery.Snippet) { s.FreshnessWindow = "" }},
		{"parent_signature", func(s *discovery.Snippet) { s.ParentSignature = "" }},
	}
	for _, tc := range fields {
		s := goodSnippet()
		tc.set(&s)
		if err := discovery.ValidateSnippet(s); err == nil {
			t.Errorf("§7.6: empty %s should be rejected", tc.name)
		}
	}
}

// ── Sign + verify round-trip ─────────────────────────────────────────────────

func TestSignVerifySnippet_RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	s, err := discovery.SignSnippet("lobby", "General discussion", "6-25", "5m", "", priv)
	if err != nil {
		t.Fatalf("SignSnippet: %v", err)
	}
	now := time.Now()
	result, err := discovery.VerifySnippet(*s, pub, now.Unix(), now)
	if err != nil {
		t.Errorf("VerifySnippet rejected a fresh signed snippet: %v", err)
	}
	if result.Stale {
		t.Error("fresh snippet should not be stale")
	}
}

// §7.5 — Grandparent signer (wrong key)
func TestVerifySnippet_WrongKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	wrongPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	s, err := discovery.SignSnippet("lobby", "test", "1", "5m", "", priv)
	if err != nil {
		t.Fatalf("SignSnippet: %v", err)
	}
	now := time.Now()
	if _, err := discovery.VerifySnippet(*s, wrongPub, now.Unix(), now); err == nil {
		t.Error("§7.5: snippet with wrong key should fail verification")
	}
}

// §7.4 — Freshness stacking: stale state at one hop degrades the chain
func TestComposeFreshnessWindows_AnyStalePropagate(t *testing.T) {
	chain := []discovery.SnippetResult{
		{Snippet: discovery.Snippet{FreshnessWindow: "5m"}, Stale: true},
		{Snippet: discovery.Snippet{FreshnessWindow: "1h"}, Stale: false},
	}
	minWindow, anyStale, err := discovery.ComposeFreshnessWindows(chain)
	if err != nil {
		t.Fatalf("ComposeFreshnessWindows: %v", err)
	}
	if !anyStale {
		t.Error("anyStale should be true when any hop is stale")
	}
	// Min window should be 5m, not 1h
	if minWindow != 5*60*1e9 { // 5 minutes in nanoseconds
		t.Errorf("min window = %v, want 5m", minWindow)
	}
}

// Staleness: snippet past its freshness window is degraded (not rejected)
func TestVerifySnippet_StaleIsDegradedNotRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	s, err := discovery.SignSnippet("lobby", "test", "1", "5m", "", priv)
	if err != nil {
		t.Fatalf("SignSnippet: %v", err)
	}
	// Message timestamp 1 hour ago — 5m freshness window expired
	msgTimestamp := time.Now().Add(-time.Hour).Unix()
	result, err := discovery.VerifySnippet(*s, pub, msgTimestamp, time.Now())
	if err != nil {
		t.Errorf("stale snippet should be degraded (not rejected): %v", err)
	}
	if !result.Stale {
		t.Error("snippet past freshness window should be marked Stale")
	}
}

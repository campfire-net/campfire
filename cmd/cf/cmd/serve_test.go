package cmd

// Tests for workspace-zia: serve command does not resolve campfire ID prefix.
//
// Before the fix, serve.go passed args[0] directly to s.GetMembership without
// calling resolveCampfireID(). A 6-char prefix that is a valid prefix of a known
// campfire ID would always return nil membership (raw prefix ≠ full ID), and the
// subsequent campfireID[:12] slice would panic if the ID was shorter than 12 chars.
//
// After the fix, serve calls resolveCampfireID() before the membership check, so:
//  1. A prefix resolves to the full ID when unambiguous.
//  2. A prefix with no match returns a descriptive error (not a panic).
//  3. An ambiguous prefix returns an "ambiguous" error.

import (
	"os"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
)

// TestServe_ShortIDNoMatch verifies that passing a short (< 12 char) campfire ID
// that matches nothing returns an error rather than panicking.
//
// Before the fix: campfireID[:12] would panic with "index out of range".
// After the fix: resolveCampfireID returns "no campfire found matching prefix".
func TestServe_ShortIDNoMatch(t *testing.T) {
	emptyDir := t.TempDir()
	os.Setenv("CF_BEACON_DIR", emptyDir)
	defer os.Unsetenv("CF_BEACON_DIR")

	s, _ := makeTestStore(t, nil)
	defer s.Close()

	// 6-char prefix that matches nothing — previously would panic at [:12].
	_, err := resolveCampfireID("abc123", s)
	if err == nil {
		t.Fatal("expected error for no-match short prefix, got nil")
	}
	if !strings.Contains(err.Error(), "abc123") {
		t.Errorf("expected error to mention the prefix, got: %v", err)
	}
}

// TestServe_PrefixResolves verifies that a valid prefix of a membership campfire ID
// is resolved to the full ID by resolveCampfireID (the call now made by serve).
//
// Before the fix: serve passed the raw prefix to GetMembership → always "not a member".
// After the fix: serve calls resolveCampfireID first → prefix expands to full ID.
func TestServe_PrefixResolves(t *testing.T) {
	emptyDir := t.TempDir()
	os.Setenv("CF_BEACON_DIR", emptyDir)
	defer os.Unsetenv("CF_BEACON_DIR")

	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	full := id.PublicKeyHex()
	prefix := full[:10]

	s, _ := makeTestStore(t, []string{full})
	defer s.Close()

	got, err := resolveCampfireID(prefix, s)
	if err != nil {
		t.Fatalf("resolveCampfireID(%q): unexpected error: %v", prefix, err)
	}
	if got != full {
		t.Errorf("resolveCampfireID(%q) = %q, want %q", prefix, got, full)
	}

	// Simulate what serve now does after resolveCampfireID: look up membership.
	// The resolved full ID must be a member.
	m, err := s.GetMembership(got)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Errorf("GetMembership(%q) returned nil — serve would report 'not a member' (regression)", got)
	}
}

// TestServe_ExactIDPassThrough verifies that a full 64-char ID is returned as-is.
func TestServe_ExactIDPassThrough(t *testing.T) {
	s, _ := makeTestStore(t, nil)
	defer s.Close()

	exact := strings.Repeat("a", 64)
	got, err := resolveCampfireID(exact, s)
	if err != nil {
		t.Fatalf("unexpected error for exact ID: %v", err)
	}
	if got != exact {
		t.Errorf("got %q, want %q", got, exact)
	}
}

// TestServe_PrefixSafeSlice verifies that after resolveCampfireID, the returned
// ID is always >= 12 characters, so campfireID[:12] in error messages is safe.
func TestServe_PrefixSafeSlice(t *testing.T) {
	emptyDir := t.TempDir()
	os.Setenv("CF_BEACON_DIR", emptyDir)
	defer os.Unsetenv("CF_BEACON_DIR")

	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	full := id.PublicKeyHex()
	s, _ := makeTestStore(t, []string{full})
	defer s.Close()

	// Resolve with a 6-char prefix.
	resolved, err := resolveCampfireID(full[:6], s)
	if err != nil {
		t.Fatalf("resolveCampfireID: %v", err)
	}
	// The result must be at least 12 chars so [:12] in serve.go is safe.
	if len(resolved) < 12 {
		t.Errorf("resolved ID length %d < 12, campfireID[:12] in serve.go would panic", len(resolved))
	}
	// It should equal the full 64-char ID.
	if resolved != full {
		t.Errorf("resolved %q, want %q", resolved, full)
	}
}

// TestServe_AmbiguousPrefixError verifies that an ambiguous prefix returns an error
// containing "ambiguous", not a panic.
func TestServe_AmbiguousPrefixError(t *testing.T) {
	emptyDir := t.TempDir()
	os.Setenv("CF_BEACON_DIR", emptyDir)
	defer os.Unsetenv("CF_BEACON_DIR")

	id1, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	s, _ := makeTestStore(t, []string{id1.PublicKeyHex(), id2.PublicKeyHex()})
	defer s.Close()

	// Empty prefix matches all — should be ambiguous.
	_, err = resolveCampfireID("", s)
	if err == nil {
		t.Fatal("expected ambiguous error, got nil")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected 'ambiguous' in error, got: %v", err)
	}
}

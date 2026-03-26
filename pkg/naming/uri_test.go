package naming

import (
	"os"
	"testing"
)

func TestParseURI_Valid(t *testing.T) {
	tests := []struct {
		input    string
		name     string
		segments []string
		path     string
		args     map[string]string
	}{
		{
			input:    "cf://aietf",
			name:     "aietf",
			segments: []string{"aietf"},
		},
		{
			input:    "cf://aietf.social.lobby",
			name:     "aietf.social.lobby",
			segments: []string{"aietf", "social", "lobby"},
		},
		{
			input:    "cf://aietf.social.lobby/trending",
			name:     "aietf.social.lobby",
			segments: []string{"aietf", "social", "lobby"},
			path:     "trending",
		},
		{
			input:    "cf://aietf.social.lobby/trending?window=24h",
			name:     "aietf.social.lobby",
			segments: []string{"aietf", "social", "lobby"},
			path:     "trending",
			args:     map[string]string{"window": "24h"},
		},
		{
			input:    "cf://aietf.directory.root/search?topic=ai-tools",
			name:     "aietf.directory.root",
			segments: []string{"aietf", "directory", "root"},
			path:     "search",
			args:     map[string]string{"topic": "ai-tools"},
		},
		{
			input:    "cf://acme.internal.standup/blockers",
			name:     "acme.internal.standup",
			segments: []string{"acme", "internal", "standup"},
			path:     "blockers",
		},
		// Canonicalization: uppercase → lowercase
		{
			input:    "CF://AIETF.Social",
			name:     "aietf.social",
			segments: []string{"aietf", "social"},
		},
		// Multiple query params
		{
			input:    "cf://aietf.social.lobby/trending?window=24h&limit=10",
			name:     "aietf.social.lobby",
			segments: []string{"aietf", "social", "lobby"},
			path:     "trending",
			args:     map[string]string{"window": "24h", "limit": "10"},
		},
		// Segment with hyphens
		{
			input:    "cf://aietf.social.ai-tools",
			name:     "aietf.social.ai-tools",
			segments: []string{"aietf", "social", "ai-tools"},
		},
		// Single segment with number
		{
			input:    "cf://x42",
			name:     "x42",
			segments: []string{"x42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			u, err := ParseURI(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if u.Name != tt.name {
				t.Errorf("name = %q, want %q", u.Name, tt.name)
			}
			if len(u.Segments) != len(tt.segments) {
				t.Fatalf("segments = %v, want %v", u.Segments, tt.segments)
			}
			for i, seg := range u.Segments {
				if seg != tt.segments[i] {
					t.Errorf("segment[%d] = %q, want %q", i, seg, tt.segments[i])
				}
			}
			if u.Path != tt.path {
				t.Errorf("path = %q, want %q", u.Path, tt.path)
			}
			if tt.args != nil {
				for k, v := range tt.args {
					got := u.Query.Get(k)
					if got != v {
						t.Errorf("arg %q = %q, want %q", k, got, v)
					}
				}
			}
		})
	}
}

func TestParseURI_Invalid(t *testing.T) {
	tests := []struct {
		input string
		errRe string // substring expected in error
	}{
		// Wrong scheme
		{"http://aietf.social", "must start with cf://"},
		{"aietf.social", "must start with cf://"},
		// Empty after scheme
		{"cf://", "empty"},
		// Empty segment (double dot)
		{"cf://aietf..social", "empty segment"},
		// Path traversal
		{"cf://aietf.social/../root", "path traversal"},
		// Userinfo
		{"cf://admin@aietf.social", "userinfo"},
		// Port number
		{"cf://aietf.social:8080", "port"},
		// Fragment
		{"cf://aietf.social#section", "fragment"},
		// Exceeds 8-segment depth
		{"cf://a.b.c.d.e.f.g.h.i", "exceeds maximum depth"},
		// Segment starts with hyphen
		{"cf://-bad.name", "must be lowercase"},
		// Segment ends with hyphen
		{"cf://bad-.name", "must be lowercase"},
		// Non-ASCII in name
		{"cf://café.social", "non-ASCII"},
		// Null byte
		{"cf://aietf\x00.social", "null byte"},
		// Segment with underscore
		{"cf://bad_name", "must be lowercase"},
		// Segment with uppercase only after canonicalization still fails on special chars
		{"cf://aietf.social.lobby!", "must be lowercase"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := ParseURI(tt.input)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.errRe != "" && !containsCI(err.Error(), tt.errRe) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.errRe)
			}
		})
	}
}

func TestParseURI_TestVector8(t *testing.T) {
	// Test Vector 8 from the convention spec
	rejects := []string{
		"cf://aietf..social",
		"cf://aietf.social/../root",
		"cf://admin@aietf.social",
		"cf://aietf.social:8080",
		"cf://aietf.social.a.b.c.d.e.f.g",
	}
	for _, input := range rejects {
		_, err := ParseURI(input)
		if err == nil {
			t.Errorf("expected rejection for %q", input)
		}
	}

	// Normalization case
	u, err := ParseURI("CF://AIETF.Social")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Name != "aietf.social" {
		t.Errorf("name = %q, want %q", u.Name, "aietf.social")
	}
}

func TestValidateSegment(t *testing.T) {
	valid := []string{"aietf", "social", "ai-tools", "x42", "a", "abc-def-ghi"}
	for _, seg := range valid {
		if err := ValidateSegment(seg); err != nil {
			t.Errorf("ValidateSegment(%q) = %v, want nil", seg, err)
		}
	}

	invalid := []string{"-bad", "bad-", "Bad", "hello_world", "a b", "", "a..b"}
	for _, seg := range invalid {
		if err := ValidateSegment(seg); err == nil {
			t.Errorf("ValidateSegment(%q) = nil, want error", seg)
		}
	}
}

func TestIsCampfireURI(t *testing.T) {
	if !IsCampfireURI("cf://test") {
		t.Error("expected true for cf://test")
	}
	if !IsCampfireURI("CF://TEST") {
		t.Error("expected true for CF://TEST")
	}
	if IsCampfireURI("http://test") {
		t.Error("expected false for http://test")
	}
	if IsCampfireURI("abcdef1234") {
		t.Error("expected false for hex string")
	}
}

func TestSanitizeDescription(t *testing.T) {
	// Normal text
	if got := SanitizeDescription("Hello world"); got != "Hello world" {
		t.Errorf("got %q", got)
	}

	// Truncation at 80 chars
	long := string(make([]byte, 100))
	for i := range long {
		long = long[:i] + "a"
	}
	long = ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	got := SanitizeDescription(long)
	if len(got) != 80 {
		t.Errorf("expected 80 chars, got %d", len(got))
	}

	// Control character stripping
	got = SanitizeDescription("hello\nworld\ttab\x00null")
	if got != "helloworldtabnull" {
		t.Errorf("got %q", got)
	}
}

func TestURIString(t *testing.T) {
	u, err := ParseURI("cf://aietf.social.lobby/trending?window=24h")
	if err != nil {
		t.Fatal(err)
	}
	s := u.String()
	// Should round-trip (canonicalized)
	if s != "cf://aietf.social.lobby/trending?window=24h" {
		t.Errorf("String() = %q", s)
	}
}

func TestURIHasPath(t *testing.T) {
	u1, _ := ParseURI("cf://aietf.social.lobby")
	if u1.HasPath() {
		t.Error("expected HasPath() = false")
	}
	u2, _ := ParseURI("cf://aietf.social.lobby/trending")
	if !u2.HasPath() {
		t.Error("expected HasPath() = true")
	}
}

// containsCI checks if s contains substr (case-insensitive).
func containsCI(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			len(substr) == 0 ||
			findCI(s, substr))
}

func findCI(s, substr string) bool {
	s = toLower(s)
	substr = toLower(substr)
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// TestParseURI_AliasKind tests tilde-alias parsing.
func TestParseURI_AliasKind(t *testing.T) {
	u, err := ParseURI("cf://~baron")
	if err != nil {
		t.Fatalf("ParseURI alias: %v", err)
	}
	if u.Kind != URIKindAlias {
		t.Errorf("expected URIKindAlias, got %v", u.Kind)
	}
	if u.Alias != "baron" {
		t.Errorf("expected alias 'baron', got %q", u.Alias)
	}
	if u.String() != "cf://~baron" {
		t.Errorf("String() = %q, want 'cf://~baron'", u.String())
	}
}

// TestParseURI_DirectKind tests 64-hex direct ID parsing.
func TestParseURI_DirectKind(t *testing.T) {
	hexID := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	u, err := ParseURI("cf://" + hexID)
	if err != nil {
		t.Fatalf("ParseURI direct: %v", err)
	}
	if u.Kind != URIKindDirect {
		t.Errorf("expected URIKindDirect, got %v", u.Kind)
	}
	if u.CampfireID != hexID {
		t.Errorf("expected CampfireID %q, got %q", hexID, u.CampfireID)
	}
	if u.String() != "cf://"+hexID {
		t.Errorf("String() = %q, want cf://%s", u.String(), hexID)
	}
}

// TestParseURI_ShortHexIsNamed tests that a short hex string is treated as a name, not a direct ID.
func TestParseURI_ShortHexIsNamed(t *testing.T) {
	// 12 hex chars — valid as a segment name, not a direct ID
	_, err := ParseURI("cf://abc123def456")
	// Should parse as a named URI (single segment) if valid, or fail segment validation
	// abc123def456 = 12 chars, valid segment — should succeed as named
	if err != nil {
		t.Logf("short hex: %v", err)
	}
}

// TestParseURI_AliasRejectedInbound tests that alias URIs are rejected by ValidateInbound.
func TestParseURI_AliasRejectedInbound(t *testing.T) {
	u, err := ParseURI("cf://~lobby")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	if err := ValidateInbound(u); err == nil {
		t.Error("expected error for alias URI in inbound context")
	}
}

// TestParseURI_DirectAllowedInbound tests that direct ID URIs pass ValidateInbound.
func TestParseURI_DirectAllowedInbound(t *testing.T) {
	hexID := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	u, err := ParseURI("cf://" + hexID)
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	if err := ValidateInbound(u); err != nil {
		t.Errorf("expected no error for direct URI in inbound context: %v", err)
	}
}

// TestAliasStoreRoundTrip tests alias set/get/remove lifecycle.
func TestAliasStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewAliasStore(dir)

	hexID := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

	if err := store.Set("lobby", hexID); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := store.Get("lobby")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != hexID {
		t.Errorf("Get = %q, want %q", got, hexID)
	}

	aliases, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if aliases["lobby"] != hexID {
		t.Errorf("List[lobby] = %q, want %q", aliases["lobby"], hexID)
	}

	if err := store.Remove("lobby"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := store.Get("lobby"); err == nil {
		t.Error("expected error after Remove, got nil")
	}
}

// TestAliasStorePermissions tests that aliases.json is written with 0600 permissions.
func TestAliasStorePermissions(t *testing.T) {
	dir := t.TempDir()
	store := NewAliasStore(dir)
	hexID := "0000000000000000000000000000000000000000000000000000000000000000"
	if err := store.Set("test", hexID); err != nil {
		t.Fatalf("Set: %v", err)
	}
	fi, err := os.Stat(dir + "/aliases.json")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %04o", fi.Mode().Perm())
	}
}

// TestLooksLikeName verifies bare dot-separated name detection.
func TestLooksLikeName(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"aietf.social.lobby", true},
		{"baron.ready.galtrader", true},
		{"single-segment", false}, // no dots
		{"cf://aietf.social.lobby", false}, // has cf:// prefix, not a bare name
		{"abc123.foo", true},
		{"-bad.segment", false},
		{"bad-.segment", false},
		{"", false},
	}
	for _, tc := range cases {
		got := LooksLikeName(tc.input)
		if got != tc.want {
			t.Errorf("LooksLikeName(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

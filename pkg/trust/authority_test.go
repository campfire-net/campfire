package trust

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
)

// helpers to build minimal declarations for tests.

func declWithSignerType(st convention.SignerType) *convention.Declaration {
	return &convention.Declaration{
		Convention: "test",
		Version:    "0.1",
		Operation:  "send",
		Signing:    string(st),
		SignerType: st,
		Antecedents: "none",
	}
}

func argWith(name, typ string, maxLen, min, max, maxCount int, values []string) convention.ArgDescriptor {
	return convention.ArgDescriptor{
		Name:      name,
		Type:      typ,
		MaxLength: maxLen,
		Min:       min,
		Max:       max,
		MaxCount:  maxCount,
		Values:    values,
	}
}

// TestResolveAuthority_RegistryDeclaration — convention_registry signer → AuthoritySemantic.
func TestResolveAuthority_RegistryDeclaration(t *testing.T) {
	decl := declWithSignerType(convention.SignerConventionRegistry)
	got := ResolveAuthority(decl, nil)
	if got != AuthoritySemantic {
		t.Errorf("expected AuthoritySemantic, got %q", got)
	}
}

// TestResolveAuthority_CampfireKeyDeclaration — campfire_key signer → AuthorityOperational.
func TestResolveAuthority_CampfireKeyDeclaration(t *testing.T) {
	decl := declWithSignerType(convention.SignerCampfireKey)
	got := ResolveAuthority(decl, nil)
	if got != AuthorityOperational {
		t.Errorf("expected AuthorityOperational, got %q", got)
	}
}

// TestResolveAuthority_MemberDeclaration — member_key signer → AuthorityUntrusted.
func TestResolveAuthority_MemberDeclaration(t *testing.T) {
	decl := declWithSignerType(convention.SignerMemberKey)
	got := ResolveAuthority(decl, nil)
	if got != AuthorityUntrusted {
		t.Errorf("expected AuthorityUntrusted, got %q", got)
	}
}

// TestValidateOperationalOverride_Tightening — local has lower max_length → nil.
func TestValidateOperationalOverride_Tightening(t *testing.T) {
	registry := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			argWith("body", "string", 1024, 0, 0, 0, nil),
		},
	}
	local := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			argWith("body", "string", 512, 0, 0, 0, nil),
		},
	}
	if err := ValidateOperationalOverride(registry, local); err != nil {
		t.Errorf("expected nil error for tightening, got %v", err)
	}
}

// TestValidateOperationalOverride_Loosening — local has higher max_length → error.
func TestValidateOperationalOverride_Loosening(t *testing.T) {
	registry := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			argWith("body", "string", 1024, 0, 0, 0, nil),
		},
	}
	local := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			argWith("body", "string", 2048, 0, 0, 0, nil),
		},
	}
	if err := ValidateOperationalOverride(registry, local); err == nil {
		t.Error("expected error for loosening max_length, got nil")
	}
}

// TestValidateOperationalOverride_SubsetValues — subset ok, superset → error.
func TestValidateOperationalOverride_SubsetValues(t *testing.T) {
	registry := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			argWith("priority", "enum", 0, 0, 0, 0, []string{"low", "medium", "high"}),
		},
	}

	// Subset: remove "high" — should be allowed.
	localSubset := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			argWith("priority", "enum", 0, 0, 0, 0, []string{"low", "medium"}),
		},
	}
	if err := ValidateOperationalOverride(registry, localSubset); err != nil {
		t.Errorf("expected nil for value subset, got %v", err)
	}

	// Superset: add a new value — should be rejected.
	localSuperset := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			argWith("priority", "enum", 0, 0, 0, 0, []string{"low", "medium", "high", "critical"}),
		},
	}
	if err := ValidateOperationalOverride(registry, localSuperset); err == nil {
		t.Error("expected error for adding enum value, got nil")
	}
}

// TestSemanticFingerprint_Match — same semantic fields → same hash.
func TestSemanticFingerprint_Match(t *testing.T) {
	decl1 := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			{Name: "body", Type: "string", Required: true},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "chat:message", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
		Signing:     "member_key",
		Steps: []convention.Step{
			{Action: "send"},
		},
	}
	decl2 := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			{Name: "body", Type: "string", Required: true},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "chat:message", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
		Signing:     "member_key",
		Steps: []convention.Step{
			{Action: "send"},
		},
		// Operational fields differ — fingerprint should still match.
		RateLimit: &convention.RateLimit{Max: 10, Per: "sender", Window: "1m"},
	}
	h1 := SemanticFingerprint(decl1)
	h2 := SemanticFingerprint(decl2)
	if h1 != h2 {
		t.Errorf("expected same fingerprint, got %q vs %q", h1, h2)
	}
}

// TestSemanticFingerprint_Mismatch — change arg type → different hash.
func TestSemanticFingerprint_Mismatch(t *testing.T) {
	decl1 := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			{Name: "count", Type: "integer", Required: true},
		},
		Antecedents: "none",
		Signing:     "member_key",
	}
	decl2 := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			{Name: "count", Type: "string", Required: true},
		},
		Antecedents: "none",
		Signing:     "member_key",
	}
	h1 := SemanticFingerprint(decl1)
	h2 := SemanticFingerprint(decl2)
	if h1 == h2 {
		t.Error("expected different fingerprints for different arg types, got same")
	}
}

// TestCampfireKeyGate — signing="campfire_key" but SignerType=member → untrusted.
func TestCampfireKeyGate(t *testing.T) {
	decl := &convention.Declaration{
		Signing:    "campfire_key",
		SignerType: convention.SignerMemberKey, // mismatch
		Antecedents: "none",
	}
	got := ResolveAuthority(decl, nil)
	// A member key claiming campfire_key signing is gated → untrusted.
	if got != AuthorityUntrusted {
		t.Errorf("expected AuthorityUntrusted for mismatched campfire_key gate, got %q", got)
	}
}

// TestMonotonicVersion — higher version supersedes lower.
func TestMonotonicVersion(t *testing.T) {
	if !CompareVersions("0.4", "0.3") {
		t.Error("expected 0.4 to supersede 0.3")
	}
	if CompareVersions("0.3", "0.4") {
		t.Error("expected 0.3 NOT to supersede 0.4")
	}
	if CompareVersions("0.3", "0.3") {
		t.Error("expected equal versions NOT to supersede each other")
	}
	if !CompareVersions("1.0", "0.9") {
		t.Error("expected 1.0 to supersede 0.9")
	}
	if !CompareVersions("0.10", "0.9") {
		t.Error("expected 0.10 to supersede 0.9 (numeric comparison)")
	}
}

// TestValidateOperationalOverride_RateLimit — tighter rate limit is ok; looser is error.
func TestValidateOperationalOverride_RateLimit(t *testing.T) {
	registry := &convention.Declaration{
		RateLimit: &convention.RateLimit{Max: 50, Per: "sender", Window: "1h"},
	}
	localTighter := &convention.Declaration{
		RateLimit: &convention.RateLimit{Max: 20, Per: "sender", Window: "1h"},
	}
	if err := ValidateOperationalOverride(registry, localTighter); err != nil {
		t.Errorf("expected nil for tighter rate limit, got %v", err)
	}

	localLooser := &convention.Declaration{
		RateLimit: &convention.RateLimit{Max: 100, Per: "sender", Window: "1h"},
	}
	if err := ValidateOperationalOverride(registry, localLooser); err == nil {
		t.Error("expected error for loosened rate_limit.max, got nil")
	}
}

// TestValidateOperationalOverride_MinMax — narrowing is ok; widening is error.
func TestValidateOperationalOverride_MinMax(t *testing.T) {
	registry := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			{Name: "count", Type: "integer", Min: 1, Max: 100},
		},
	}

	// Narrowing: raise min, lower max.
	localNarrow := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			{Name: "count", Type: "integer", Min: 5, Max: 50},
		},
	}
	if err := ValidateOperationalOverride(registry, localNarrow); err != nil {
		t.Errorf("expected nil for narrowing min/max, got %v", err)
	}

	// Widening min (lower than registry).
	localWiderMin := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			{Name: "count", Type: "integer", Min: 0, Max: 100},
		},
	}
	if err := ValidateOperationalOverride(registry, localWiderMin); err == nil {
		t.Error("expected error for lowering min, got nil")
	}

	// Widening max (higher than registry).
	localWiderMax := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			{Name: "count", Type: "integer", Min: 1, Max: 200},
		},
	}
	if err := ValidateOperationalOverride(registry, localWiderMax); err == nil {
		t.Error("expected error for raising max, got nil")
	}

	// Local max=0 (unset/no limit) when registry has a limit → loosening.
	localNoMax := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			{Name: "count", Type: "integer", Min: 1, Max: 0},
		},
	}
	if err := ValidateOperationalOverride(registry, localNoMax); err == nil {
		t.Error("expected error for local max=0 (no limit) when registry has max=100")
	}
}

// TestValidateOperationalOverride_ZeroUpperBound — local=0 for upper-bound fields is loosening.
func TestValidateOperationalOverride_ZeroUpperBound(t *testing.T) {
	// max_length: local=0 (no limit) when registry has a limit → loosening.
	regMaxLen := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			argWith("body", "string", 1024, 0, 0, 0, nil),
		},
	}
	localNoMaxLen := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			argWith("body", "string", 0, 0, 0, 0, nil),
		},
	}
	if err := ValidateOperationalOverride(regMaxLen, localNoMaxLen); err == nil {
		t.Error("expected error for local max_length=0 (no limit) when registry has max_length=1024")
	}

	// max_count: local=0 (no limit) when registry has a limit → loosening.
	regMaxCount := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			argWith("tags", "tag_set", 0, 0, 0, 10, nil),
		},
	}
	localNoMaxCount := &convention.Declaration{
		Args: []convention.ArgDescriptor{
			argWith("tags", "tag_set", 0, 0, 0, 0, nil),
		},
	}
	if err := ValidateOperationalOverride(regMaxCount, localNoMaxCount); err == nil {
		t.Error("expected error for local max_count=0 (no limit) when registry has max_count=10")
	}
}

// TestValidateOperationalOverride_RateLimitRemoval — removing rate_limit entirely is a loosening.
func TestValidateOperationalOverride_RateLimitRemoval(t *testing.T) {
	registry := &convention.Declaration{
		RateLimit: &convention.RateLimit{Max: 50, Per: "sender", Window: "1h"},
	}
	// Local sets rate_limit = nil — this removes the constraint, which is loosening.
	localNil := &convention.Declaration{
		RateLimit: nil,
	}
	if err := ValidateOperationalOverride(registry, localNil); err == nil {
		t.Error("expected error when local removes registry rate_limit entirely (nil), got nil")
	}
}

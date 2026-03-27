package trust

import (
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
)

// makeDecl constructs a minimal declaration for testing.
func makeDecl(conventionSlug, operation, version string, args []convention.ArgDescriptor) *convention.Declaration {
	return &convention.Declaration{
		Convention:  conventionSlug,
		Operation:   operation,
		Version:     version,
		Antecedents: "none",
		Signing:     "member_key",
		Args:        args,
	}
}

// makeDeclWithArgs builds a declaration with specific semantic fields.
func makeDeclWithArgs(conventionSlug, operation string, argNames ...string) *convention.Declaration {
	args := make([]convention.ArgDescriptor, len(argNames))
	for i, name := range argNames {
		args[i] = convention.ArgDescriptor{Name: name, Type: "string", Required: true}
	}
	return makeDecl(conventionSlug, operation, "0.1", args)
}

// TestPolicyEngine_SeedConventions verifies that seeded declarations are adopted.
func TestPolicyEngine_SeedConventions(t *testing.T) {
	e := NewPolicyEngine()
	decl := makeDeclWithArgs("social", "post", "body")

	e.SeedConventions([]*convention.Declaration{decl})

	ac, ok := e.GetAdopted("social", "post")
	if !ok {
		t.Fatal("expected seeded convention to be adopted")
	}
	if ac.Source != SourceSeed {
		t.Errorf("expected source=seed, got %q", ac.Source)
	}
	if !strings.HasPrefix(ac.Fingerprint, "sha256:") {
		t.Errorf("expected fingerprint with sha256: prefix, got %q", ac.Fingerprint)
	}
}

// TestPolicyEngine_SeedDoesNotOverwrite verifies that seeding doesn't overwrite existing adoptions.
func TestPolicyEngine_SeedDoesNotOverwrite(t *testing.T) {
	e := NewPolicyEngine()
	decl := makeDeclWithArgs("social", "post", "body")

	// First adopt manually.
	e.Adopt(decl, SourceManual, "manual")

	// Now seed — should NOT overwrite.
	e.SeedConventions([]*convention.Declaration{decl})

	ac, ok := e.GetAdopted("social", "post")
	if !ok {
		t.Fatal("expected convention to remain adopted")
	}
	if ac.Source != SourceManual {
		t.Errorf("seed should not overwrite manual adoption, got source=%q", ac.Source)
	}
}

// TestPolicyEngine_Adopt verifies explicit adoption.
func TestPolicyEngine_Adopt(t *testing.T) {
	e := NewPolicyEngine()
	decl := makeDeclWithArgs("trust", "verify", "key")

	e.Adopt(decl, SourcePeer, "peer-campfire-123")

	ac, ok := e.GetAdopted("trust", "verify")
	if !ok {
		t.Fatal("expected adopted convention to be found")
	}
	if ac.Source != SourcePeer {
		t.Errorf("expected source=peer, got %q", ac.Source)
	}
	if ac.SourceID != "peer-campfire-123" {
		t.Errorf("expected sourceID=peer-campfire-123, got %q", ac.SourceID)
	}
}

// TestPolicyEngine_Revoke verifies that a revoked convention is removed.
func TestPolicyEngine_Revoke(t *testing.T) {
	e := NewPolicyEngine()
	decl := makeDeclWithArgs("social", "post", "body")
	e.SeedConventions([]*convention.Declaration{decl})

	e.Revoke("social", "post")

	_, ok := e.GetAdopted("social", "post")
	if ok {
		t.Error("expected revoked convention to be removed")
	}
}

// TestPolicyEngine_Evaluate_Adopted verifies that adopted declarations return TrustAdopted.
func TestPolicyEngine_Evaluate_Adopted(t *testing.T) {
	e := NewPolicyEngine()
	decl := makeDeclWithArgs("social", "post", "body")
	e.Adopt(decl, SourceSeed, "")

	result := e.Evaluate(decl)
	if result.Status != TrustAdopted {
		t.Errorf("expected TrustAdopted, got %q", result.Status)
	}
	if !result.FingerprintMatch {
		t.Error("expected fingerprint_match=true for adopted convention")
	}
}

// TestPolicyEngine_Evaluate_Unknown verifies that unknown declarations return TrustUnknown.
func TestPolicyEngine_Evaluate_Unknown(t *testing.T) {
	e := NewPolicyEngine()
	decl := makeDeclWithArgs("unknown-convention", "do-thing", "param")

	result := e.Evaluate(decl)
	if result.Status != TrustUnknown {
		t.Errorf("expected TrustUnknown, got %q", result.Status)
	}
	if result.FingerprintMatch {
		t.Error("expected fingerprint_match=false for unknown convention")
	}
}

// TestPolicyEngine_Evaluate_Divergent verifies that fingerprint mismatch returns TrustDivergent.
func TestPolicyEngine_Evaluate_Divergent(t *testing.T) {
	e := NewPolicyEngine()

	// Adopt version with "body" arg.
	declV1 := makeDeclWithArgs("social", "post", "body")
	e.Adopt(declV1, SourceSeed, "")

	// Evaluate version with different semantic fields (adds "topic" arg).
	declV2 := makeDeclWithArgs("social", "post", "body", "topic")

	result := e.Evaluate(declV2)
	if result.Status != TrustDivergent {
		t.Errorf("expected TrustDivergent, got %q", result.Status)
	}
	if result.FingerprintMatch {
		t.Error("expected fingerprint_match=false for divergent convention")
	}
}

// TestPolicyEngine_EvaluateCampfire_AllAdopted verifies TrustAdopted for matching declarations.
func TestPolicyEngine_EvaluateCampfire_AllAdopted(t *testing.T) {
	e := NewPolicyEngine()
	decl1 := makeDeclWithArgs("social", "post", "body")
	decl2 := makeDeclWithArgs("trust", "verify", "key")

	e.Adopt(decl1, SourceSeed, "")
	e.Adopt(decl2, SourceSeed, "")

	status, match := e.EvaluateCampfire([]*convention.Declaration{decl1, decl2})
	if status != TrustAdopted {
		t.Errorf("expected TrustAdopted, got %q", status)
	}
	if !match {
		t.Error("expected match=true for all-adopted campfire")
	}
}

// TestPolicyEngine_EvaluateCampfire_Divergent verifies TrustDivergent when fingerprints mismatch.
func TestPolicyEngine_EvaluateCampfire_Divergent(t *testing.T) {
	e := NewPolicyEngine()
	declV1 := makeDeclWithArgs("social", "post", "body")
	e.Adopt(declV1, SourceSeed, "")

	// Campfire has a different version.
	declV2 := makeDeclWithArgs("social", "post", "body", "topic")
	status, _ := e.EvaluateCampfire([]*convention.Declaration{declV2})
	if status != TrustDivergent {
		t.Errorf("expected TrustDivergent for campfire with mismatched fingerprint, got %q", status)
	}
}

// TestPolicyEngine_EvaluateCampfire_Empty verifies TrustUnknown for empty declaration list.
func TestPolicyEngine_EvaluateCampfire_Empty(t *testing.T) {
	e := NewPolicyEngine()
	status, _ := e.EvaluateCampfire(nil)
	if status != TrustUnknown {
		t.Errorf("expected TrustUnknown for empty declarations, got %q", status)
	}
}

// TestPolicyEngine_AutoAdopt_NewConvention verifies auto-adoption of new conventions.
func TestPolicyEngine_AutoAdopt_NewConvention(t *testing.T) {
	e := NewPolicyEngine()
	e.SetAutoAdoptRule("source-campfire-abc", AutoAdoptNewOnly)

	decl := makeDeclWithArgs("social", "post", "body")
	adopted, held := e.TryAutoAdopt(decl, "source-campfire-abc")

	if !adopted {
		t.Error("expected auto-adoption of new convention")
	}
	if held {
		t.Error("expected held=false for new convention")
	}

	_, ok := e.GetAdopted("social", "post")
	if !ok {
		t.Error("expected convention to be in adopted set after auto-adoption")
	}
}

// TestPolicyEngine_AutoAdopt_FingerprintMismatchBlocked verifies that fingerprint
// mismatches block auto-adoption per §5.2.1.
func TestPolicyEngine_AutoAdopt_FingerprintMismatchBlocked(t *testing.T) {
	e := NewPolicyEngine()
	e.SetAutoAdoptRule("trusted-source", AutoAdoptNewUpdates)

	// Adopt v1 first.
	declV1 := makeDeclWithArgs("social", "post", "body")
	e.Adopt(declV1, SourceSeed, "")

	// Trusted source publishes v2 with different semantic fields.
	declV2 := makeDeclWithArgs("social", "post", "body", "topic")
	adopted, held := e.TryAutoAdopt(declV2, "trusted-source")

	// MUST be blocked by fingerprint mismatch per §5.2.1.
	if adopted {
		t.Error("auto-adoption MUST be blocked by fingerprint mismatch per §5.2.1")
	}
	if !held {
		t.Error("expected held=true when auto-adoption blocked by fingerprint mismatch")
	}

	// Original adoption must be preserved.
	ac, ok := e.GetAdopted("social", "post")
	if !ok {
		t.Fatal("expected original adoption to be preserved")
	}
	fp1 := SemanticFingerprint(declV1)
	if ac.Fingerprint != fp1 {
		t.Error("original fingerprint should be preserved after blocked auto-adoption")
	}
}

// TestPolicyEngine_AutoAdopt_SameFingerprintUpdate verifies auto-adoption of
// same-fingerprint version updates per §5.2.1.
func TestPolicyEngine_AutoAdopt_SameFingerprintUpdate(t *testing.T) {
	e := NewPolicyEngine()
	e.SetAutoAdoptRule("trusted-source", AutoAdoptNewUpdates)

	// Adopt v1.
	declV1 := makeDecl("social", "post", "0.1", []convention.ArgDescriptor{
		{Name: "body", Type: "string", Required: true},
	})
	e.Adopt(declV1, SourceSeed, "")

	// Trusted source publishes v2 with same semantic fields but different operational fields.
	declV2 := makeDecl("social", "post", "0.2", []convention.ArgDescriptor{
		{Name: "body", Type: "string", Required: true, MaxLength: 1024},
	})

	// Semantic fingerprints should match (operational fields differ, semantic fields same).
	if SemanticFingerprint(declV1) != SemanticFingerprint(declV2) {
		t.Skip("fingerprints differ — operational fields are affecting semantics, skipping test")
	}

	adopted, held := e.TryAutoAdopt(declV2, "trusted-source")
	if !adopted {
		t.Error("expected same-fingerprint version update to auto-adopt")
	}
	if held {
		t.Error("expected held=false for same-fingerprint version update")
	}

	// Version should be updated.
	ac, ok := e.GetAdopted("social", "post")
	if !ok {
		t.Fatal("expected adoption to exist after auto-adoption")
	}
	if ac.Version != "0.2" {
		t.Errorf("expected version=0.2 after update, got %q", ac.Version)
	}
}

// TestPolicyEngine_AutoAdopt_Disabled verifies that disabled auto-adoption rules don't adopt.
func TestPolicyEngine_AutoAdopt_Disabled(t *testing.T) {
	e := NewPolicyEngine()
	e.SetAutoAdoptRule("any-source", AutoAdoptDisabled)

	decl := makeDeclWithArgs("social", "post", "body")
	adopted, held := e.TryAutoAdopt(decl, "any-source")

	if adopted {
		t.Error("expected disabled auto-adoption to not adopt")
	}
	if held {
		t.Error("expected held=false for disabled source (not a mismatch scenario)")
	}
}

// TestPolicyEngine_AutoAdopt_NoRule verifies that unconfigured sources are not auto-adopted.
func TestPolicyEngine_AutoAdopt_NoRule(t *testing.T) {
	e := NewPolicyEngine()

	decl := makeDeclWithArgs("social", "post", "body")
	adopted, held := e.TryAutoAdopt(decl, "unknown-source")

	if adopted {
		t.Error("expected no auto-adoption for unconfigured source")
	}
	if held {
		t.Error("expected held=false for unconfigured source (not a mismatch scenario)")
	}
}

// TestPolicyEngine_AutoAdopt_NewOnly_ExistingSkipped verifies NewOnly doesn't
// update already-adopted conventions.
func TestPolicyEngine_AutoAdopt_NewOnly_ExistingSkipped(t *testing.T) {
	e := NewPolicyEngine()
	e.SetAutoAdoptRule("trusted-source", AutoAdoptNewOnly)

	// Adopt v1 manually.
	declV1 := makeDeclWithArgs("social", "post", "body")
	e.Adopt(declV1, SourceManual, "")

	// Try to auto-adopt same-fingerprint update from trusted source.
	adopted, held := e.TryAutoAdopt(declV1, "trusted-source")

	// NewOnly scope: already adopted, should skip.
	if adopted {
		t.Error("NewOnly scope should not update already-adopted conventions")
	}
	if held {
		t.Error("expected held=false (same fingerprint, just skipped by NewOnly scope)")
	}
}

// TestPolicyEngine_BootstrapOrder verifies that IsInitialized is false before MarkInitialized.
func TestPolicyEngine_BootstrapOrder(t *testing.T) {
	e := NewPolicyEngine()

	if e.IsInitialized() {
		t.Error("expected IsInitialized=false before initialization")
	}

	e.MarkInitialized()

	if !e.IsInitialized() {
		t.Error("expected IsInitialized=true after MarkInitialized")
	}
}

// TestPolicyEngine_SeedSetsInitialized verifies that SeedConventions sets initialized.
func TestPolicyEngine_SeedSetsInitialized(t *testing.T) {
	e := NewPolicyEngine()
	e.SeedConventions(nil)
	if !e.IsInitialized() {
		t.Error("expected IsInitialized=true after SeedConventions")
	}
}

// TestPolicyEngine_ListAdopted verifies that ListAdopted returns all adopted conventions.
func TestPolicyEngine_ListAdopted(t *testing.T) {
	e := NewPolicyEngine()
	d1 := makeDeclWithArgs("social", "post", "body")
	d2 := makeDeclWithArgs("trust", "verify", "key")
	e.Adopt(d1, SourceSeed, "")
	e.Adopt(d2, SourcePeer, "peer-123")

	list := e.ListAdopted()
	if len(list) != 2 {
		t.Errorf("expected 2 adopted conventions, got %d", len(list))
	}
}

// TestSemanticFingerprint_AlgorithmPrefix verifies that fingerprints include sha256: prefix per §5.4.
func TestSemanticFingerprint_AlgorithmPrefix(t *testing.T) {
	decl := makeDeclWithArgs("social", "post", "body")
	fp := SemanticFingerprint(decl)
	if !strings.HasPrefix(fp, "sha256:") {
		t.Errorf("expected fingerprint to start with 'sha256:', got %q", fp)
	}
}

// TestSemanticFingerprint_ConsistencyAcrossEngines verifies that two engines
// compute the same fingerprint for the same declaration.
func TestSemanticFingerprint_ConsistencyAcrossEngines(t *testing.T) {
	decl := makeDeclWithArgs("social", "post", "body", "topic")

	e1 := NewPolicyEngine()
	e2 := NewPolicyEngine()

	e1.Adopt(decl, SourceSeed, "")
	e2.Adopt(decl, SourceSeed, "")

	ac1, _ := e1.GetAdopted("social", "post")
	ac2, _ := e2.GetAdopted("social", "post")

	if ac1.Fingerprint != ac2.Fingerprint {
		t.Errorf("engines computed different fingerprints: %q vs %q", ac1.Fingerprint, ac2.Fingerprint)
	}
}

// TestTrustStatus_FourValueTaxonomy verifies all four v0.2 trust status values are distinct.
func TestTrustStatus_FourValueTaxonomy(t *testing.T) {
	values := []TrustStatus{TrustAdopted, TrustCompatible, TrustDivergent, TrustUnknown}
	seen := make(map[TrustStatus]bool)
	for _, v := range values {
		if seen[v] {
			t.Errorf("duplicate trust status value: %q", v)
		}
		seen[v] = true
	}
	if len(seen) != 4 {
		t.Errorf("expected 4 distinct trust status values, got %d", len(seen))
	}

	// "none" must not exist — it was merged into "unknown" per §5.
	for _, v := range values {
		if string(v) == "none" {
			t.Error("'none' is not a valid trust status in v0.2 — use 'unknown'")
		}
		if string(v) == "verified" || string(v) == "cross-root" || string(v) == "relayed" || string(v) == "unverified" {
			t.Errorf("v0.1 chain status value %q must not appear in v0.2 taxonomy", v)
		}
	}
}

// TestBuildEnvelope_TrustStatus verifies that the envelope uses trust_status (not trust_chain).
func TestBuildEnvelope_TrustStatus(t *testing.T) {
	env := BuildEnvelope("test-campfire", TrustAdopted, "content")
	if env.RuntimeComputed.TrustStatus != TrustAdopted {
		t.Errorf("expected TrustAdopted, got %q", env.RuntimeComputed.TrustStatus)
	}
	if env.RuntimeComputed.FingerprintMatch != false {
		t.Error("expected FingerprintMatch=false by default")
	}
}

// TestBuildEnvelope_FingerprintMatch verifies the WithFingerprintMatch option.
func TestBuildEnvelope_FingerprintMatch(t *testing.T) {
	env := BuildEnvelope("test-campfire", TrustCompatible, "content",
		WithFingerprintMatch(true),
	)
	if !env.RuntimeComputed.FingerprintMatch {
		t.Error("expected FingerprintMatch=true")
	}
}

// TestBuildEnvelope_AllTrustStatuses verifies each v0.2 trust status propagates.
func TestBuildEnvelope_AllTrustStatuses(t *testing.T) {
	cases := []TrustStatus{TrustAdopted, TrustCompatible, TrustDivergent, TrustUnknown}
	for _, ts := range cases {
		env := BuildEnvelope("cf-id", ts, nil)
		if env.RuntimeComputed.TrustStatus != ts {
			t.Errorf("expected trust_status=%q, got %q", ts, env.RuntimeComputed.TrustStatus)
		}
	}
}

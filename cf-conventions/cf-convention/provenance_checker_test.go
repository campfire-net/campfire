package convention_test

// provenance_checker_test.go — ProvenanceCheckerV2 interface declaration tests.
//
// Bead: campfireagent-2ac
// Design: 0.30-design.md §4.2 — ProvenanceChecker interface declared at L2
// (cf-convention), default impl at L3 (cf-authority/provenance).
//
// Done conditions verified here:
//  1. ProvenanceCheckerV2 interface exists in cf-convention with the v2 signature.
//  2. ProvenanceRequest and ProvenanceResult types are declared in cf-convention.
//  3. ProvenanceLevel constants (0–3) match design v2 §2.1.
//  4. AllowAll stub always returns ProvenanceLevelPresent (3).
//  5. The stub satisfies the ProvenanceCheckerV2 interface (compile-time).
//  6. WithProvenanceV2 accepts the stub and the dispatcher enforces level gates.
//
// Depguard constraint (L2 cannot import L3 cf-authority) is enforced by
// TestDepguardL2NoExtensionsClean in depguard_test.go.

import (
	"context"
	"testing"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// TestProvenanceCheckerTypes verifies the type declarations are exported and correctly typed.
func TestProvenanceCheckerTypes(t *testing.T) {
	// ProvenanceRequest must be a concrete struct with a SenderKey field.
	req := convention.ProvenanceRequest{SenderKey: "deadbeef"}
	if req.SenderKey != "deadbeef" {
		t.Errorf("ProvenanceRequest.SenderKey round-trip failed: got %q", req.SenderKey)
	}

	// ProvenanceResult must carry a Level field of type ProvenanceLevel.
	result := convention.ProvenanceResult{Level: convention.ProvenanceLevelPresent}
	if result.Level != convention.ProvenanceLevelPresent {
		t.Errorf("ProvenanceResult.Level round-trip failed: got %d", result.Level)
	}
}

// TestProvenanceLevelConstants verifies the four level constants match design v2 §2.1.
func TestProvenanceLevelConstants(t *testing.T) {
	cases := []struct {
		name     string
		level    convention.ProvenanceLevel
		expected int
	}{
		{"Anonymous", convention.ProvenanceLevelAnonymous, 0},
		{"Claimed", convention.ProvenanceLevelClaimed, 1},
		{"Contactable", convention.ProvenanceLevelContactable, 2},
		{"Present", convention.ProvenanceLevelPresent, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if int(c.level) != c.expected {
				t.Errorf("ProvenanceLevel%s: want %d, got %d", c.name, c.expected, int(c.level))
			}
		})
	}
}

// TestAllowAllProvenanceChecker_AlwaysReturnsPresent verifies the stub default
// implementation returns ProvenanceLevelPresent (3) for any sender key.
func TestAllowAllProvenanceChecker_AlwaysReturnsPresent(t *testing.T) {
	checker := convention.NewAllowAllProvenanceChecker()

	keys := []string{
		"",
		"unknown",
		"deadbeef" + "0000000000000000000000000000000000000000000000000000000000",
		"aabbccdd",
	}

	for _, key := range keys {
		t.Run("key="+key, func(t *testing.T) {
			result := checker.CheckProvenance(context.Background(), convention.ProvenanceRequest{
				SenderKey: key,
			})
			if result.Level != convention.ProvenanceLevelPresent {
				t.Errorf("AllowAllProvenanceChecker: key=%q: want level %d (Present), got %d",
					key, convention.ProvenanceLevelPresent, result.Level)
			}
		})
	}
}

// TestAllowAllProvenanceChecker_ImplementsInterface verifies the stub satisfies
// the ProvenanceCheckerV2 interface at compile time.
func TestAllowAllProvenanceChecker_ImplementsInterface(t *testing.T) {
	var _ convention.ProvenanceCheckerV2 = convention.NewAllowAllProvenanceChecker()
}

// TestProvenanceCheckerV2_StubInDispatcher verifies that the Executor accepts a
// ProvenanceCheckerV2 via WithProvenanceV2 and dispatches correctly when the stub
// always returns ProvenanceLevelPresent (3 >= any min_operator_level gate).
func TestProvenanceCheckerV2_StubInDispatcher(t *testing.T) {
	decl := parseGatedDecl(t, 2) // gate requires level 2

	transport := &noopTransport{}
	exec := convention.NewExecutorForTest(transport, provSenderKey).
		WithProvenanceV2(convention.NewAllowAllProvenanceChecker())

	_, err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key": provSenderKey, // 64-char hex key (6 + 58 zeros)
	})

	if err != nil {
		t.Fatalf("expected AllowAll stub to pass level-2 gate, got error: %v", err)
	}
	if len(transport.sent) != 1 {
		t.Errorf("expected 1 message sent, got %d", len(transport.sent))
	}
}

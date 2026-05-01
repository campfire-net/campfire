package convention_test

// gate_evaluator_test.go — GateEvaluator interface declaration tests.
//
// Tracked bead: campfireagent-2b9 (Stage 2 — interface declaration).
//
// Tests verify:
//  1. Interface signature matches design v2 §2.5.1 exactly.
//  2. AllowAllGateEvaluator stub returns GateAllow unconditionally.
//  3. AllowAllGateEvaluator can be assigned to the GateEvaluator interface
//     (compile-time conformance check via variable assignment).
//  4. DenyReason stable codes are all present (frozen at cf-authority 1.0).
//  5. Decision constants are non-zero and ordered (GateAllow=1, GateDeny=2, GateUnresolvable=3).
//  6. Depguard adversarial probe: L2-no-authority rule fires on an intentional
//     import of cf-conventions/cf-authority/* from within cf-convention/.
//
// TDD note: these tests were written before the implementation to ensure the interface
// signature is correct by construction (failing-first, then green after implementation).

import (
	"context"
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time conformance: AllowAllGateEvaluator satisfies GateEvaluator
// ─────────────────────────────────────────────────────────────────────────────

// TestGateEvaluatorInterfaceCompiles is a compile-time assertion that
// AllowAllGateEvaluator satisfies the GateEvaluator interface.
// If the interface signature changes incompatibly, this test will fail to compile.
var _ convention.GateEvaluator = convention.AllowAllGateEvaluator{}

// ─────────────────────────────────────────────────────────────────────────────
// AllowAllGateEvaluator: stub behaviour
// ─────────────────────────────────────────────────────────────────────────────

// TestAllowAllGateEvaluator_ReturnsAllow verifies that the stub always returns
// GateAllow regardless of inputs, including empty/nil request fields.
func TestAllowAllGateEvaluator_ReturnsAllow(t *testing.T) {
	eval := convention.AllowAllGateEvaluator{}

	cases := []struct {
		name string
		req  convention.EvaluateRequest
	}{
		{
			name: "empty request",
			req:  convention.EvaluateRequest{},
		},
		{
			name: "populated request",
			req: convention.EvaluateRequest{
				Request: convention.GateOpRequest{
					Convention: "swarm-coordination",
					Operation:  "claim-item",
					CampfireID: "abc123",
					Tags:       []string{"swarm-coordination:claim-item"},
				},
				ChainMessages: []convention.GateChainMessage{
					{ID: "msg-1", Payload: []byte{0x01, 0x02}},
				},
				CurrentTime: time.Now(),
				OwnerPolicy: convention.GateOwnerPolicy{
					MaxRevocationStaleness: 5 * time.Minute,
				},
			},
		},
		{
			name: "nil chain messages",
			req: convention.EvaluateRequest{
				Request: convention.GateOpRequest{
					Convention: "test-convention",
					Operation:  "test-op",
				},
				ChainMessages: nil,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := eval.Evaluate(context.Background(), tc.req)
			if result.Decision != convention.GateAllow {
				t.Errorf("AllowAllGateEvaluator: expected GateAllow, got %v", result.Decision)
			}
			if result.Reason != "" {
				t.Errorf("AllowAllGateEvaluator: expected empty Reason on Allow, got %q", result.Reason)
			}
			if result.MissingMessageID != "" {
				t.Errorf("AllowAllGateEvaluator: expected empty MissingMessageID on Allow, got %q", result.MissingMessageID)
			}
		})
	}
}

// TestAllowAllGateEvaluator_AllowIsDeterministic verifies that the stub
// returns byte-equal results on repeated calls with identical inputs.
// This mirrors the §4.1 determinism check in the conformance harness.
func TestAllowAllGateEvaluator_AllowIsDeterministic(t *testing.T) {
	eval := convention.AllowAllGateEvaluator{}
	req := convention.EvaluateRequest{
		Request: convention.GateOpRequest{
			Convention: "test-convention",
			Operation:  "test-op",
			CampfireID: "deadbeef",
		},
		CurrentTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	results := make([]convention.EvaluateResult, 3)
	for i := range results {
		results[i] = eval.Evaluate(context.Background(), req)
	}

	for i := 1; i < 3; i++ {
		if results[i].Decision != results[0].Decision {
			t.Errorf("run %d: Decision changed: %v != %v", i, results[i].Decision, results[0].Decision)
		}
		if results[i].Reason != results[0].Reason {
			t.Errorf("run %d: Reason changed: %q != %q", i, results[i].Reason, results[0].Reason)
		}
		if results[i].MissingMessageID != results[0].MissingMessageID {
			t.Errorf("run %d: MissingMessageID changed: %q != %q", i, results[i].MissingMessageID, results[0].MissingMessageID)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Decision constants: values and ordering
// ─────────────────────────────────────────────────────────────────────────────

// TestDecisionConstants_Ordered verifies the Decision constants are non-zero
// and ordered GateAllow < GateDeny < GateUnresolvable per design v2 §2.5.1.
// The numeric values are load-bearing for serialization compatibility.
func TestDecisionConstants_Ordered(t *testing.T) {
	if convention.GateAllow == 0 {
		t.Error("GateAllow must be non-zero (iota+1 sentinel)")
	}
	if convention.GateDeny == 0 {
		t.Error("GateDeny must be non-zero")
	}
	if convention.GateUnresolvable == 0 {
		t.Error("GateUnresolvable must be non-zero")
	}
	if !(convention.GateAllow < convention.GateDeny) {
		t.Errorf("expected GateAllow(%d) < GateDeny(%d)", convention.GateAllow, convention.GateDeny)
	}
	if !(convention.GateDeny < convention.GateUnresolvable) {
		t.Errorf("expected GateDeny(%d) < GateUnresolvable(%d)", convention.GateDeny, convention.GateUnresolvable)
	}
}

// TestDecisionConstants_Values verifies exact numeric values are frozen.
// Serialization (CBOR encoding of grant chain responses) depends on these values.
func TestDecisionConstants_Values(t *testing.T) {
	if convention.GateAllow != 1 {
		t.Errorf("GateAllow must be 1 (frozen); got %d", convention.GateAllow)
	}
	if convention.GateDeny != 2 {
		t.Errorf("GateDeny must be 2 (frozen); got %d", convention.GateDeny)
	}
	if convention.GateUnresolvable != 3 {
		t.Errorf("GateUnresolvable must be 3 (frozen); got %d", convention.GateUnresolvable)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DenyReason stable codes (frozen at cf-authority 1.0)
// ─────────────────────────────────────────────────────────────────────────────

// TestDenyReasonCodes_AllStableCodesPresent verifies that all ten stable DenyReason
// codes from design v2 §2.5.1 are present as typed constants.
// These codes are frozen — any addition or change requires design review.
func TestDenyReasonCodes_AllStableCodesPresent(t *testing.T) {
	stable := map[convention.DenyReason]string{
		convention.DenyExpired:         "expired",
		convention.DenyRevoked:         "revoked",
		convention.DenyDepthExceeded:   "depth_exceeded",
		convention.DenyScopeMismatch:   "scope_mismatch",
		convention.DenyScopeWidening:   "scope_widening",
		convention.DenyStaleRevocation: "stale_revocation",
		convention.DenyReservedOpFloor: "reserved_op_floor",
		convention.DenyOwnerCeiling:    "owner_ceiling",
		convention.DenyPredicate:       "predicate_unsatisfied",
		convention.DenyStoreRead:       "store_read_error",
	}

	if len(stable) != 10 {
		t.Fatalf("expected 10 stable DenyReason codes (design v2 §2.5.1), got %d", len(stable))
	}

	for code, want := range stable {
		if string(code) != want {
			t.Errorf("DenyReason %q: expected string %q, got %q", want, want, string(code))
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// EvaluateRequest field coverage: L2-owned types use only stdlib
// ─────────────────────────────────────────────────────────────────────────────

// TestEvaluateRequest_TypesAreStdlibOnly is a compile-time check that EvaluateRequest
// and its supporting types (GateOpRequest, GateChainMessage, GateRevocationViewEntry,
// GateOwnerPolicy) only reference stdlib types (ed25519, time, byte, string).
//
// This test constructs every struct literal to ensure all fields are populated,
// catching any future addition of L3 types at compile time.
func TestEvaluateRequest_TypesAreStdlibOnly(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	req := convention.EvaluateRequest{
		Request: convention.GateOpRequest{
			Convention:          "test-convention",
			Operation:           "test-op",
			CampfireID:          "abc123",
			Tags:                []string{"test-convention:test-op"},
			Sender:              pub,
			SerializedPredicate: []byte{0xA1, 0x61, 0x6C, 0x01}, // CBOR {level:1}
		},
		ChainMessages: []convention.GateChainMessage{
			{
				ID:      "msg-abc",
				Payload: []byte{0x01, 0x02, 0x03},
			},
		},
		RootPrincipal: pub,
		CurrentTime:   time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		RevocationView: []convention.GateRevocationViewEntry{
			{
				CampfireID:          "cf-abc",
				LatestObservedMsgID: "msg-def",
				ObservedAt:          time.Now(),
			},
		},
		OwnerPolicy: convention.GateOwnerPolicy{
			MaxRevocationStaleness: 5 * time.Minute,
			MinLevelOverride:       0,
			BlanketDeny:            []string{"test-convention:admin-op"},
		},
	}

	// Exercise the evaluator with a fully-populated request.
	eval := convention.AllowAllGateEvaluator{}
	result := eval.Evaluate(context.Background(), req)
	if result.Decision != convention.GateAllow {
		t.Errorf("expected GateAllow, got %v", result.Decision)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Depguard adversarial probe: L2 must not import cf-authority
// ─────────────────────────────────────────────────────────────────────────────

// TestDepguardL2NoCfAuthorityClean verifies the current cf-conventions/cf-convention/
// tree has zero L2-no-authority depguard violations.
func TestDepguardL2NoCfAuthorityClean(t *testing.T) {
	lintBin := findLintBin(t)
	if lintBin == "" {
		t.Skip("golangci-lint not found; skipping depguard verification")
	}

	repoRoot := findConventionRepoRoot(t)
	cmd := exec.Command(lintBin, "run", "--fast-only", "./cf-conventions/cf-convention/...")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "L2-no-authority") || strings.Contains(string(out), "cf-authority") {
			t.Errorf("L2-no-authority rule found violations in cf-conventions/cf-convention/:\n%s", out)
		}
	}
}

// TestDepguardL2NoCfAuthorityCatchesForbiddenImport proves the L2-no-authority
// depguard rule fires when cf-conventions/cf-convention/ imports
// cf-conventions/cf-authority (L3).
//
// This is the adversarial condition from done-condition 3 in campfireagent-2b9.
func TestDepguardL2NoCfAuthorityCatchesForbiddenImport(t *testing.T) {
	lintBin := findLintBin(t)
	if lintBin == "" {
		t.Skip("golangci-lint not found; skipping depguard adversarial verification")
	}

	repoRoot := findConventionRepoRoot(t)

	// Create a temporary sub-package inside cf-conventions/cf-convention/ that imports
	// cf-conventions/cf-authority/trust — a deliberate L2-no-authority violation.
	// Placed inside cf-conventions/cf-convention/ so the depguard rule's files pattern matches.
	violationDir := filepath.Join(repoRoot, "cf-conventions", "cf-convention", "depguard-l2-authority-violation-probe")
	if err := os.MkdirAll(violationDir, 0750); err != nil {
		t.Fatalf("creating violation dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(violationDir) })

	violationFile := filepath.Join(violationDir, "violation.go")
	const violationSrc = `// DO NOT COMMIT: deliberate L2-no-authority violation for depguard test.
// Created by cf-conventions/cf-convention/gate_evaluator_test.go; deleted on test exit.
package depguardl2authorityviolationprobe

import (
	// L2-no-authority violation: L2 machinery importing L3 authority.
	_ "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)
`
	if err := os.WriteFile(violationFile, []byte(violationSrc), 0600); err != nil {
		t.Fatalf("writing violation file: %v", err)
	}

	// Run golangci-lint only against the violation package.
	// We expect depguard to flag the import; the command should produce output
	// containing "depguard" or "L2-no-authority".
	cmd := exec.Command(lintBin, "run", "--fast-only",
		"./cf-conventions/cf-convention/depguard-l2-authority-violation-probe/...")
	cmd.Dir = repoRoot
	out, _ := cmd.CombinedOutput()
	outStr := string(out)

	if strings.Contains(outStr, "depguard") || strings.Contains(outStr, "L2-no-authority") || strings.Contains(outStr, "cf-authority") {
		t.Logf("PASS: depguard correctly rejected the L2→L3 cf-authority import:")
		t.Logf("  %s", strings.TrimSpace(outStr))
	} else {
		t.Errorf("FAIL: depguard did NOT catch the L2-no-authority violation. lint output:\n%s", outStr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func findLintBin(t *testing.T) string {
	t.Helper()
	if path, err := exec.LookPath("golangci-lint"); err == nil {
		return path
	}
	home, err := os.UserHomeDir()
	if err == nil {
		candidate := filepath.Join(home, "bin", "golangci-lint")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func findConventionRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting cwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod from cwd")
		}
		dir = parent
	}
}

package trust_test

// nil_empty_chain_test.go — Conformance test: nil and empty ChainMessages MUST
// produce identical decisions through all L3 (DefaultGateEvaluator) code paths.
//
// # Why this test exists
//
// The -cb3 forward constraint (PR #447, campfireagent-cb3) introduced
// conventionOpPayload.GrantChain []string at L2 (cf-conventions/cf-convention/
// dispatcher.go:200-211). That PR tested L2 behavior with chain=nil, chain=[],
// chain=other-op, and chain=expired — all denied at L2 for reserved ops.
//
// The forward constraint: when L3 (DefaultGateEvaluator) consumes the GrantChain
// from L2, it MUST produce the same decision for nil and empty inputs through ALL
// code paths. The L2 dispatcher can emit either form:
//   - JSON {"grant_chain": null}  → nil under Go's JSON decoder
//   - JSON {"grant_chain": []}    → empty slice
//   - field absent under omitempty → nil
//
// # The equivalence is emergent from Go's len() semantics
//
// Go guarantees len(nil) == 0 for slices, so len(req.ChainMessages) == 0 already
// treats nil and empty identically. Every branch in the evaluator uses the len()
// idiom, not a nil pointer check. This is correct — but "implicit by language
// semantics" is not the same as "guaranteed by contract."
//
// A future refactor might introduce:
//   - if req.ChainMessages == nil { ... }  // breaks nil/empty equivalence
//   - req.ChainMessages[0]                 // panics on nil if len not checked first
//   - a helper that checks != nil          // silently diverges
//
// This test IS the contract. If it breaks, the code regressed.
//
// # Cross-references
//
// - cf-conventions/cf-convention/reserved_op_d5_test.go — Wave B cross-reference
//   that explicitly calls out "nil and empty GrantChain are treated equivalently
//   (depth=0/1, allowed); see campfireagent-f52 for the L3 evaluator-side counterpart."
// - PR #447 (campfireagent-cb3) — the -cb3 forward constraint source.
// - cf-authority spec, §4.1: the evaluator is a pure function of its inputs;
//   nil and empty are semantically equivalent inputs for an empty chain.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
	cfprotocol "github.com/campfire-net/campfire/cf-protocol/protocol"
)

// canonicalT0NilEmpty is 2026-01-01T00:00:00Z in nanoseconds since Unix epoch UTC.
// Matches the canonical timestamp used in the conformance harness.
const canonicalT0NilEmpty int64 = 1767225600000000000

// oneDayNilEmpty is 24 hours in nanoseconds.
const oneDayNilEmpty int64 = 86_400_000_000_000

// t0NilEmpty returns the canonical evaluation time as a time.Time.
func t0NilEmpty() time.Time {
	return time.Unix(0, canonicalT0NilEmpty).UTC()
}

// nilEmptyTestKeys holds generated ed25519 keys for nil/empty chain tests.
type nilEmptyTestKeys struct {
	root   ed25519.PublicKey
	sender ed25519.PublicKey
}

// newNilEmptyKeys generates fresh test keys.
func newNilEmptyKeys(t *testing.T) *nilEmptyTestKeys {
	t.Helper()
	root, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating root key: %v", err)
	}
	sender, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating sender key: %v", err)
	}
	return &nilEmptyTestKeys{root: root, sender: sender}
}

// assertNilEmptyEquivalence runs f(nil) and f([]ChainMessage{}) and asserts
// that the Decision, Reason, and MissingMessageID fields are identical.
// This is the core contract assertion for this conformance test.
func assertNilEmptyEquivalence(t *testing.T, label string, f func(chain []trust.ChainMessage) trust.EvaluateResult) {
	t.Helper()
	nilResult := f(nil)
	emptyResult := f([]trust.ChainMessage{})
	if nilResult.Decision != emptyResult.Decision ||
		nilResult.Reason != emptyResult.Reason ||
		nilResult.MissingMessageID != emptyResult.MissingMessageID {
		t.Errorf("%s: nil and empty ChainMessages diverged\n  nil:   Decision=%v Reason=%q MissingMessageID=%q\n  empty: Decision=%v Reason=%q MissingMessageID=%q",
			label,
			nilResult.Decision, nilResult.Reason, nilResult.MissingMessageID,
			emptyResult.Decision, emptyResult.Reason, emptyResult.MissingMessageID)
	}
}

// TestDefaultGateEvaluator_NilEmptyChainEquivalence is a table-driven conformance
// test that exercises all public decision paths through DefaultGateEvaluator with
// both nil and empty ChainMessages, asserting byte-identical results.
//
// Six representative shapes cover every major code path in evaluator.go:
//
//  1. Anchor-self Allow: sender == RootPrincipal, no chain → Allow
//  2. Anchor-self Unresolvable: sender != RootPrincipal, no chain → Unresolvable
//  3. OwnerPolicy.BlanketDeny fires before chain check → Deny(owner_ceiling)
//  4. Reserved-op depth-0, sender == root → Allow (reserved-op floor passes)
//  5. Reserved-op depth-0, sender != root → Unresolvable (anchor-self path)
//  6. Empty chain + no stale revocation → anchor-self path (identical to case 1 or 2)
func TestDefaultGateEvaluator_NilEmptyChainEquivalence(t *testing.T) {
	eval := trust.DefaultGateEvaluator{}
	keys := newNilEmptyKeys(t)

	// A known reserved op for cases 4 and 5.
	const reservedOp = "delegation-grant"
	if !cfprotocol.IsReservedOp(reservedOp) {
		t.Fatalf("test setup error: %q is not a reserved op; pick one from cfprotocol.ReservedOps", reservedOp)
	}

	cases := []struct {
		name  string
		build func(chain []trust.ChainMessage) trust.EvaluateRequest
		// wantDecision is checked against the nil-chain result for documentation.
		// The equivalence assertion independently checks nil==empty.
		wantDecision trust.Decision
	}{
		{
			name: "anchor-self-allow",
			build: func(chain []trust.ChainMessage) trust.EvaluateRequest {
				return trust.EvaluateRequest{
					Request: trust.OpRequest{
						Convention: "ready",
						Operation:  "claim",
						CampfireID: "deadbeef",
						Sender:     keys.root, // sender == root → Allow
					},
					ChainMessages: chain,
					RootPrincipal: keys.root,
					CurrentTime:   t0NilEmpty(),
				}
			},
			wantDecision: trust.Allow,
		},
		{
			name: "anchor-self-unresolvable",
			build: func(chain []trust.ChainMessage) trust.EvaluateRequest {
				return trust.EvaluateRequest{
					Request: trust.OpRequest{
						Convention: "ready",
						Operation:  "claim",
						CampfireID: "deadbeef",
						Sender:     keys.sender, // sender != root → Unresolvable
					},
					ChainMessages: chain,
					RootPrincipal: keys.root,
					CurrentTime:   t0NilEmpty(),
				}
			},
			wantDecision: trust.Unresolvable,
		},
		{
			name: "owner-blanketdeny-before-chain-check",
			build: func(chain []trust.ChainMessage) trust.EvaluateRequest {
				return trust.EvaluateRequest{
					Request: trust.OpRequest{
						Convention: "ready",
						Operation:  "claim",
						CampfireID: "deadbeef",
						// BlanketDeny fires before chain walk — sender doesn't matter.
						Sender: keys.root,
					},
					ChainMessages: chain,
					RootPrincipal: keys.root,
					CurrentTime:   t0NilEmpty(),
					OwnerPolicy: trust.OwnerPolicy{
						// Pattern "ready:claim" matches exactly.
						BlanketDeny: []string{"ready:claim"},
					},
				}
			},
			wantDecision: trust.Deny,
		},
		{
			name: "reserved-op-depth0-sender-is-root",
			build: func(chain []trust.ChainMessage) trust.EvaluateRequest {
				return trust.EvaluateRequest{
					Request: trust.OpRequest{
						Convention: "cf-protocol",
						Operation:  reservedOp,
						CampfireID: "deadbeef",
						Sender:     keys.root, // depth-0: sender == root → floor passes → Allow
					},
					ChainMessages: chain,
					RootPrincipal: keys.root,
					CurrentTime:   t0NilEmpty(),
				}
			},
			wantDecision: trust.Allow,
		},
		{
			name: "reserved-op-depth0-sender-not-root",
			build: func(chain []trust.ChainMessage) trust.EvaluateRequest {
				return trust.EvaluateRequest{
					Request: trust.OpRequest{
						Convention: "cf-protocol",
						Operation:  reservedOp,
						CampfireID: "deadbeef",
						Sender:     keys.sender, // sender != root, empty chain → Unresolvable
					},
					ChainMessages: chain,
					RootPrincipal: keys.root,
					CurrentTime:   t0NilEmpty(),
				}
			},
			wantDecision: trust.Unresolvable,
		},
		{
			name: "empty-chain-no-stale-revocation",
			// RevocationView present but not stale → falls through to anchor-self path.
			// Sender == root so result is Allow.
			build: func(chain []trust.ChainMessage) trust.EvaluateRequest {
				observedAt := t0NilEmpty().Add(-1 * time.Hour) // 1h ago
				return trust.EvaluateRequest{
					Request: trust.OpRequest{
						Convention: "ready",
						Operation:  "publish",
						CampfireID: "deadbeef",
						Sender:     keys.root,
					},
					ChainMessages: chain,
					RootPrincipal: keys.root,
					CurrentTime:   t0NilEmpty(),
					RevocationView: []trust.RevocationViewEntry{
						{
							CampfireID:          "deadbeef",
							LatestObservedMsgID: "msg-001",
							ObservedAt:          observedAt,
						},
					},
					OwnerPolicy: trust.OwnerPolicy{
						// MaxRevocationStaleness = 2h: view is 1h old → not stale.
						// Staleness check only fires inside walkChain (non-empty chain path),
						// so this entry is a no-op for empty chain — confirming that the
						// nil/empty path doesn't touch walkChain at all.
						MaxRevocationStaleness: 2 * time.Hour,
					},
				}
			},
			wantDecision: trust.Allow,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Assert nil == empty equivalence (the primary contract).
			assertNilEmptyEquivalence(t, tc.name, func(chain []trust.ChainMessage) trust.EvaluateResult {
				return eval.Evaluate(context.Background(), tc.build(chain))
			})

			// Secondary: verify the expected decision for the nil-chain case
			// (documents the contract; a mismatch here indicates test setup error).
			nilResult := eval.Evaluate(context.Background(), tc.build(nil))
			if nilResult.Decision != tc.wantDecision {
				t.Errorf("%s: nil chain: want Decision=%v, got Decision=%v (reason=%q)",
					tc.name, tc.wantDecision, nilResult.Decision, nilResult.Reason)
			}
		})
	}
}

// TestConventionAdapter_NilEmptyChainEquivalence validates that the
// ConventionAdapter's wire→eval translation (translateRequest in adapter.go)
// normalizes both nil and empty L2 ChainMessages to identical L3 results.
//
// Context: translateRequest (adapter.go:78) always uses
//
//	chain := make([]ChainMessage, 0, len(req.ChainMessages))
//
// for the L3 chain slice. This means:
//   - nil L2 chain  → make([]ChainMessage, 0, 0) → non-nil empty L3 slice
//   - empty L2 chain → make([]ChainMessage, 0, 0) → non-nil empty L3 slice
//
// Both produce a non-nil empty L3 slice, so DefaultGateEvaluator sees the same
// input either way. This test pins that normalization.
func TestConventionAdapter_NilEmptyChainEquivalence(t *testing.T) {
	adapter := trust.NewConventionAdapter()
	keys := newNilEmptyKeys(t)

	build := func(chain []convention.GateChainMessage) convention.EvaluateRequest {
		return convention.EvaluateRequest{
			Request: convention.GateOpRequest{
				Convention: "ready",
				Operation:  "claim",
				CampfireID: "deadbeef",
				Sender:     keys.root, // sender == root → Allow after translation
			},
			ChainMessages: chain,
			RootPrincipal: ed25519.PublicKey(keys.root),
			CurrentTime:   t0NilEmpty(),
		}
	}

	nilResult := adapter.Evaluate(context.Background(), build(nil))
	emptyResult := adapter.Evaluate(context.Background(), build([]convention.GateChainMessage{}))

	if nilResult.Decision != emptyResult.Decision ||
		nilResult.Reason != emptyResult.Reason ||
		nilResult.MissingMessageID != emptyResult.MissingMessageID {
		t.Errorf("ConventionAdapter: nil and empty L2 ChainMessages diverged\n  nil:   Decision=%v Reason=%q MissingMessageID=%q\n  empty: Decision=%v Reason=%q MissingMessageID=%q",
			nilResult.Decision, nilResult.Reason, nilResult.MissingMessageID,
			emptyResult.Decision, emptyResult.Reason, emptyResult.MissingMessageID)
	}

	// Secondary: anchor-self (sender == root, no chain) must be Allow.
	if nilResult.Decision != convention.GateAllow {
		t.Errorf("ConventionAdapter anchor-self: want GateAllow, got %v (reason=%q)",
			nilResult.Decision, nilResult.Reason)
	}
}

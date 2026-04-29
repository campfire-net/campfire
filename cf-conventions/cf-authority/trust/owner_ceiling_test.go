package trust_test

// ---------------------------------------------------------------------------
// Conformance case 13 — D6 owner-ceiling: BlanketDeny overrides grant + convention
//
// WHY: D6 gap from veracity audit (campfireagent-e48). No canonical conformance
// case proved that OwnerPolicy.BlanketDeny overrides a grant+convention permit.
// This test adds two adversarial variants per the done-condition:
//   (a) BlanketDeny = ["ready:*"]   — glob/prefix match
//   (b) BlanketDeny = ["ready:claim"] — exact match
//
// SPEC: docs/cf-authority-spec.md §3.4 (DenyOwnerCeiling = "owner_ceiling");
//       docs/0.30-deal-breakers.md §D6; docs/cf-authority-spec.md §4.5.
//
// REAL EVALUATOR: uses the DefaultGateEvaluator from evaluator.go.
// NO MOCK EVALUATOR.
// ---------------------------------------------------------------------------

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

// canonical t0 per §5.4: 2026-01-01T00:00:00Z = 1767225600000000000 ns
const canonicalT0 int64 = 1767225600000000000

// makeTestKeys generates a deterministic sender keypair for test fixtures.
// Uses a fixed seed so grant payloads are byte-identical across runs (§4.1).
func makeTestKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey), priv
}

// makeRootKeys generates a deterministic root keypair (different seed from sender).
func makeRootKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 100)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey), priv
}

// buildGrantPayload encodes a one-hop grant from rootPub to senderPub with
// convention "ready", op_pattern as specified. Returns CBOR-encoded GrantPayload
// bytes ready for inclusion in ChainMessages.
func buildGrantPayload(t *testing.T, convention, opPattern string, until int64) []byte {
	t.Helper()
	nonce := make([]byte, 16)
	for i := range nonce {
		nonce[i] = byte(i + 7)
	}
	senderPub, _ := makeTestKeys(t)

	cap := trust.Capability{
		Convention: convention,
		OpPattern:  opPattern,
		Where:      []trust.WhereMatcherCBOR{},
		Bounds:     map[string]any{},
		Until:      until,
		Nonce:      nonce,
	}
	gp := trust.GrantPayload{
		ParentGrantID: nil, // owner-root grant
		ChildPubkey:   senderPub,
		Capabilities:  []trust.Capability{cap},
		Depth:         0,
	}
	encoded, err := trust.MarshalGrantPayloadCBOR(gp)
	if err != nil {
		t.Fatalf("buildGrantPayload: MarshalGrantPayloadCBOR: %v", err)
	}
	return encoded
}

// ---------------------------------------------------------------------------
// Case 13a: BlanketDeny=["ready:*"] (glob) — grant+convention both permit
// ---------------------------------------------------------------------------

// TestD6_OwnerCeiling_BlanketDeny_GlobMatch asserts that OwnerPolicy.BlanketDeny
// with a glob pattern "ready:*" overrides a parent grant that permits "ready:claim"
// and a convention predicate that also permits "ready:claim".
//
// Expected: Deny / DenyOwnerCeiling ("owner_ceiling").
// Falsifies: implementations that allow owner-ceiling bypass via grant or convention.
func TestD6_OwnerCeiling_BlanketDeny_GlobMatch(t *testing.T) {
	rootPub, _ := makeRootKeys(t)
	senderPub, _ := makeTestKeys(t)

	// Grant payload: root→sender, convention="ready", op_pattern="claim", not expired.
	grantPayload := buildGrantPayload(t, "ready", "claim", canonicalT0+int64(24*time.Hour))

	req := trust.EvaluateRequest{
		Request: trust.OpRequest{
			Convention: "ready",
			Operation:  "claim",
			CampfireID: "deadbeef01234567deadbeef01234567deadbeef01234567deadbeef01234567",
			Tags:       nil,
			Sender:     senderPub,
			// Predicate: grant: ready:claim — the convention permits this op.
			Predicate: trust.PredicateAST{
				Kind: trust.KindGrant,
				Leaf: &trust.LeafPredicate{
					GrantConvention: "ready",
					GrantOp:         "claim",
				},
			},
		},
		ChainMessages: []trust.ChainMessage{
			{
				ID:      "msg-root-to-sender-001",
				Payload: grantPayload,
			},
		},
		RootPrincipal: rootPub,
		CurrentTime:   time.Unix(0, canonicalT0).UTC(),
		RevocationView: nil,
		OwnerPolicy: trust.OwnerPolicy{
			MaxRevocationStaleness: 0, // no staleness check
			MinLevelOverride:       0,
			BlanketDeny:            []string{"ready:*"}, // glob: deny all ready ops
		},
	}

	evaluator := trust.NewDefaultGateEvaluator()
	result := evaluator.Evaluate(context.Background(), req)

	if result.Decision != trust.Deny {
		t.Errorf("case 13a (glob): expected Deny; got %v", result.Decision)
	}
	if result.Reason != trust.DenyOwnerCeiling {
		t.Errorf("case 13a (glob): expected DenyReason=%q; got %q", trust.DenyOwnerCeiling, result.Reason)
	}
}

// TestD6_OwnerCeiling_BlanketDeny_GlobMatch_Determinism runs case 13a three times
// and asserts identical results (§4.1 determinism contract).
func TestD6_OwnerCeiling_BlanketDeny_GlobMatch_Determinism(t *testing.T) {
	rootPub, _ := makeRootKeys(t)
	senderPub, _ := makeTestKeys(t)
	grantPayload := buildGrantPayload(t, "ready", "claim", canonicalT0+int64(24*time.Hour))

	req := trust.EvaluateRequest{
		Request: trust.OpRequest{
			Convention: "ready",
			Operation:  "claim",
			CampfireID: "deadbeef01234567deadbeef01234567deadbeef01234567deadbeef01234567",
			Sender:     senderPub,
			Predicate: trust.PredicateAST{
				Kind: trust.KindGrant,
				Leaf: &trust.LeafPredicate{GrantConvention: "ready", GrantOp: "claim"},
			},
		},
		ChainMessages:  []trust.ChainMessage{{ID: "msg-det-001", Payload: grantPayload}},
		RootPrincipal:  rootPub,
		CurrentTime:    time.Unix(0, canonicalT0).UTC(),
		RevocationView: nil,
		OwnerPolicy:    trust.OwnerPolicy{BlanketDeny: []string{"ready:*"}},
	}

	evaluator := trust.NewDefaultGateEvaluator()
	var results [3]trust.EvaluateResult
	for i := range results {
		results[i] = evaluator.Evaluate(context.Background(), req)
	}
	for i := 1; i < 3; i++ {
		if results[0].Decision != results[i].Decision || results[0].Reason != results[i].Reason {
			t.Errorf("case 13a determinism: run 0 vs run %d: Decision=%v/%v Reason=%v/%v",
				i, results[0].Decision, results[i].Decision, results[0].Reason, results[i].Reason)
		}
	}
}

// ---------------------------------------------------------------------------
// Case 13b: BlanketDeny=["ready:claim"] (exact match) — same assertion
// ---------------------------------------------------------------------------

// TestD6_OwnerCeiling_BlanketDeny_ExactMatch asserts that OwnerPolicy.BlanketDeny
// with exact pattern "ready:claim" also overrides the grant+convention permit.
//
// Expected: Deny / DenyOwnerCeiling ("owner_ceiling").
func TestD6_OwnerCeiling_BlanketDeny_ExactMatch(t *testing.T) {
	rootPub, _ := makeRootKeys(t)
	senderPub, _ := makeTestKeys(t)
	grantPayload := buildGrantPayload(t, "ready", "claim", canonicalT0+int64(24*time.Hour))

	req := trust.EvaluateRequest{
		Request: trust.OpRequest{
			Convention: "ready",
			Operation:  "claim",
			CampfireID: "deadbeef01234567deadbeef01234567deadbeef01234567deadbeef01234567",
			Sender:     senderPub,
			Predicate: trust.PredicateAST{
				Kind: trust.KindGrant,
				Leaf: &trust.LeafPredicate{GrantConvention: "ready", GrantOp: "claim"},
			},
		},
		ChainMessages:  []trust.ChainMessage{{ID: "msg-exact-001", Payload: grantPayload}},
		RootPrincipal:  rootPub,
		CurrentTime:    time.Unix(0, canonicalT0).UTC(),
		RevocationView: nil,
		OwnerPolicy: trust.OwnerPolicy{
			BlanketDeny: []string{"ready:claim"}, // exact match
		},
	}

	evaluator := trust.NewDefaultGateEvaluator()
	result := evaluator.Evaluate(context.Background(), req)

	if result.Decision != trust.Deny {
		t.Errorf("case 13b (exact): expected Deny; got %v", result.Decision)
	}
	if result.Reason != trust.DenyOwnerCeiling {
		t.Errorf("case 13b (exact): expected DenyReason=%q; got %q", trust.DenyOwnerCeiling, result.Reason)
	}
}

// ---------------------------------------------------------------------------
// Negative: BlanketDeny does NOT match a different op — should not deny
// ---------------------------------------------------------------------------

// TestD6_OwnerCeiling_BlanketDeny_NoMatch asserts that BlanketDeny only fires
// when the pattern matches the requested convention:op. A non-matching deny
// policy must NOT produce DenyOwnerCeiling.
//
// This is the "does not over-deny" adversarial check: BlanketDeny=["other:op"]
// must not affect "ready:claim" requests.
func TestD6_OwnerCeiling_BlanketDeny_NoMatch(t *testing.T) {
	rootPub, _ := makeRootKeys(t)
	senderPub, _ := makeTestKeys(t)
	grantPayload := buildGrantPayload(t, "ready", "claim", canonicalT0+int64(24*time.Hour))

	req := trust.EvaluateRequest{
		Request: trust.OpRequest{
			Convention: "ready",
			Operation:  "claim",
			CampfireID: "deadbeef01234567deadbeef01234567deadbeef01234567deadbeef01234567",
			Sender:     senderPub,
			Predicate: trust.PredicateAST{
				Kind: trust.KindGrant,
				Leaf: &trust.LeafPredicate{GrantConvention: "ready", GrantOp: "claim"},
			},
		},
		ChainMessages:  []trust.ChainMessage{{ID: "msg-nomatch-001", Payload: grantPayload}},
		RootPrincipal:  rootPub,
		CurrentTime:    time.Unix(0, canonicalT0).UTC(),
		RevocationView: nil,
		OwnerPolicy: trust.OwnerPolicy{
			BlanketDeny: []string{"other:op"}, // does NOT match ready:claim
		},
	}

	evaluator := trust.NewDefaultGateEvaluator()
	result := evaluator.Evaluate(context.Background(), req)

	// Must NOT be DenyOwnerCeiling — the pattern doesn't match.
	if result.Reason == trust.DenyOwnerCeiling {
		t.Errorf("no-match: BlanketDeny=[\"other:op\"] should not deny ready:claim; got DenyOwnerCeiling")
	}
}

package convention_test

// reserved_op_d5_test.go — Stage 2 D5 additional enforcement tests (campfireagent-c85).
//
// Architecture (reserved-ops-chain.md §L2):
//
//	L1 — cf-protocol     LIST — IsReservedOp(op)
//	L2 — cf-conventions  ENFORCER — fires BEFORE invoking GateEvaluator (L3)
//	                      Adds two D5 checks on top of the base -935 enforcement:
//	                       D5a: reserved op with GrantChain depth > 1 → DENY(depth_exceeded)
//	                       D5b: convention declaration declares level:0 on a reserved op → rejected
//	L3 — cf-authority    EVALUATOR — grant-chain policy (future; campfireagent-02a)
//
// Cases:
//  1. Convention declares min_operator_level: 0 on "disband" → Parse returns error (D5b).
//  2. GrantChain with 2 entries on "delegation-grant" → Dispatch returns false (D5a depth-1 cap).
//  3. Happy path: non-reserved op, any depth, any level → no enforcement fires.
//  4. Adversarial: reserved op in a chain that looks depth-1 but has 2 hops → enforcer catches it.
//
// TDD: These tests FAIL before the implementation is added to parser.go and dispatcher.go.

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cfprotocol "github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// D5b — Parse-time rejection: reserved op with min_operator_level: 0
// ─────────────────────────────────────────────────────────────────────────────

// TestD5_ParseRejectsReservedOpWithLevelZero verifies that Parse returns an error
// when a convention declaration sets min_operator_level: 0 on a reserved operation.
//
// Reserved ops require owner-level authority; level 0 (anonymous/no restriction)
// is below the reserved-op floor and must be rejected at parse time.
func TestD5_ParseRejectsReservedOpWithLevelZero(t *testing.T) {
	for _, reservedOp := range cfprotocol.ReservedOps {
		reservedOp := reservedOp
		t.Run("op="+reservedOp, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"convention":         "my-test-convention",
				"version":            "0.1",
				"operation":          reservedOp,
				"description":        "Attempts to declare a reserved op with level 0 — forbidden",
				"signing":            "member_key",
				"antecedents":        "none",
				"min_operator_level": 0, // explicitly declaring zero — below reserved-op floor
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			tags := []string{convention.ConventionOperationTag}
			_, _, parseErr := convention.Parse(tags, payload, "senderabc", "campfirekey", convention.DefaultDeniedTagPrefixes)
			if parseErr == nil {
				t.Errorf("Parse(reserved_op=%q, min_operator_level=0): expected error (D5b reserved-op floor), got nil", reservedOp)
				return
			}
			if !strings.Contains(parseErr.Error(), "reserved") && !strings.Contains(parseErr.Error(), "reserved_op_floor") {
				t.Errorf("Parse(reserved_op=%q): error should mention 'reserved', got: %v", reservedOp, parseErr)
			}
		})
	}
}

// TestD5_ParseRejectsReservedOpWithLevelZeroExplicit_Disband is the canonical case
// from the item spec: "convention declaration declares level: 0 on disband → enforcer rejects".
func TestD5_ParseRejectsReservedOpWithLevelZeroExplicit_Disband(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"convention":         "bad-convention",
		"version":            "0.1",
		"operation":          "disband",
		"description":        "Explicitly tries to lower disband to level 0",
		"signing":            "member_key",
		"antecedents":        "none",
		"min_operator_level": 0,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	tags := []string{convention.ConventionOperationTag}
	_, _, parseErr := convention.Parse(tags, payload, "senderabc", "campfirekey", convention.DefaultDeniedTagPrefixes)
	if parseErr == nil {
		t.Fatal("Parse(disband, level=0): expected rejection (D5b reserved-op floor), got nil")
	}
	if !strings.Contains(parseErr.Error(), "reserved") && !strings.Contains(parseErr.Error(), "reserved_op_floor") {
		t.Errorf("Parse error should mention 'reserved', got: %v", parseErr)
	}
}

// TestD5_ParseInfrastructureConventionExempt verifies that InfrastructureConvention
// ("convention-extension") is exempt from the D5b reserved-op floor.
// The convention-extension convention legitimately declares the "revoke" reserved
// operation (it IS the convention lifecycle manager). Blocking it would break
// the built-in convention revocation primitive.
func TestD5_ParseInfrastructureConventionExempt(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"convention":         "convention-extension",
		"version":            "0.1",
		"operation":          "revoke",
		"description":        "Revoke a convention declaration (infrastructure exception)",
		"signing":            "campfire_key",
		"antecedents":        "none",
		"min_operator_level": 0, // allowed for infrastructure convention despite being a reserved op
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Use the campfire key as sender so campfire_key signing check passes.
	const key = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	tags := []string{convention.ConventionOperationTag}
	_, _, parseErr := convention.Parse(tags, payload, key, key, convention.DefaultDeniedTagPrefixes)
	if parseErr != nil {
		t.Errorf("Parse(convention-extension:revoke, level=0): expected no error (infrastructure exception), got: %v", parseErr)
	}
}

// TestD5_ParseAllowsNonReservedOpWithLevelZero verifies that a non-reserved op
// with min_operator_level: 0 is accepted — the level-0 restriction applies only
// to reserved ops.
func TestD5_ParseAllowsNonReservedOpWithLevelZero(t *testing.T) {
	nonReservedOps := []string{"claim", "complete", "publish", "my-custom-op"}
	for _, op := range nonReservedOps {
		op := op
		t.Run("op="+op, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"convention":         "my-convention",
				"version":            "0.1",
				"operation":          op,
				"description":        "Non-reserved op; level 0 is fine",
				"signing":            "member_key",
				"antecedents":        "none",
				"min_operator_level": 0,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			tags := []string{convention.ConventionOperationTag}
			decl, _, parseErr := convention.Parse(tags, payload, "senderabc", "campfirekey", convention.DefaultDeniedTagPrefixes)
			if parseErr != nil {
				t.Errorf("Parse(op=%q, level=0): unexpected error for non-reserved op: %v", op, parseErr)
				return
			}
			if decl == nil {
				t.Errorf("Parse(op=%q): expected non-nil declaration", op)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// D5a — Dispatch-time rejection: reserved op with GrantChain depth > 1
// ─────────────────────────────────────────────────────────────────────────────

// buildD5Msg builds a message for a reserved op with an explicit grant_chain slice.
func buildD5Msg(t *testing.T, conventionName, opName string, chainEntries []string) *store.MessageRecord {
	t.Helper()
	type payload struct {
		Convention string   `json:"convention"`
		Operation  string   `json:"operation"`
		GrantChain []string `json:"grant_chain,omitempty"`
	}
	p, err := json.Marshal(payload{Convention: conventionName, Operation: opName, GrantChain: chainEntries})
	if err != nil {
		t.Fatalf("buildD5Msg marshal: %v", err)
	}
	return &store.MessageRecord{
		ID:          "testmsg-d5-001",
		CampfireID:  "cf-d5-test",
		Sender:      "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd",
		Payload:     p,
		Tags:        []string{conventionName + ":" + opName},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
		Signature:   []byte("sig"),
	}
}

// newD5Dispatcher creates a fresh ConventionDispatcher for D5 tests.
func newD5Dispatcher(t *testing.T) *convention.ConventionDispatcher {
	t.Helper()
	return convention.NewConventionDispatcher(convention.NewMemoryDispatchStore(), log.Default())
}

// TestD5_DispatchRejectsReservedOpWithDepth2Chain is the canonical case from the
// item spec: "Parent grant attempts depth-2 chain on delegation-grant → enforcer rejects".
//
// A GrantChain with 2 entries = delegation depth 2. Reserved ops are capped at depth 1.
// Dispatch must return false (DENY) without invoking the handler.
func TestD5_DispatchRejectsReservedOpWithDepth2Chain(t *testing.T) {
	const reservedOp = "delegation-grant"
	const conventionName = "test-convention"

	d := newD5Dispatcher(t)

	handlerCalled := false
	_ = d.RegisterTier1Handler("cf-d5-test", conventionName, reservedOp, nil,
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			handlerCalled = true
			return &convention.Response{}, nil
		},
		"server-d5", "")

	// Chain with 2 entries — exceeds depth-1 cap for reserved ops.
	depth2Chain := []string{
		"grant:scope=" + conventionName + ":" + reservedOp + ":depth=1:hop=1",
		"grant:scope=" + conventionName + ":" + reservedOp + ":depth=1:hop=2",
	}
	msg := buildD5Msg(t, conventionName, reservedOp, depth2Chain)
	dispatched := d.Dispatch(context.Background(), "cf-d5-test", msg)

	if dispatched {
		t.Errorf("Dispatch(op=%q, chain_depth=2): expected false (D5a depth-1 cap exceeded), got true", reservedOp)
	}
	time.Sleep(50 * time.Millisecond)
	if handlerCalled {
		t.Errorf("Dispatch(op=%q, chain_depth=2): handler must NOT be called — depth cap fires before L3", reservedOp)
	}
}

// TestD5_DispatchRejectsAllReservedOpsWithDepth2Chain verifies depth-2 rejection
// for every one of the ten reserved ops.
func TestD5_DispatchRejectsAllReservedOpsWithDepth2Chain(t *testing.T) {
	for _, reservedOp := range cfprotocol.ReservedOps {
		reservedOp := reservedOp
		t.Run("op="+reservedOp, func(t *testing.T) {
			d := newD5Dispatcher(t)
			handlerCalled := false
			_ = d.RegisterTier1Handler("cf-d5-test", "test-conv", reservedOp, nil,
				func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
					handlerCalled = true
					return &convention.Response{}, nil
				},
				"server-d5", "")

			depth2Chain := []string{
				"grant:op=" + reservedOp + ":hop=1",
				"grant:op=" + reservedOp + ":hop=2",
			}
			msg := buildD5Msg(t, "test-conv", reservedOp, depth2Chain)
			dispatched := d.Dispatch(context.Background(), "cf-d5-test", msg)

			if dispatched {
				t.Errorf("op=%q depth-2 chain: expected DENY (false), got true", reservedOp)
			}
			time.Sleep(50 * time.Millisecond)
			if handlerCalled {
				t.Errorf("op=%q depth-2 chain: handler must not be called", reservedOp)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Case 3 — Happy path: non-reserved op, any depth, any level → no enforcement fires
// ─────────────────────────────────────────────────────────────────────────────

// TestD5_HappyPath_NonReservedOpAnyDepthAnyLevel verifies that non-reserved ops
// are not subject to either D5a or D5b enforcement.
//
// A non-reserved op with a depth-3 chain and level 0 should proceed normally
// (the handler IS dispatched, not blocked by the reserved-op enforcer).
func TestD5_HappyPath_NonReservedOpAnyDepthAnyLevel(t *testing.T) {
	const nonReservedOp = "claim-item"
	const conventionName = "swarm-coordination"

	d := newD5Dispatcher(t)

	// Use atomic to avoid data race between handler goroutine and test read.
	var handlerCalled int32
	err := d.RegisterTier1Handler("cf-d5-test", conventionName, nonReservedOp, nil,
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			atomic.StoreInt32(&handlerCalled, 1)
			// Return nil response to avoid sendFulfillment with nil client.
			return nil, nil
		},
		"server-d5", "")
	if err != nil {
		t.Fatalf("RegisterTier1Handler(non-reserved op): unexpected error: %v", err)
	}

	// Depth-3 chain on a non-reserved op — D5a does not fire.
	depth3Chain := []string{"hop-1", "hop-2", "hop-3"}
	msg := buildD5Msg(t, conventionName, nonReservedOp, depth3Chain)
	dispatched := d.Dispatch(context.Background(), "cf-d5-test", msg)

	if !dispatched {
		t.Errorf("Dispatch(non-reserved op=%q, chain_depth=3): expected true (no enforcement), got false", nonReservedOp)
	}
	// Give the goroutine time to run.
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&handlerCalled) == 0 {
		t.Errorf("Dispatch(non-reserved op=%q): handler SHOULD be called for non-reserved ops", nonReservedOp)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Case 4 — Adversarial: "looks like depth-1 but is actually depth-2"
// ─────────────────────────────────────────────────────────────────────────────

// TestD5_Adversarial_ImplicitDepth2_CaughtByEnforcer verifies the adversarial case
// from the item spec: "A reserved op nested in a chain that LOOKS like depth-1 but
// actually has implicit depth-2".
//
// Scenario: the GrantChain slice has exactly 2 entries, but the outer entry wraps
// an inner delegation (simulating "depth-1 outer + depth-1 inner = depth-2 effective").
// The enforcer counts chain entries: len(GrantChain) > 1 → DENY regardless of how
// the entries are structured.
//
// This test proves the enforcer uses len(GrantChain) as the canonical depth signal and
// cannot be fooled by encoding tricks in the chain entries themselves.
func TestD5_Adversarial_ImplicitDepth2_CaughtByEnforcer(t *testing.T) {
	const reservedOp = "evict"
	const conventionName = "test-convention"

	d := newD5Dispatcher(t)

	handlerCalled := false
	_ = d.RegisterTier1Handler("cf-d5-test", conventionName, reservedOp, nil,
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			handlerCalled = true
			return &convention.Response{}, nil
		},
		"server-d5", "")

	// Adversarial chain: outer entry embeds "nested_depth=1" claim to look like depth-1,
	// but is actually 2 entries in the slice — the enforcer counts len, not inner claims.
	adversarialChain := []string{
		// outer hop claims to be depth-1 but wraps a sub-delegation:
		`{"hop":1,"depth_claim":1,"inner_grant":{"hop":2,"nested_depth":1,"op":"evict"}}`,
		// second explicit hop — makes this depth-2 regardless of inner claims:
		`{"hop":2,"depth":1,"op":"evict"}`,
	}
	msg := buildD5Msg(t, conventionName, reservedOp, adversarialChain)
	dispatched := d.Dispatch(context.Background(), "cf-d5-test", msg)

	if dispatched {
		t.Errorf("Dispatch(op=%q, adversarial-depth-2): expected DENY (depth cap fires), got true", reservedOp)
	}
	time.Sleep(50 * time.Millisecond)
	if handlerCalled {
		t.Errorf("Dispatch(op=%q, adversarial-depth-2): handler must NOT be called", reservedOp)
	}
}

// TestD5_Adversarial_Depth3Chain_ReservedOp_Denied verifies that a depth-3 grant chain
// on a reserved op is also rejected (stricter than the general depth ≤ 2 rule).
func TestD5_Adversarial_Depth3Chain_ReservedOp_Denied(t *testing.T) {
	const reservedOp = "admit"
	const conventionName = "test-convention"

	d := newD5Dispatcher(t)

	handlerCalled := false
	_ = d.RegisterTier1Handler("cf-d5-test", conventionName, reservedOp, nil,
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			handlerCalled = true
			return &convention.Response{}, nil
		},
		"server-d5", "")

	depth3Chain := []string{"hop-1", "hop-2", "hop-3"}
	msg := buildD5Msg(t, conventionName, reservedOp, depth3Chain)
	dispatched := d.Dispatch(context.Background(), "cf-d5-test", msg)

	if dispatched {
		t.Errorf("Dispatch(op=%q, chain_depth=3): expected DENY, got true", reservedOp)
	}
	time.Sleep(50 * time.Millisecond)
	if handlerCalled {
		t.Errorf("Dispatch(op=%q, chain_depth=3): handler must NOT be called", reservedOp)
	}
}

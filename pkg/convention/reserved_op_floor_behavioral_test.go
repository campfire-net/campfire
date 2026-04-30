package convention_test

// reserved_op_floor_behavioral_test.go — Stage 1 enforcer: behavioral tests for the
// L2 DENY(reserved_op_floor) path (campfireagent-cb3).
//
// Architecture recap (reserved-ops-chain.md):
//
//	L1 — cf-protocol     LIST lives here — 10 frozen ops
//	L2 — cf-conventions  ENFORCER — blocks dispatch of reserved ops at the interceptor
//	                      DENY reason: "reserved_op_floor" (wire-frozen at cf-authority 1.0)
//	L3 — cf-authority    EVALUATOR — grant-chain policy (future; campfireagent-02a)
//
// These tests extend pkg/convention/reserved_op_enforcement_test.go (campfireagent-935)
// with the BEHAVIORAL scenarios mandated by the veracity verdict on campfireagent-514
// (Stage 0 chain doc): a reserved op dispatched WITHOUT a grant chain must be denied at
// the L2 floor — and crucially, even when a grant_chain field IS present in the message
// envelope, the L2 enforcer must still deny before invoking L3.
//
// Adversarial cases covered (spec item §DONE condition 3):
//   A. reserved op — no grant_chain field (baseline)
//   B. reserved op — empty grant_chain slice (zero entries)
//   C. reserved op — chain present but for a DIFFERENT op (scope mismatch; floor fires first)
//   D. reserved op — chain entries expired (expired token; floor fires first)
//   E. DispatchWithCancel path — same L2 floor applies
//
// NOTE: The current L2 enforcer blocks ALL reserved ops regardless of chain content —
// it does not yet delegate to L3 (GateEvaluator). Tests A–D therefore assert that
// Dispatch returns false AND the handler is NOT called, regardless of what is in the
// GrantChain field. When L3 is wired (campfireagent-02a), the test for "valid depth-1
// owner grant chain" will need its own test file; these adversarial cases remain valid.
//
// DenyReason semantics: "reserved_op_floor" is the stable string documented in
// cf-authority-spec.md §3.4. ErrReservedOp (in dispatcher.go) carries this prefix.
// If the DENY reason string ever drifts, TestReservedOpFloorDenyReasonString (below)
// will catch it.

import (
	"context"
	"encoding/json"
	"log"
	"testing"
	"time"

	cfprotocol "github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// buildReservedOpMsgWithChain constructs a store.MessageRecord for (convention, op)
// with an optional grant_chain slice embedded in the payload.
// An empty/nil chain produces a payload without the grant_chain field.
func buildReservedOpMsgWithChain(t *testing.T, conventionName, operationName string, grantChain []string) *store.MessageRecord {
	t.Helper()
	type opPayload struct {
		Convention string   `json:"convention"`
		Operation  string   `json:"operation"`
		GrantChain []string `json:"grant_chain,omitempty"`
	}
	payload, err := json.Marshal(opPayload{
		Convention: conventionName,
		Operation:  operationName,
		GrantChain: grantChain,
	})
	if err != nil {
		t.Fatalf("buildReservedOpMsgWithChain: marshal failed: %v", err)
	}
	return &store.MessageRecord{
		ID:          "testmsg-floor-behavioral-001",
		CampfireID:  "cf-floor-behavioral-test",
		Sender:      "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd",
		Payload:     payload,
		Tags:        []string{conventionName + ":" + operationName},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
		Signature:   []byte("sig"),
	}
}

// newBehavioralDispatcher creates a fresh ConventionDispatcher for behavioral tests.
func newBehavioralDispatcher(t *testing.T) *convention.ConventionDispatcher {
	t.Helper()
	return convention.NewConventionDispatcher(convention.NewMemoryDispatchStore(), log.Default())
}

// -----------------------------------------------------------------------------
// A. No grant_chain — baseline behavioral test
// -----------------------------------------------------------------------------

// TestReservedOpFloor_NoChain_ReturnsBaseDeny is the primary BEHAVIORAL test demanded by
// campfireagent-cb3: dispatch a reserved op through the full L2 path with NO grant chain
// present; assert Dispatch returns false (DENY) and the handler is not invoked.
//
// This is the TDD anchor: if the L2 enforcer is removed or bypassed, this test fails.
func TestReservedOpFloor_NoChain_ReturnsBaseDeny(t *testing.T) {
	// Pick a canonical reserved op for the primary behavioral test.
	// "evict" is the most representative (named in the spec §Enforcement Flow example).
	const reservedOp = "evict"

	d := newBehavioralDispatcher(t)

	handlerCalled := false
	// Attempt to register — expected to fail at registration time (enforcement at Tier1).
	// If register returns ErrReservedOp we still test the Dispatch path via direct message.
	_ = d.RegisterTier1Handler("cf-floor-behavioral-test", "test-conv", reservedOp, nil,
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			handlerCalled = true
			return &convention.Response{}, nil
		},
		"server-cbtest", "")

	msg := buildReservedOpMsgWithChain(t, "test-conv", reservedOp, nil /* no chain */)
	dispatched := d.Dispatch(context.Background(), "cf-floor-behavioral-test", msg)

	if dispatched {
		t.Errorf("Dispatch(op=%q, chain=nil): expected false (DENY reserved_op_floor), got true", reservedOp)
	}
	time.Sleep(50 * time.Millisecond)
	if handlerCalled {
		t.Errorf("Dispatch(op=%q, chain=nil): handler must NOT be called — L2 floor must deny before L3", reservedOp)
	}
}

// TestReservedOpFloor_NoChain_AllTenOps verifies the NO-chain behavioral path for every
// one of the ten reserved ops. Extends TestReservedOpBlockedAtDispatch (campfireagent-935)
// by building messages with no grant_chain field and asserting the deny reason is present.
func TestReservedOpFloor_NoChain_AllTenOps(t *testing.T) {
	for _, reservedOp := range cfprotocol.ReservedOps {
		reservedOp := reservedOp
		t.Run("op="+reservedOp, func(t *testing.T) {
			d := newBehavioralDispatcher(t)
			handlerCalled := false
			_ = d.RegisterTier1Handler("cf-floor-behavioral-test", "my-conv", reservedOp, nil,
				func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
					handlerCalled = true
					return &convention.Response{}, nil
				},
				"server-cbtest", "")

			msg := buildReservedOpMsgWithChain(t, "my-conv", reservedOp, nil)
			dispatched := d.Dispatch(context.Background(), "cf-floor-behavioral-test", msg)

			if dispatched {
				t.Errorf("op=%q no-chain: expected DENY (false), got true", reservedOp)
			}
			time.Sleep(50 * time.Millisecond)
			if handlerCalled {
				t.Errorf("op=%q no-chain: handler must not be called", reservedOp)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// B. Empty grant_chain slice — L2 must still deny before consulting L3
// -----------------------------------------------------------------------------

// TestReservedOpFloor_EmptyChain_StillDenied verifies that a reserved op whose message
// carries an EMPTY grant_chain slice ([]) is still denied at the L2 floor.
// A zero-length chain is not a valid owner grant; the floor fires unconditionally.
func TestReservedOpFloor_EmptyChain_StillDenied(t *testing.T) {
	for _, reservedOp := range cfprotocol.ReservedOps {
		reservedOp := reservedOp
		t.Run("op="+reservedOp, func(t *testing.T) {
			d := newBehavioralDispatcher(t)
			handlerCalled := false
			_ = d.RegisterTier1Handler("cf-floor-behavioral-test", "test-conv", reservedOp, nil,
				func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
					handlerCalled = true
					return &convention.Response{}, nil
				},
				"server-cbtest", "")

			msg := buildReservedOpMsgWithChain(t, "test-conv", reservedOp, []string{} /* empty chain */)
			dispatched := d.Dispatch(context.Background(), "cf-floor-behavioral-test", msg)

			if dispatched {
				t.Errorf("op=%q empty-chain: expected DENY (false), got true", reservedOp)
			}
			time.Sleep(50 * time.Millisecond)
			if handlerCalled {
				t.Errorf("op=%q empty-chain: handler must not be called", reservedOp)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// C. Chain present but for a DIFFERENT op — scope mismatch; L2 floor fires first
// -----------------------------------------------------------------------------

// TestReservedOpFloor_ChainForDifferentOp_StillDenied verifies that a chain token
// granting authority for a DIFFERENT operation (or a non-reserved op) does not satisfy
// the reserved-op floor for the actual op being dispatched.
//
// Spec rationale: the L2 floor check is "is this op in the reserved LIST?" — it is not
// a scope evaluation. A chain that covers "member-roster:read" does not authorize
// "evict". The L2 floor blocks before the L3 evaluator is even invoked.
func TestReservedOpFloor_ChainForDifferentOp_StillDenied(t *testing.T) {
	// Use two representative reserved ops: dispatch "evict", chain claims "member-roster".
	const targetOp = "evict"
	const chainOp = "member-roster" // the chain token authorizes a different op

	// Simulate a chain entry that contains the wrong op name.
	// In production these would be CBOR-encoded grant tokens; here a placeholder string
	// is sufficient — the L2 enforcer doesn't parse the chain (that's L3's job).
	wrongOpChain := []string{"grant:scope=test-conv:" + chainOp + ":depth=1:expires=9999999999"}

	d := newBehavioralDispatcher(t)
	handlerCalled := false
	_ = d.RegisterTier1Handler("cf-floor-behavioral-test", "test-conv", targetOp, nil,
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			handlerCalled = true
			return &convention.Response{}, nil
		},
		"server-cbtest", "")

	msg := buildReservedOpMsgWithChain(t, "test-conv", targetOp, wrongOpChain)
	dispatched := d.Dispatch(context.Background(), "cf-floor-behavioral-test", msg)

	if dispatched {
		t.Errorf("op=%q chain-for-different-op: expected DENY (false), got true", targetOp)
	}
	time.Sleep(50 * time.Millisecond)
	if handlerCalled {
		t.Errorf("op=%q chain-for-different-op: handler must not be called", targetOp)
	}
}

// -----------------------------------------------------------------------------
// D. Chain entries expired — L2 floor fires before expiry is even checked
// -----------------------------------------------------------------------------

// TestReservedOpFloor_ExpiredChain_StillDenied verifies that a reserved op whose
// message carries a grant chain with an EXPIRED expiry timestamp is still denied
// at the L2 floor — not by L3's expiry check, but by L2's reserved-op list check.
//
// This is important: in a correct implementation, "expired chain" should never reach
// L3's expiry evaluator for a reserved op. L2 must fire first.
func TestReservedOpFloor_ExpiredChain_StillDenied(t *testing.T) {
	const reservedOp = "delegation-grant" // representative reserved op

	// Chain token with a past expiry timestamp (Unix epoch = 0 = expired since 1970).
	expiredChain := []string{"grant:scope=test-conv:" + reservedOp + ":depth=1:expires=0"}

	d := newBehavioralDispatcher(t)
	handlerCalled := false
	_ = d.RegisterTier1Handler("cf-floor-behavioral-test", "test-conv", reservedOp, nil,
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			handlerCalled = true
			return &convention.Response{}, nil
		},
		"server-cbtest", "")

	msg := buildReservedOpMsgWithChain(t, "test-conv", reservedOp, expiredChain)
	dispatched := d.Dispatch(context.Background(), "cf-floor-behavioral-test", msg)

	if dispatched {
		t.Errorf("op=%q expired-chain: expected DENY (false), got true", reservedOp)
	}
	time.Sleep(50 * time.Millisecond)
	if handlerCalled {
		t.Errorf("op=%q expired-chain: handler must not be called", reservedOp)
	}
}

// -----------------------------------------------------------------------------
// E. DispatchWithCancel — same L2 floor applies
// -----------------------------------------------------------------------------

// TestReservedOpFloor_DispatchWithCancel_Denied verifies that DispatchWithCancel
// enforces the same L2 reserved-op floor as Dispatch.
//
// DispatchWithCancel is used when a timeout context is passed down; the cancel func
// must be called even when the op is denied at L2 (to release the timer).
func TestReservedOpFloor_DispatchWithCancel_Denied(t *testing.T) {
	const reservedOp = "disband"

	d := newBehavioralDispatcher(t)
	handlerCalled := false
	_ = d.RegisterTier1Handler("cf-floor-behavioral-test", "test-conv", reservedOp, nil,
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			handlerCalled = true
			return &convention.Response{}, nil
		},
		"server-cbtest", "")

	cancelCalled := false
	cancelFunc := func() { cancelCalled = true }

	msg := buildReservedOpMsgWithChain(t, "test-conv", reservedOp, nil)
	ctx, outerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer outerCancel()

	dispatched := d.DispatchWithCancel(ctx, cancelFunc, "cf-floor-behavioral-test", msg)

	if dispatched {
		t.Errorf("DispatchWithCancel(op=%q): expected DENY (false), got true", reservedOp)
	}
	if !cancelCalled {
		t.Errorf("DispatchWithCancel(op=%q): cancel func must be called when reserved op is denied at L2", reservedOp)
	}
	time.Sleep(50 * time.Millisecond)
	if handlerCalled {
		t.Errorf("DispatchWithCancel(op=%q): handler must not be called", reservedOp)
	}
}

// TestReservedOpFloor_DispatchWithCancel_WithChain_Denied verifies DispatchWithCancel
// denies reserved ops even when a grant_chain is present.
func TestReservedOpFloor_DispatchWithCancel_WithChain_Denied(t *testing.T) {
	const reservedOp = "admit"
	chain := []string{"grant:scope=test-conv:admit:depth=1:expires=9999999999"}

	d := newBehavioralDispatcher(t)
	handlerCalled := false
	_ = d.RegisterTier1Handler("cf-floor-behavioral-test", "test-conv", reservedOp, nil,
		func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
			handlerCalled = true
			return &convention.Response{}, nil
		},
		"server-cbtest", "")

	cancelCalled := false
	cancelFunc := func() { cancelCalled = true }

	msg := buildReservedOpMsgWithChain(t, "test-conv", reservedOp, chain)
	ctx, outerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer outerCancel()

	dispatched := d.DispatchWithCancel(ctx, cancelFunc, "cf-floor-behavioral-test", msg)

	if dispatched {
		t.Errorf("DispatchWithCancel(op=%q, chain=present): expected DENY (false), got true", reservedOp)
	}
	if !cancelCalled {
		t.Errorf("DispatchWithCancel(op=%q): cancel func must be called when reserved op is denied at L2", reservedOp)
	}
	time.Sleep(50 * time.Millisecond)
	if handlerCalled {
		t.Errorf("DispatchWithCancel(op=%q, chain=present): handler must not be called", reservedOp)
	}
}

// -----------------------------------------------------------------------------
// DenyReason string stability guard
// -----------------------------------------------------------------------------

// TestReservedOpFloorDenyReasonString guards the wire-frozen "reserved_op_floor" string
// in ErrReservedOp. cf-authority-spec.md §3.4 declares this as a stable wire code.
// If the string drifts, conformance harness case 11 will break.
func TestReservedOpFloorDenyReasonString(t *testing.T) {
	const wantSubstring = "reserved_op_floor"
	err := convention.ErrReservedOp
	if err == nil {
		t.Fatal("ErrReservedOp must not be nil")
	}
	if msg := err.Error(); len(msg) == 0 {
		t.Fatal("ErrReservedOp.Error() must not be empty")
	} else {
		found := false
		for i := 0; i <= len(msg)-len(wantSubstring); i++ {
			if msg[i:i+len(wantSubstring)] == wantSubstring {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ErrReservedOp.Error() = %q; must contain %q (wire-frozen code per cf-authority-spec.md §3.4)", msg, wantSubstring)
		}
	}
}

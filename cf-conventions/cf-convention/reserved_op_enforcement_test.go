package convention_test

// reserved_op_enforcement_test.go — Stage 1 enforcement contract tests
// (campfireagent-935: reserved-op floor wired into L2 dispatch interceptor).
//
// Architecture (protocol-spec.md §Reserved-Op Floor):
//   L1 (cf-protocol/internal/reserved-ops): LIST — IsReserved(op)
//   L2 (pkg/convention/dispatcher):         ENFORCER — blocks dispatch of reserved ops
//   L3 (cf-authority):                      EVALUATOR — grant-chain policy (future)
//
// Enforcement points tested here:
//   1. Dispatch: a message whose convention payload carries a reserved op name
//      is silently dropped (returns false, handler is NOT called).
//   2. RegisterTier1Handler / RegisterTier2Handler: return error for reserved op names.
//   3. Non-reserved ops are not affected.
//   4. All ten reserved ops are blocked uniformly (count = 10).
//
// TDD note: these tests are written BEFORE the implementation. They fail on HEAD
// until dispatcher.go blocks reserved ops at both registration and dispatch time.

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"
	"time"

	cfprotocol "github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// newReservedTestStore creates an in-memory dispatch store for enforcement tests.
func newReservedTestStore(t *testing.T) convention.DispatchStore {
	t.Helper()
	return convention.NewMemoryDispatchStore()
}

// buildReservedOpMsg builds a store.MessageRecord whose payload declares a
// convention invocation for the given (convention, operation) pair. The message
// carries the "<convention>:<operation>" invocation tag so Dispatch picks it up.
func buildReservedOpMsg(conventionName, operationName string) *store.MessageRecord {
	type opPayload struct {
		Convention string `json:"convention"`
		Operation  string `json:"operation"`
	}
	payload, _ := json.Marshal(opPayload{Convention: conventionName, Operation: operationName})
	return &store.MessageRecord{
		ID:          "testmsg-reserved-001",
		CampfireID:  "cf-reserved-test",
		Sender:      "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd",
		Payload:     payload,
		Tags:        []string{conventionName + ":" + operationName},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
		Signature:   []byte("sig"),
	}
}

// TestReservedOpBlockedAtDispatch verifies that Dispatch returns false and does NOT
// invoke the handler when the convention operation name is a reserved op.
//
// This test FAILS if enforcement is absent (handler called when it should be blocked).
func TestReservedOpBlockedAtDispatch(t *testing.T) {
	for _, reservedOp := range cfprotocol.ReservedOps {
		t.Run("op="+reservedOp, func(t *testing.T) {
			s := newReservedTestStore(t)
			d := convention.NewConventionDispatcher(s, log.Default())

			handlerCalled := false
			// Register a handler for (convention="my-test-conv", operation=<reserved>).
			// This might fail at registration time (if that enforcement is added), or
			// the handler might be registered but Dispatch should still block it.
			_ = d.RegisterTier1Handler("cf-reserved-test", "my-test-conv", reservedOp, nil,
				func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
					handlerCalled = true
					return &convention.Response{}, nil
				},
				"server-test", "")

			msg := buildReservedOpMsg("my-test-conv", reservedOp)
			dispatched := d.Dispatch(context.Background(), "cf-reserved-test", msg)

			if dispatched {
				t.Errorf("Dispatch(op=%q): expected false (reserved op blocked), got true", reservedOp)
			}
			// Give any accidentally-spawned goroutine a moment to run.
			time.Sleep(50 * time.Millisecond)
			if handlerCalled {
				t.Errorf("Dispatch(op=%q): handler must NOT be called for reserved ops", reservedOp)
			}
		})
	}
}

// TestReservedOpBlockedAtRegistration_Tier1 verifies that RegisterTier1Handler
// returns an error when the operation name is reserved. If enforcement is absent,
// this test fails because the handler is registered without error.
//
// This test FAILS if Register* does not gate on reserved ops.
func TestReservedOpBlockedAtRegistration_Tier1(t *testing.T) {
	s := newReservedTestStore(t)
	d := convention.NewConventionDispatcher(s, log.Default())

	for _, op := range cfprotocol.ReservedOps {
		err := d.RegisterTier1Handler("campfire-123", "my-convention", op, nil,
			func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
				return &convention.Response{}, nil
			},
			"serverid", "")
		if err == nil {
			t.Errorf("RegisterTier1Handler(op=%q): expected error for reserved op, got nil", op)
		} else if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("RegisterTier1Handler(op=%q): error should mention 'reserved', got: %v", op, err)
		}
	}
}

// TestReservedOpBlockedAtRegistration_Tier2 verifies that RegisterTier2Handler
// returns an error when the operation name is reserved.
func TestReservedOpBlockedAtRegistration_Tier2(t *testing.T) {
	s := newReservedTestStore(t)
	d := convention.NewConventionDispatcher(s, log.Default())

	for _, op := range cfprotocol.ReservedOps {
		err := d.RegisterTier2Handler("campfire-123", "my-convention", op, "http://handler.test/", nil, "serverid", "")
		if err == nil {
			t.Errorf("RegisterTier2Handler(op=%q): expected error for reserved op, got nil", op)
		}
	}
}

// TestNonReservedOpRegistrationSucceeds verifies that normal (non-reserved) op
// names are not blocked by the enforcement check.
func TestNonReservedOpRegistrationSucceeds(t *testing.T) {
	s := newReservedTestStore(t)
	d := convention.NewConventionDispatcher(s, log.Default())

	nonReserved := []string{"claim", "complete", "publish", "subscribe", "my-convention-op"}
	for _, op := range nonReserved {
		err := d.RegisterTier1Handler("campfire-123", "my-convention", op, nil,
			func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
				return &convention.Response{}, nil
			},
			"serverid", "")
		if err != nil {
			t.Errorf("RegisterTier1Handler(op=%q): unexpected error for non-reserved op: %v", op, err)
		}
	}
}

// TestAllTenReservedOpsBlocked verifies that all 10 documented reserved ops are
// blocked. This test fails if any op is removed from the enforcement list or
// if the count drifts from the canonical 10.
func TestAllTenReservedOpsBlocked(t *testing.T) {
	const wantCount = 10
	if got := len(cfprotocol.ReservedOps); got != wantCount {
		t.Fatalf("reserved op list has %d ops, want %d — enforcement coverage incomplete", got, wantCount)
	}

	s := newReservedTestStore(t)
	d := convention.NewConventionDispatcher(s, log.Default())

	blocked := 0
	for _, op := range cfprotocol.ReservedOps {
		err := d.RegisterTier1Handler("campfire-123", "test-convention", op, nil,
			func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
				return &convention.Response{}, nil
			},
			"server", "")
		if err != nil {
			blocked++
		}
	}
	if blocked != wantCount {
		t.Errorf("expected all %d reserved ops to be blocked at registration, got %d blocked", wantCount, blocked)
	}
}

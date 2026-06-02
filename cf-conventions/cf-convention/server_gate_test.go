package convention_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// staticGateEvaluator is a test GateEvaluator that always returns a fixed decision.
type staticGateEvaluator struct {
	decision convention.Decision
	reason   convention.DenyReason
	mu       sync.Mutex
	calls    int
}

func (g *staticGateEvaluator) Evaluate(_ context.Context, _ convention.EvaluateRequest) convention.EvaluateResult {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	return convention.EvaluateResult{Decision: g.decision, Reason: g.reason}
}

func (g *staticGateEvaluator) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// runGatedDispatch starts a Server with the given gate evaluator, sends one
// social:post message, and returns whether the handler ran and the error
// fulfillment (if any) threaded back to the sent message.
func runGatedDispatch(t *testing.T, eval convention.GateEvaluator) (handlerRan bool, errMsg *protocol.Message) {
	t.Helper()
	env := setupServerTestEnv(t)
	decl := socialPostDecl()

	var mu sync.Mutex
	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond).
		WithGateEvaluator(eval)

	srv.RegisterHandler("post", func(_ context.Context, _ *convention.Request) (*convention.Response, error) {
		mu.Lock()
		handlerRan = true
		mu.Unlock()
		return &convention.Response{Payload: map[string]any{"ok": true}}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.Serve(ctx, env.campfireID) //nolint:errcheck
	}()
	time.Sleep(20 * time.Millisecond)

	sentMsg, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte(`{"text":"hello"}`),
		Tags:       []string{"social:post"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Poll for either an error fulfillment or the handler running.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		result, readErr := env.callerClient.Read(protocol.ReadRequest{
			CampfireID: env.campfireID,
			Tags:       []string{"convention:error"},
		})
		if readErr != nil {
			t.Fatalf("Read: %v", readErr)
		}
		for i, msg := range result.Messages {
			for _, ant := range msg.Antecedents {
				if ant == sentMsg.ID {
					errMsg = &result.Messages[i]
				}
			}
		}
		mu.Lock()
		ran := handlerRan
		mu.Unlock()
		if errMsg != nil || ran {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	return handlerRan, errMsg
}

// TestServerSDK_GateEvaluator_Denied verifies that a GateEvaluator returning
// GateDeny blocks the handler and sends a convention:error fulfillment.
func TestServerSDK_GateEvaluator_Denied(t *testing.T) {
	eval := &staticGateEvaluator{decision: convention.GateDeny, reason: convention.DenyPredicate}
	handlerRan, errMsg := runGatedDispatch(t, eval)

	if handlerRan {
		t.Error("handler must NOT run when the gate denies the request")
	}
	if errMsg == nil {
		t.Fatal("expected a convention:error fulfillment but none arrived")
	}
	if eval.callCount() == 0 {
		t.Error("gate evaluator was never called")
	}
	errText, parseErr := convention.ParseErrorResponse(errMsg)
	if parseErr != nil {
		t.Fatalf("ParseErrorResponse: %v", parseErr)
	}
	if !strings.Contains(errText, "gate denied") {
		t.Errorf("error payload = %q, want it to mention 'gate denied'", errText)
	}
}

// TestServerSDK_GateEvaluator_Unresolvable verifies the fail-closed contract:
// GateUnresolvable is treated like a deny (handler blocked, error sent).
func TestServerSDK_GateEvaluator_Unresolvable(t *testing.T) {
	eval := &staticGateEvaluator{decision: convention.GateUnresolvable}
	handlerRan, errMsg := runGatedDispatch(t, eval)

	if handlerRan {
		t.Error("handler must NOT run when the gate is unresolvable (fail closed)")
	}
	if errMsg == nil {
		t.Fatal("expected a convention:error fulfillment for an unresolvable gate")
	}
}

// TestServerSDK_GateEvaluator_Allowed verifies that an explicit allow evaluator
// lets the operation through to the handler.
func TestServerSDK_GateEvaluator_Allowed(t *testing.T) {
	eval := &staticGateEvaluator{decision: convention.GateAllow}
	handlerRan, errMsg := runGatedDispatch(t, eval)

	if !handlerRan {
		t.Error("handler must run when the gate allows the request")
	}
	if errMsg != nil {
		errText, _ := convention.ParseErrorResponse(errMsg)
		t.Errorf("unexpected error fulfillment on allow: %q", errText)
	}
	if eval.callCount() == 0 {
		t.Error("gate evaluator was never called")
	}
}

// TestServerSDK_GateEvaluator_DefaultAllows verifies that a Server with no
// WithGateEvaluator call (and one passed nil) defaults to allow-all, preserving
// pre-existing behavior for servers that don't opt into gating.
func TestServerSDK_GateEvaluator_DefaultAllows(t *testing.T) {
	// nil resets to AllowAllGateEvaluator per the WithGateEvaluator contract.
	handlerRan, errMsg := runGatedDispatch(t, nil)
	if !handlerRan {
		t.Error("handler must run by default (allow-all) when gating is not configured")
	}
	if errMsg != nil {
		t.Error("no error fulfillment expected under default allow-all gating")
	}
}

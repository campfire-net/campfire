package hosting

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// stubBalanceChecker is a test double for BalanceChecker.
type stubBalanceChecker struct {
	mu      sync.Mutex
	balance int64
	err     error
	calls   int
}

func (s *stubBalanceChecker) Balance(_ context.Context, _ string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.balance, s.err
}

func (s *stubBalanceChecker) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// newTestGate returns a BillingGate with short TTLs so tests run quickly.
func newTestGate(checker BalanceChecker) *BillingGate {
	g := NewBillingGate(checker)
	g.CacheTTL = 30 * time.Second
	g.ErrorThreshold = 3
	g.ErrorWindow = 60 * time.Second
	g.CircuitOpenDuration = 30 * time.Second
	return g
}

// ── Balance checks ────────────────────────────────────────────────────────────

func TestBillingGate_PositiveBalanceAllows(t *testing.T) {
	stub := &stubBalanceChecker{balance: 1000}
	g := newTestGate(stub)
	if err := g.AllowDurableWrite(context.Background(), "acc1"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestBillingGate_ZeroBalanceDenies(t *testing.T) {
	stub := &stubBalanceChecker{balance: 0}
	g := newTestGate(stub)
	err := g.AllowDurableWrite(context.Background(), "acc1")
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("expected ErrInsufficientBalance, got %v", err)
	}
}

func TestBillingGate_NegativeBalanceDenies(t *testing.T) {
	stub := &stubBalanceChecker{balance: -500}
	g := newTestGate(stub)
	err := g.AllowDurableWrite(context.Background(), "acc1")
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("expected ErrInsufficientBalance, got %v", err)
	}
}

func TestBillingGate_ErrCarries402StatusCode(t *testing.T) {
	type statuser interface{ StatusCode() int }
	stub := &stubBalanceChecker{balance: 0}
	g := newTestGate(stub)
	err := g.AllowDurableWrite(context.Background(), "acc1")
	s, ok := err.(statuser)
	if !ok {
		t.Fatal("ErrInsufficientBalance does not implement StatusCode()")
	}
	if s.StatusCode() != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", s.StatusCode())
	}
}

// ── Balance cache ─────────────────────────────────────────────────────────────

func TestBillingGate_CacheAvoidsDuplicateForgeCall(t *testing.T) {
	stub := &stubBalanceChecker{balance: 1000}
	g := newTestGate(stub)

	// Two calls — Forge should only be hit once.
	_ = g.AllowDurableWrite(context.Background(), "acc1")
	_ = g.AllowDurableWrite(context.Background(), "acc1")

	if stub.callCount() != 1 {
		t.Fatalf("expected 1 Forge call, got %d", stub.callCount())
	}
}

func TestBillingGate_CacheExpiryTriggersFreshCheck(t *testing.T) {
	now := time.Now()
	stub := &stubBalanceChecker{balance: 1000}
	g := newTestGate(stub)
	g.CacheTTL = 10 * time.Millisecond
	g.now = func() time.Time { return now }

	// First call — populates cache.
	_ = g.AllowDurableWrite(context.Background(), "acc1")
	if stub.callCount() != 1 {
		t.Fatalf("expected 1 Forge call after first request, got %d", stub.callCount())
	}

	// Advance time beyond TTL.
	g.now = func() time.Time { return now.Add(20 * time.Millisecond) }

	// Second call — cache is stale, should hit Forge again.
	_ = g.AllowDurableWrite(context.Background(), "acc1")
	if stub.callCount() != 2 {
		t.Fatalf("expected 2 Forge calls after cache expiry, got %d", stub.callCount())
	}
}

// ── Circuit breaker ───────────────────────────────────────────────────────────

func TestBillingGate_SingleForgeErrorDoesNotOpenCircuit(t *testing.T) {
	stub := &stubBalanceChecker{err: errors.New("forge down")}
	g := newTestGate(stub)

	// One error — circuit should stay closed, gate should still fail-open.
	err := g.AllowDurableWrite(context.Background(), "acc1")
	if err != nil {
		t.Fatalf("expected fail-open nil, got %v", err)
	}

	g.mu.Lock()
	open := !g.circuit.openUntil.IsZero()
	g.mu.Unlock()
	if open {
		t.Fatal("circuit opened after only 1 error; threshold is 3")
	}
}

func TestBillingGate_ThreeErrorsOpenCircuit(t *testing.T) {
	stub := &stubBalanceChecker{err: errors.New("forge down")}

	var changes []bool
	g := newTestGate(stub)
	g.OnCircuitChange = func(open bool) { changes = append(changes, open) }

	for i := 0; i < 3; i++ {
		_ = g.AllowDurableWrite(context.Background(), "acc1")
	}

	g.mu.Lock()
	open := !g.circuit.openUntil.IsZero()
	g.mu.Unlock()
	if !open {
		t.Fatal("circuit should be open after 3 errors")
	}
	if len(changes) == 0 || !changes[len(changes)-1] {
		t.Fatal("OnCircuitChange should have been called with open=true")
	}
}

func TestBillingGate_OpenCircuitFailsOpen(t *testing.T) {
	stub := &stubBalanceChecker{err: errors.New("forge down")}
	g := newTestGate(stub)

	// Trip the circuit.
	for i := 0; i < 3; i++ {
		_ = g.AllowDurableWrite(context.Background(), "acc1")
	}

	// Now the circuit is open — even with balance == 0, we should allow writes.
	stub.mu.Lock()
	stub.err = nil
	stub.balance = 0
	stub.mu.Unlock()

	err := g.AllowDurableWrite(context.Background(), "acc1")
	if err != nil {
		t.Fatalf("open circuit should fail-open (nil), got %v", err)
	}
}

func TestBillingGate_CircuitClosesAfterDuration(t *testing.T) {
	now := time.Now()
	stub := &stubBalanceChecker{err: errors.New("forge down")}
	g := newTestGate(stub)
	g.CircuitOpenDuration = 30 * time.Second
	g.now = func() time.Time { return now }

	// Trip the circuit.
	for i := 0; i < 3; i++ {
		_ = g.AllowDurableWrite(context.Background(), "acc1")
	}

	// Advance time past circuit-open duration.
	g.now = func() time.Time { return now.Add(31 * time.Second) }

	// Forge now healthy with positive balance.
	stub.mu.Lock()
	stub.err = nil
	stub.balance = 100
	stub.mu.Unlock()

	// Next call should re-query Forge (circuit reset) and allow.
	err := g.AllowDurableWrite(context.Background(), "acc1")
	if err != nil {
		t.Fatalf("expected nil after circuit recovery, got %v", err)
	}
}

func TestBillingGate_IsInsufficientBalance(t *testing.T) {
	stub := &stubBalanceChecker{balance: 0}
	g := newTestGate(stub)
	err := g.AllowDurableWrite(context.Background(), "acc1")
	if !IsInsufficientBalance(err) {
		t.Fatalf("IsInsufficientBalance should return true for ErrInsufficientBalance, got %v", err)
	}
}

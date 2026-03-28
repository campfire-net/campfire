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

	// One error — circuit should stay closed, gate should fail-CLOSED.
	err := g.AllowDurableWrite(context.Background(), "acc1")
	if !errors.Is(err, ErrBillingUnavailable) {
		t.Fatalf("expected ErrBillingUnavailable (fail-closed), got %v", err)
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

// ── Fail-closed security fix ──────────────────────────────────────────────────

func TestBillingGate_SingleForgeErrorFailsClosed(t *testing.T) {
	stub := &stubBalanceChecker{err: errors.New("network timeout")}
	g := newTestGate(stub)

	// A single Forge error must NOT grant free durable writes.
	err := g.AllowDurableWrite(context.Background(), "acc1")
	if err == nil {
		t.Fatal("expected fail-closed error, got nil (security: free write granted on single error)")
	}
	if !errors.Is(err, ErrBillingUnavailable) {
		t.Fatalf("expected ErrBillingUnavailable, got %v", err)
	}
}

func TestBillingGate_TwoForgeErrorsFailClosed(t *testing.T) {
	stub := &stubBalanceChecker{err: errors.New("forge down")}
	g := newTestGate(stub)

	// Two errors — below threshold, still fail-closed.
	for i := 0; i < 2; i++ {
		err := g.AllowDurableWrite(context.Background(), "acc1")
		if !errors.Is(err, ErrBillingUnavailable) {
			t.Fatalf("call %d: expected ErrBillingUnavailable (fail-closed), got %v", i+1, err)
		}
	}
}

func TestBillingGate_ThresholdErrorsFailOpen(t *testing.T) {
	stub := &stubBalanceChecker{err: errors.New("forge down")}
	g := newTestGate(stub)

	// First threshold-1 calls fail-closed.
	for i := 0; i < g.ErrorThreshold-1; i++ {
		err := g.AllowDurableWrite(context.Background(), "acc1")
		if !errors.Is(err, ErrBillingUnavailable) {
			t.Fatalf("call %d: expected ErrBillingUnavailable, got %v", i+1, err)
		}
	}

	// The threshold-th error opens the circuit — should fail-open.
	err := g.AllowDurableWrite(context.Background(), "acc1")
	if err != nil {
		t.Fatalf("circuit-opening error: expected nil (fail-open), got %v", err)
	}
}

// ── 4xx error classification ───────────────────────────────────────────────────

// forgeClientError simulates a 4xx HTTP error from Forge (e.g., invalid API key).
type forgeClientError struct{ code int }

func (e *forgeClientError) Error() string   { return "forge client error" }
func (e *forgeClientError) StatusCode() int { return e.code }

func TestBillingGate_4xxErrorDoesNotCountTowardCircuit(t *testing.T) {
	stub := &stubBalanceChecker{err: &forgeClientError{code: http.StatusUnauthorized}}
	g := newTestGate(stub)

	// Make 10 calls with a 401 error — should never open the circuit.
	for i := 0; i < 10; i++ {
		_ = g.AllowDurableWrite(context.Background(), "acc1")
	}

	g.mu.Lock()
	count := g.circuit.errorCount
	open := !g.circuit.openUntil.IsZero()
	g.mu.Unlock()

	if open {
		t.Fatal("circuit opened after 4xx errors; only 5xx/network errors should trip circuit")
	}
	if count != 0 {
		t.Fatalf("circuit error count = %d after 4xx errors; expected 0", count)
	}
}

func TestBillingGate_4xxErrorFailsClosedWithoutCircuitEffect(t *testing.T) {
	stub := &stubBalanceChecker{err: &forgeClientError{code: http.StatusForbidden}}
	g := newTestGate(stub)

	err := g.AllowDurableWrite(context.Background(), "acc1")
	if !errors.Is(err, ErrBillingUnavailable) {
		t.Fatalf("expected ErrBillingUnavailable for 4xx error, got %v", err)
	}

	// Circuit should not have been incremented.
	g.mu.Lock()
	count := g.circuit.errorCount
	g.mu.Unlock()
	if count != 0 {
		t.Fatalf("4xx error incremented circuit count to %d; expected 0", count)
	}
}

func TestBillingGate_5xxErrorCountsTowardCircuit(t *testing.T) {
	stub := &stubBalanceChecker{err: &forgeClientError{code: http.StatusInternalServerError}}
	g := newTestGate(stub)

	_ = g.AllowDurableWrite(context.Background(), "acc1")

	g.mu.Lock()
	count := g.circuit.errorCount
	g.mu.Unlock()
	if count != 1 {
		t.Fatalf("5xx error should increment circuit count; got %d", count)
	}
}

func TestIsForgeServerError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantSrv bool
	}{
		{"nil", nil, false},
		{"network error (no status)", errors.New("connection refused"), true},
		{"401 unauthorized", &forgeClientError{code: 401}, false},
		{"403 forbidden", &forgeClientError{code: 403}, false},
		{"404 not found", &forgeClientError{code: 404}, false},
		{"429 too many requests", &forgeClientError{code: 429}, false},
		{"500 internal server error", &forgeClientError{code: 500}, true},
		{"502 bad gateway", &forgeClientError{code: 502}, true},
		{"503 service unavailable", &forgeClientError{code: 503}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isForgeServerError(tt.err)
			if got != tt.wantSrv {
				t.Errorf("isForgeServerError(%v) = %v, want %v", tt.err, got, tt.wantSrv)
			}
		})
	}
}

// ── Cache size limit ──────────────────────────────────────────────────────────

func TestBillingGate_CacheEvictsWhenFull(t *testing.T) {
	// Use a gate with a very small cache limit.
	stub := &stubBalanceChecker{balance: 1000}
	g := newTestGate(stub)
	g.CacheMaxEntries = 3

	// Fill the cache to the limit.
	for i := 0; i < 3; i++ {
		acct := string(rune('a' + i))
		_ = g.AllowDurableWrite(context.Background(), acct)
	}

	g.mu.Lock()
	sizeBefore := len(g.cache)
	g.mu.Unlock()
	if sizeBefore != 3 {
		t.Fatalf("expected 3 cache entries, got %d", sizeBefore)
	}

	// One more account — triggers eviction.
	_ = g.AllowDurableWrite(context.Background(), "new-account")

	g.mu.Lock()
	sizeAfter := len(g.cache)
	g.mu.Unlock()
	// After eviction, the cache is cleared and only the new entry is present.
	if sizeAfter != 1 {
		t.Fatalf("expected 1 cache entry after eviction, got %d", sizeAfter)
	}
}

func TestBillingGate_CacheBelowLimitNotEvicted(t *testing.T) {
	stub := &stubBalanceChecker{balance: 500}
	g := newTestGate(stub)
	g.CacheMaxEntries = 10

	for i := 0; i < 5; i++ {
		acct := string(rune('a' + i))
		_ = g.AllowDurableWrite(context.Background(), acct)
	}

	// Forge should have been called exactly 5 times.
	if stub.callCount() != 5 {
		t.Fatalf("expected 5 Forge calls, got %d", stub.callCount())
	}

	// Cache should have 5 entries (no eviction).
	g.mu.Lock()
	size := len(g.cache)
	g.mu.Unlock()
	if size != 5 {
		t.Fatalf("expected 5 cache entries, got %d", size)
	}
}

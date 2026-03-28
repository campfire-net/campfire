package hosting

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// ErrInsufficientBalance is returned by BillingGate.AllowDurableWrite when the
// operator's Forge balance is zero or negative. It carries HTTP 402 so callers
// can use x402.ChallengeFromError to write a PaymentChallenge response.
var ErrInsufficientBalance = &billingError{msg: "insufficient balance: top up your Forge account to continue"}

// billingError is a typed error that carries an HTTP status code.
type billingError struct{ msg string }

func (e *billingError) Error() string  { return e.msg }
func (e *billingError) StatusCode() int { return http.StatusPaymentRequired }

// IsInsufficientBalance reports whether err is ErrInsufficientBalance.
func IsInsufficientBalance(err error) bool { return errors.Is(err, ErrInsufficientBalance) }

// BalanceChecker is satisfied by forge.Client (and test doubles).
type BalanceChecker interface {
	Balance(ctx context.Context, accountID string) (int64, error)
}

// balanceCacheEntry holds a cached balance result with its expiry.
type balanceCacheEntry struct {
	balance   int64
	expiresAt time.Time
}

// circuitState tracks consecutive Forge errors for the circuit breaker.
type circuitState struct {
	// errorCount is the number of consecutive errors in the tracking window.
	errorCount int
	// windowStart is when the current error window began.
	windowStart time.Time
	// openUntil is set when the circuit is open; zero means closed.
	openUntil time.Time
}

// BillingGate gates durable message writes on a positive Forge balance.
//
// Balance checks are cached for CacheTTL (default 30s) to avoid a Forge call
// on every message burst. A circuit breaker opens after ErrorThreshold
// consecutive Forge errors within ErrorWindow (default 60s) and stays open for
// CircuitOpenDuration (default 30s); while open the gate fails-open (allows
// writes) so a Forge outage does not block campfire operations.
type BillingGate struct {
	checker BalanceChecker

	// CacheTTL is how long a balance result is considered fresh. Default 30s.
	CacheTTL time.Duration
	// ErrorThreshold is the number of consecutive errors that opens the circuit. Default 3.
	ErrorThreshold int
	// ErrorWindow is the window in which consecutive errors are counted. Default 60s.
	ErrorWindow time.Duration
	// CircuitOpenDuration is how long the circuit stays open after tripping. Default 30s.
	CircuitOpenDuration time.Duration

	// OnCircuitChange is called when the circuit opens or closes. Optional.
	OnCircuitChange func(open bool)

	mu      sync.Mutex
	cache   map[string]balanceCacheEntry
	circuit circuitState

	// now is injectable for testing; defaults to time.Now.
	now func() time.Time
}

// NewBillingGate returns a BillingGate backed by the given BalanceChecker.
func NewBillingGate(checker BalanceChecker) *BillingGate {
	return &BillingGate{
		checker: checker,
		cache:   make(map[string]balanceCacheEntry),
		now:     time.Now,
	}
}

func (g *BillingGate) cacheTTL() time.Duration {
	if g.CacheTTL > 0 {
		return g.CacheTTL
	}
	return 30 * time.Second
}

func (g *BillingGate) errorThreshold() int {
	if g.ErrorThreshold > 0 {
		return g.ErrorThreshold
	}
	return 3
}

func (g *BillingGate) errorWindow() time.Duration {
	if g.ErrorWindow > 0 {
		return g.ErrorWindow
	}
	return 60 * time.Second
}

func (g *BillingGate) circuitOpenDuration() time.Duration {
	if g.CircuitOpenDuration > 0 {
		return g.CircuitOpenDuration
	}
	return 30 * time.Second
}

// isCircuitOpen reports whether the circuit breaker is currently open.
// Must be called with g.mu held.
func (g *BillingGate) isCircuitOpen() bool {
	if g.circuit.openUntil.IsZero() {
		return false
	}
	now := g.now()
	if now.Before(g.circuit.openUntil) {
		return true
	}
	// Circuit has recovered — reset.
	wasOpen := !g.circuit.openUntil.IsZero()
	g.circuit = circuitState{}
	if wasOpen && g.OnCircuitChange != nil {
		// Notify outside the lock to avoid deadlocks.
		// We'll fire it after releasing — store a flag.
		// For simplicity, fire inline since OnCircuitChange is user-supplied and
		// the caller is expected not to re-enter BillingGate.
		g.OnCircuitChange(false)
	}
	return false
}

// recordForgeError updates the circuit breaker state after a Forge error.
// Must be called with g.mu held.
func (g *BillingGate) recordForgeError() {
	now := g.now()
	// Reset the window if too old.
	if !g.circuit.windowStart.IsZero() && now.Sub(g.circuit.windowStart) > g.errorWindow() {
		g.circuit.errorCount = 0
		g.circuit.windowStart = time.Time{}
	}
	if g.circuit.windowStart.IsZero() {
		g.circuit.windowStart = now
	}
	g.circuit.errorCount++
	if g.circuit.errorCount >= g.errorThreshold() && g.circuit.openUntil.IsZero() {
		g.circuit.openUntil = now.Add(g.circuitOpenDuration())
		if g.OnCircuitChange != nil {
			g.OnCircuitChange(true)
		}
	}
}

// AllowDurableWrite returns nil if the operator identified by accountID may
// write a durable message, or ErrInsufficientBalance if their Forge balance is
// zero or negative.
//
// If Forge is unreachable and the circuit breaker has opened, the gate
// fails-open (returns nil) so that a Forge outage does not block campfire.
func (g *BillingGate) AllowDurableWrite(ctx context.Context, accountID string) error {
	now := g.now()

	g.mu.Lock()
	// Fail-open when circuit is open.
	if g.isCircuitOpen() {
		g.mu.Unlock()
		return nil
	}
	// Check cache.
	if entry, ok := g.cache[accountID]; ok && now.Before(entry.expiresAt) {
		balance := entry.balance
		g.mu.Unlock()
		if balance <= 0 {
			return ErrInsufficientBalance
		}
		return nil
	}
	g.mu.Unlock()

	// Cache miss or expired — call Forge.
	balance, err := g.checker.Balance(ctx, accountID)
	if err != nil {
		g.mu.Lock()
		g.recordForgeError()
		open := g.isCircuitOpen()
		g.mu.Unlock()
		if open {
			// Circuit just opened or was already open — fail-open.
			return nil
		}
		// Circuit still closed (not enough errors yet) — fail-open conservatively.
		return nil
	}

	// Success — cache the result and reset error count.
	g.mu.Lock()
	g.cache[accountID] = balanceCacheEntry{
		balance:   balance,
		expiresAt: now.Add(g.cacheTTL()),
	}
	// Reset circuit error count on a successful call.
	if !g.circuit.openUntil.IsZero() || g.circuit.errorCount > 0 {
		wasOpen := !g.circuit.openUntil.IsZero()
		g.circuit = circuitState{}
		if wasOpen && g.OnCircuitChange != nil {
			g.OnCircuitChange(false)
		}
	}
	g.mu.Unlock()

	if balance <= 0 {
		return ErrInsufficientBalance
	}
	return nil
}

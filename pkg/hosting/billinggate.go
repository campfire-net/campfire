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

// ErrBillingUnavailable is returned when Forge is temporarily unreachable but
// the circuit breaker has not yet opened (< ErrorThreshold consecutive errors).
// The gate fails-CLOSED in this state: writes are denied until Forge confirms
// reachability or the circuit opens after sustained failure.
var ErrBillingUnavailable = &billingError{msg: "billing unavailable: Forge is temporarily unreachable"}

// billingError is a typed error that carries an HTTP status code.
type billingError struct{ msg string }

func (e *billingError) Error() string   { return e.msg }
func (e *billingError) StatusCode() int { return http.StatusPaymentRequired }

// IsInsufficientBalance reports whether err is ErrInsufficientBalance.
func IsInsufficientBalance(err error) bool { return errors.Is(err, ErrInsufficientBalance) }

// IsBillingUnavailable reports whether err is ErrBillingUnavailable.
func IsBillingUnavailable(err error) bool { return errors.Is(err, ErrBillingUnavailable) }

// isForgeServerError returns true for 5xx HTTP errors and network/non-HTTP
// errors (which indicate Forge is down), and false for 4xx client errors
// (which indicate the request is wrong, not that Forge is unreachable).
func isForgeServerError(err error) bool {
	if err == nil {
		return false
	}
	// Check if the error carries an HTTP status code.
	type statuser interface{ StatusCode() int }
	var s statuser
	if errors.As(err, &s) {
		code := s.StatusCode()
		// 4xx errors are client errors — the caller is wrong, not Forge.
		if code >= 400 && code < 500 {
			return false
		}
		// 5xx errors mean Forge is having problems.
		return true
	}
	// No HTTP status — treat as a network/transport error (counts toward circuit).
	return true
}

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

// defaultCacheMaxEntries is the maximum number of entries allowed in the
// balance cache before it is cleared to prevent unbounded memory growth.
const defaultCacheMaxEntries = 10000

// BillingGate gates durable message writes on a positive Forge balance.
//
// Balance checks are cached for CacheTTL (default 30s) to avoid a Forge call
// on every message burst. A circuit breaker opens after ErrorThreshold
// consecutive Forge errors within ErrorWindow (default 60s) and stays open for
// CircuitOpenDuration (default 30s); while open the gate fails-open (allows
// writes) so a Forge outage does not block campfire operations.
//
// When Forge returns an error and the circuit is CLOSED (below the threshold),
// the gate fails-CLOSED (returns ErrBillingUnavailable). Only once the circuit
// is OPEN (sustained Forge failure) does the gate fail-open to avoid blocking
// campfire operations during a confirmed Forge outage.
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
	// CacheMaxEntries is the maximum number of cache entries before eviction. Default 10000.
	CacheMaxEntries int

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

func (g *BillingGate) cacheMaxEntries() int {
	if g.CacheMaxEntries > 0 {
		return g.CacheMaxEntries
	}
	return defaultCacheMaxEntries
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
		serverErr := isForgeServerError(err)
		g.mu.Lock()
		if serverErr {
			// Only 5xx/network errors count toward the circuit threshold.
			g.recordForgeError()
		}
		open := g.isCircuitOpen()
		g.mu.Unlock()
		if open {
			// Circuit is OPEN — Forge has been unreachable for the full threshold
			// period. Fail-open to avoid blocking campfire during sustained outage.
			return nil
		}
		// Circuit is CLOSED (not enough server errors yet, or this was a 4xx
		// client error). Fail-CLOSED: a single transient error does not grant
		// free durable writes.
		return ErrBillingUnavailable
	}

	// Success — cache the result and reset error count.
	g.mu.Lock()
	// Evict cache if it has grown too large (prevent unbounded memory growth).
	if len(g.cache) >= g.cacheMaxEntries() {
		g.cache = make(map[string]balanceCacheEntry)
	}
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

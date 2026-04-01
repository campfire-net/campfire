// Package ratelimit provides a decorator that wraps a store.Store to enforce
// per-campfire rate limits, message size limits, and monthly message caps.
//
// All limit state is in-memory. No persistence is provided; counts reset on
// process restart. The monthly cap counter can be loaded and reset via the
// Wrapper's SetMonthlyCount and ResetMonthlyCount methods for integration with
// external metering systems.
package ratelimit

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// BalanceChecker abstracts the Forge balance query for testability.
// Balance returns the account balance in micro-USD.
// Implementations must be safe for concurrent use.
type BalanceChecker interface {
	Balance(ctx context.Context, accountID string) (int64, error)
}

// Sentinel errors returned when a limit is exceeded.
// Each error carries an HTTP status code hint via StatusCode().
var (
	// ErrRateLimited is returned when a campfire exceeds the per-minute message rate.
	// HTTP 429 Too Many Requests.
	ErrRateLimited = &limitError{msg: "rate limit exceeded: too many messages per minute", code: http.StatusTooManyRequests}

	// ErrMessageTooLarge is returned when the message payload exceeds the size limit.
	// HTTP 413 Request Entity Too Large.
	ErrMessageTooLarge = &limitError{msg: "message too large: payload exceeds maximum size", code: http.StatusRequestEntityTooLarge}

	// ErrMonthlyCapExceeded is returned when the campfire has reached its monthly message cap.
	// HTTP 402 Payment Required.
	ErrMonthlyCapExceeded = &limitError{msg: "monthly message cap exceeded", code: http.StatusPaymentRequired}
)

// limitError is a typed error that carries an HTTP status code hint.
type limitError struct {
	msg  string
	code int
}

func (e *limitError) Error() string { return e.msg }

// StatusCode returns the HTTP status code that best represents this error.
func (e *limitError) StatusCode() int { return e.code }

// IsRateLimited reports whether err is ErrRateLimited.
func IsRateLimited(err error) bool { return errors.Is(err, ErrRateLimited) }

// IsMessageTooLarge reports whether err is ErrMessageTooLarge.
func IsMessageTooLarge(err error) bool { return errors.Is(err, ErrMessageTooLarge) }

// IsMonthlyCapExceeded reports whether err is ErrMonthlyCapExceeded.
func IsMonthlyCapExceeded(err error) bool { return errors.Is(err, ErrMonthlyCapExceeded) }

// Config holds the limit parameters for a Wrapper.
type Config struct {
	// MaxMessagesPerMinute is the per-campfire sliding-window rate limit.
	// Default (0) uses DefaultMaxMessagesPerMinute.
	MaxMessagesPerMinute int

	// MaxMessageBytes is the maximum allowed payload size in bytes.
	// Default (0) uses DefaultMaxMessageBytes.
	MaxMessageBytes int

	// MonthlyMessageCap is the free-tier monthly message cap per campfire.
	// Default (0) uses DefaultMonthlyMessageCap.
	MonthlyMessageCap int

	// Now is the clock function used for sliding-window expiry.
	// Defaults to time.Now if nil. Override in tests to control time.
	Now func() time.Time

	// ForgeAccountID is the Forge account ID to check for balance enforcement.
	// When empty (default), Forge balance enforcement is skipped and existing
	// rate limits apply unchanged. Can also be set after construction via
	// Wrapper.SetForgeAccount.
	ForgeAccountID string

	// BalanceChecker is the interface used to query the Forge balance.
	// When nil (default), Forge balance enforcement is skipped.
	// Both ForgeAccountID and BalanceChecker must be non-empty/non-nil to
	// enable balance enforcement. Can also be set after construction via
	// Wrapper.SetForgeAccount.
	BalanceChecker BalanceChecker

	// BalanceRefreshInterval controls how often the cached balance is refreshed.
	// Default (0) uses defaultBalanceRefreshInterval (5 minutes).
	// Override in tests to speed up refresh cycles.
	BalanceRefreshInterval time.Duration
}

const (
	DefaultMaxMessagesPerMinute = 100
	DefaultMaxMessageBytes      = 64 * 1024 // 64 KB
	DefaultMonthlyMessageCap    = 1000
)

// defaultBalanceRefreshInterval is how often the cached Forge balance is
// refreshed in the background. Stale cache is kept on refresh errors (fail-open).
const defaultBalanceRefreshInterval = 5 * time.Minute

func (c *Config) maxPerMinute() int {
	if c.MaxMessagesPerMinute <= 0 {
		return DefaultMaxMessagesPerMinute
	}
	return c.MaxMessagesPerMinute
}

func (c *Config) maxBytes() int {
	if c.MaxMessageBytes <= 0 {
		return DefaultMaxMessageBytes
	}
	return c.MaxMessageBytes
}

func (c *Config) monthlyCap() int {
	if c.MonthlyMessageCap <= 0 {
		return DefaultMonthlyMessageCap
	}
	return c.MonthlyMessageCap
}

func (c *Config) now() func() time.Time {
	if c.Now != nil {
		return c.Now
	}
	return time.Now
}

func (c *Config) balanceRefreshInterval() time.Duration {
	if c.BalanceRefreshInterval > 0 {
		return c.BalanceRefreshInterval
	}
	return defaultBalanceRefreshInterval
}

// campfireState holds per-campfire rate limit state.
type campfireState struct {
	mu           sync.Mutex
	minuteWindow []time.Time // timestamps of messages in the current sliding window
	monthlyCount int
}

// forgeConfig holds the mutable Forge enforcement configuration.
// Protected by Wrapper.balanceMu.
type forgeConfig struct {
	accountID string
	checker   BalanceChecker
}

// Wrapper is a store.Store decorator that enforces rate limits before delegating
// AddMessage calls to the underlying store. All other store methods are passed
// through unchanged.
//
// Wrapper is safe for concurrent use.
type Wrapper struct {
	store.Store
	cfg    Config
	mu     sync.Mutex
	states map[string]*campfireState

	// balanceMu protects all balance cache fields and the forge config.
	balanceMu        sync.RWMutex
	forgeCfg         forgeConfig // mutable; set via SetForgeAccount
	cachedBalance    int64       // micro-USD; refreshed periodically
	balanceRefreshed time.Time   // when the balance was last successfully fetched
	refreshOnce      sync.Once   // ensures the background refresh goroutine is started at most once
	stopRefresh      chan struct{}
}

// New wraps inner with rate limiting, size enforcement, and monthly cap enforcement
// as specified by cfg.
//
// Passing a zero Config uses the default limits (100 msg/min, 64 KB, 1000 msg/mo).
//
// When cfg.ForgeAccountID and cfg.BalanceChecker are both set (or after calling
// SetForgeAccount), AddMessage will also enforce a Forge balance check: messages
// are rejected with ErrMonthlyCapExceeded (HTTP 402) when the cached balance is <= 0.
// The balance is refreshed every cfg.BalanceRefreshInterval (default 5 minutes)
// in the background; on refresh errors the stale cache is kept (fail-open).
func New(inner store.Store, cfg Config) *Wrapper {
	w := &Wrapper{
		Store:       inner,
		cfg:         cfg,
		states:      make(map[string]*campfireState),
		stopRefresh: make(chan struct{}),
		// cachedBalance starts at 1 (positive) so the first call is allowed
		// while the initial refresh runs. The refresh goroutine is started on
		// the first AddMessage call when Forge enforcement is configured.
		cachedBalance: 1,
	}
	// Copy ForgeAccountID / BalanceChecker from Config into forgeCfg so they
	// can be updated later via SetForgeAccount without a race.
	if cfg.ForgeAccountID != "" && cfg.BalanceChecker != nil {
		w.forgeCfg = forgeConfig{
			accountID: cfg.ForgeAccountID,
			checker:   cfg.BalanceChecker,
		}
	}
	return w
}

// SetForgeAccount enables (or updates) Forge balance enforcement on this Wrapper.
// Call this after campfire_init provisions the operator's Forge account ID.
// Both accountID and checker must be non-empty/non-nil; calling with empty
// accountID disables enforcement.
//
// SetForgeAccount is safe to call concurrently with AddMessage.
func (w *Wrapper) SetForgeAccount(accountID string, checker BalanceChecker) {
	w.balanceMu.Lock()
	if accountID != "" && checker != nil {
		w.forgeCfg = forgeConfig{accountID: accountID, checker: checker}
	} else {
		w.forgeCfg = forgeConfig{}
	}
	w.balanceMu.Unlock()
}

// Stop stops the background balance refresh goroutine (if any). Call this when
// the Wrapper is no longer needed to avoid goroutine leaks in long-lived
// processes that create many wrappers (e.g. per-session wrappers).
func (w *Wrapper) Stop() {
	select {
	case <-w.stopRefresh:
		// already stopped
	default:
		close(w.stopRefresh)
	}
}

// startBalanceRefresher starts the background goroutine that periodically
// refreshes the cached Forge balance. It is started at most once per Wrapper
// via sync.Once.
func (w *Wrapper) startBalanceRefresher() {
	w.refreshOnce.Do(func() {
		// Do an initial fetch synchronously so the first non-trivial AddMessage
		// gets a real balance. Errors are ignored (fail-open: start at 1).
		w.refreshBalanceNow()

		go func() {
			ticker := time.NewTicker(w.cfg.balanceRefreshInterval())
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					w.refreshBalanceNow()
				case <-w.stopRefresh:
					return
				}
			}
		}()
	})
}

// refreshBalanceNow fetches the current balance from Forge and updates the cache.
// On error it logs and keeps the stale cache (fail-open).
func (w *Wrapper) refreshBalanceNow() {
	// Read forge config under read lock.
	w.balanceMu.RLock()
	fc := w.forgeCfg
	w.balanceMu.RUnlock()

	if fc.accountID == "" || fc.checker == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	bal, err := fc.checker.Balance(ctx, fc.accountID)
	if err != nil {
		log.Printf("ratelimit: balance refresh for account %s failed: %v (keeping stale cache)", fc.accountID, err)
		return
	}

	w.balanceMu.Lock()
	w.cachedBalance = bal
	w.balanceRefreshed = time.Now()
	w.balanceMu.Unlock()
}

// isCachedBalanceStale reports whether the cached balance is older than the
// configured refresh interval. Caller must not hold balanceMu.
func (w *Wrapper) isCachedBalanceStale() bool {
	w.balanceMu.RLock()
	defer w.balanceMu.RUnlock()
	return time.Since(w.balanceRefreshed) > w.cfg.balanceRefreshInterval()
}

// campfireStateLocked returns the state for the given campfire ID, creating it
// if necessary. Caller must not hold w.mu.
func (w *Wrapper) campfireStateLocked(campfireID string) *campfireState {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.states[campfireID]
	if !ok {
		s = &campfireState{}
		w.states[campfireID] = s
	}
	return s
}

// AddMessage enforces limits before delegating to the underlying store.
//
// Checks are applied in order:
//  1. Payload size (ErrMessageTooLarge / 413)
//  2. Forge balance (ErrMonthlyCapExceeded / 402) — only when a Forge account is configured
//  3. Monthly cap (ErrMonthlyCapExceeded / 402)
//  4. Per-minute rate (ErrRateLimited / 429)
//
// If all checks pass, the call is forwarded to the wrapped store. On success
// the monthly counter and minute window are updated.
func (w *Wrapper) AddMessage(m store.MessageRecord) (bool, error) {
	// 1. Size check — cheap, no locking needed.
	if len(m.Payload) > w.cfg.maxBytes() {
		return false, ErrMessageTooLarge
	}

	// 2. Forge balance check — only when a Forge account is configured.
	w.balanceMu.RLock()
	fc := w.forgeCfg
	w.balanceMu.RUnlock()

	if fc.accountID != "" && fc.checker != nil {
		// Ensure the background refresh goroutine is running.
		w.startBalanceRefresher()

		// If the cache is stale, trigger an async refresh (don't block the caller).
		if w.isCachedBalanceStale() {
			go w.refreshBalanceNow()
		}

		// Read the cached balance under read lock.
		w.balanceMu.RLock()
		bal := w.cachedBalance
		w.balanceMu.RUnlock()

		if bal <= 0 {
			return false, ErrMonthlyCapExceeded
		}
	}

	s := w.campfireStateLocked(m.CampfireID)

	s.mu.Lock()
	defer s.mu.Unlock()

	// 3. Monthly cap check.
	if s.monthlyCount >= w.cfg.monthlyCap() {
		return false, ErrMonthlyCapExceeded
	}

	// 4. Sliding-window per-minute rate check.
	now := w.cfg.now()()
	cutoff := now.Add(-time.Minute)
	// Evict timestamps older than 1 minute.
	fresh := s.minuteWindow[:0]
	for _, t := range s.minuteWindow {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	s.minuteWindow = fresh

	if len(s.minuteWindow) >= w.cfg.maxPerMinute() {
		return false, ErrRateLimited
	}

	// All checks passed — delegate to the inner store.
	ok, err := w.Store.AddMessage(m)
	if err != nil {
		return ok, err
	}

	// Update in-memory state only on success.
	s.minuteWindow = append(s.minuteWindow, now)
	s.monthlyCount++

	return ok, nil
}

// SetMonthlyCount sets the monthly message count for a campfire.
// Use this to seed the counter from an external metering store on startup.
func (w *Wrapper) SetMonthlyCount(campfireID string, count int) {
	s := w.campfireStateLocked(campfireID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.monthlyCount = count
}

// ResetMonthlyCount resets the monthly message counter for a campfire to zero.
// Call this at the start of each billing month.
func (w *Wrapper) ResetMonthlyCount(campfireID string) {
	w.SetMonthlyCount(campfireID, 0)
}

// MonthlyCount returns the current monthly message count for a campfire.
func (w *Wrapper) MonthlyCount(campfireID string) int {
	s := w.campfireStateLocked(campfireID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.monthlyCount
}

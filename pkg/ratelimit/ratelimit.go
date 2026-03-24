// Package ratelimit provides a decorator that wraps a store.Store to enforce
// per-campfire rate limits, message size limits, and monthly message caps.
//
// All limit state is in-memory. No persistence is provided; counts reset on
// process restart. The monthly cap counter can be loaded and reset via the
// Wrapper's SetMonthlyCount and ResetMonthlyCount methods for integration with
// external metering systems.
package ratelimit

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

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
}

const (
	DefaultMaxMessagesPerMinute = 100
	DefaultMaxMessageBytes      = 64 * 1024 // 64 KB
	DefaultMonthlyMessageCap    = 1000
)

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

// campfireState holds per-campfire rate limit state.
type campfireState struct {
	mu           sync.Mutex
	minuteWindow []time.Time // timestamps of messages in the current sliding window
	monthlyCount int
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
}

// New wraps inner with rate limiting, size enforcement, and monthly cap enforcement
// as specified by cfg.
//
// Passing a zero Config uses the default limits (100 msg/min, 64 KB, 1000 msg/mo).
func New(inner store.Store, cfg Config) *Wrapper {
	return &Wrapper{
		Store:  inner,
		cfg:    cfg,
		states: make(map[string]*campfireState),
	}
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
//  2. Monthly cap (ErrMonthlyCapExceeded / 402)
//  3. Per-minute rate (ErrRateLimited / 429)
//
// If all checks pass, the call is forwarded to the wrapped store. On success
// the monthly counter and minute window are updated.
func (w *Wrapper) AddMessage(m store.MessageRecord) (bool, error) {
	// 1. Size check — cheap, no locking needed.
	if len(m.Payload) > w.cfg.maxBytes() {
		return false, ErrMessageTooLarge
	}

	s := w.campfireStateLocked(m.CampfireID)

	s.mu.Lock()
	defer s.mu.Unlock()

	// 2. Monthly cap check.
	if s.monthlyCount >= w.cfg.monthlyCap() {
		return false, ErrMonthlyCapExceeded
	}

	// 3. Sliding-window per-minute rate check.
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

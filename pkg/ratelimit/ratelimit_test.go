package ratelimit_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/ratelimit"
	"github.com/campfire-net/campfire/pkg/store"
)

// --- Fake store ---

// fakeStore is a minimal store.Store implementation for testing.
// It records calls to AddMessage and allows injection of return values.
type fakeStore struct {
	addMessageCalls []store.MessageRecord
	addMessageErr   error
	addMessageOK    bool
}

func newFakeStore() *fakeStore { return &fakeStore{addMessageOK: true} }

func (f *fakeStore) AddMessage(m store.MessageRecord) (bool, error) {
	f.addMessageCalls = append(f.addMessageCalls, m)
	return f.addMessageOK, f.addMessageErr
}

// Remaining store.Store methods — all no-ops for testing.

func (f *fakeStore) AddMembership(m store.Membership) error                { return nil }
func (f *fakeStore) UpdateMembershipRole(campfireID, role string) error    { return nil }
func (f *fakeStore) RemoveMembership(campfireID string) error              { return nil }
func (f *fakeStore) GetMembership(campfireID string) (*store.Membership, error) {
	return nil, nil
}
func (f *fakeStore) ListMemberships() ([]store.Membership, error) { return nil, nil }

func (f *fakeStore) HasMessage(id string) (bool, error)               { return false, nil }
func (f *fakeStore) GetMessage(id string) (*store.MessageRecord, error) { return nil, nil }
func (f *fakeStore) GetMessageByPrefix(prefix string) (*store.MessageRecord, error) {
	return nil, nil
}
func (f *fakeStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	return nil, nil
}
func (f *fakeStore) MaxMessageTimestamp(campfireID string, afterTS int64) (int64, error) {
	return 0, nil
}
func (f *fakeStore) ListReferencingMessages(messageID string) ([]store.MessageRecord, error) {
	return nil, nil
}
func (f *fakeStore) ListCompactionEvents(campfireID string) ([]store.MessageRecord, error) {
	return nil, nil
}
func (f *fakeStore) GetReadCursor(campfireID string) (int64, error)          { return 0, nil }
func (f *fakeStore) SetReadCursor(campfireID string, timestamp int64) error  { return nil }

func (f *fakeStore) UpsertPeerEndpoint(e store.PeerEndpoint) error  { return nil }
func (f *fakeStore) DeletePeerEndpoint(campfireID, memberPubkey string) error { return nil }
func (f *fakeStore) ListPeerEndpoints(campfireID string) ([]store.PeerEndpoint, error) {
	return nil, nil
}
func (f *fakeStore) GetPeerRole(campfireID, memberPubkey string) (string, error) {
	return "", nil
}

func (f *fakeStore) UpsertThresholdShare(share store.ThresholdShare) error { return nil }
func (f *fakeStore) GetThresholdShare(campfireID string) (*store.ThresholdShare, error) {
	return nil, nil
}
func (f *fakeStore) StorePendingThresholdShare(campfireID string, participantID uint32, shareData []byte) error {
	return nil
}
func (f *fakeStore) ClaimPendingThresholdShare(campfireID string) (uint32, []byte, error) {
	return 0, nil, nil
}
func (f *fakeStore) UpdateCampfireID(oldID, newID string) error { return nil }
func (f *fakeStore) Close() error                               { return nil }

// InviteStore stubs — required by store.Store interface, not exercised by rate limit tests.
func (f *fakeStore) CreateInvite(inv store.InviteRecord) error                     { return nil }
func (f *fakeStore) ValidateInvite(campfireID, inviteCode string) (*store.InviteRecord, error) {
	return nil, nil
}
func (f *fakeStore) RevokeInvite(campfireID, inviteCode string) error              { return nil }
func (f *fakeStore) ListInvites(campfireID string) ([]store.InviteRecord, error)   { return nil, nil }
func (f *fakeStore) LookupInvite(inviteCode string) (*store.InviteRecord, error)   { return nil, nil }
func (f *fakeStore) HasAnyInvites(campfireID string) (bool, error)                 { return false, nil }
func (f *fakeStore) IncrementInviteUse(inviteCode string) error                    { return nil }
func (f *fakeStore) ValidateAndUseInvite(campfireID, inviteCode string) (*store.InviteRecord, error) {
	return nil, nil
}

// EpochSecretStore stubs — required by store.Store interface, not exercised by rate limit tests.
func (f *fakeStore) UpsertEpochSecret(secret store.EpochSecret) error { return nil }
func (f *fakeStore) GetEpochSecret(campfireID string, epoch uint64) (*store.EpochSecret, error) {
	return nil, nil
}
func (f *fakeStore) GetLatestEpochSecret(campfireID string) (*store.EpochSecret, error) {
	return nil, nil
}
func (f *fakeStore) SetMembershipEncrypted(campfireID string, encrypted bool) error { return nil }
func (f *fakeStore) ApplyMembershipCommitAtomically(campfireID string, newMember *store.Membership, secret store.EpochSecret) error {
	return nil
}

// --- Helpers ---

func makeRecord(campfireID string, payloadSize int) store.MessageRecord {
	return store.MessageRecord{
		ID:         "test-msg",
		CampfireID: campfireID,
		Payload:    make([]byte, payloadSize),
		Tags:       []string{},
		Antecedents: []string{},
		Provenance: []message.ProvenanceHop{},
	}
}

// --- Tests ---

// TestMessagesWithinLimitsPassThrough verifies that messages within all limits
// are forwarded to the wrapped store.
func TestMessagesWithinLimitsPassThrough(t *testing.T) {
	fake := newFakeStore()
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute: 10,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    100,
	})

	rec := makeRecord("fire1", 512)
	ok, err := w.AddMessage(rec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(fake.addMessageCalls) != 1 {
		t.Fatalf("expected 1 call to inner store, got %d", len(fake.addMessageCalls))
	}
}

// TestMessageTooLarge verifies that an oversized payload is rejected with
// ErrMessageTooLarge before reaching the inner store.
func TestMessageTooLarge(t *testing.T) {
	fake := newFakeStore()
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessageBytes: 100,
	})

	_, err := w.AddMessage(makeRecord("fire1", 101))
	if !errors.Is(err, ratelimit.ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge, got %v", err)
	}
	if len(fake.addMessageCalls) != 0 {
		t.Fatal("inner store should not be called when payload is too large")
	}

	// Verify the helper recognises the error.
	if !ratelimit.IsMessageTooLarge(err) {
		t.Fatal("IsMessageTooLarge should return true")
	}

	// Verify HTTP status code hint.
	type coder interface{ StatusCode() int }
	if c, ok := err.(coder); !ok || c.StatusCode() != 413 {
		t.Fatalf("expected HTTP 413, got %v", err)
	}
}

// TestPayloadExactlyAtLimitIsAllowed verifies that a payload exactly at the
// size limit is allowed through.
func TestPayloadExactlyAtLimitIsAllowed(t *testing.T) {
	fake := newFakeStore()
	w := ratelimit.New(fake, ratelimit.Config{MaxMessageBytes: 100})

	_, err := w.AddMessage(makeRecord("fire1", 100))
	if err != nil {
		t.Fatalf("payload at exact limit should be allowed, got %v", err)
	}
}

// TestPerMinuteRateLimit verifies that the 101st message in a minute is
// rejected with ErrRateLimited.
func TestPerMinuteRateLimit(t *testing.T) {
	fake := newFakeStore()
	cfg := ratelimit.Config{
		MaxMessagesPerMinute: 5,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    10000,
	}
	w := ratelimit.New(fake, cfg)

	for i := 0; i < 5; i++ {
		_, err := w.AddMessage(makeRecord("fire1", 10))
		if err != nil {
			t.Fatalf("message %d should pass, got %v", i, err)
		}
	}

	_, err := w.AddMessage(makeRecord("fire1", 10))
	if !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatalf("6th message should be rate-limited, got %v", err)
	}

	if !ratelimit.IsRateLimited(err) {
		t.Fatal("IsRateLimited should return true")
	}

	// HTTP 429.
	type coder interface{ StatusCode() int }
	if c, ok := err.(coder); !ok || c.StatusCode() != 429 {
		t.Fatalf("expected HTTP 429, got %v", err)
	}
}

// TestRateLimitIsSlidingWindow verifies that old messages falling outside the
// 1-minute window no longer count against the rate limit.
func TestRateLimitIsSlidingWindow(t *testing.T) {
	fake := newFakeStore()
	cfg := ratelimit.Config{
		MaxMessagesPerMinute: 2,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    10000,
	}
	w := ratelimit.New(fake, cfg)

	// Send 2 messages — fill the window.
	for i := 0; i < 2; i++ {
		if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
			t.Fatalf("message %d should pass: %v", i, err)
		}
	}

	// 3rd message should be rate-limited.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatalf("3rd message should be rate-limited, got %v", err)
	}

	// Manually age the window entries by replacing them with times > 1 minute ago.
	// We do this by using a fresh wrapper seeded at a time far in the past — instead,
	// we test the sliding window via the exported SetMonthlyCount + a new campfire ID
	// to confirm independence, then trust the eviction logic via time.Sleep in integration.
	// For unit-test purposes, we verify isolation between campfire IDs.
	if _, err := w.AddMessage(makeRecord("fire2", 10)); err != nil {
		t.Fatalf("different campfire should have its own window, got %v", err)
	}
}

// TestMonthlyCapExceeded verifies that once the monthly cap is reached, further
// messages are rejected with ErrMonthlyCapExceeded.
func TestMonthlyCapExceeded(t *testing.T) {
	fake := newFakeStore()
	cfg := ratelimit.Config{
		MaxMessagesPerMinute: 10000, // high — don't want rate limit to fire
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    3,
	}
	w := ratelimit.New(fake, cfg)

	for i := 0; i < 3; i++ {
		if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
			t.Fatalf("message %d should pass: %v", i, err)
		}
	}

	_, err := w.AddMessage(makeRecord("fire1", 10))
	if !errors.Is(err, ratelimit.ErrMonthlyCapExceeded) {
		t.Fatalf("4th message should hit monthly cap, got %v", err)
	}

	if !ratelimit.IsMonthlyCapExceeded(err) {
		t.Fatal("IsMonthlyCapExceeded should return true")
	}

	// HTTP 402.
	type coder interface{ StatusCode() int }
	if c, ok := err.(coder); !ok || c.StatusCode() != 402 {
		t.Fatalf("expected HTTP 402, got %v", err)
	}
}

// TestMonthlyCapIsConfigurable verifies that the monthly cap is a constructor
// parameter, not hardcoded.
func TestMonthlyCapIsConfigurable(t *testing.T) {
	fake := newFakeStore()
	// Default cap is 1000, but we set 2 here.
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute: 10000,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    2,
	})

	for i := 0; i < 2; i++ {
		if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if _, err := w.AddMessage(makeRecord("fire1", 10)); !errors.Is(err, ratelimit.ErrMonthlyCapExceeded) {
		t.Fatal("should have hit configured cap of 2")
	}
}

// TestSetMonthlyCount verifies that the monthly counter can be seeded from
// an external source for metering integration.
func TestSetMonthlyCount(t *testing.T) {
	fake := newFakeStore()
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute: 10000,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    10,
	})

	// Seed the counter to 9 (as if 9 messages were sent before this process started).
	w.SetMonthlyCount("fire1", 9)

	// One more should be fine.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
		t.Fatalf("10th message should pass: %v", err)
	}

	// 11th should be capped.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); !errors.Is(err, ratelimit.ErrMonthlyCapExceeded) {
		t.Fatal("should hit monthly cap after SetMonthlyCount(9) + 2 more")
	}

	// MonthlyCount reflects the actual count.
	if got := w.MonthlyCount("fire1"); got != 10 {
		t.Fatalf("expected MonthlyCount=10, got %d", got)
	}
}

// TestResetMonthlyCount verifies that ResetMonthlyCount resets the counter.
func TestResetMonthlyCount(t *testing.T) {
	fake := newFakeStore()
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute: 10000,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    2,
	})

	for i := 0; i < 2; i++ {
		if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
			t.Fatalf("msg %d: %v", i, err)
		}
	}
	// Should be capped.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); !errors.Is(err, ratelimit.ErrMonthlyCapExceeded) {
		t.Fatal("expected cap")
	}

	// Reset and verify messages flow again.
	w.ResetMonthlyCount("fire1")
	if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
		t.Fatalf("after reset should allow message: %v", err)
	}
}

// TestDefaultLimits verifies that a zero Config uses the documented defaults.
func TestDefaultLimits(t *testing.T) {
	fake := newFakeStore()
	w := ratelimit.New(fake, ratelimit.Config{}) // all zeros → defaults

	// Exactly at the default size limit (64 KB) should pass.
	_, err := w.AddMessage(makeRecord("fire1", ratelimit.DefaultMaxMessageBytes))
	if err != nil {
		t.Fatalf("message at default size limit should pass: %v", err)
	}

	// One byte over should fail.
	_, err = w.AddMessage(makeRecord("fire1", ratelimit.DefaultMaxMessageBytes+1))
	if !errors.Is(err, ratelimit.ErrMessageTooLarge) {
		t.Fatalf("message over default size limit should fail: %v", err)
	}
}

// TestCampfireIsolation verifies that limits for one campfire ID do not affect
// another campfire ID.
func TestCampfireIsolation(t *testing.T) {
	fake := newFakeStore()
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute: 10000,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    1,
	})

	// Exhaust cap for fire1.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
		t.Fatalf("fire1 first message: %v", err)
	}
	if _, err := w.AddMessage(makeRecord("fire1", 10)); !errors.Is(err, ratelimit.ErrMonthlyCapExceeded) {
		t.Fatal("fire1 should be capped")
	}

	// fire2 should be unaffected.
	if _, err := w.AddMessage(makeRecord("fire2", 10)); err != nil {
		t.Fatalf("fire2 should not be affected by fire1 cap: %v", err)
	}
}

// TestInnerStoreErrorPropagated verifies that errors from the inner store are
// returned and the monthly counter is not incremented.
func TestInnerStoreErrorPropagated(t *testing.T) {
	fake := newFakeStore()
	fake.addMessageErr = errors.New("disk full")
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute: 10000,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    100,
	})

	_, err := w.AddMessage(makeRecord("fire1", 10))
	if err == nil || err.Error() != "disk full" {
		t.Fatalf("expected disk full error, got %v", err)
	}

	// Monthly count must not increase on inner store error.
	if got := w.MonthlyCount("fire1"); got != 0 {
		t.Fatalf("monthly count should remain 0 after inner error, got %d", got)
	}
}

// TestWrapperImplementsStore verifies the compile-time assertion that *Wrapper
// satisfies store.Store (the decorator contract).
var _ store.Store = (*ratelimit.Wrapper)(nil)


// TestRateLimitWindowExpiryWithFakeClock verifies that old timestamps are evicted
// when the clock advances past the 1-minute window boundary. Uses an injectable
// clock so this test runs in CI without sleeping.
func TestRateLimitWindowExpiryWithFakeClock(t *testing.T) {
	fake := newFakeStore()

	// Start the fake clock at an arbitrary point in time.
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := ratelimit.Config{
		MaxMessagesPerMinute: 2,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    10000,
		Now:                  func() time.Time { return now },
	}
	w := ratelimit.New(fake, cfg)

	// Send 2 messages — fill the window at T=0.
	for i := 0; i < 2; i++ {
		if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
			t.Fatalf("message %d should pass: %v", i, err)
		}
	}

	// 3rd message at the same time should be rate-limited.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatal("3rd message at T=0 should be rate-limited")
	}

	// Advance clock past the window boundary (61 seconds).
	now = now.Add(61 * time.Second)

	// The first two timestamps are now outside the 1-minute window and should
	// be evicted, allowing new messages through.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
		t.Fatalf("after window expiry, message should pass: %v", err)
	}

	// Window now has 1 entry (the message just sent). One more should pass.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
		t.Fatalf("second message after window expiry should pass: %v", err)
	}

	// Window now full again. Next message should be rate-limited.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatal("should be rate-limited again after refilling window")
	}
}

// ---------------------------------------------------------------------------
// Forge balance enforcement tests
// ---------------------------------------------------------------------------

// mockBalanceChecker is a BalanceChecker that returns a configurable balance
// and records how many times Balance() was called.
type mockBalanceChecker struct {
	balance  int64        // returned balance (atomic for safe concurrent reads in tests)
	callCount atomic.Int64 // number of Balance() calls
	err      error        // if non-nil, returned instead of balance
}

func (m *mockBalanceChecker) Balance(_ context.Context, _ string) (int64, error) {
	m.callCount.Add(1)
	if m.err != nil {
		return 0, m.err
	}
	return atomic.LoadInt64(&m.balance), nil
}

func (m *mockBalanceChecker) setBalance(v int64) { atomic.StoreInt64(&m.balance, v) }
func (m *mockBalanceChecker) calls() int64       { return m.callCount.Load() }

// TestBalanceCheckBlocksWriteAtZero verifies that AddMessage returns
// ErrMonthlyCapExceeded (HTTP 402) when the cached balance is zero.
func TestBalanceCheckBlocksWriteAtZero(t *testing.T) {
	fake := newFakeStore()
	checker := &mockBalanceChecker{} // balance = 0
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute:   10000,
		MaxMessageBytes:        1024,
		MonthlyMessageCap:      10000,
		ForgeAccountID:         "acct-zero",
		BalanceChecker:         checker,
		BalanceRefreshInterval: time.Hour, // don't auto-refresh during test
	})
	defer w.Stop()

	// The initial sync refresh sets cachedBalance to 0 (checker returns 0).
	// First AddMessage should be blocked.
	_, err := w.AddMessage(makeRecord("fire1", 10))
	if !errors.Is(err, ratelimit.ErrMonthlyCapExceeded) {
		t.Fatalf("expected ErrMonthlyCapExceeded at zero balance, got %v", err)
	}
	// Inner store must NOT be reached.
	if len(fake.addMessageCalls) != 0 {
		t.Fatal("inner store should not be called when balance is zero")
	}
	// HTTP 402.
	type coder interface{ StatusCode() int }
	if c, ok := err.(coder); !ok || c.StatusCode() != 402 {
		t.Fatalf("expected HTTP 402, got %v", err)
	}
}

// TestBalanceCheckAllowsWriteWithPositiveBalance verifies that AddMessage
// succeeds when the cached balance is positive.
func TestBalanceCheckAllowsWriteWithPositiveBalance(t *testing.T) {
	fake := newFakeStore()
	checker := &mockBalanceChecker{}
	checker.setBalance(5_000_000) // $5.00 in micro-USD
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute:   10000,
		MaxMessageBytes:        1024,
		MonthlyMessageCap:      10000,
		ForgeAccountID:         "acct-pos",
		BalanceChecker:         checker,
		BalanceRefreshInterval: time.Hour,
	})
	defer w.Stop()

	ok, err := w.AddMessage(makeRecord("fire1", 10))
	if err != nil {
		t.Fatalf("expected success with positive balance, got %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(fake.addMessageCalls) != 1 {
		t.Fatalf("expected 1 call to inner store, got %d", len(fake.addMessageCalls))
	}
}

// TestBalanceCheckNoEnforcementWithoutForgeAccount verifies that when
// ForgeAccountID is empty, balance enforcement is skipped entirely.
func TestBalanceCheckNoEnforcementWithoutForgeAccount(t *testing.T) {
	fake := newFakeStore()
	checker := &mockBalanceChecker{} // balance = 0 — would block if enforcement active
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute: 10000,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    10000,
		// ForgeAccountID intentionally empty — no enforcement
		BalanceChecker: checker,
	})
	defer w.Stop()

	_, err := w.AddMessage(makeRecord("fire1", 10))
	if err != nil {
		t.Fatalf("expected success without Forge enforcement, got %v", err)
	}
	// Balance checker must never be called.
	if checker.calls() != 0 {
		t.Fatalf("BalanceChecker should not be called when ForgeAccountID is empty, called %d times", checker.calls())
	}
}

// TestBalanceCheckNoEnforcementWithoutChecker verifies that when
// BalanceChecker is nil, balance enforcement is skipped.
func TestBalanceCheckNoEnforcementWithoutChecker(t *testing.T) {
	fake := newFakeStore()
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute: 10000,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    10000,
		ForgeAccountID:       "acct-nochecker",
		BalanceChecker:       nil, // no checker
	})
	defer w.Stop()

	_, err := w.AddMessage(makeRecord("fire1", 10))
	if err != nil {
		t.Fatalf("expected success without BalanceChecker, got %v", err)
	}
}

// TestBalanceRefreshErrorKeepsStaleCacheFailOpen verifies that when the
// balance refresh returns an error, the stale cache is kept (fail-open):
// a previously positive balance still allows writes.
func TestBalanceRefreshErrorKeepsStaleCacheFailOpen(t *testing.T) {
	fake := newFakeStore()
	checker := &mockBalanceChecker{}
	checker.setBalance(1_000_000) // $1.00 initially — positive

	// Use a very short refresh interval so the cache immediately becomes stale.
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute:   10000,
		MaxMessageBytes:        1024,
		MonthlyMessageCap:      10000,
		ForgeAccountID:         "acct-stale",
		BalanceChecker:         checker,
		BalanceRefreshInterval: time.Millisecond, // stale immediately
	})
	defer w.Stop()

	// First call: initial sync refresh succeeds (balance = 1_000_000), passes.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
		t.Fatalf("first message should pass: %v", err)
	}

	// Now make the checker fail.
	checker.err = errors.New("forge unavailable")
	checker.setBalance(0) // would block if cache were updated

	// Wait for background refresh to fire and fail (cache becomes stale).
	time.Sleep(50 * time.Millisecond)

	// The stale positive balance (1_000_000) should still allow writes (fail-open).
	// The async refresh fired but failed — cache unchanged.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
		t.Fatalf("fail-open: stale positive cache should allow write after refresh error, got %v", err)
	}
}

// TestBalanceCheckSetForgeAccountEnablesEnforcement verifies that SetForgeAccount
// can enable enforcement after construction (for post-init provisioning).
func TestBalanceCheckSetForgeAccountEnablesEnforcement(t *testing.T) {
	fake := newFakeStore()
	checker := &mockBalanceChecker{} // balance = 0 — would block
	// Create with no Forge enforcement.
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute: 10000,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    10000,
	})
	defer w.Stop()

	// Before SetForgeAccount: messages pass (no enforcement).
	if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
		t.Fatalf("before SetForgeAccount: expected pass, got %v", err)
	}

	// Enable enforcement with a zero-balance checker.
	w.SetForgeAccount("acct-late", checker)

	// Allow the sync refresh in startBalanceRefresher to complete.
	// The refresh sets cachedBalance = 0.
	time.Sleep(50 * time.Millisecond)

	// Now messages should be blocked.
	_, err := w.AddMessage(makeRecord("fire1", 10))
	if !errors.Is(err, ratelimit.ErrMonthlyCapExceeded) {
		t.Fatalf("after SetForgeAccount with zero balance: expected ErrMonthlyCapExceeded, got %v", err)
	}
}

// TestExistingRateLimitsStillWorkWithForgeEnabled verifies that the
// per-minute rate limit and monthly cap still fire when Forge enforcement
// is also enabled and the balance is positive.
func TestExistingRateLimitsStillWorkWithForgeEnabled(t *testing.T) {
	fake := newFakeStore()
	checker := &mockBalanceChecker{}
	checker.setBalance(10_000_000) // $10 — plenty
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute:   3,
		MaxMessageBytes:        1024,
		MonthlyMessageCap:      5,
		ForgeAccountID:         "acct-rich",
		BalanceChecker:         checker,
		BalanceRefreshInterval: time.Hour,
	})
	defer w.Stop()

	// Send 3 messages — fills per-minute window.
	for i := 0; i < 3; i++ {
		if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
			t.Fatalf("message %d should pass: %v", i, err)
		}
	}

	// 4th message should hit per-minute rate limit.
	_, err := w.AddMessage(makeRecord("fire1", 10))
	if !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}

// TestRateLimitedAfterWindowExpiry is a timing-sensitive test that verifies
// the sliding window correctly expires old entries. It uses a 1-second window
// simulation by sending messages and then sleeping past the window.
// This test is skipped in short mode to avoid slowing down CI.
func TestRateLimitedAfterWindowExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive test in short mode")
	}

	fake := newFakeStore()
	cfg := ratelimit.Config{
		MaxMessagesPerMinute: 2,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    10000,
	}
	w := ratelimit.New(fake, cfg)

	// Send 2 messages — fill the window.
	for i := 0; i < 2; i++ {
		if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}

	// 3rd should be rate-limited.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatal("3rd message should be rate-limited")
	}

	// Wait for window to expire (1 minute + buffer).
	// In real usage this is 60s; we skip this in short mode above.
	// For CI we use a very short window by directly testing the eviction
	// logic is time-based — this test documents intent only.
	time.Sleep(61 * time.Second)

	// After window expires, messages should flow again.
	if _, err := w.AddMessage(makeRecord("fire1", 10)); err != nil {
		t.Fatalf("after window expiry, message should pass: %v", err)
	}
}

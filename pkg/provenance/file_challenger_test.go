package provenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFileChallenger_ActiveChallengePersistedAcrossRestart is the regression test for
// campfire-agent-ql4: active challenges must survive process restarts.
//
// Before the fix, NewChallenger() was ephemeral — each CLI invocation started with an
// empty active map and fresh rate-limit counters. This meant:
//   - A challenge issued in one invocation was invisible to the next invocation's ValidateResponse.
//   - Rate limits reset on every restart, allowing unlimited challenges to the same target.
//
// This test simulates a restart by discarding the first FileChallenger, creating a new
// one from the same path, and verifying the challenge is still active and answerable.
func TestFileChallenger_ActiveChallengePersistedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "challenger.json")

	now := time.Now()

	// --- First "process invocation" ---
	fc1, err := NewFileChallenger(path)
	if err != nil {
		t.Fatalf("NewFileChallenger (first open) failed: %v", err)
	}

	ch, err := fc1.IssueChallenge("msg-persist-001", testInitiatorKey, testTargetKey, testCallback, now)
	if err != nil {
		t.Fatalf("IssueChallenge failed: %v", err)
	}

	// The state file must exist on disk.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		t.Fatal("challenger.json was not created on disk after IssueChallenge")
	}

	// --- Simulate process restart: discard fc1, open a new FileChallenger ---
	// fc1 goes out of scope here (no Close method — GC handles it).
	fc2, err := NewFileChallenger(path)
	if err != nil {
		t.Fatalf("NewFileChallenger (restart) failed: %v", err)
	}

	// The challenge issued by fc1 MUST still be active in fc2.
	resp := validResponse(ch)
	matched, err := fc2.ValidateResponse(resp, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("ValidateResponse after restart failed — active challenge was lost: %v", err)
	}
	if matched.ID != ch.ID {
		t.Errorf("wrong challenge ID after restart: want %q, got %q", ch.ID, matched.ID)
	}
}

// TestFileChallenger_RateLimitPersistedAcrossRestart verifies that rate-limit
// timestamps survive process restarts.
//
// Before the fix, rate limits reset on every invocation, so a target could receive
// unlimited challenges simply by the challenger cycling through CLI invocations.
func TestFileChallenger_RateLimitPersistedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "challenger.json")

	now := time.Now()

	// First invocation: fill the rate limit for testTargetKey.
	fc1, err := NewFileChallenger(path)
	if err != nil {
		t.Fatalf("NewFileChallenger failed: %v", err)
	}
	for i := 0; i < challengeRateMax; i++ {
		id := "msg-rl-persist-" + string(rune('a'+i))
		_, err := fc1.IssueChallenge(id, testInitiatorKey, testTargetKey, testCallback, now)
		if err != nil {
			t.Fatalf("challenge %d: unexpected error: %v", i, err)
		}
	}

	// Verify the limit is enforced in the first invocation.
	_, err = fc1.IssueChallenge("msg-rl-persist-overflow", testInitiatorKey, testTargetKey, testCallback, now)
	if err != ErrRateLimitExceeded {
		t.Fatalf("expected ErrRateLimitExceeded in first invocation, got %v", err)
	}

	// Simulate restart.
	fc2, err := NewFileChallenger(path)
	if err != nil {
		t.Fatalf("NewFileChallenger (restart) failed: %v", err)
	}

	// Rate limit MUST still be enforced after restart — timestamps were persisted.
	_, err = fc2.IssueChallenge("msg-rl-persist-after-restart", testInitiatorKey, testTargetKey, testCallback, now)
	if err != ErrRateLimitExceeded {
		t.Errorf("rate limit was NOT enforced after restart — timestamps lost on restart (got %v)", err)
	}
}

// TestFileChallenger_ConsumedChallengeNotResurrectedAfterRestart verifies that
// once a challenge is answered (consumed), it does not reappear after a restart.
func TestFileChallenger_ConsumedChallengeNotResurrectedAfterRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "challenger.json")

	now := time.Now()

	// First invocation: issue and consume a challenge.
	fc1, err := NewFileChallenger(path)
	if err != nil {
		t.Fatalf("NewFileChallenger failed: %v", err)
	}

	ch, err := fc1.IssueChallenge("msg-consumed-001", testInitiatorKey, testTargetKey, testCallback, now)
	if err != nil {
		t.Fatalf("IssueChallenge failed: %v", err)
	}

	resp := validResponse(ch)
	if _, err := fc1.ValidateResponse(resp, now.Add(10*time.Second)); err != nil {
		t.Fatalf("ValidateResponse failed: %v", err)
	}

	// Simulate restart.
	fc2, err := NewFileChallenger(path)
	if err != nil {
		t.Fatalf("NewFileChallenger (restart) failed: %v", err)
	}

	// The consumed challenge MUST NOT be active after restart.
	resp2 := validResponse(ch)
	_, err = fc2.ValidateResponse(resp2, now.Add(20*time.Second))
	if err != ErrChallengeNotFound {
		t.Errorf("expected ErrChallengeNotFound for consumed challenge after restart, got %v", err)
	}
}

// TestFileChallenger_EmptyFile creates a FileChallenger on a non-existent path —
// should succeed with an empty state.
func TestFileChallenger_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	fc, err := NewFileChallenger(path)
	if err != nil {
		t.Fatalf("NewFileChallenger on non-existent file should succeed, got: %v", err)
	}

	// Should be able to issue a challenge immediately.
	now := time.Now()
	_, err = fc.IssueChallenge("msg-empty-001", testInitiatorKey, testTargetKey, testCallback, now)
	if err != nil {
		t.Errorf("IssueChallenge on empty store failed: %v", err)
	}
}

// TestFileChallenger_RateLimitWindowExpiryAfterRestart verifies that rate-limit
// timestamps loaded from disk are still subject to window expiry.
// Timestamps outside the rate window should not count — even after a restart.
func TestFileChallenger_RateLimitWindowExpiryAfterRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "challenger.json")

	// First invocation: issue max challenges with past timestamps (outside the window).
	past := time.Now().Add(-2 * challengeRateWindow)
	fc1, err := NewFileChallenger(path)
	if err != nil {
		t.Fatalf("NewFileChallenger failed: %v", err)
	}
	for i := 0; i < challengeRateMax; i++ {
		id := "msg-old-" + string(rune('a'+i))
		_, err := fc1.IssueChallenge(id, testInitiatorKey, testTargetKey, testCallback, past)
		if err != nil {
			t.Fatalf("past challenge %d: unexpected: %v", i, err)
		}
	}

	// Simulate restart.
	fc2, err := NewFileChallenger(path)
	if err != nil {
		t.Fatalf("NewFileChallenger (restart) failed: %v", err)
	}

	// New challenge issued now should succeed — old timestamps expired.
	now := time.Now()
	_, err = fc2.IssueChallenge("msg-after-expiry-001", testInitiatorKey, testTargetKey, testCallback, now)
	if err != nil {
		t.Errorf("expected no error after rate window expiry (restart), got: %v", err)
	}
}

package provenance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// challengerState is the on-disk format for the Challenger.
type challengerState struct {
	Active           map[string]*Challenge       `json:"active"`
	TargetTimestamps map[string][]time.Time      `json:"target_timestamps"`
}

// FileChallenger wraps Challenger and persists its state (active challenges and
// rate-limit timestamps) to disk on every mutation.
//
// This solves the ephemeral Challenger problem: without persistence, process
// restarts reset rate limits and lose active challenges, allowing an agent to
// be challenged unlimited times across restarts.
//
// The approach mirrors FileStore: atomic writes via temp-file rename, JSON on disk.
type FileChallenger struct {
	mu    sync.Mutex
	path  string
	inner *Challenger
}

// NewFileChallenger loads an existing Challenger state from path (or creates an
// empty state if the file does not exist) and wraps it with automatic persistence.
// Every mutation (IssueChallenge, ValidateResponse) flushes to disk atomically.
func NewFileChallenger(path string) (*FileChallenger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	inner := NewChallenger()

	// Load existing state if the file is present.
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(data) > 0 {
		var state challengerState
		if jsonErr := json.Unmarshal(data, &state); jsonErr != nil {
			return nil, jsonErr
		}
		inner.mu.Lock()
		if state.Active != nil {
			inner.active = state.Active
		}
		if state.TargetTimestamps != nil {
			inner.targetTimestamps = state.TargetTimestamps
		}
		inner.mu.Unlock()
	}

	return &FileChallenger{path: path, inner: inner}, nil
}

// IssueChallenge creates and registers a new challenge, then persists state to disk.
// Returns ErrRateLimitExceeded if the target has exceeded the rate limit.
func (fc *FileChallenger) IssueChallenge(id, initiatorKey, targetKey, callbackCampfire string, now time.Time) (*Challenge, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	ch, err := fc.inner.IssueChallenge(id, initiatorKey, targetKey, callbackCampfire, now)
	if err != nil {
		return nil, err
	}

	if flushErr := fc.flush(); flushErr != nil {
		// Roll back: remove the challenge we just added.
		fc.inner.mu.Lock()
		delete(fc.inner.active, id)
		// Trim the last timestamp entry for targetKey.
		ts := fc.inner.targetTimestamps[targetKey]
		if len(ts) > 0 {
			ts = ts[:len(ts)-1]
		}
		if len(ts) == 0 {
			delete(fc.inner.targetTimestamps, targetKey)
		} else {
			fc.inner.targetTimestamps[targetKey] = ts
		}
		fc.inner.mu.Unlock()
		return nil, flushErr
	}

	return ch, nil
}

// ValidateResponse validates a challenge response and persists the updated state
// (challenge consumed) to disk.
func (fc *FileChallenger) ValidateResponse(resp *ChallengeResponse, now time.Time) (*Challenge, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	ch, err := fc.inner.ValidateResponse(resp, now)
	if err != nil {
		return nil, err
	}

	// Challenge consumed — flush updated state (challenge removed from active map).
	if flushErr := fc.flush(); flushErr != nil {
		// Re-insert the consumed challenge so in-memory state stays consistent.
		fc.inner.mu.Lock()
		fc.inner.active[ch.ID] = ch
		fc.inner.mu.Unlock()
		return nil, flushErr
	}

	return ch, nil
}

// flush writes challenger state to disk atomically.
// Caller must hold fc.mu.
func (fc *FileChallenger) flush() error {
	fc.inner.mu.Lock()

	// Snapshot active challenges.
	active := make(map[string]*Challenge, len(fc.inner.active))
	for k, v := range fc.inner.active {
		active[k] = v
	}

	// Snapshot rate-limit timestamps.
	ts := make(map[string][]time.Time, len(fc.inner.targetTimestamps))
	for k, v := range fc.inner.targetTimestamps {
		cp := make([]time.Time, len(v))
		copy(cp, v)
		ts[k] = cp
	}

	fc.inner.mu.Unlock()

	state := challengerState{
		Active:           active,
		TargetTimestamps: ts,
	}

	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	// Atomic write: write to temp file then rename.
	tmp := fc.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, fc.path)
}

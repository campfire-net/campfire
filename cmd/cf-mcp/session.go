package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// idleTimeout is the duration after which an inactive session closes its store.
const idleTimeout = 10 * time.Minute

// Session represents one agent's isolated state: a per-session directory
// containing identity.json and store.db, with the store held open in memory.
// The store opens once when the session is created and remains open until the
// session goes idle (no MCP calls for idleTimeout).
type Session struct {
	token        string
	cfHome       string
	beaconDir    string
	st           *store.Store
	lastActivity time.Time
	mu           sync.Mutex
}

// server returns a *server wired to this session's cfHome and beaconDir.
// The caller must hold s.mu.
func (s *Session) server() *server {
	return &server{
		cfHome:         s.cfHome,
		beaconDir:      s.beaconDir,
		cfHomeExplicit: true,
	}
}

// touch updates lastActivity under the session lock.
func (s *Session) touch() {
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.mu.Unlock()
}

// Close closes the session's store if it is open.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st != nil {
		s.st.Close()
		s.st = nil
	}
}

// SessionManager maps Bearer tokens to active Sessions.
// All sessions are isolated: each has its own directory tree under sessionsDir.
type SessionManager struct {
	sessionsDir string
	sessions    sync.Map // token → *Session
	stopCh      chan struct{}
}

// NewSessionManager creates a SessionManager rooted at sessionsDir and
// starts the background idle-session reaper.
func NewSessionManager(sessionsDir string) *SessionManager {
	m := &SessionManager{
		sessionsDir: sessionsDir,
		stopCh:      make(chan struct{}),
	}
	go m.reaper()
	return m
}

// Stop shuts down the background reaper.
func (m *SessionManager) Stop() {
	close(m.stopCh)
}

// getOrCreate returns the existing Session for token, or creates a new one.
// On first call for a given token it creates the per-session directory tree
// and opens the SQLite store.
func (m *SessionManager) getOrCreate(token string) (*Session, error) {
	if v, ok := m.sessions.Load(token); ok {
		sess := v.(*Session)
		sess.touch()
		return sess, nil
	}

	// Create under a new-session lock keyed by token (optimistic: two concurrent
	// firsts for the same token are harmless because LoadOrStore atomically
	// decides which one wins).
	cfHome := filepath.Join(m.sessionsDir, token)
	beaconDir := filepath.Join(cfHome, "beacons")
	if err := os.MkdirAll(beaconDir, 0700); err != nil {
		return nil, err
	}

	st, err := store.Open(store.StorePath(cfHome))
	if err != nil {
		return nil, err
	}

	sess := &Session{
		token:        token,
		cfHome:       cfHome,
		beaconDir:    beaconDir,
		st:           st,
		lastActivity: time.Now(),
	}

	actual, loaded := m.sessions.LoadOrStore(token, sess)
	if loaded {
		// Another goroutine created the session first; clean up ours.
		st.Close()
		return actual.(*Session), nil
	}
	return sess, nil
}

// reaper closes stores for sessions that have been idle longer than idleTimeout.
func (m *SessionManager) reaper() {
	ticker := time.NewTicker(idleTimeout / 2)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.sessions.Range(func(k, v interface{}) bool {
				sess := v.(*Session)
				sess.mu.Lock()
				idle := time.Since(sess.lastActivity) > idleTimeout
				sess.mu.Unlock()
				if idle {
					sess.Close()
				}
				return true
			})
		}
	}
}

// generateToken returns a random 32-byte hex session token.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

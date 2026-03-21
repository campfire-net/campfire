package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// idleTimeout is the duration after which an inactive session closes its store.
const idleTimeout = 10 * time.Minute

// Session represents one agent's isolated state: a per-session directory
// containing identity.json and store.db, with the store held open in memory.
// The store opens once when the session is created and remains open until the
// session goes idle (no MCP calls for idleTimeout).
//
// In hosted HTTP mode, each session also holds an HTTP transport instance that
// handles peer-to-peer communication for campfires created by this agent.
type Session struct {
	token         string
	cfHome        string
	beaconDir     string
	st            *store.Store
	httpTransport *cfhttp.Transport // non-nil in hosted HTTP mode
	lastActivity  time.Time
	mu            sync.Mutex
}

// server returns a *server wired to this session's cfHome and beaconDir.
// If manager is non-nil and has a transport router, the returned server
// is configured for hosted HTTP mode with the session's transport instance.
func (s *Session) server(manager *SessionManager) *server {
	srv := &server{
		cfHome:         s.cfHome,
		beaconDir:      s.beaconDir,
		cfHomeExplicit: true,
	}
	if manager != nil && manager.router != nil {
		srv.httpTransport = s.httpTransport
		srv.transportRouter = manager.router
		srv.externalAddr = manager.externalAddr
	}
	return srv
}

// touch updates lastActivity under the session lock.
func (s *Session) touch() {
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.mu.Unlock()
}

// Close closes the session's store and transport if open.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpTransport != nil {
		s.httpTransport.StopNoncePruner()
	}
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
	// router is non-nil in hosted HTTP mode. It maps campfire IDs to the
	// session's HTTP transport instance so external peers can reach hosted agents.
	router *TransportRouter
	// externalAddr is the public URL of the hosted server (e.g. "http://localhost:8080").
	// Used as the HTTP transport endpoint in beacon transport configs.
	externalAddr string
	// idleTimeoutOverride allows tests to set a custom idle timeout.
	// When non-zero, reaper uses this instead of the package constant.
	idleTimeoutOverride time.Duration
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
// and opens the SQLite store. In hosted HTTP mode (router non-nil), it also
// creates a per-session HTTP transport instance.
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

	// In hosted HTTP mode, create a per-session HTTP transport.
	if m.router != nil {
		t := cfhttp.New("", st)
		t.StartNoncePruner()
		sess.httpTransport = t
		m.router.RegisterSession(token, t)
	}

	actual, loaded := m.sessions.LoadOrStore(token, sess)
	if loaded {
		// Another goroutine created the session first; clean up ours.
		if sess.httpTransport != nil {
			sess.httpTransport.StopNoncePruner()
		}
		st.Close()
		return actual.(*Session), nil
	}
	return sess, nil
}

// reaper closes stores for sessions that have been idle longer than idleTimeout.
func (m *SessionManager) reaper() {
	timeout := idleTimeout
	if m.idleTimeoutOverride > 0 {
		timeout = m.idleTimeoutOverride
	}
	ticker := time.NewTicker(timeout / 2)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.sessions.Range(func(k, v interface{}) bool {
				sess := v.(*Session)
				sess.mu.Lock()
				idle := time.Since(sess.lastActivity) > timeout
				sess.mu.Unlock()
				if idle {
					m.sessions.Delete(k)
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

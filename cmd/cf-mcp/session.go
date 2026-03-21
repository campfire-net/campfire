package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// idleTimeout is the duration after which an inactive session closes its store.
const idleTimeout = 10 * time.Minute

// defaultMaxSessions is the maximum number of concurrent active sessions
// when no override is provided to NewSessionManager.
const defaultMaxSessions = 1000

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
	router        *TransportRouter  // non-nil in hosted HTTP mode; used by Close to unregister routes
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
		sessionToken:   s.token,
		st:             s.st,
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

// Close closes the session's store and transport if open. In hosted HTTP mode,
// it also unregisters all campfire routes and the session transport from the
// router so that subsequent /campfire/{id}/deliver requests return 404 instead
// of hitting a stopped transport.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.router != nil {
		s.router.UnregisterSession(s.token)
	}
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
	sessions    sync.Map   // token → *Session
	createMu    sync.Mutex // serializes first-call creation to prevent concurrent SQLite opens for the same token
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
	// maxSessions is the maximum number of concurrent active sessions.
	// getOrCreate returns an error when this limit is reached.
	// Zero means use defaultMaxSessions.
	maxSessions int
}

// NewSessionManager creates a SessionManager rooted at sessionsDir and
// starts the background idle-session reaper. The session limit defaults to
// defaultMaxSessions; use SessionManager.maxSessions directly in tests to
// override.
func NewSessionManager(sessionsDir string) *SessionManager {
	m := &SessionManager{
		sessionsDir: sessionsDir,
		stopCh:      make(chan struct{}),
		maxSessions: defaultMaxSessions,
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
	// Fast path: session already exists.
	if v, ok := m.sessions.Load(token); ok {
		sess := v.(*Session)
		sess.touch()
		return sess, nil
	}

	// Slow path: serialize creation to prevent concurrent goroutines for the
	// same token from each trying to open the same SQLite store (SQLITE_BUSY).
	m.createMu.Lock()
	defer m.createMu.Unlock()

	// Re-check under the lock — another goroutine may have created it.
	if v, ok := m.sessions.Load(token); ok {
		sess := v.(*Session)
		sess.touch()
		return sess, nil
	}

	// Enforce the session limit before allocating any resources.
	limit := m.maxSessions
	if limit <= 0 {
		limit = defaultMaxSessions
	}
	var count int
	m.sessions.Range(func(_, _ interface{}) bool {
		count++
		return count < limit // stop early once the limit is reached
	})
	if count >= limit {
		return nil, &sessionLimitError{limit: limit}
	}

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
		// Set the key provider once at session init so that repeated campfire
		// creations within the same session do not overwrite it. The closure
		// captures cfHome (constant for this session) and resolves the
		// campfire-specific state on each call via the campfireID argument.
		fsT := fs.New(cfHome)
		t.SetKeyProvider(func(campfireID string) (privKey []byte, pubKey []byte, err error) {
			state, err := fsT.ReadState(campfireID)
			if err != nil {
				return nil, nil, err
			}
			return state.PrivateKey, state.PublicKey, nil
		})
		sess.httpTransport = t
		sess.router = m.router
		m.router.RegisterSession(token, t)
	}

	m.sessions.Store(token, sess)
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

// sessionLimitError is returned by getOrCreate when the active session count
// reaches maxSessions. Callers translate this to a -32000 JSON-RPC error with
// an HTTP 503 status so clients know to retry later.
type sessionLimitError struct {
	limit int
}

func (e *sessionLimitError) Error() string {
	return fmt.Sprintf("session limit reached (%d active sessions)", e.limit)
}

// generateToken returns a random 32-byte hex session token.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

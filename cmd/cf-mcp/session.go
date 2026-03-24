package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/ratelimit"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// idleTimeout is the duration after which an inactive session closes its store.
const idleTimeout = 10 * time.Minute

// defaultMaxSessions is the maximum number of concurrent active sessions
// when no override is provided to NewSessionManager.
const defaultMaxSessions = 1000

// defaultTokenTTL is the default time-to-live for session tokens.
const defaultTokenTTL = 1 * time.Hour

// defaultRotationGracePeriod is the grace period for old tokens after rotation.
const defaultRotationGracePeriod = 30 * time.Second

// ---------------------------------------------------------------------------
// TokenRegistry
// ---------------------------------------------------------------------------

// tokenEntry holds metadata for an issued token.
type tokenEntry struct {
	internalID string
	issuedAt   time.Time
	revoked    bool
	// gracePeriodUntil is non-zero for tokens that have been rotated out:
	// they remain valid until this time to allow in-flight requests to drain.
	gracePeriodUntil time.Time
}

// TokenRegistry maps external bearer tokens to internal session IDs.
// It is the issuance authority: only tokens in the registry are valid.
type TokenRegistry struct {
	mu     sync.RWMutex
	tokens map[string]*tokenEntry // token → entry
}

func newTokenRegistry() *TokenRegistry {
	return &TokenRegistry{
		tokens: make(map[string]*tokenEntry),
	}
}

// issue generates a new token and assigns it a fresh internalID.
// Returns (token, error).
func (r *TokenRegistry) issue() (string, error) {
	tok, err := generateToken()
	if err != nil {
		return "", err
	}
	id, err := generateToken() // use as internalID (UUID-like opaque string)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.tokens[tok] = &tokenEntry{
		internalID: id,
		issuedAt:   time.Now(),
	}
	r.mu.Unlock()
	return tok, nil
}

// issueFor issues a new token that maps to an existing internalID.
// Used for token rotation: same session, new external credential.
func (r *TokenRegistry) issueFor(internalID string) (string, error) {
	tok, err := generateToken()
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.tokens[tok] = &tokenEntry{
		internalID: internalID,
		issuedAt:   time.Now(),
	}
	r.mu.Unlock()
	return tok, nil
}

// lookup validates a token and returns its internalID.
// Returns an error for tokens not in the registry, revoked, or expired (ttl=0 means no expiry check).
// Returns tokenExpiredError if expired, tokenRevokedError if revoked, tokenUnknownError if not found.
func (r *TokenRegistry) lookup(token string, ttl time.Duration) (string, error) {
	r.mu.RLock()
	entry, ok := r.tokens[token]
	// Copy all fields we need while holding the lock to avoid a data race.
	// Between RUnlock and reading entry fields, another goroutine (reaper,
	// rotation, or explicit revoke) can modify or delete the entry.
	var internalID string
	var issuedAt time.Time
	var revoked bool
	var gracePeriodUntil time.Time
	if ok {
		internalID = entry.internalID
		issuedAt = entry.issuedAt
		revoked = entry.revoked
		gracePeriodUntil = entry.gracePeriodUntil
	}
	r.mu.RUnlock()
	if !ok {
		return "", &tokenUnknownError{}
	}
	if revoked {
		// Check grace period for rotated tokens.
		if !gracePeriodUntil.IsZero() && time.Now().Before(gracePeriodUntil) {
			return internalID, nil
		}
		return "", &tokenRevokedError{}
	}
	if ttl > 0 && time.Since(issuedAt) > ttl {
		return "", &tokenExpiredError{}
	}
	return internalID, nil
}

// revoke marks a token as revoked immediately.
func (r *TokenRegistry) revoke(token string) {
	r.mu.Lock()
	if entry, ok := r.tokens[token]; ok {
		entry.revoked = true
		entry.gracePeriodUntil = time.Time{} // no grace period for explicit revoke
	}
	r.mu.Unlock()
}

// revokeWithGrace marks a token as revoked but keeps it valid until gracePeriodUntil.
// Used for token rotation so in-flight requests can drain.
func (r *TokenRegistry) revokeWithGrace(token string, gracePeriodUntil time.Time) {
	r.mu.Lock()
	if entry, ok := r.tokens[token]; ok {
		entry.revoked = true
		entry.gracePeriodUntil = gracePeriodUntil
	}
	r.mu.Unlock()
}

// delete removes a token entry entirely. Used after grace period expires.
func (r *TokenRegistry) delete(token string) {
	r.mu.Lock()
	delete(r.tokens, token)
	r.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Token error types
// ---------------------------------------------------------------------------

type tokenUnknownError struct{}

func (e *tokenUnknownError) Error() string { return "token not recognized" }

type tokenRevokedError struct{}

func (e *tokenRevokedError) Error() string { return "token has been revoked" }

type tokenExpiredError struct{}

func (e *tokenExpiredError) Error() string { return "token has expired" }

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

// Session represents one agent's isolated state: a per-session directory
// containing identity.json and store.db, with the store held open in memory.
// The store opens once when the session is created and remains open until the
// session goes idle (no MCP calls for idleTimeout).
//
// In hosted HTTP mode, each session also holds an HTTP transport instance that
// handles peer-to-peer communication for campfires created by this agent.
type Session struct {
	token        string // external: current valid Bearer token
	internalID   string // internal: stable filesystem directory name
	cfHome       string // filepath.Join(sessionsDir, internalID)
	beaconDir    string
	st           store.Store
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

// ---------------------------------------------------------------------------
// SessionManager
// ---------------------------------------------------------------------------

// SessionManager maps Bearer tokens to active Sessions via the token registry.
// All sessions are isolated: each has its own directory tree under sessionsDir.
type SessionManager struct {
	sessionsDir string
	sessions    sync.Map   // internalID → *Session
	createMu    sync.Mutex // serializes first-call creation to prevent concurrent SQLite opens for the same token
	stopCh      chan struct{}
	registry    *TokenRegistry
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
	// tokenTTL is the time-to-live for session tokens. Zero uses defaultTokenTTL.
	tokenTTL time.Duration
	// rotationGracePeriod is the grace period for old tokens after rotation.
	// Zero uses defaultRotationGracePeriod.
	rotationGracePeriod time.Duration
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
		registry:    newTokenRegistry(),
	}
	go m.reaper()
	return m
}

// Stop shuts down the background reaper.
func (m *SessionManager) Stop() {
	close(m.stopCh)
}

// ttl returns the effective token TTL.
func (m *SessionManager) ttl() time.Duration {
	if m.tokenTTL > 0 {
		return m.tokenTTL
	}
	return defaultTokenTTL
}

// gracePeriod returns the effective rotation grace period.
func (m *SessionManager) gracePeriod() time.Duration {
	if m.rotationGracePeriod > 0 {
		return m.rotationGracePeriod
	}
	return defaultRotationGracePeriod
}

// issueToken issues a new token and registers it in the registry.
// Called by handleMCPSessioned on campfire_init with no existing token.
func (m *SessionManager) issueToken() (string, error) {
	return m.registry.issue()
}

// validateToken validates a bearer token and returns its internalID.
// Returns a typed error (tokenExpiredError, tokenRevokedError, tokenUnknownError) on failure.
func (m *SessionManager) validateToken(token string) (string, error) {
	return m.registry.lookup(token, m.ttl())
}

// getSession returns the active Session for a token, or nil if not found.
// The token is validated against the registry (with TTL check); if invalid, nil is returned.
// This is a read-only lookup — it does not create new sessions.
func (m *SessionManager) getSession(token string) *Session {
	internalID, err := m.registry.lookup(token, m.ttl())
	if err != nil {
		return nil
	}
	if v, ok := m.sessions.Load(internalID); ok {
		return v.(*Session)
	}
	return nil
}

// revokeSession revokes a session by token: removes from registry, closes session.
func (m *SessionManager) revokeSession(token string) {
	// Look up internalID before revoking (lookup still works since we haven't revoked yet).
	internalID, err := m.registry.lookup(token, 0)
	m.registry.revoke(token)
	if err != nil {
		return
	}
	if v, ok := m.sessions.Load(internalID); ok {
		m.sessions.Delete(internalID)
		v.(*Session).Close()
	}
}

// rotateToken issues a new token mapped to the same internalID as oldToken.
// oldToken is placed in grace period. Returns new token.
func (m *SessionManager) rotateToken(oldToken string) (string, error) {
	internalID, err := m.registry.lookup(oldToken, m.ttl())
	if err != nil {
		return "", err
	}
	newToken, err := m.registry.issueFor(internalID)
	if err != nil {
		return "", err
	}
	// Place old token in grace period.
	m.registry.revokeWithGrace(oldToken, time.Now().Add(m.gracePeriod()))
	// Schedule cleanup of old token after grace period.
	go func() {
		timer := time.NewTimer(m.gracePeriod())
		defer timer.Stop()
		select {
		case <-timer.C:
			m.registry.delete(oldToken)
		case <-m.stopCh:
		}
	}()

	// Update the session's token field so router registration stays correct.
	if v, ok := m.sessions.Load(internalID); ok {
		sess := v.(*Session)
		sess.mu.Lock()
		sess.token = newToken
		// Update router registration: old token → new token.
		if sess.router != nil {
			sess.router.UnregisterSession(oldToken)
			sess.router.RegisterSession(newToken, sess.httpTransport)
		}
		sess.mu.Unlock()
	}

	return newToken, nil
}

// getOrCreate returns the existing Session for the given internalID, or creates
// a new one. The token must have already been validated by validateToken.
func (m *SessionManager) getOrCreate(token string) (*Session, error) {
	// Validate token against registry and get internalID.
	internalID, err := m.registry.lookup(token, m.ttl())
	if err != nil {
		return nil, err
	}

	// Fast path: session already exists.
	if v, ok := m.sessions.Load(internalID); ok {
		sess := v.(*Session)
		sess.touch()
		return sess, nil
	}

	// Slow path: serialize creation to prevent concurrent goroutines for the
	// same token from each trying to open the same SQLite store (SQLITE_BUSY).
	m.createMu.Lock()
	defer m.createMu.Unlock()

	// Re-check under the lock — another goroutine may have created it.
	if v, ok := m.sessions.Load(internalID); ok {
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

	// Use internalID as the directory name (NOT the token).
	cfHome := filepath.Join(m.sessionsDir, internalID)
	beaconDir := filepath.Join(cfHome, "beacons")
	if err := os.MkdirAll(beaconDir, 0700); err != nil {
		return nil, err
	}

	rawStore, err := store.Open(store.StorePath(cfHome))
	if err != nil {
		return nil, err
	}
	// Wrap the raw store with free-tier rate limiting (1000 msg/month by default).
	// The wrapper implements store.Store, so it is drop-in for all operations.
	st := ratelimit.New(rawStore, ratelimit.Config{})

	sess := &Session{
		token:        token,
		internalID:   internalID,
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

	m.sessions.Store(internalID, sess)
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

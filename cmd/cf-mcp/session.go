package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// defaultInitRateLimit is the maximum number of campfire_init calls (new
// session creations) allowed per IP address per initRateWindow.
// Design doc §5.b / adversary finding S9: 10 sessions per IP per minute.
const defaultInitRateLimit = 10

// initRateWindow is the sliding window duration for per-IP init rate limiting.
const initRateWindow = 1 * time.Minute

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
	mu          sync.RWMutex
	tokens      map[string]*tokenEntry // token → entry
	persistPath string                 // if non-empty, registry is persisted to this file
}

// tokenEntryJSON is the on-disk representation of a tokenEntry.
type tokenEntryJSON struct {
	InternalID       string    `json:"internal_id"`
	IssuedAt         time.Time `json:"issued_at"`
	Revoked          bool      `json:"revoked"`
	GracePeriodUntil time.Time `json:"grace_period_until,omitempty"`
}

// tokenRegistryJSON is the on-disk representation of the full registry.
type tokenRegistryJSON struct {
	Tokens map[string]*tokenEntryJSON `json:"tokens"`
}

// newTokenRegistry creates a new in-memory TokenRegistry (no persistence).
func newTokenRegistry() *TokenRegistry {
	return &TokenRegistry{
		tokens: make(map[string]*tokenEntry),
	}
}

// newTokenRegistryFromFile creates a TokenRegistry backed by a JSON file.
// If the file exists, its contents are loaded. If it does not exist, an empty
// registry is created. Mutations are automatically persisted to the file.
func newTokenRegistryFromFile(path string) (*TokenRegistry, error) {
	r := &TokenRegistry{
		tokens:      make(map[string]*tokenEntry),
		persistPath: path,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil // fresh registry, file will be created on first mutation
		}
		return nil, fmt.Errorf("token registry: read %s: %w", path, err)
	}
	var reg tokenRegistryJSON
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("token registry: parse %s: %w", path, err)
	}
	for tok, e := range reg.Tokens {
		r.tokens[tok] = &tokenEntry{
			internalID:       e.InternalID,
			issuedAt:         e.IssuedAt,
			revoked:          e.Revoked,
			gracePeriodUntil: e.GracePeriodUntil,
		}
	}
	return r, nil
}

// save atomically writes the registry to r.persistPath.
// Must be called with r.mu held (at least read-locked).
// Uses a temp file + rename for atomicity.
func (r *TokenRegistry) save() error {
	if r.persistPath == "" {
		return nil
	}
	reg := tokenRegistryJSON{
		Tokens: make(map[string]*tokenEntryJSON, len(r.tokens)),
	}
	for tok, e := range r.tokens {
		reg.Tokens[tok] = &tokenEntryJSON{
			InternalID:       e.internalID,
			IssuedAt:         e.issuedAt,
			Revoked:          e.revoked,
			GracePeriodUntil: e.gracePeriodUntil,
		}
	}
	data, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("token registry: marshal: %w", err)
	}
	dir := filepath.Dir(r.persistPath)
	tmp, err := os.CreateTemp(dir, "token-registry-*.tmp")
	if err != nil {
		return fmt.Errorf("token registry: create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("token registry: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("token registry: close temp: %w", err)
	}
	if err := os.Rename(tmpName, r.persistPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("token registry: rename: %w", err)
	}
	return nil
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
	r.save() //nolint:errcheck // persist best-effort; mutation already applied in memory
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
	r.save() //nolint:errcheck // persist best-effort
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
	r.save() //nolint:errcheck // persist best-effort
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
	r.save() //nolint:errcheck // persist best-effort
	r.mu.Unlock()
}

// delete removes a token entry entirely. Used after grace period expires.
func (r *TokenRegistry) delete(token string) {
	r.mu.Lock()
	delete(r.tokens, token)
	r.save() //nolint:errcheck // persist best-effort
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
	auditWriter   *AuditWriter      // non-nil after first campfire_init; persisted across per-request server instances
	lastActivity  time.Time
	mu            sync.Mutex
}

// server returns a *server wired to this session's cfHome and beaconDir.
// If manager is non-nil and has a transport router, the returned server
// is configured for hosted HTTP mode with the session's transport instance.
// The session's auditWriter (if any) is propagated so that repeated
// campfire_init calls reuse the same AuditWriter rather than leaking
// goroutines by creating a new one each time.
func (s *Session) server(manager *SessionManager) *server {
	srv := &server{
		cfHome:         s.cfHome,
		beaconDir:      s.beaconDir,
		cfHomeExplicit: true,
		sessionToken:   s.token,
		st:             s.st,
		sess:           s,
		auditWriter:    s.auditWriter,
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

// Close closes the session's store, audit writer, and transport if open.
// In hosted HTTP mode, it also unregisters all campfire routes and the session
// transport from the router so that subsequent /campfire/{id}/deliver requests
// return 404 instead of hitting a stopped transport.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auditWriter != nil {
		s.auditWriter.Close()
		s.auditWriter = nil
	}
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
// initRateLimiter — per-IP sliding window for campfire_init
// ---------------------------------------------------------------------------

// initRateLimiter enforces a sliding-window rate limit on campfire_init calls
// (new session creation) keyed by client IP address. It is safe for concurrent
// use. Zero value is not useful — use newInitRateLimiter.
type initRateLimiter struct {
	mu      sync.Mutex
	entries map[string][]time.Time // IP → timestamps of recent init calls
	limit   int
	window  time.Duration
}

// newInitRateLimiter creates a limiter allowing at most limit calls per window.
func newInitRateLimiter(limit int, window time.Duration) *initRateLimiter {
	return &initRateLimiter{
		entries: make(map[string][]time.Time),
		limit:   limit,
		window:  window,
	}
}

// allow returns true if the IP is below the rate limit, recording the attempt.
// Returns false (without recording) if the limit has been reached.
func (l *initRateLimiter) allow(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	// Trim stale timestamps outside the window.
	times := l.entries[ip]
	start := 0
	for start < len(times) && times[start].Before(cutoff) {
		start++
	}
	times = times[start:]

	if len(times) >= l.limit {
		l.entries[ip] = times
		return false
	}

	l.entries[ip] = append(times, now)
	return true
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
	// initLimiter enforces per-IP rate limiting on campfire_init (new session
	// creation). Never nil after NewSessionManager.
	initLimiter *initRateLimiter
}

// NewSessionManager creates a SessionManager rooted at sessionsDir and
// starts the background idle-session reaper. The session limit defaults to
// defaultMaxSessions; use SessionManager.maxSessions directly in tests to
// override.
//
// The token registry is persisted to sessionsDir/token-registry.json so that
// active tokens survive server restarts.
func NewSessionManager(sessionsDir string) *SessionManager {
	registryPath := filepath.Join(sessionsDir, "token-registry.json")
	reg, err := newTokenRegistryFromFile(registryPath)
	if err != nil {
		// Log but continue with an empty in-memory registry rather than
		// failing to start. A corrupt registry is recoverable: clients
		// will re-init and receive new tokens.
		fmt.Fprintf(os.Stderr, "warning: token registry load failed (%v); starting fresh\n", err)
		reg = newTokenRegistry()
	}
	m := &SessionManager{
		sessionsDir: sessionsDir,
		stopCh:      make(chan struct{}),
		maxSessions: defaultMaxSessions,
		registry:    reg,
		initLimiter: newInitRateLimiter(defaultInitRateLimit, initRateWindow),
	}
	go m.reaper()
	return m
}

// checkInitRateLimit returns true if the given IP is allowed to create a new
// session. Returns false and an initRateLimitError if the per-IP limit is
// exceeded. ip should be the bare IP (no port).
func (m *SessionManager) checkInitRateLimit(ip string) error {
	if m.initLimiter == nil {
		return nil
	}
	if !m.initLimiter.allow(ip) {
		return &initRateLimitError{ip: ip, limit: m.initLimiter.limit, window: m.initLimiter.window}
	}
	return nil
}

// Stop shuts down the background reaper and closes all active sessions.
// This ensures audit writers are drained before the session directory
// is cleaned up (e.g., by t.TempDir in tests).
func (m *SessionManager) Stop() {
	close(m.stopCh)
	m.sessions.Range(func(k, v interface{}) bool {
		v.(*Session).Close()
		m.sessions.Delete(k)
		return true
	})
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
		// Close before Delete to match the reaper ordering: the session is
		// fully torn down before it is removed from the map, preventing a
		// concurrent getOrCreate from receiving a half-closed session.
		v.(*Session).Close()
		m.sessions.Delete(internalID)
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
		// Transfer router registration from old token to new token.
		// RotateSession preserves campfire→transport mappings so that
		// GetCampfireTransport and LookupInviteAcrossAllStores continue
		// to resolve correctly after rotation.
		if sess.router != nil {
			sess.router.RotateSession(oldToken, newToken, sess.httpTransport)
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
					// Close before Delete: a concurrent getOrCreate can re-add
					// the session between Delete and Close, getting a session
					// that is mid-close. Closing first (while still in the map)
					// means the session is fully torn down before it disappears
					// from the map. getOrCreate will see the session is gone
					// and create a fresh one.
					sess.Close()
					m.sessions.Delete(k)
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

// initRateLimitError is returned by checkInitRateLimit when the per-IP limit
// is exceeded. Callers translate this to HTTP 429 Too Many Requests.
type initRateLimitError struct {
	ip     string
	limit  int
	window time.Duration
}

func (e *initRateLimitError) Error() string {
	return fmt.Sprintf("rate limit exceeded: %d new sessions per %s per IP (from %s)", e.limit, e.window, e.ip)
}

// generateToken returns a random 32-byte hex session token.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

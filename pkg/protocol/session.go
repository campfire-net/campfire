package protocol

// session.go — zero-ceremony multi-agent coordination via session tokens.
//
// A session is a short-lived campfire identified by a bearer token. The token
// embeds all state needed to send and receive messages without requiring cf
// init or CF_HOME — enabling ephemeral, zero-ceremony coordination.
//
// SECURITY: Session tokens are bearer credentials. Anyone holding a valid
// token can post to and read from the session campfire. There is no per-sender
// attribution inside a session — all participants share the same ephemeral
// signing key. Use cf join for attributable, durable campfires.
//
// The token format is versioned (cfs1_ prefix) to allow future evolution.
// TTL enforcement is client-side: tokens rejected even 1 nanosecond past expiry.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/session"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// MaxSessionTTL is the maximum allowed session TTL (24 hours).
// Re-exported from pkg/session for CLI and SDK callers.
const MaxSessionTTL = session.MaxTTL

// Session is a handle to a session campfire. It wraps a temporary client and
// campfire state derived from a decoded session token.
//
// Session is NOT safe for concurrent use from multiple goroutines.
// Call Close when done to release resources.
type Session struct {
	campfireID   string
	campfireDir  string // campfire transport directory
	client       *Client
	storeDir     string // temporary directory holding the local store; removed on Close
	ownStore     bool   // this Session owns (and should close) the store
}

// sessionTransportConfig is the JSON structure embedded in the token's
// TransportConfig field. It records the campfire transport directory so
// participants can locate the filesystem transport without CF_HOME.
type sessionTransportConfig struct {
	Protocol    string `json:"protocol"`
	CampfireDir string `json:"campfire_dir"`
}

// NewSession creates a new session campfire and returns a bearer token.
//
// The caller's identity is used to sign the token. The session campfire is
// created in a temporary directory — it lives as long as the process or until
// the caller calls Session.End.
//
// ttl must be > 0 and <= 24h. Returns an error if ttl exceeds the maximum.
//
// SECURITY: The returned token is a bearer credential. Treat it like a
// password — do not log it, do not embed it in URLs, transmit only over
// encrypted channels.
func (c *Client) NewSession(ttl time.Duration) (*Session, string, error) {
	if c.identity == nil {
		return nil, "", fmt.Errorf("protocol.NewSession: identity required")
	}
	if ttl <= 0 {
		return nil, "", fmt.Errorf("protocol.NewSession: TTL must be > 0")
	}
	if ttl > session.MaxTTL {
		return nil, "", fmt.Errorf("protocol.NewSession: TTL %v exceeds maximum %v", ttl, session.MaxTTL)
	}

	// Create session campfire keypair.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		return nil, "", fmt.Errorf("protocol.NewSession: creating campfire: %w", err)
	}
	campfireID := cf.PublicKeyHex()

	// Generate ephemeral signing keypair shared among all session participants.
	// Using the creator's identity is intentionally avoided — the shared key
	// means no per-sender attribution inside the session (documented trade-off).
	ephPub, ephPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("protocol.NewSession: generating ephemeral key: %w", err)
	}

	// Create temporary directory for the session campfire transport.
	tmpDir, err := os.MkdirTemp("", "cf-session-")
	if err != nil {
		return nil, "", fmt.Errorf("protocol.NewSession: creating temp dir: %w", err)
	}

	// Initialize filesystem transport.
	tr := fs.New(tmpDir)
	if err := tr.Init(cf); err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("protocol.NewSession: initializing transport: %w", err)
	}

	// Admit creator as first member using the shared ephemeral key.
	campfireDir := tr.CampfireDir(campfireID)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: ephPub,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("protocol.NewSession: writing member record: %w", err)
	}

	// Build transport config for embedding in the token.
	transportConfig := sessionTransportConfig{
		Protocol:    "filesystem",
		CampfireDir: campfireDir,
	}
	transportConfigBytes, err := json.Marshal(transportConfig)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("protocol.NewSession: encoding transport config: %w", err)
	}

	// Encode the session token (signed by creator's identity key).
	tok, err := session.EncodeToken(session.TokenParams{
		CampfireID:       cf.PublicKey,
		EphemeralPubKey:  ephPub,
		EphemeralPrivKey: ephPriv,
		TransportConfig:  transportConfigBytes,
		TTL:              ttl,
		CreatorPub:       c.identity.PublicKey,
		CreatorPriv:      c.identity.PrivateKey,
	})
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("protocol.NewSession: encoding token: %w", err)
	}

	// Open a local store for session membership.
	storeDir, err := os.MkdirTemp("", "cf-session-store-")
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("protocol.NewSession: creating store dir: %w", err)
	}

	sess, err := buildSessionClient(campfireID, campfireDir, storeDir, ephPub, ephPriv)
	if err != nil {
		os.RemoveAll(tmpDir)
		os.RemoveAll(storeDir)
		return nil, "", fmt.Errorf("protocol.NewSession: building session client: %w", err)
	}
	sess.ownStore = true

	return sess, tok, nil
}

// JoinSession decodes a session token and returns a Session handle for
// sending and reading messages in the session campfire.
//
// creatorPub is the expected token creator's public key. Pass nil to skip
// creator identity verification (less strict — token signature is still
// verified using the key embedded in the token itself).
//
// Returns an error if the token is expired, malformed, or the signature fails.
//
// SECURITY: The token is a bearer credential. All participants share the same
// ephemeral signing key — there is no per-sender attribution.
func JoinSession(tok string, creatorPub ed25519.PublicKey) (*Session, error) {
	decoded, err := session.DecodeToken(tok, creatorPub)
	if err != nil {
		return nil, fmt.Errorf("protocol.JoinSession: %w", err)
	}

	var transportConfig sessionTransportConfig
	if err := json.Unmarshal(decoded.TransportConfig, &transportConfig); err != nil {
		return nil, fmt.Errorf("protocol.JoinSession: decoding transport config: %w", err)
	}

	campfireID := fmt.Sprintf("%x", decoded.CampfireID)

	// Open a temporary store for this joiner's membership.
	storeDir, err := os.MkdirTemp("", "cf-session-store-")
	if err != nil {
		return nil, fmt.Errorf("protocol.JoinSession: creating store dir: %w", err)
	}

	sess, err := buildSessionClient(
		campfireID, transportConfig.CampfireDir, storeDir,
		decoded.EphemeralPubKey, decoded.EphemeralPrivKey,
	)
	if err != nil {
		os.RemoveAll(storeDir)
		return nil, fmt.Errorf("protocol.JoinSession: %w", err)
	}
	sess.ownStore = true

	// Write the joiner's ephemeral member record to the filesystem transport.
	// This is best-effort: if the campfire dir is remote or already has this
	// member record, it may fail; that's acceptable.
	tr := fs.ForDir(transportConfig.CampfireDir)
	tr.WriteMember(campfireID, campfire.MemberRecord{ //nolint:errcheck
		PublicKey: decoded.EphemeralPubKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	})

	return sess, nil
}

// buildSessionClient creates a Client backed by a new store in storeDir,
// using the shared ephemeral keypair as the session identity.
func buildSessionClient(
	campfireID, campfireDir, storeDir string,
	ephPub ed25519.PublicKey, ephPriv ed25519.PrivateKey,
) (*Session, error) {
	rawStore, err := store.Open(store.StorePath(storeDir))
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}

	// Construct a temporary identity using the shared ephemeral keypair.
	// identity.Identity is a plain struct — we can construct one directly.
	ephID := &identity.Identity{
		PublicKey:  ephPub,
		PrivateKey: ephPriv,
		CreatedAt:  time.Now().UnixNano(),
	}

	client := New(rawStore, ephID)

	// Record membership so Send/Read can resolve the transport.
	membership := store.Membership{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}
	if err := rawStore.AddMembership(membership); err != nil {
		rawStore.Close()
		return nil, fmt.Errorf("recording membership: %w", err)
	}

	return &Session{
		campfireID:  campfireID,
		campfireDir: campfireDir,
		client:      client,
		storeDir:    storeDir,
	}, nil
}

// CampfireID returns the hex-encoded campfire ID for this session.
func (s *Session) CampfireID() string {
	return s.campfireID
}

// Send posts a message to the session campfire.
// Returns the created message on success.
// Returns an error if the session has been closed or ended.
func (s *Session) Send(payload string) (*message.Message, error) {
	if s.client == nil {
		return nil, fmt.Errorf("session is closed")
	}
	return s.client.Send(SendRequest{
		CampfireID: s.campfireID,
		Payload:    []byte(payload),
	})
}

// Read returns messages from the session campfire.
// Messages are returned in timestamp order.
// Returns an error if the session has been closed or ended.
func (s *Session) Read() ([]Message, error) {
	if s.client == nil {
		return nil, fmt.Errorf("session is closed")
	}
	result, err := s.client.Read(ReadRequest{
		CampfireID: s.campfireID,
	})
	if err != nil {
		return nil, err
	}
	return result.Messages, nil
}

// End disbands the session campfire and releases resources.
// After End, the session is unusable.
func (s *Session) End() error {
	if s.campfireDir != "" {
		tr := fs.ForDir(s.campfireDir)
		tr.Remove(s.campfireID) //nolint:errcheck
		s.campfireDir = ""
	}
	return s.Close()
}

// Close releases the store held by this session without removing the campfire.
// Use End to both disband and release.
func (s *Session) Close() error {
	if s.client != nil {
		if s.ownStore {
			if err := s.client.Close(); err != nil {
				return err
			}
		}
		s.client = nil
	}
	if s.storeDir != "" {
		os.RemoveAll(s.storeDir) //nolint:errcheck
		s.storeDir = ""
	}
	return nil
}

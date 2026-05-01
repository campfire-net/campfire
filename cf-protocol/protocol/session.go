// session.go — Session type for campfire session campfires (cf 0.30+).
//
// MIGRATION NOTE (cf 0.30 / campfireagent-c3a):
// The shared-key session token mechanism has been REMOVED per design v2 §1.2.
// The old form (NewSession generating a shared ephemeral key, JoinSession
// decoding a shared private key from the token) is gone.
//
// The new form uses cf-conventions/cf-session for full session orchestration:
//   - Lazy-mint per-worker grant issuance (§2.9.1)
//   - Jail-write or signing-proxy key receipt (§2.9.2)
//   - Disposable session log, canonical grant log (§2.9.3)
//
// This file retains only the minimal Session type needed by:
//   - EmitSessionOpen / EmitSessionClose (session_events.go)
//   - The cf-session package (cf-conventions/cf-session/)
//   - Existing callers that create session campfires for coordination
//
// Session campfires are now created via NewSession (below) which uses the
// creator's own identity key (not a shared ephemeral key). The per-worker
// grant machinery lives in cf-conventions/cf-session.
package protocol

import (
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/internal/campfire"
	"github.com/campfire-net/campfire/cf-protocol/internal/message"
	"github.com/campfire-net/campfire/cf-protocol/internal/store"
	"github.com/campfire-net/campfire/cf-protocol/internal/transport/fs"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/session"
)

// MaxSessionTTL is the maximum allowed session TTL (24 hours).
// Re-exported from pkg/session for CLI and SDK callers.
const MaxSessionTTL = session.MaxTTL

// Session is a handle to a session campfire. It wraps a protocol Client
// backed by the session's campfire transport.
//
// Session is NOT safe for concurrent use from multiple goroutines.
// Call Close (or End) when done to release resources.
type Session struct {
	campfireID  string
	campfireDir string // filesystem transport directory for this session campfire
	client      *Client
	storeDir    string // temp dir holding the local store; removed on Close
	ownStore    bool   // whether this Session owns (and should close) the store
}

// NewSession creates a new session campfire using the caller's own identity key.
//
// The creator's identity signs all messages in this session. Per-worker key
// issuance and grant management are handled by cf-conventions/cf-session.
//
// ttl must be > 0 and ≤ MaxSessionTTL (24h).
//
// Returns the session handle and the hex-encoded campfire ID on success.
// Returns an error if the client has no identity or the ttl is out of range.
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

	// Admit creator as first member using their own identity key.
	campfireDir := tr.CampfireDir(campfireID)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: c.identity.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("protocol.NewSession: writing member record: %w", err)
	}

	// Open a local store for session membership.
	storeDir, err := os.MkdirTemp("", "cf-session-store-")
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("protocol.NewSession: creating store dir: %w", err)
	}

	sess, err := buildSessionClient(campfireID, campfireDir, storeDir, c.identity)
	if err != nil {
		os.RemoveAll(tmpDir)
		os.RemoveAll(storeDir)
		return nil, "", fmt.Errorf("protocol.NewSession: building session client: %w", err)
	}
	sess.ownStore = true

	return sess, campfireID, nil
}

// buildSessionClient creates a Client backed by a new store in storeDir,
// using the given identity for signing.
func buildSessionClient(
	campfireID, campfireDir, storeDir string,
	id *identity.Identity,
) (*Session, error) {
	rawStore, err := store.Open(store.StorePath(storeDir))
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}

	client := New(rawStore, id)

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

// CampfireDir returns the filesystem transport directory for this session campfire.
// Used by session create to encode the transport location in the cfs2_ token so
// that other processes (cf session send/read/end) can find the campfire.
func (s *Session) CampfireDir() string {
	return s.campfireDir
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

// sendTagged posts a message with explicit tags to the session campfire.
// Used by L1 emission helpers (EmitSessionOpen, EmitSessionClose) to attach
// session lifecycle tags without changing the public Send API.
// Returns an error if the session has been closed.
func (s *Session) sendTagged(payload []byte, tags []string) (*message.Message, error) {
	if s.client == nil {
		return nil, fmt.Errorf("session is closed")
	}
	return s.client.Send(SendRequest{
		CampfireID: s.campfireID,
		Payload:    payload,
		Tags:       tags,
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

package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

const defaultPollIntervalSecs = 30

// ErrEncryptAtRestNotSupported is returned by New when EncryptAtRest is requested.
// Encryption at rest is not yet implemented — callers should not silently send
// plaintext when they expected encrypted messages.
var ErrEncryptAtRestNotSupported = errors.New("github transport: encrypt_at_rest is not yet supported")

// Config holds configuration for the GitHub transport.
type Config struct {
	// Repo is the coordination repository in "owner/repo" format.
	Repo string

	// IssueNumber is the GitHub Issue number used as the campfire relay.
	// Set to 0 before calling CreateCampfire.
	IssueNumber int

	// Token is the GitHub PAT or App token. Not stored in beacons — resolved at runtime.
	Token string

	// PollIntervalSecs is the polling interval in seconds. Defaults to 30.
	PollIntervalSecs int

	// EncryptAtRest controls whether message payloads are encrypted before posting.
	// Not yet implemented — setting this to true returns ErrEncryptAtRestNotSupported.
	EncryptAtRest bool

	// BaseURL overrides the GitHub API base URL (for GitHub Enterprise or testing).
	// Defaults to "https://api.github.com".
	BaseURL string
}

// Transport implements the GitHub Issues transport for the Campfire protocol.
// Each campfire maps to one GitHub Issue in the coordination repository.
// Messages are posted as Issue comments using the campfire-msg-v1: encoding.
type Transport struct {
	cfg    Config
	client *githubClient
	store  *store.Store

	mu           sync.RWMutex
	lastSeen     map[string]time.Time // campfireID -> timestamp of last seen comment
	etagCache    map[string]string    // campfireID -> last ETag from GitHub
	issueNumbers map[string]int       // campfireID -> GitHub Issue number

	stopCh  chan struct{}
	doneCh  chan struct{} // closed by pollLoop when it exits
	running bool
}

// New creates a new Transport. Returns ErrEncryptAtRestNotSupported if
// cfg.EncryptAtRest is true (not yet implemented).
func New(cfg Config, s *store.Store) (*Transport, error) {
	if cfg.EncryptAtRest {
		return nil, ErrEncryptAtRestNotSupported
	}
	if cfg.PollIntervalSecs == 0 {
		cfg.PollIntervalSecs = defaultPollIntervalSecs
	}
	return &Transport{
		cfg:          cfg,
		client:       newGithubClient(cfg.BaseURL, cfg.Token),
		store:        s,
		lastSeen:     make(map[string]time.Time),
		etagCache:    make(map[string]string),
		issueNumbers: make(map[string]int),
		stopCh:       make(chan struct{}),
	}, nil
}

// RegisterCampfire associates a campfire ID with its GitHub Issue number and
// initialises tracking state. Must be called before Poll or Start.
func (t *Transport) RegisterCampfire(campfireID string, issueNumber int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.issueNumbers[campfireID] = issueNumber
	if _, ok := t.lastSeen[campfireID]; !ok {
		t.lastSeen[campfireID] = time.Time{}
	}
	if _, ok := t.etagCache[campfireID]; !ok {
		t.etagCache[campfireID] = ""
	}
}

// Start launches the background poll loop. Returns an error if already running.
func (t *Transport) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running {
		return errors.New("github transport: already running")
	}
	t.running = true
	t.stopCh = make(chan struct{})
	t.doneCh = make(chan struct{})
	go t.pollLoop()
	return nil
}

// Stop shuts down the background poll loop and waits for it to exit.
// It is safe to call Stop multiple times or before Start.
func (t *Transport) Stop() error {
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return nil
	}
	close(t.stopCh)
	t.running = false
	doneCh := t.doneCh
	t.mu.Unlock()
	// Wait for pollLoop to exit so callers know the goroutine is gone.
	<-doneCh
	return nil
}

// pollLoop runs continuously, polling all registered campfires on each tick.
// It closes doneCh when it exits so Stop() can synchronize.
func (t *Transport) pollLoop() {
	defer close(t.doneCh)
	interval := time.Duration(t.cfg.PollIntervalSecs) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.mu.RLock()
			campfireIDs := make([]string, 0, len(t.issueNumbers))
			for id := range t.issueNumbers {
				campfireIDs = append(campfireIDs, id)
			}
			t.mu.RUnlock()

			for _, id := range campfireIDs {
				if _, err := t.Poll(id); err != nil {
					// Log and continue — transient errors should not stop the loop.
					_ = err
				}
			}
		case <-t.stopCh:
			return
		}
	}
}

// Send encodes msg as a campfire-msg-v1: comment and POSTs it to the campfire's
// GitHub Issue. campfireID must have been registered via RegisterCampfire.
func (t *Transport) Send(campfireID string, msg *message.Message) error {
	t.mu.RLock()
	issueNumber, ok := t.issueNumbers[campfireID]
	t.mu.RUnlock()
	if !ok {
		// Fall back to cfg.IssueNumber for single-campfire transports.
		issueNumber = t.cfg.IssueNumber
	}

	body, err := EncodeComment(msg)
	if err != nil {
		return fmt.Errorf("send: encode message: %w", err)
	}

	if err := t.client.CreateComment(t.cfg.Repo, issueNumber, body); err != nil {
		return fmt.Errorf("send: post comment: %w", err)
	}
	return nil
}

// Poll fetches new comments from the campfire's GitHub Issue, decodes them,
// verifies Ed25519 signatures, stores verified messages in SQLite, and returns
// the verified messages. Comments with invalid signatures or non-campfire format
// are silently skipped.
func (t *Transport) Poll(campfireID string) ([]message.Message, error) {
	t.mu.RLock()
	issueNumber, ok := t.issueNumbers[campfireID]
	if !ok {
		issueNumber = t.cfg.IssueNumber
	}
	lastSeen := t.lastSeen[campfireID]
	etag := t.etagCache[campfireID]
	t.mu.RUnlock()

	comments, newEtag, err := t.client.ListComments(t.cfg.Repo, issueNumber, lastSeen, etag)
	if err != nil {
		return nil, fmt.Errorf("poll: list comments: %w", err)
	}

	// Update etag regardless of whether comments arrived.
	t.mu.Lock()
	t.etagCache[campfireID] = newEtag
	t.mu.Unlock()

	if len(comments) == 0 {
		return []message.Message{}, nil
	}

	var result []message.Message
	var latestSeen time.Time

	for _, c := range comments {
		msg, err := DecodeComment(c.Body)
		if err != nil {
			// Not a campfire message or malformed — skip silently.
			continue
		}

		// Verify Ed25519 signature.
		if !msg.VerifySignature() {
			continue
		}

		// Deduplicate: skip if already stored.
		exists, err := t.store.HasMessage(msg.ID)
		if err != nil {
			continue
		}
		if exists {
			if c.CreatedAt.After(latestSeen) {
				latestSeen = c.CreatedAt
			}
			continue
		}

		// Serialize slice fields to JSON for the store schema.
		tagsJSON, _ := json.Marshal(msg.Tags)
		antecedentsJSON, _ := json.Marshal(msg.Antecedents)
		provenanceJSON, _ := json.Marshal(msg.Provenance)

		// Store in SQLite.
		rec := store.MessageRecord{
			ID:          msg.ID,
			CampfireID:  campfireID,
			Sender:      fmt.Sprintf("%x", msg.Sender),
			Payload:     msg.Payload,
			Tags:        string(tagsJSON),
			Antecedents: string(antecedentsJSON),
			Timestamp:   msg.Timestamp,
			Signature:   msg.Signature,
			Provenance:  string(provenanceJSON),
			ReceivedAt:  time.Now().UnixNano(),
			Instance:    msg.Instance,
		}
		if _, err := t.store.AddMessage(rec); err != nil {
			continue
		}

		result = append(result, *msg)
		if c.CreatedAt.After(latestSeen) {
			latestSeen = c.CreatedAt
		}
	}

	// Advance lastSeen cursor only if we saw new comments.
	if !latestSeen.IsZero() {
		t.mu.Lock()
		if latestSeen.After(t.lastSeen[campfireID]) {
			t.lastSeen[campfireID] = latestSeen
		}
		t.mu.Unlock()
	}

	if result == nil {
		return []message.Message{}, nil
	}
	return result, nil
}

// CreateCampfire creates a GitHub Issue for a new campfire and returns the issue number.
// The issue title is "campfire:{campfireID}" and the body contains the campfire's
// public key and description.
func (t *Transport) CreateCampfire(c *campfire.Campfire, description string) (int, error) {
	title := fmt.Sprintf("campfire:%s", c.PublicKeyHex()[:min(16, len(c.PublicKeyHex()))])
	body := fmt.Sprintf("campfire_id: %s\ndescription: %s", c.PublicKeyHex(), description)

	issueNum, err := t.client.CreateIssue(t.cfg.Repo, title, body)
	if err != nil {
		return 0, fmt.Errorf("create campfire issue: %w", err)
	}
	return issueNum, nil
}

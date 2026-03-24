package main

// audit.go — Transparency log for the campfire MCP server (security model §5.e).
//
// The AuditWriter maintains a per-agent audit campfire. Every action the server
// takes on behalf of an agent is recorded as an AuditEntry serialized as JSON
// and sent to the audit campfire with the tag "campfire:audit".
//
// Merkle roots are computed over accumulated entries and published as
// "campfire:audit-root" tagged messages every 1000 entries or 1 hour,
// whichever comes first.
//
// Writes are asynchronous — entries are enqueued on a buffered channel and
// written by a background goroutine so the main request path is not blocked.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// merkleRootInterval defines how many entries trigger a Merkle root publish.
const merkleRootInterval = 1000

// merkleRootMaxAge defines how often a Merkle root is published regardless of entry count.
const merkleRootMaxAge = 1 * time.Hour

// auditChannelSize is the buffer size for the async audit entry channel.
const auditChannelSize = 256

// ---------------------------------------------------------------------------
// AuditEntry
// ---------------------------------------------------------------------------

// AuditEntry records a single server action taken on behalf of an agent.
type AuditEntry struct {
	Sequence    uint64 `json:"sequence"`
	Timestamp   int64  `json:"timestamp"`    // UnixNano
	Action      string `json:"action"`       // "send","join","create","export","invite","revoke"
	AgentKey    string `json:"agent_key"`    // hex-encoded Ed25519 public key
	CampfireID  string `json:"campfire_id"`  // hex campfire ID (if applicable)
	RequestHash string `json:"request_hash"` // SHA-256 of MCP request body
	Commitment  string `json:"commitment,omitempty"` // blind commit (send action only)
}

// ---------------------------------------------------------------------------
// AuditWriter
// ---------------------------------------------------------------------------

// AuditWriter manages a per-agent audit campfire and writes AuditEntry records
// to it asynchronously.
type AuditWriter struct {
	campfireID string
	srv        *server
	agentID    *identity.Identity
	st         store.Store

	ch       chan AuditEntry
	done     chan struct{}
	wg       sync.WaitGroup
	flushReq chan chan struct{}

	// dropped counts entries silently dropped due to a full channel.
	// Accessed atomically; readable from any goroutine via Dropped().
	dropped atomic.Int64

	// Merkle state and sequence counter — accessed only by the background goroutine.
	seq            uint64
	pendingEntries []AuditEntry
	lastRootAt     time.Time
}

// NewAuditWriter initialises an AuditWriter for the given server.
// It creates a dedicated audit campfire (or loads an existing one recorded
// in cfHome/audit-campfire-id) and starts a background write goroutine.
func NewAuditWriter(srv *server) (*AuditWriter, error) {
	agentID, err := identity.Load(srv.identityPath())
	if err != nil {
		return nil, fmt.Errorf("loading identity: %w", err)
	}

	// Resolve or create the audit store.
	st := srv.st
	var ownStore bool
	if st == nil {
		st, err = store.Open(srv.storePath())
		if err != nil {
			return nil, fmt.Errorf("opening store: %w", err)
		}
		ownStore = true
	}

	campfireID, err := loadOrCreateAuditCampfire(srv, agentID, st)
	if err != nil {
		if ownStore {
			st.Close()
		}
		return nil, fmt.Errorf("audit campfire: %w", err)
	}

	aw := &AuditWriter{
		campfireID: campfireID,
		srv:        srv,
		agentID:    agentID,
		st:         st,
		ch:         make(chan AuditEntry, auditChannelSize),
		done:       make(chan struct{}),
		flushReq:   make(chan chan struct{}, 4),
		lastRootAt: time.Now(),
	}

	aw.wg.Add(1)
	go aw.loop()

	return aw, nil
}

// CampfireID returns the audit campfire's hex public key.
func (aw *AuditWriter) CampfireID() string {
	return aw.campfireID
}

// Dropped returns the total number of audit entries dropped due to channel overflow.
func (aw *AuditWriter) Dropped() int64 {
	return aw.dropped.Load()
}

// Log enqueues an audit entry for async writing. Non-blocking: if the channel
// is full the entry is dropped (audit writes must not block the request path).
//
// Sequence numbers are assigned by the write goroutine (consumer side) so that
// only entries that are actually written receive a sequence number. Pre-assigning
// here would consume sequence numbers for dropped entries, creating gaps
// indistinguishable from log tampering.
func (aw *AuditWriter) Log(entry AuditEntry) {
	// Clear any caller-supplied sequence — the write goroutine owns assignment.
	entry.Sequence = 0
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().UnixNano()
	}
	select {
	case aw.ch <- entry:
	default:
		// Channel full — drop rather than block.
		prev := aw.dropped.Add(1)
		if prev == 1 {
			// First drop: emit a one-time warning to stderr so operators notice.
			fmt.Fprintf(os.Stderr, "campfire-mcp: audit channel full — entries are being dropped (action=%s)\n", entry.Action)
		}
	}
}

// Flush blocks until all enqueued entries have been written to the audit campfire.
func (aw *AuditWriter) Flush() {
	done := make(chan struct{})
	select {
	case aw.flushReq <- done:
		<-done
	case <-aw.done:
	}
}

// Close flushes pending entries and stops the background goroutine.
func (aw *AuditWriter) Close() {
	close(aw.done)
	aw.wg.Wait()
}

// loop is the background goroutine that drains the entry channel and writes
// entries to the audit campfire.
func (aw *AuditWriter) loop() {
	defer aw.wg.Done()
	ticker := time.NewTicker(merkleRootMaxAge)
	defer ticker.Stop()

	drainAndFlush := func(flushCh chan struct{}) {
		for {
			select {
			case entry := <-aw.ch:
				aw.writeEntry(entry)
			default:
				if flushCh != nil {
					close(flushCh)
				}
				return
			}
		}
	}

	for {
		select {
		case entry := <-aw.ch:
			aw.writeEntry(entry)

		case flushCh := <-aw.flushReq:
			drainAndFlush(flushCh)

		case <-ticker.C:
			aw.maybePublishRoot(true)

		case <-aw.done:
			drainAndFlush(nil)
			aw.maybePublishRoot(true)
			return
		}
	}
}

// writeEntry serialises entry as JSON and posts it to the audit campfire.
// Sequence numbers are assigned here (consumer side) so that only written
// entries consume a sequence number — dropped entries never do.
func (aw *AuditWriter) writeEntry(entry AuditEntry) {
	aw.seq++
	entry.Sequence = aw.seq
	aw.pendingEntries = append(aw.pendingEntries, entry)

	payload, err := json.Marshal(entry)
	if err != nil {
		return
	}

	_ = aw.postMessage(string(payload), []string{"campfire:audit"})

	if len(aw.pendingEntries) >= merkleRootInterval {
		aw.maybePublishRoot(false)
	}
}

// postMessage sends a message to the audit campfire signed by the agent's key.
func (aw *AuditWriter) postMessage(payload string, tags []string) error {
	fsT := aw.srv.fsTransport()
	state, err := fsT.ReadState(aw.campfireID)
	if err != nil {
		return fmt.Errorf("reading audit campfire state: %w", err)
	}
	members, err := fsT.ListMembers(aw.campfireID)
	if err != nil {
		return fmt.Errorf("listing audit campfire members: %w", err)
	}

	msg, err := message.NewMessage(
		aw.agentID.PrivateKey, aw.agentID.PublicKey,
		[]byte(payload), tags, nil,
	)
	if err != nil {
		return fmt.Errorf("creating audit message: %w", err)
	}

	cf := campfireFromState(state, members)
	if err := msg.AddHop(
		state.PrivateKey, state.PublicKey,
		cf.MembershipHash(), len(members),
		state.JoinProtocol, state.ReceptionRequirements,
		campfire.RoleFull,
	); err != nil {
		return fmt.Errorf("adding provenance hop: %w", err)
	}

	if aw.srv.httpTransport != nil {
		if _, err := aw.st.AddMessage(store.MessageRecordFromMessage(aw.campfireID, msg, store.NowNano())); err != nil {
			return fmt.Errorf("storing audit message: %w", err)
		}
		aw.srv.httpTransport.PollBrokerNotify(aw.campfireID)
	} else {
		if err := fsT.WriteMessage(aw.campfireID, msg); err != nil {
			return fmt.Errorf("writing audit message: %w", err)
		}
	}
	return nil
}

// maybePublishRoot publishes a Merkle root over all pending entries.
// If force is false, only publishes if the interval threshold is reached.
func (aw *AuditWriter) maybePublishRoot(force bool) {
	if len(aw.pendingEntries) == 0 {
		return
	}
	if !force && len(aw.pendingEntries) < merkleRootInterval {
		return
	}

	root := computeMerkleRoot(aw.pendingEntries)
	payload := fmt.Sprintf(`{"merkle_root":%q,"entry_count":%d,"computed_at":%d}`,
		root, len(aw.pendingEntries), time.Now().UnixNano())

	_ = aw.postMessage(payload, []string{"campfire:audit-root"})

	// Reset for the next batch.
	aw.pendingEntries = nil
	aw.lastRootAt = time.Now()
}

// ---------------------------------------------------------------------------
// Merkle tree
// ---------------------------------------------------------------------------

// computeMerkleRoot builds a simple binary Merkle tree over the given entries.
// Each entry is serialised as JSON and hashed with SHA-256. The tree is built
// bottom-up by repeatedly hashing pairs of nodes until one root remains.
// Returns the root as a lowercase hex string.
func computeMerkleRoot(entries []AuditEntry) string {
	if len(entries) == 0 {
		return ""
	}

	// Leaf hashes.
	hashes := make([][]byte, len(entries))
	for i, e := range entries {
		b, _ := json.Marshal(e)
		h := sha256.Sum256(b)
		hashes[i] = h[:]
	}

	// Build tree.
	for len(hashes) > 1 {
		var next [][]byte
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				combined := make([]byte, len(hashes[i])+len(hashes[i+1]))
				copy(combined, hashes[i])
				copy(combined[len(hashes[i]):], hashes[i+1])
				h := sha256.Sum256(combined)
				next = append(next, h[:])
			} else {
				// Odd node: promote without pairing.
				next = append(next, hashes[i])
			}
		}
		hashes = next
	}

	return hex.EncodeToString(hashes[0])
}

// ---------------------------------------------------------------------------
// Audit campfire lifecycle
// ---------------------------------------------------------------------------

// auditCampfireIDFile is the filename within cfHome where the audit campfire ID is persisted.
const auditCampfireIDFile = "audit-campfire-id"

// loadOrCreateAuditCampfire loads the audit campfire ID from disk, or creates
// a new audit campfire and persists its ID.
func loadOrCreateAuditCampfire(srv *server, agentID *identity.Identity, st store.Store) (string, error) {
	idPath := srv.cfHome + "/" + auditCampfireIDFile
	if data, err := os.ReadFile(idPath); err == nil {
		campfireID := string(data)
		if campfireID != "" {
			return campfireID, nil
		}
	}

	// Create a new campfire for audit logs.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		return "", fmt.Errorf("creating audit campfire: %w", err)
	}
	cf.AddMember(agentID.PublicKey)

	fsT := srv.fsTransport()
	if err := fsT.Init(cf); err != nil {
		return "", fmt.Errorf("initializing audit campfire state: %w", err)
	}

	now := time.Now().UnixNano()
	if err := fsT.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  now,
	}); err != nil {
		return "", fmt.Errorf("writing audit campfire member: %w", err)
	}

	if err := st.AddMembership(store.Membership{
		CampfireID:   cf.PublicKeyHex(),
		TransportDir: fsT.CampfireDir(cf.PublicKeyHex()),
		JoinProtocol: "open",
		Role:         campfire.RoleFull,
		JoinedAt:     now,
		Description:  "audit log",
	}); err != nil {
		return "", fmt.Errorf("recording audit campfire membership: %w", err)
	}

	// Persist the audit campfire ID so subsequent sessions reuse the same campfire.
	if err := os.WriteFile(idPath, []byte(cf.PublicKeyHex()), 0600); err != nil {
		// Non-fatal: we have the ID in memory.
		_ = err
	}

	return cf.PublicKeyHex(), nil
}

// ---------------------------------------------------------------------------
// requestHash helper
// ---------------------------------------------------------------------------

// requestHash computes SHA-256 of the given MCP request body bytes.
func requestHash(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

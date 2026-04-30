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
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// merkleRootInterval defines how many entries trigger a Merkle root publish.
const merkleRootInterval = 1000

// merkleRootMaxAge defines how often a Merkle root is published regardless of entry count.
const merkleRootMaxAge = 1 * time.Hour

// auditChannelSize is the buffer size for the async audit entry channel.
const auditChannelSize = 256

// instanceIDShift is the bit position of the instance seed within a sequence uint64.
// Bits 48–63 carry the 16-bit instance seed; bits 0–47 carry the per-instance counter.
// This partitioning keeps sequence numbers unique across instances without external
// coordination. Old entries (written before this change) have instance seed = 0 in
// the high bits and are treated as the legacy "instance 0" group by detectSequenceGaps.
const instanceIDShift = 48

// instanceIDMask masks the 16-bit instance seed stored in bits 48–63 of a sequence.
const instanceIDMask = uint64(0xFFFF) << instanceIDShift

// localSeqMask masks the 48-bit per-instance counter stored in bits 0–47.
const localSeqMask = uint64(0x0000FFFFFFFFFFFF)

// newInstanceSeed derives a 16-bit instance seed from the Azure Functions instance
// environment variable WEBSITE_INSTANCE_ID, falling back to random bytes.
// The seed is returned pre-shifted into bits 48–63 of a uint64.
// A seed of 0 is reserved for legacy entries written before this feature.
func newInstanceSeed() uint64 {
	// Azure Functions sets WEBSITE_INSTANCE_ID to a unique string per instance.
	if envID := os.Getenv("WEBSITE_INSTANCE_ID"); envID != "" {
		h := sha256.Sum256([]byte(envID))
		seed := binary.BigEndian.Uint16(h[:2])
		if seed == 0 {
			seed = 1 // reserve 0 for legacy/no-instance entries
		}
		return uint64(seed) << instanceIDShift
	}
	// Fall back to random 16-bit seed so parallel instances don't collide.
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err == nil {
		seed := binary.BigEndian.Uint16(buf[:])
		if seed == 0 {
			seed = 1
		}
		return uint64(seed) << instanceIDShift
	}
	// Absolute fallback: use a fixed non-zero seed so the instance is still
	// distinguishable from legacy entries (instance seed = 0).
	return uint64(1) << instanceIDShift
}

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

	// written counts entries successfully written to the audit campfire.
	// Incremented by the background goroutine after each writeEntry call.
	// Readable from any goroutine via Written().
	written atomic.Int64

	// Merkle state and sequence counter — accessed only by the background goroutine.
	//
	// instanceSeed occupies bits 48–63 of every sequence number written by this
	// AuditWriter. It is derived once at startup from WEBSITE_INSTANCE_ID (Azure
	// Functions) or random bytes, ensuring that parallel instances never produce
	// overlapping sequence numbers even though each starts its local counter at 0.
	//
	// seq counts written entries within this instance (0–2^48-1). The full
	// sequence number stored in AuditEntry.Sequence is (instanceSeed | seq),
	// where instanceSeed is already pre-shifted into bits 48–63.
	instanceSeed   uint64
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
		campfireID:   campfireID,
		srv:          srv,
		agentID:      agentID,
		st:           st,
		ch:           make(chan AuditEntry, auditChannelSize),
		done:         make(chan struct{}),
		flushReq:     make(chan chan struct{}, 4),
		lastRootAt:   time.Now(),
		instanceSeed: newInstanceSeed(),
	}

	aw.wg.Add(1)
	go aw.loop()

	return aw, nil
}

// CampfireID returns the audit campfire's hex public key.
func (aw *AuditWriter) CampfireID() string {
	return aw.campfireID
}

// globalStore returns the global (non-namespaced) store for cross-instance
// campfire state lookup, or nil if one is not configured.
func (aw *AuditWriter) globalStore() store.Store {
	if aw.srv == nil || aw.srv.transportRouter == nil {
		return nil
	}
	return aw.srv.transportRouter.GlobalStore()
}

// Dropped returns the total number of audit entries dropped due to channel overflow.
func (aw *AuditWriter) Dropped() int64 {
	return aw.dropped.Load()
}

// Written returns the total number of audit entries successfully written to the
// audit campfire. Incremented atomically after each writeEntry call.
func (aw *AuditWriter) Written() int64 {
	return aw.written.Load()
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
//
// The full sequence number encodes both the instance seed (bits 48–63) and
// the per-instance counter (bits 0–47), ensuring uniqueness across instances.
func (aw *AuditWriter) writeEntry(entry AuditEntry) {
	aw.seq++
	// Compose the globally unique sequence: instance seed in high bits, local counter in low bits.
	entry.Sequence = aw.instanceSeed | (aw.seq & localSeqMask)
	aw.pendingEntries = append(aw.pendingEntries, entry)

	payload, err := json.Marshal(entry)
	if err != nil {
		return
	}

	_ = aw.postMessage(string(payload), []string{campfire.TagAudit})
	aw.written.Add(1)

	if len(aw.pendingEntries) >= merkleRootInterval {
		aw.maybePublishRoot(false)
	}
}

// postMessage sends a message to the audit campfire signed by the agent's key.
func (aw *AuditWriter) postMessage(payload string, tags []string) error {
	fsT := aw.srv.fsTransport()
	state, stateErr := fsT.ReadState(aw.campfireID)
	if stateErr != nil {
		// Local CBOR not found — try global store for cross-instance cold-start recovery.
		// This mirrors the KeyProvider fallback in session.go (lines 871–885).
		if gs := aw.globalStore(); gs != nil {
			if gm, gerr := gs.GetMembership(aw.campfireID); gerr == nil && gm != nil && gm.CampfireID == aw.campfireID && gm.CampfirePrivKey != "" {
				pk, decErr := hex.DecodeString(gm.CampfirePrivKey)
				if decErr == nil && len(pk) == ed25519.PrivateKeySize {
					pub := ed25519.PrivateKey(pk).Public().(ed25519.PublicKey)
					state = &campfire.CampfireState{
						PublicKey:    pub,
						PrivateKey:   pk,
						JoinProtocol: "open",
						Threshold:    1,
					}
					stateErr = nil
				}
			}
		}
		if stateErr != nil {
			return fmt.Errorf("reading audit campfire state: %w", stateErr)
		}
	}
	members, err := fsT.ListMembers(aw.campfireID)
	if err != nil {
		// Tolerate missing local members when state was reconstructed from global store.
		// The audit campfire has a single member (the agent identity).
		members = []campfire.MemberRecord{{
			PublicKey: aw.agentID.PublicKey,
			JoinedAt:  store.NowNano(),
		}}
	}

	auditSigner := aw.agentID.NewSigner()
	msg, err := message.NewMessage(auditSigner, []byte(payload), tags, nil)
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

	_ = aw.postMessage(payload, []string{campfire.TagAuditRoot})

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

// auditSessionStore is the interface used by loadOrCreateAuditCampfire for
// cloud persistence of the audit campfire ID. The real implementation is
// aztable.SessionStore; tests inject a fake in-memory implementation.
type auditSessionStore interface {
	SaveAuditCampfireID(agentKey, campfireID string) error
	LoadAuditCampfireID(agentKey string) (string, bool, error)
}

// loadOrCreateAuditCampfire loads the audit campfire ID using a three-level lookup:
//  1. Local file (cfHome/audit-campfire-id) — fast path for the same instance.
//  2. Cloud SessionStore (Azure Table Storage) — for cold starts on a new instance.
//  3. Create a new audit campfire and persist to both local file and cloud.
//
// When a new campfire is created, it is also registered in the global store
// (if available) with CampfirePrivKey set, so that other instances can reconstruct
// the campfire state and post audit messages without holding local CBOR.
//
// Concurrent cold start safety: SaveAuditCampfireID uses insert-if-not-exists
// semantics. After saving, the ID is loaded back to determine which instance
// won the race. If another instance wrote first, the winner's ID is used and
// the locally created campfire is abandoned.
func loadOrCreateAuditCampfire(srv *server, agentID *identity.Identity, st store.Store) (string, error) {
	return loadOrCreateAuditCampfireWithStore(srv, agentID, st, sessionStoreFrom(srv))
}

// loadOrCreateAuditCampfireWithStore is the implementation of
// loadOrCreateAuditCampfire that accepts an explicit auditSessionStore. This
// allows tests to inject a fake in-memory store without needing Azure Table
// Storage. Production code calls loadOrCreateAuditCampfire which resolves the
// store from the server.
func loadOrCreateAuditCampfireWithStore(srv *server, agentID *identity.Identity, st store.Store, ss auditSessionStore) (string, error) {
	idPath := srv.cfHome + "/" + auditCampfireIDFile

	// Level 1: local file.
	if data, err := os.ReadFile(idPath); err == nil {
		campfireID := string(data)
		if campfireID != "" {
			return campfireID, nil
		}
	}

	// Level 2: cloud SessionStore.
	if ss != nil {
		if id, ok, err := ss.LoadAuditCampfireID(agentID.PublicKeyHex()); err == nil && ok && id != "" {
			// Found in cloud — write to local file so subsequent lookups are fast.
			_ = os.WriteFile(idPath, []byte(id), 0600)
			return id, nil
		}
	}

	// Level 3: create a new campfire for audit logs.
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

	campfireID := cf.PublicKeyHex()
	campfirePrivKeyHex := fmt.Sprintf("%x", cf.PrivateKey)

	// Persist to local file (tentative — may be overwritten below if we lose the race).
	_ = os.WriteFile(idPath, []byte(campfireID), 0600)

	// Persist to cloud SessionStore with insert-if-not-exists semantics.
	// If another instance already wrote an ID, the insert is a no-op.
	// We then load back to find out which ID actually won the race.
	if ss != nil {
		if saveErr := ss.SaveAuditCampfireID(agentID.PublicKeyHex(), campfireID); saveErr != nil {
			// Save failed (network error, etc.) — continue with the locally created ID.
			// A subsequent cold start will retry the cloud lookup.
			_ = saveErr
		} else {
			// Verify we won the race: load back the canonical ID from cloud.
			if canonicalID, ok, loadErr := ss.LoadAuditCampfireID(agentID.PublicKeyHex()); loadErr == nil && ok && canonicalID != "" && canonicalID != campfireID {
				// Another instance won the race — use the canonical ID and update local file.
				campfireID = canonicalID
				_ = os.WriteFile(idPath, []byte(campfireID), 0600)
				// Return early: the winning instance already registered in global store.
				return campfireID, nil
			}
		}
	}

	// Register in global store with CampfirePrivKey so other instances can
	// reconstruct the campfire state for postMessage (global store fallback path).
	if gs := globalStoreFrom(srv); gs != nil {
		_ = gs.AddMembership(store.Membership{
			CampfireID:      campfireID,
			TransportDir:    srv.externalAddr,
			JoinProtocol:    "open",
			Role:            campfire.RoleFull,
			JoinedAt:        now,
			Threshold:       1,
			Description:     "audit log",
			CreatorPubkey:   agentID.PublicKeyHex(),
			CampfirePrivKey: campfirePrivKeyHex,
		})
	}

	return campfireID, nil
}

// sessionStoreFrom returns the SessionStore from srv as an auditSessionStore,
// or nil if not available. The SessionStore is only present in hosted HTTP
// mode with Azure Table Storage.
func sessionStoreFrom(srv *server) auditSessionStore {
	if srv == nil || srv.sessManager == nil {
		return nil
	}
	ss := srv.sessManager.sessionStore
	if ss == nil {
		return nil
	}
	return ss
}

// globalStoreFrom returns the global (non-namespaced) store from the transport
// router, or nil if not configured.
func globalStoreFrom(srv *server) store.Store {
	if srv == nil || srv.transportRouter == nil {
		return nil
	}
	return srv.transportRouter.GlobalStore()
}

// ---------------------------------------------------------------------------
// Anomaly detection
// ---------------------------------------------------------------------------

// detectSequenceGaps analyses a set of sequence numbers from audit log entries
// and returns a list of human-readable anomaly descriptions.
//
// Sequence numbers encode both an instance seed (bits 48–63) and a per-instance
// counter (bits 0–47). Entries are first grouped by instance seed, then each
// group is checked for gaps within its local counter sequence. This ensures that
// parallel Azure Functions instances — each starting at counter=0 — do not
// produce false gap anomalies for each other's entries.
//
// Legacy entries written before this feature have instance seed = 0 and are
// grouped together and analysed as a single sequence (backwards compatible).
//
// Returns a non-nil (possibly empty) slice so callers always get a JSON array.
func detectSequenceGaps(seqs []uint64) []string {
	anomalies := []string{}
	if len(seqs) < 2 {
		return anomalies
	}

	// Group sequences by instance seed (bits 48–63).
	groups := make(map[uint64][]uint64)
	for _, s := range seqs {
		seed := s >> instanceIDShift
		local := s & localSeqMask
		groups[seed] = append(groups[seed], local)
	}

	// For each instance group, sort and scan for gaps.
	for seed, locals := range groups {
		// Sort ascending (simple insertion sort — audit logs are typically small).
		for i := 1; i < len(locals); i++ {
			for j := i; j > 0 && locals[j] < locals[j-1]; j-- {
				locals[j], locals[j-1] = locals[j-1], locals[j]
			}
		}

		instanceLabel := ""
		if seed > 0 {
			instanceLabel = fmt.Sprintf(" (instance %d)", seed)
		}

		for i := 1; i < len(locals); i++ {
			prev, cur := locals[i-1], locals[i]
			if cur <= prev {
				anomalies = append(anomalies,
					fmt.Sprintf("duplicate sequence number: seq %d appears more than once%s", cur, instanceLabel))
				continue
			}
			if gap := cur - prev; gap > 1 {
				anomalies = append(anomalies,
					fmt.Sprintf("sequence gap: missing %d entries between seq %d and seq %d%s", gap-1, prev, cur, instanceLabel))
			}
		}
	}

	return anomalies
}

// ---------------------------------------------------------------------------
// requestHash helper
// ---------------------------------------------------------------------------

// requestHash computes SHA-256 of the given MCP request body bytes.
func requestHash(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

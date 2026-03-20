package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS campfire_memberships (
    campfire_id     TEXT PRIMARY KEY,
    transport_dir   TEXT NOT NULL,
    join_protocol   TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'full',
    joined_at       INTEGER NOT NULL,
    threshold       INTEGER NOT NULL DEFAULT 1,
    description     TEXT NOT NULL DEFAULT '',
    creator_pubkey  TEXT NOT NULL DEFAULT '',
    transport_type  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS messages (
    id             TEXT PRIMARY KEY,
    campfire_id    TEXT NOT NULL,
    sender         TEXT NOT NULL,
    payload        BLOB NOT NULL,
    tags           TEXT NOT NULL DEFAULT '[]',
    antecedents    TEXT NOT NULL DEFAULT '[]',
    timestamp      INTEGER NOT NULL,
    signature      BLOB NOT NULL,
    provenance     TEXT NOT NULL DEFAULT '[]',
    received_at    INTEGER NOT NULL,
    instance       TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (campfire_id) REFERENCES campfire_memberships(campfire_id)
);

CREATE INDEX IF NOT EXISTS idx_messages_campfire_ts ON messages(campfire_id, timestamp);

CREATE TABLE IF NOT EXISTS read_cursors (
    campfire_id    TEXT PRIMARY KEY,
    last_read_at   INTEGER NOT NULL,
    FOREIGN KEY (campfire_id) REFERENCES campfire_memberships(campfire_id)
);

CREATE TABLE IF NOT EXISTS filters (
    campfire_id    TEXT NOT NULL,
    direction      TEXT NOT NULL,
    pass_through   TEXT NOT NULL DEFAULT '[]',
    suppress       TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (campfire_id, direction),
    FOREIGN KEY (campfire_id) REFERENCES campfire_memberships(campfire_id)
);

CREATE TABLE IF NOT EXISTS peer_endpoints (
    campfire_id       TEXT NOT NULL,
    member_pubkey     TEXT NOT NULL,
    endpoint          TEXT NOT NULL,
    participant_id    INTEGER NOT NULL DEFAULT 0,
    role              TEXT NOT NULL DEFAULT 'member',
    PRIMARY KEY (campfire_id, member_pubkey)
);

CREATE TABLE IF NOT EXISTS threshold_shares (
    campfire_id      TEXT PRIMARY KEY,
    participant_id   INTEGER NOT NULL,
    secret_share     BLOB NOT NULL,
    public_data      BLOB
);

-- Stores pending DKG shares that the creator distributes to joining members.
-- Each row is a serialized DKGResult for participant_id that has not yet been claimed.
-- The creator pre-generates all participant shares during campfire creation.
CREATE TABLE IF NOT EXISTS pending_threshold_shares (
    campfire_id      TEXT NOT NULL,
    participant_id   INTEGER NOT NULL,
    share_data       BLOB NOT NULL,
    PRIMARY KEY (campfire_id, participant_id)
);

-- Migration tracking: one row per applied migration, in order.
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INTEGER PRIMARY KEY,
    description TEXT    NOT NULL,
    applied_at  INTEGER NOT NULL
);
`
// migration describes a single schema migration step.
type migration struct {
	version     int
	description string
	// sql is the DDL or DML statement to execute. For idempotent DDL (ALTER TABLE
	// ADD COLUMN) SQLite returns "duplicate column name" when the column already
	// exists; runMigrations treats that as success so the migration is recorded.
	sql string
}

// schemaMigrations is the ordered list of all schema migrations. New migrations
// must be appended to the end. Versions are 1-based and must be consecutive.
var schemaMigrations = []migration{
	{
		version:     1,
		description: "add instance column to messages",
		sql:         "ALTER TABLE messages ADD COLUMN instance TEXT NOT NULL DEFAULT ''",
	},
	{
		version:     2,
		description: "add description column to campfire_memberships",
		sql:         "ALTER TABLE campfire_memberships ADD COLUMN description TEXT NOT NULL DEFAULT ''",
	},
	{
		version:     3,
		description: "add creator_pubkey column to campfire_memberships",
		sql:         "ALTER TABLE campfire_memberships ADD COLUMN creator_pubkey TEXT NOT NULL DEFAULT ''",
	},
	{
		version:     4,
		description: "add role column to peer_endpoints",
		sql:         "ALTER TABLE peer_endpoints ADD COLUMN role TEXT NOT NULL DEFAULT 'member'",
	},
	{
		version:     5,
		description: "add transport_type column to campfire_memberships",
		sql:         "ALTER TABLE campfire_memberships ADD COLUMN transport_type TEXT NOT NULL DEFAULT ''",
	},
}

// runMigrations applies any unapplied migrations in schemaMigrations to db.
// Each migration is recorded in schema_migrations after successful execution.
// ALTER TABLE ADD COLUMN failures due to the column already existing are treated
// as success (idempotent for databases upgraded by the previous bare-Exec code).
// Any other error (disk full, corruption) is returned so Open fails loudly.
func runMigrations(db *sql.DB) error {
	for _, m := range schemaMigrations {
		var count int
		err := db.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version,
		).Scan(&count)
		if err != nil {
			return fmt.Errorf("checking migration %d: %w", m.version, err)
		}
		if count > 0 {
			// Already applied.
			continue
		}
		if _, execErr := db.Exec(m.sql); execErr != nil {
			// SQLite reports "duplicate column name" when the column already exists.
			// Treat this as a no-op so that databases upgraded by the previous
			// bare-Exec code are accepted by the new migration runner.
			if !strings.Contains(execErr.Error(), "duplicate column name") {
				return fmt.Errorf("migration %d (%s): %w", m.version, m.description, execErr)
			}
		}
		if _, err := db.Exec(
			`INSERT INTO schema_migrations (version, description, applied_at) VALUES (?, ?, ?)`,
			m.version, m.description, time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("recording migration %d: %w", m.version, err)
		}
	}
	return nil
}


// PeerRole constants define the peer endpoint role namespace.
//
// This is a separate namespace from the campfire membership role (defined in
// pkg/campfire as RoleObserver/RoleWriter/RoleFull). The two roles live on
// different types with different semantics:
//
//   - Membership role (campfire.Member.Role): governs what a member may do
//     within a campfire — "observer", "writer", or "full". Managed by the
//     campfire owner and stored in campfire.MemberRecord files on disk.
//   - Peer endpoint role (store.PeerEndpoint.Role): records the peer's
//     standing relative to this agent's local store — "creator" or "member".
//     Used for server-side delivery enforcement in handleDeliver. Stored in
//     the peer_endpoints SQLite table. "creator" implies the peer founded the
//     campfire (full access); "member" is the backward-compatible default.
//
// campfire.EffectiveRole maps legacy peer roles ("creator", "member") → RoleFull
// for backward compatibility with older wire formats. These constants are the
// canonical source for any code that sets or compares PeerEndpoint.Role values.
const (
	// PeerRoleCreator is assigned to the peer that created the campfire.
	// Implies full access (read, write, and sign system messages).
	PeerRoleCreator = "creator"
	// PeerRoleMember is the default role for all other peers.
	// Implies full access (read and write), but not campfire creation rights.
	PeerRoleMember = "member"
)

// supersededCacheEntry is a cached result of collectSupersededIDs for a campfire.
// It is valid as long as the maximum compaction event timestamp hasn't changed.
type supersededCacheEntry struct {
	maxCompactionTS int64
	superseded      map[string]bool
}

// Store is the local SQLite database for an agent.
type Store struct {
	db *sql.DB

	// supersededCache caches the superseded-ID sets per campfire to avoid
	// fetching and parsing all compaction payloads on every ListMessages call.
	// Key: campfireID. Cache is invalidated when max compaction timestamp changes.
	// Cross-campfire queries (campfireID=="") are not cached.
	// (Fix for workspace-x9p: O(events × ids) work on every ListMessages call.)
	supersededMu    sync.RWMutex
	supersededCache map[string]supersededCacheEntry
}

// Membership represents a campfire membership record.
type Membership struct {
	CampfireID    string `json:"campfire_id"`
	TransportDir  string `json:"transport_dir"`
	JoinProtocol  string `json:"join_protocol"`
	Role          string `json:"role"`
	JoinedAt      int64  `json:"joined_at"`
	Threshold     uint   `json:"threshold"`
	Description   string `json:"description"`
	CreatorPubkey string `json:"creator_pubkey"`
	// TransportType is the explicit transport type for this membership.
	// Valid values: "filesystem", "github", "p2p-http", "".
	// Empty string means the type was inferred from TransportDir by
	// inferTransportType() at insert time (backward-compatible for legacy rows).
	// transport.ResolveType uses this field directly when non-empty.
	TransportType string `json:"transport_type,omitempty"`
}

// inferTransportType applies the legacy heuristic to determine a transport type
// string from a campfire ID and transport directory. This is used at insert time
// (AddMembership) and during migration backfill so that ResolveType never needs
// to make a filesystem call on the hot path.
func inferTransportType(campfireID, transportDir string) string {
	if strings.HasPrefix(transportDir, "github:") {
		return "github"
	}
	if transportDir != "" && campfireID != "" {
		statePath := filepath.Join(transportDir, campfireID+".cbor")
		if _, err := os.Stat(statePath); err == nil {
			return "p2p-http"
		}
	}
	return "filesystem"
}

// backfillTransportTypes sets transport_type for rows where it is currently empty.
// This runs once per Open() call after the column migration. The p2p-http
// detection makes a filesystem call here (migration time) rather than on every
// ResolveType invocation, eliminating repeated hot-path I/O.
//
// A COUNT(*) guard is checked first so that already-migrated databases incur
// only a single cheap query rather than a full table scan on every startup.
func backfillTransportTypes(db *sql.DB) error {
	var needsBackfill int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM campfire_memberships WHERE transport_type = ''`,
	).Scan(&needsBackfill); err != nil {
		return fmt.Errorf("counting rows needing transport_type backfill: %w", err)
	}
	if needsBackfill == 0 {
		return nil
	}

	rows, err := db.Query(
		`SELECT campfire_id, transport_dir FROM campfire_memberships WHERE transport_type = ''`,
	)
	if err != nil {
		return fmt.Errorf("querying rows for transport_type backfill: %w", err)
	}
	defer rows.Close()

	type row struct {
		campfireID   string
		transportDir string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.campfireID, &r.transportDir); err != nil {
			return fmt.Errorf("scanning membership for backfill: %w", err)
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	for _, r := range pending {
		tt := inferTransportType(r.campfireID, r.transportDir)
		if _, err := db.Exec(
			`UPDATE campfire_memberships SET transport_type = ? WHERE campfire_id = ? AND transport_type = ''`,
			tt, r.campfireID,
		); err != nil {
			return fmt.Errorf("updating transport_type for %s: %w", r.campfireID, err)
		}
	}
	return nil
}

// Open opens or creates the SQLite store at the given path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating store directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}
	// Apply versioned migrations. Each migration is tracked in schema_migrations
	// and executed at most once. ALTER TABLE failures due to the column already
	// existing (from pre-migration deployments) are treated as success. Any other
	// error (disk full, corruption) surfaces and aborts Open.
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	// Backfill transport_type for legacy rows where it is empty.
	// Uses inferTransportType() which mirrors the old transport.ResolveType heuristic.
	// This runs the filesystem probe (for p2p-http detection) once at migration time
	// rather than on every ResolveType call, eliminating hot-path I/O.
	// A COUNT(*) guard inside backfillTransportTypes ensures already-migrated
	// databases skip the full table scan on subsequent startups.
	if err := backfillTransportTypes(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("backfilling transport types: %w", err)
	}
	return &Store{
		db:              db,
		supersededCache: make(map[string]supersededCacheEntry),
	}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// AddMembership records that this agent is a member of a campfire.
// If TransportType is empty, it is inferred from TransportDir using inferTransportType.
func (s *Store) AddMembership(m Membership) error {
	threshold := m.Threshold
	if threshold == 0 {
		threshold = 1
	}
	tt := m.TransportType
	if tt == "" {
		tt = inferTransportType(m.CampfireID, m.TransportDir)
	}
	_, err := s.db.Exec(
		`INSERT INTO campfire_memberships (campfire_id, transport_dir, join_protocol, role, joined_at, threshold, description, creator_pubkey, transport_type)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.CampfireID, m.TransportDir, m.JoinProtocol, m.Role, m.JoinedAt, threshold, m.Description, m.CreatorPubkey, tt,
	)
	if err != nil {
		return fmt.Errorf("adding membership: %w", err)
	}
	return nil
}

// UpdateMembershipRole updates the role field for an existing membership.
func (s *Store) UpdateMembershipRole(campfireID, role string) error {
	res, err := s.db.Exec(`UPDATE campfire_memberships SET role = ? WHERE campfire_id = ?`, role, campfireID)
	if err != nil {
		return fmt.Errorf("updating membership role: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("membership not found: %s", campfireID)
	}
	return nil
}

// RemoveMembership removes a campfire membership.
func (s *Store) RemoveMembership(campfireID string) error {
	_, err := s.db.Exec(`DELETE FROM campfire_memberships WHERE campfire_id = ?`, campfireID)
	if err != nil {
		return fmt.Errorf("removing membership: %w", err)
	}
	return nil
}

// GetMembership returns a single membership by campfire ID.
func (s *Store) GetMembership(campfireID string) (*Membership, error) {
	row := s.db.QueryRow(
		`SELECT campfire_id, transport_dir, join_protocol, role, joined_at, threshold, description, creator_pubkey, transport_type
		 FROM campfire_memberships WHERE campfire_id = ?`, campfireID,
	)
	var m Membership
	err := row.Scan(&m.CampfireID, &m.TransportDir, &m.JoinProtocol, &m.Role, &m.JoinedAt, &m.Threshold, &m.Description, &m.CreatorPubkey, &m.TransportType)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying membership: %w", err)
	}
	return &m, nil
}

// ListMemberships returns all campfire memberships.
func (s *Store) ListMemberships() ([]Membership, error) {
	rows, err := s.db.Query(
		`SELECT campfire_id, transport_dir, join_protocol, role, joined_at, threshold, description, creator_pubkey, transport_type
		 FROM campfire_memberships ORDER BY joined_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing memberships: %w", err)
	}
	defer rows.Close()

	var memberships []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.CampfireID, &m.TransportDir, &m.JoinProtocol, &m.Role, &m.JoinedAt, &m.Threshold, &m.Description, &m.CreatorPubkey, &m.TransportType); err != nil {
			return nil, fmt.Errorf("scanning membership: %w", err)
		}
		memberships = append(memberships, m)
	}
	return memberships, rows.Err()
}

// MessageRecord is a stored message.
// Tags, Antecedents, and Provenance are typed Go values; JSON serialization
// to/from SQLite TEXT columns is handled at the store boundary (marshal on
// write, unmarshal on read) so callers never deal with raw JSON strings.
type MessageRecord struct {
	ID          string                  `json:"id"`
	CampfireID  string                  `json:"campfire_id"`
	Sender      string                  `json:"sender"`
	Payload     []byte                  `json:"payload"`
	Tags        []string                `json:"tags"`
	Antecedents []string                `json:"antecedents"`
	Timestamp   int64                   `json:"timestamp"`
	Signature   []byte                  `json:"signature"`
	Provenance  []message.ProvenanceHop `json:"provenance"`
	ReceivedAt  int64                   `json:"received_at"`
	// Instance is tainted (sender-asserted, not verified) metadata identifying
	// the sender's role or instance name. Empty string for backward compatibility.
	Instance string `json:"instance,omitempty"`
}

// rawMessageRecord is used for scanning SQL rows where Tags, Antecedents, and
// Provenance are stored as JSON text. scanMessageRecord converts it to a clean
// MessageRecord at the store boundary.
type rawMessageRecord struct {
	ID          string
	CampfireID  string
	Sender      string
	Payload     []byte
	Tags        string
	Antecedents string
	Timestamp   int64
	Signature   []byte
	Provenance  string
	ReceivedAt  int64
	Instance    string
}

// toMessageRecord deserializes JSON fields from rawMessageRecord into a typed
// MessageRecord. Malformed JSON in any field is treated as an empty slice.
func (r rawMessageRecord) toMessageRecord() MessageRecord {
	var tags []string
	if err := json.Unmarshal([]byte(r.Tags), &tags); err != nil || tags == nil {
		tags = []string{}
	}
	var antecedents []string
	if err := json.Unmarshal([]byte(r.Antecedents), &antecedents); err != nil || antecedents == nil {
		antecedents = []string{}
	}
	var provenance []message.ProvenanceHop
	if err := json.Unmarshal([]byte(r.Provenance), &provenance); err != nil || provenance == nil {
		provenance = []message.ProvenanceHop{}
	}
	return MessageRecord{
		ID:          r.ID,
		CampfireID:  r.CampfireID,
		Sender:      r.Sender,
		Payload:     r.Payload,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   r.Timestamp,
		Signature:   r.Signature,
		Provenance:  provenance,
		ReceivedAt:  r.ReceivedAt,
		Instance:    r.Instance,
	}
}

// scanMessageRecord scans the next row into a MessageRecord, deserializing
// JSON text columns at the store boundary.
func scanMessageRecord(scan func(dest ...any) error) (MessageRecord, error) {
	var r rawMessageRecord
	if err := scan(&r.ID, &r.CampfireID, &r.Sender, &r.Payload, &r.Tags, &r.Antecedents, &r.Timestamp, &r.Signature, &r.Provenance, &r.ReceivedAt, &r.Instance); err != nil {
		return MessageRecord{}, err
	}
	return r.toMessageRecord(), nil
}

// AddMessage inserts a message if not already present. Returns true if inserted.
func (s *Store) AddMessage(m MessageRecord) (bool, error) {
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if m.Antecedents == nil {
		m.Antecedents = []string{}
	}
	if m.Provenance == nil {
		m.Provenance = []message.ProvenanceHop{}
	}
	tagsJSON, _ := json.Marshal(m.Tags)
	anteJSON, _ := json.Marshal(m.Antecedents)
	provJSON, _ := json.Marshal(m.Provenance)
	result, err := s.db.Exec(
		`INSERT OR IGNORE INTO messages (id, campfire_id, sender, payload, tags, antecedents, timestamp, signature, provenance, received_at, instance)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.CampfireID, m.Sender, m.Payload, string(tagsJSON), string(anteJSON), m.Timestamp, m.Signature, string(provJSON), m.ReceivedAt, m.Instance,
	)
	if err != nil {
		return false, fmt.Errorf("adding message: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows > 0 && isCompactionEvent(m) {
		// TOCTOU fix (workspace-zqdc): invalidate the superseded-ID cache for this
		// campfire immediately after the insert commits. Any concurrent reader that
		// observed the old cache before this point will have gotten a valid (non-stale)
		// result, because the compaction event wasn't in the DB yet. Any reader that
		// runs after this delete will see a cache miss and rebuild from the DB, picking
		// up the new compaction event. This eliminates the window where a cache hit
		// could be returned for a campfire that just received a new compaction event.
		s.supersededMu.Lock()
		delete(s.supersededCache, m.CampfireID)
		s.supersededMu.Unlock()
	}
	return rows > 0, nil
}

// HasMessage checks if a message ID exists in the store.
func (s *Store) HasMessage(id string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE id = ?`, id).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking message: %w", err)
	}
	return count > 0, nil
}

// GetMessage retrieves a single message by ID.
func (s *Store) GetMessage(id string) (*MessageRecord, error) {
	row := s.db.QueryRow(
		`SELECT id, campfire_id, sender, payload, tags, antecedents, timestamp, signature, provenance, received_at, instance
		 FROM messages WHERE id = ?`, id,
	)
	m, err := scanMessageRecord(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying message: %w", err)
	}
	return &m, nil
}

// GetMessageByPrefix resolves a message ID prefix to a single message.
// Returns nil if no message matches. Returns an error if the prefix is ambiguous.
//
// Security: the prefix is escaped before use in LIKE to prevent wildcard injection
// via '%' or '_' characters in user-supplied input (workspace-4dr).
//
// The query uses LIMIT 2 so SQLite stops after fetching at most 2 rows — only
// 2 are needed to detect ambiguity. When ambiguous, rows.Close() is called
// explicitly before returning so the cursor is released immediately rather than
// waiting for the GC to finalize the rows object (workspace-0eu).
func (s *Store) GetMessageByPrefix(prefix string) (*MessageRecord, error) {
	escaped := strings.NewReplacer(`%`, `\%`, `_`, `\_`).Replace(prefix)
	rows, err := s.db.Query(
		`SELECT id, campfire_id, sender, payload, tags, antecedents, timestamp, signature, provenance, received_at, instance
		 FROM messages WHERE id LIKE ? ESCAPE '\' ORDER BY id LIMIT 2`,
		escaped+"%",
	)
	if err != nil {
		return nil, fmt.Errorf("querying messages by prefix: %w", err)
	}
	defer rows.Close()

	var matches []MessageRecord
	for rows.Next() {
		m, err := scanMessageRecord(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		matches = append(matches, m)
		if len(matches) > 1 {
			rows.Close() // release cursor immediately; defer is still safe to call on closed rows
			return nil, fmt.Errorf("ambiguous message ID prefix %s, matches multiple messages", prefix)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &matches[0], nil
}

// MessageFilter holds optional tag and sender filters for ListMessages.
// Tags uses OR semantics: a message matches if it has ANY of the specified tags.
// Sender matches on prefix of the sender hex string (case-insensitive).
// LIKE wildcards ('%' and '_') in Sender are escaped before use in the query
// so they are treated as literals, not SQL wildcards (workspace-ipzx).
// Empty fields mean no filtering for that dimension.
// When RespectCompaction is true, messages superseded by a compaction event are excluded
// (compaction events themselves are always included).
// AfterReceivedAt filters by received_at > value instead of timestamp > afterTimestamp.
// Use this in the poll path so cursor and filter use the same field, preventing
// message loss when sender clocks are skewed relative to the server clock.
// (Fix for workspace-d68.)
type MessageFilter struct {
	Tags              []string
	Sender            string
	RespectCompaction bool
	AfterReceivedAt   int64 // if > 0, overrides afterTimestamp; filters by received_at
}

// ListMessages returns messages for a campfire, ordered by timestamp.
// If campfireID is empty, returns messages across all campfires.
// If afterTimestamp > 0, only returns messages with timestamp > afterTimestamp.
// An optional MessageFilter applies tag and sender filtering at the SQL level.
// When filter.RespectCompaction is true, superseded messages are excluded.
// When filter.AfterReceivedAt > 0, filters by received_at instead of timestamp.
func (s *Store) ListMessages(campfireID string, afterTimestamp int64, filter ...MessageFilter) ([]MessageRecord, error) {
	var f MessageFilter
	if len(filter) > 0 {
		f = filter[0]
	}

	// Build WHERE clauses and args dynamically.
	// When AfterReceivedAt is set, filter by received_at (the poll cursor field)
	// instead of timestamp (message creation time). This aligns cursor and filter
	// to the same field, preventing message loss from sender clock skew.
	// (Fix for workspace-d68.)
	var conditions []string
	var args []any
	if f.AfterReceivedAt > 0 {
		conditions = []string{"received_at > ?"}
		args = []any{f.AfterReceivedAt}
	} else {
		conditions = []string{"timestamp > ?"}
		args = []any{afterTimestamp}
	}

	if campfireID != "" {
		conditions = append(conditions, "campfire_id = ?")
		args = append(args, campfireID)
	}

	if len(f.Tags) > 0 {
		// Match messages that have ANY of the given tags using json_each.
		placeholders := make([]string, len(f.Tags))
		for i, t := range f.Tags {
			placeholders[i] = "?"
			args = append(args, strings.ToLower(t))
		}
		tagClause := "EXISTS (SELECT 1 FROM json_each(tags) WHERE LOWER(value) IN (" + strings.Join(placeholders, ",") + "))"
		conditions = append(conditions, tagClause)
	}

	if f.Sender != "" {
		// Escape LIKE wildcards in the caller-supplied sender prefix so that
		// '%' and '_' are treated as literals, not SQL wildcards.
		// Consistent with the fix applied to GetMessageByPrefix (workspace-4dr).
		escapedSender := strings.NewReplacer(`%`, `\%`, `_`, `\_`).Replace(f.Sender)
		conditions = append(conditions, `LOWER(sender) LIKE LOWER(?) || '%' ESCAPE '\'`)
		args = append(args, escapedSender)
	}

	query := `SELECT id, campfire_id, sender, payload, tags, antecedents, timestamp, signature, provenance, received_at, instance
	          FROM messages WHERE ` + strings.Join(conditions, " AND ") + ` ORDER BY timestamp`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	defer rows.Close()

	var msgs []MessageRecord
	for rows.Next() {
		m, err := scanMessageRecord(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if !f.RespectCompaction {
		return msgs, nil
	}

	// Collect superseded message IDs from all compaction events in the relevant campfire(s).
	superseded, err := s.collectSupersededIDs(campfireID)
	if err != nil {
		return nil, fmt.Errorf("collecting superseded IDs: %w", err)
	}
	if len(superseded) == 0 {
		return msgs, nil
	}

	// Filter out superseded messages but always keep compaction events themselves.
	filtered := msgs[:0]
	for _, m := range msgs {
		if superseded[m.ID] && !isCompactionEvent(m) {
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered, nil
}

// isCompactionEvent is defined below using HasTag for exact matching.

// maxCompactionTimestamp returns the maximum timestamp among campfire:compact events
// for the given campfire. Returns 0 if there are none.
func (s *Store) maxCompactionTimestamp(campfireID string) (int64, error) {
	var conditions []string
	var args []any
	conditions = append(conditions, `EXISTS (SELECT 1 FROM json_each(tags) WHERE LOWER(value) = 'campfire:compact')`)
	if campfireID != "" {
		conditions = append(conditions, "campfire_id = ?")
		args = append(args, campfireID)
	}
	query := `SELECT COALESCE(MAX(timestamp), 0) FROM messages WHERE ` + strings.Join(conditions, " AND ")
	var maxTS int64
	if err := s.db.QueryRow(query, args...).Scan(&maxTS); err != nil {
		return 0, fmt.Errorf("querying max compaction timestamp: %w", err)
	}
	return maxTS, nil
}

// collectSupersededIDs returns the set of message IDs superseded by any compaction
// event in the given campfire. If campfireID is empty, collects across all campfires.
//
// Results are cached per campfire keyed by the max compaction event timestamp.
// A new compaction event has a newer timestamp, causing a cache miss and rebuild.
// Cross-campfire queries (campfireID=="") are not cached.
// (Fix for workspace-x9p: avoid O(events × ids) work on every ListMessages call.)
//
// TOCTOU fix (workspace-zqdc): the previous implementation queried maxCompactionTimestamp
// outside the lock, then acquired the lock to check the cache. A concurrent writer could
// insert a new compaction event between those two operations, causing the stale cache to
// be returned as a hit (the new event's timestamp wasn't yet observed). The fix moves
// cache invalidation to AddMessage: whenever a compaction event is stored, the cache
// entry for that campfire is deleted under the write lock before the insert returns.
// collectSupersededIDs now only needs a read lock for the cache hit path; cache misses
// acquire the write lock to populate.
//
// Residual TOCTOU fix (workspace-pm9m.5.8): a second, narrower window remained. After
// the cache-miss rebuild (ListCompactionEvents, no lock held), a concurrent AddMessage
// could insert a new compaction event and delete the cache entry. We would then store a
// cache entry with maxTS=T1 (the old timestamp), while the actual max is T2 > T1. The
// stale entry would be evicted on the next call (maxTS query returns T2 ≠ T1), but
// ListMessages calls in that window could return superseded messages that should be
// filtered. The fix: re-query maxCompactionTimestamp BEFORE acquiring the write lock,
// then use double-checked locking (re-check cache absence inside the lock) to avoid
// holding sync.RWMutex across any DB I/O. If the timestamp advanced, skip caching —
// the invalidation already happened and the next call will rebuild with the correct
// maxTS. If the cache entry was already populated by a concurrent goroutine, leave it.
func (s *Store) collectSupersededIDs(campfireID string) (map[string]bool, error) {
	if campfireID != "" {
		maxTS, err := s.maxCompactionTimestamp(campfireID)
		if err != nil {
			return nil, err
		}
		if maxTS == 0 {
			return nil, nil
		}

		s.supersededMu.RLock()
		entry, ok := s.supersededCache[campfireID]
		s.supersededMu.RUnlock()
		if ok && entry.maxCompactionTS == maxTS {
			// Return a copy so callers cannot mutate the cached map.
			cp := make(map[string]bool, len(entry.superseded))
			for k, v := range entry.superseded {
				cp[k] = v
			}
			return cp, nil
		}

		// Cache miss: rebuild superseded set.
		events, err := s.ListCompactionEvents(campfireID)
		if err != nil {
			return nil, err
		}
		superseded := make(map[string]bool)
		for _, ev := range events {
			var payload CompactionPayload
			if err := unmarshalCompactionPayload(ev.Payload, &payload); err != nil {
				continue
			}
			for _, id := range payload.Supersedes {
				superseded[id] = true
			}
		}

		// Double-checked locking (workspace-vja4): query currentMaxTS before acquiring
		// the write lock to avoid holding sync.RWMutex across DB I/O. Between the
		// rebuild above and here, AddMessage may have inserted a new compaction event
		// and deleted the cache entry. The two-step check handles this:
		//   (a) Re-query maxTS outside the lock: if it advanced, skip caching —
		//       the entry would be immediately stale and the next call will rebuild.
		//   (b) Inside the write lock, re-check that the cache entry is still absent
		//       (double-checked locking). If another goroutine already populated it,
		//       we leave their entry in place and avoid a redundant overwrite.
		currentMaxTS, tsErr := s.maxCompactionTimestamp(campfireID)
		s.supersededMu.Lock()
		if tsErr == nil && currentMaxTS == maxTS {
			// Only store if the entry is still absent — a concurrent goroutine may
			// have already populated it between our rebuild and this lock acquisition.
			if _, exists := s.supersededCache[campfireID]; !exists {
				s.supersededCache[campfireID] = supersededCacheEntry{
					maxCompactionTS: maxTS,
					superseded:      superseded,
				}
			}
		}
		// If tsErr != nil or currentMaxTS != maxTS, skip caching — the entry is
		// stale or the DB is unavailable. The next call will retry.
		s.supersededMu.Unlock()
		// Return a copy so callers cannot mutate the cached map.
		cp := make(map[string]bool, len(superseded))
		for k, v := range superseded {
			cp[k] = v
		}
		return cp, nil
	}

	// Cross-campfire path: no caching.
	events, err := s.ListCompactionEvents("")
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	superseded := make(map[string]bool)
	for _, ev := range events {
		var payload CompactionPayload
		if err := unmarshalCompactionPayload(ev.Payload, &payload); err != nil {
			continue
		}
		for _, id := range payload.Supersedes {
			superseded[id] = true
		}
	}
	return superseded, nil
}

// CompactionPayload is the JSON payload of a campfire:compact message.
type CompactionPayload struct {
	Supersedes     []string `json:"supersedes"`
	Summary        []byte   `json:"summary"`
	Retention      string   `json:"retention"`
	CheckpointHash string   `json:"checkpoint_hash"`
}

// unmarshalCompactionPayload decodes a CompactionPayload from the raw message payload bytes.
func unmarshalCompactionPayload(payload []byte, out *CompactionPayload) error {
	return json.Unmarshal(payload, out)
}

// ListCompactionEvents returns all campfire:compact messages for a campfire.
// If campfireID is empty, returns compaction events across all campfires.
func (s *Store) ListCompactionEvents(campfireID string) ([]MessageRecord, error) {
	var conditions []string
	var args []any

	conditions = append(conditions, `EXISTS (SELECT 1 FROM json_each(tags) WHERE LOWER(value) = 'campfire:compact')`)

	if campfireID != "" {
		conditions = append(conditions, "campfire_id = ?")
		args = append(args, campfireID)
	}

	query := `SELECT id, campfire_id, sender, payload, tags, antecedents, timestamp, signature, provenance, received_at, instance
	          FROM messages WHERE ` + strings.Join(conditions, " AND ") + ` ORDER BY timestamp`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing compaction events: %w", err)
	}
	defer rows.Close()

	var msgs []MessageRecord
	for rows.Next() {
		m, err := scanMessageRecord(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning compaction event: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// GetReadCursor returns the last-read timestamp for a campfire. Returns 0 if no cursor exists.
func (s *Store) GetReadCursor(campfireID string) (int64, error) {
	var ts int64
	err := s.db.QueryRow(`SELECT last_read_at FROM read_cursors WHERE campfire_id = ?`, campfireID).Scan(&ts)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("querying read cursor: %w", err)
	}
	return ts, nil
}

// SetReadCursor updates the read cursor for a campfire.
func (s *Store) SetReadCursor(campfireID string, timestamp int64) error {
	_, err := s.db.Exec(
		`INSERT INTO read_cursors (campfire_id, last_read_at) VALUES (?, ?)
		 ON CONFLICT(campfire_id) DO UPDATE SET last_read_at = ?`,
		campfireID, timestamp, timestamp,
	)
	if err != nil {
		return fmt.Errorf("setting read cursor: %w", err)
	}
	return nil
}

// MaxMessageTimestamp returns the maximum timestamp among all messages for a campfire
// with timestamp > afterTS. Returns 0 if no messages match.
// This is a lightweight cursor-only query — it reads no payloads and is intended to
// replace the full unfiltered ListMessages call in runOneShotMode when only the max
// timestamp is needed for cursor advancement. (Fix for workspace-pm9m.5.15.)
func (s *Store) MaxMessageTimestamp(campfireID string, afterTS int64) (int64, error) {
	var maxTS int64
	err := s.db.QueryRow(
		`SELECT COALESCE(MAX(timestamp), 0) FROM messages WHERE campfire_id = ? AND timestamp > ?`,
		campfireID, afterTS,
	).Scan(&maxTS)
	if err != nil {
		return 0, fmt.Errorf("querying max message timestamp: %w", err)
	}
	return maxTS, nil
}

// ListReferencingMessages finds messages whose antecedents contain the given message ID.
// Uses json_each to perform an exact element match, avoiding LIKE wildcard injection
// from IDs that contain '%' or '_' characters. (Security fix for workspace-kw9.)
func (s *Store) ListReferencingMessages(messageID string) ([]MessageRecord, error) {
	rows, err := s.db.Query(
		`SELECT m.id, m.campfire_id, m.sender, m.payload, m.tags, m.antecedents, m.timestamp, m.signature, m.provenance, m.received_at, m.instance
		 FROM messages m
		 WHERE EXISTS (
		     SELECT 1 FROM json_each(m.antecedents) WHERE value = ?
		 )
		 ORDER BY m.timestamp`, messageID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing referencing messages: %w", err)
	}
	defer rows.Close()

	var msgs []MessageRecord
	for rows.Next() {
		m, err := scanMessageRecord(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// PeerEndpoint records a known HTTP endpoint for a peer in a campfire.
type PeerEndpoint struct {
	CampfireID    string `json:"campfire_id"`
	MemberPubkey  string `json:"member_pubkey"`
	Endpoint      string `json:"endpoint"`
	ParticipantID uint32 `json:"participant_id,omitempty"` // FROST participant ID (0 = unknown)
	// Role is the peer's role in the campfire: PeerRoleCreator or PeerRoleMember.
	// Defaults to PeerRoleMember if not set. Used for server-side role enforcement in handleDeliver.
	// Note: this is the peer endpoint role namespace, distinct from the campfire membership
	// role (campfire.RoleObserver/RoleWriter/RoleFull). See PeerRole constants above.
	Role string `json:"role,omitempty"`
}

// UpsertPeerEndpoint inserts or replaces a peer endpoint record.
// If Role is empty, it defaults to PeerRoleMember.
func (s *Store) UpsertPeerEndpoint(e PeerEndpoint) error {
	role := e.Role
	if role == "" {
		role = PeerRoleMember
	}
	_, err := s.db.Exec(
		`INSERT INTO peer_endpoints (campfire_id, member_pubkey, endpoint, participant_id, role)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(campfire_id, member_pubkey) DO UPDATE SET
		     endpoint = excluded.endpoint,
		     participant_id = CASE WHEN excluded.participant_id > 0 THEN excluded.participant_id ELSE peer_endpoints.participant_id END,
		     role = CASE WHEN excluded.role != '' THEN excluded.role ELSE peer_endpoints.role END`,
		e.CampfireID, e.MemberPubkey, e.Endpoint, e.ParticipantID, role,
	)
	if err != nil {
		return fmt.Errorf("upserting peer endpoint: %w", err)
	}
	return nil
}

// DeletePeerEndpoint removes a peer endpoint record.
func (s *Store) DeletePeerEndpoint(campfireID, memberPubkey string) error {
	_, err := s.db.Exec(
		`DELETE FROM peer_endpoints WHERE campfire_id = ? AND member_pubkey = ?`,
		campfireID, memberPubkey,
	)
	if err != nil {
		return fmt.Errorf("deleting peer endpoint: %w", err)
	}
	return nil
}

// ListPeerEndpoints returns all known peer endpoints for a campfire.
func (s *Store) ListPeerEndpoints(campfireID string) ([]PeerEndpoint, error) {
	rows, err := s.db.Query(
		`SELECT campfire_id, member_pubkey, endpoint, participant_id, role FROM peer_endpoints WHERE campfire_id = ?`,
		campfireID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing peer endpoints: %w", err)
	}
	defer rows.Close()

	var endpoints []PeerEndpoint
	for rows.Next() {
		var e PeerEndpoint
		if err := rows.Scan(&e.CampfireID, &e.MemberPubkey, &e.Endpoint, &e.ParticipantID, &e.Role); err != nil {
			return nil, fmt.Errorf("scanning peer endpoint: %w", err)
		}
		if e.Role == "" {
			e.Role = PeerRoleMember
		}
		endpoints = append(endpoints, e)
	}
	return endpoints, rows.Err()
}

// GetPeerRole returns the role of a specific member in a campfire.
// Returns PeerRoleMember if the peer is not found (backward-compatible default).
func (s *Store) GetPeerRole(campfireID, memberPubkey string) (string, error) {
	var role string
	err := s.db.QueryRow(
		`SELECT role FROM peer_endpoints WHERE campfire_id = ? AND member_pubkey = ?`,
		campfireID, memberPubkey,
	).Scan(&role)
	if err == sql.ErrNoRows {
		return PeerRoleMember, nil
	}
	if err != nil {
		return "", fmt.Errorf("querying peer role: %w", err)
	}
	if role == "" {
		return PeerRoleMember, nil
	}
	return role, nil
}

// ThresholdShare stores FROST DKG output for a campfire.
type ThresholdShare struct {
	CampfireID    string `json:"campfire_id"`
	ParticipantID uint32 `json:"participant_id"`
	SecretShare   []byte `json:"secret_share"` // serialized eddsa.SecretShare
	PublicData    []byte `json:"public_data"`  // serialized eddsa.Public
}

// UpsertThresholdShare stores or replaces FROST DKG share data for a campfire.
func (s *Store) UpsertThresholdShare(share ThresholdShare) error {
	_, err := s.db.Exec(
		`INSERT INTO threshold_shares (campfire_id, participant_id, secret_share, public_data)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(campfire_id) DO UPDATE SET
		     participant_id = excluded.participant_id,
		     secret_share   = excluded.secret_share,
		     public_data    = excluded.public_data`,
		share.CampfireID, share.ParticipantID, share.SecretShare, share.PublicData,
	)
	if err != nil {
		return fmt.Errorf("upserting threshold share: %w", err)
	}
	return nil
}

// GetThresholdShare retrieves FROST DKG share data for a campfire.
// Returns nil if not found.
func (s *Store) GetThresholdShare(campfireID string) (*ThresholdShare, error) {
	row := s.db.QueryRow(
		`SELECT campfire_id, participant_id, secret_share, public_data
		 FROM threshold_shares WHERE campfire_id = ?`, campfireID,
	)
	var share ThresholdShare
	err := row.Scan(&share.CampfireID, &share.ParticipantID, &share.SecretShare, &share.PublicData)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying threshold share: %w", err)
	}
	return &share, nil
}

// StorePendingThresholdShare stores a DKG share for a future joiner.
func (s *Store) StorePendingThresholdShare(campfireID string, participantID uint32, shareData []byte) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO pending_threshold_shares (campfire_id, participant_id, share_data)
		 VALUES (?, ?, ?)`,
		campfireID, participantID, shareData,
	)
	if err != nil {
		return fmt.Errorf("storing pending threshold share: %w", err)
	}
	return nil
}

// ClaimPendingThresholdShare retrieves and removes the next available pending
// DKG share for a campfire. Returns nil if none available.
func (s *Store) ClaimPendingThresholdShare(campfireID string) (participantID uint32, shareData []byte, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	row := tx.QueryRow(
		`SELECT participant_id, share_data FROM pending_threshold_shares
		 WHERE campfire_id = ? ORDER BY participant_id ASC LIMIT 1`,
		campfireID,
	)
	var pid uint32
	var data []byte
	if scanErr := row.Scan(&pid, &data); scanErr != nil {
		if scanErr == sql.ErrNoRows {
			return 0, nil, nil
		}
		return 0, nil, fmt.Errorf("querying pending share: %w", scanErr)
	}

	if _, err := tx.Exec(
		`DELETE FROM pending_threshold_shares WHERE campfire_id = ? AND participant_id = ?`,
		campfireID, pid,
	); err != nil {
		return 0, nil, fmt.Errorf("deleting pending share: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("committing transaction: %w", err)
	}
	return pid, data, nil
}

// UpdateCampfireID changes the campfire_id for all records belonging to oldID,
// renaming the campfire to newID. This is used during rekey after eviction.
func (s *Store) UpdateCampfireID(oldID, newID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	tables := []struct {
		table string
		col   string
	}{
		{"campfire_memberships", "campfire_id"},
		{"read_cursors", "campfire_id"},
		{"filters", "campfire_id"},
		{"peer_endpoints", "campfire_id"},
		{"threshold_shares", "campfire_id"},
		{"pending_threshold_shares", "campfire_id"},
	}
	for _, t := range tables {
		q := fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", t.table, t.col, t.col)
		if _, err := tx.Exec(q, newID, oldID); err != nil {
			return fmt.Errorf("updating %s.%s: %w", t.table, t.col, err)
		}
	}
	// messages table has campfire_id but also a FK; update it too.
	if _, err := tx.Exec("UPDATE messages SET campfire_id = ? WHERE campfire_id = ?", newID, oldID); err != nil {
		return fmt.Errorf("updating messages.campfire_id: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Evict the supersededCache entry for oldID so no dangling artifact
	// remains after the rename. The newID entry (if any) is also evicted
	// so the first ListMessages(newID) rebuilds from the renamed DB rows.
	s.supersededMu.Lock()
	delete(s.supersededCache, oldID)
	delete(s.supersededCache, newID)
	s.supersededMu.Unlock()

	return nil
}

// StorePath returns the default store path for a given CF_HOME.
func StorePath(cfHome string) string {
	return filepath.Join(cfHome, "store.db")
}

// NowNano returns the current time in nanoseconds.
func NowNano() int64 {
	return time.Now().UnixNano()
}

// HasTag reports whether a tags slice contains the given tag as an exact
// element match. Prevents false positives from substring matches (e.g.
// "xycampfire:compact" would not match a query for "campfire:compact").
// (Security fix for workspace-pyw.)
func HasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// isCompactionEvent returns true if the message record carries the
// "campfire:compact" tag as an exact element.
// Uses HasTag rather than strings.Contains to avoid false positives from
// tags that happen to contain the substring (e.g. "xycampfire:compact").
func isCompactionEvent(rec MessageRecord) bool {
	return HasTag(rec.Tags, "campfire:compact")
}

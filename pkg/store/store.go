package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS campfire_memberships (
    campfire_id    TEXT PRIMARY KEY,
    transport_dir  TEXT NOT NULL,
    join_protocol  TEXT NOT NULL,
    role           TEXT NOT NULL DEFAULT 'member',
    joined_at      INTEGER NOT NULL
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
`

// Store is the local SQLite database for an agent.
type Store struct {
	db *sql.DB
}

// Membership represents a campfire membership record.
type Membership struct {
	CampfireID   string `json:"campfire_id"`
	TransportDir string `json:"transport_dir"`
	JoinProtocol string `json:"join_protocol"`
	Role         string `json:"role"`
	JoinedAt     int64  `json:"joined_at"`
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
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// AddMembership records that this agent is a member of a campfire.
func (s *Store) AddMembership(m Membership) error {
	_, err := s.db.Exec(
		`INSERT INTO campfire_memberships (campfire_id, transport_dir, join_protocol, role, joined_at)
		 VALUES (?, ?, ?, ?, ?)`,
		m.CampfireID, m.TransportDir, m.JoinProtocol, m.Role, m.JoinedAt,
	)
	if err != nil {
		return fmt.Errorf("adding membership: %w", err)
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
		`SELECT campfire_id, transport_dir, join_protocol, role, joined_at
		 FROM campfire_memberships WHERE campfire_id = ?`, campfireID,
	)
	var m Membership
	err := row.Scan(&m.CampfireID, &m.TransportDir, &m.JoinProtocol, &m.Role, &m.JoinedAt)
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
		`SELECT campfire_id, transport_dir, join_protocol, role, joined_at
		 FROM campfire_memberships ORDER BY joined_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing memberships: %w", err)
	}
	defer rows.Close()

	var memberships []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.CampfireID, &m.TransportDir, &m.JoinProtocol, &m.Role, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("scanning membership: %w", err)
		}
		memberships = append(memberships, m)
	}
	return memberships, rows.Err()
}

// MessageRecord is a stored message.
type MessageRecord struct {
	ID          string `json:"id"`
	CampfireID  string `json:"campfire_id"`
	Sender      string `json:"sender"`
	Payload     []byte `json:"payload"`
	Tags        string `json:"tags"`        // JSON array
	Antecedents string `json:"antecedents"` // JSON array
	Timestamp   int64  `json:"timestamp"`
	Signature   []byte `json:"signature"`
	Provenance  string `json:"provenance"`  // JSON array
	ReceivedAt  int64  `json:"received_at"`
}

// AddMessage inserts a message if not already present. Returns true if inserted.
func (s *Store) AddMessage(m MessageRecord) (bool, error) {
	result, err := s.db.Exec(
		`INSERT OR IGNORE INTO messages (id, campfire_id, sender, payload, tags, antecedents, timestamp, signature, provenance, received_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.CampfireID, m.Sender, m.Payload, m.Tags, m.Antecedents, m.Timestamp, m.Signature, m.Provenance, m.ReceivedAt,
	)
	if err != nil {
		return false, fmt.Errorf("adding message: %w", err)
	}
	rows, _ := result.RowsAffected()
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
		`SELECT id, campfire_id, sender, payload, tags, antecedents, timestamp, signature, provenance, received_at
		 FROM messages WHERE id = ?`, id,
	)
	var m MessageRecord
	err := row.Scan(&m.ID, &m.CampfireID, &m.Sender, &m.Payload, &m.Tags, &m.Antecedents, &m.Timestamp, &m.Signature, &m.Provenance, &m.ReceivedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying message: %w", err)
	}
	return &m, nil
}

// ListMessages returns messages for a campfire, ordered by timestamp.
// If campfireID is empty, returns messages across all campfires.
// If afterTimestamp > 0, only returns messages with timestamp > afterTimestamp.
func (s *Store) ListMessages(campfireID string, afterTimestamp int64) ([]MessageRecord, error) {
	var rows *sql.Rows
	var err error
	if campfireID == "" {
		rows, err = s.db.Query(
			`SELECT id, campfire_id, sender, payload, tags, antecedents, timestamp, signature, provenance, received_at
			 FROM messages WHERE timestamp > ? ORDER BY timestamp`, afterTimestamp,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, campfire_id, sender, payload, tags, antecedents, timestamp, signature, provenance, received_at
			 FROM messages WHERE campfire_id = ? AND timestamp > ? ORDER BY timestamp`,
			campfireID, afterTimestamp,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	defer rows.Close()

	var msgs []MessageRecord
	for rows.Next() {
		var m MessageRecord
		if err := rows.Scan(&m.ID, &m.CampfireID, &m.Sender, &m.Payload, &m.Tags, &m.Antecedents, &m.Timestamp, &m.Signature, &m.Provenance, &m.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
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

// ListReferencingMessages finds messages whose antecedents contain the given message ID.
func (s *Store) ListReferencingMessages(messageID string) ([]MessageRecord, error) {
	// JSON array search: antecedents contains the ID as a quoted string
	pattern := fmt.Sprintf("%%%q%%", messageID)
	rows, err := s.db.Query(
		`SELECT id, campfire_id, sender, payload, tags, antecedents, timestamp, signature, provenance, received_at
		 FROM messages WHERE antecedents LIKE ? ORDER BY timestamp`, pattern,
	)
	if err != nil {
		return nil, fmt.Errorf("listing referencing messages: %w", err)
	}
	defer rows.Close()

	var msgs []MessageRecord
	for rows.Next() {
		var m MessageRecord
		if err := rows.Scan(&m.ID, &m.CampfireID, &m.Sender, &m.Payload, &m.Tags, &m.Antecedents, &m.Timestamp, &m.Signature, &m.Provenance, &m.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// StorePath returns the default store path for a given CF_HOME.
func StorePath(cfHome string) string {
	return filepath.Join(cfHome, "store.db")
}

// NowNano returns the current time in nanoseconds.
func NowNano() int64 {
	return time.Now().UnixNano()
}

// Package state manages the bridge's own SQLite database.
// This is separate from the campfire protocol store.
package state

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const bridgeSchema = `
CREATE TABLE IF NOT EXISTS message_map (
    campfire_msg_id   TEXT PRIMARY KEY,
    teams_activity_id TEXT NOT NULL,
    teams_conv_id     TEXT NOT NULL,
    campfire_id       TEXT NOT NULL,
    created_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_message_map_teams ON message_map(teams_activity_id);
CREATE INDEX IF NOT EXISTS idx_message_map_campfire ON message_map(campfire_id);

CREATE TABLE IF NOT EXISTS conversation_refs (
    campfire_id       TEXT NOT NULL,
    teams_conv_id     TEXT NOT NULL,
    service_url       TEXT NOT NULL,
    tenant_id         TEXT NOT NULL,
    channel_id        TEXT,
    bot_id            TEXT NOT NULL,
    updated_at        INTEGER NOT NULL,
    PRIMARY KEY (campfire_id, teams_conv_id)
);

CREATE TABLE IF NOT EXISTS teams_acl (
    teams_user_id     TEXT NOT NULL,
    campfire_id       TEXT NOT NULL,
    display_name      TEXT,
    granted_at        INTEGER NOT NULL,
    PRIMARY KEY (teams_user_id, campfire_id)
);

CREATE TABLE IF NOT EXISTS dedup_log (
    teams_activity_id TEXT PRIMARY KEY,
    campfire_msg_id   TEXT NOT NULL,
    created_at        INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS identity_registry (
    pubkey_hex        TEXT PRIMARY KEY,
    display_name      TEXT NOT NULL,
    role              TEXT,
    color             TEXT
);
`

// DB wraps the bridge state database.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) the bridge state database at the given path.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening bridge db: %w", err)
	}
	if _, err := db.Exec(bridgeSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating bridge schema: %w", err)
	}
	return &DB{db: db}, nil
}

// Close closes the database.
func (d *DB) Close() error {
	return d.db.Close()
}

// --- message_map ---

// MapMessage records a campfire→Teams message ID mapping.
func (d *DB) MapMessage(campfireMsgID, teamsActivityID, teamsConvID, campfireID string) error {
	_, err := d.db.Exec(
		`INSERT OR REPLACE INTO message_map (campfire_msg_id, teams_activity_id, teams_conv_id, campfire_id, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		campfireMsgID, teamsActivityID, teamsConvID, campfireID, time.Now().Unix(),
	)
	return err
}

// LookupTeamsActivity returns the Teams activity ID for a campfire message.
func (d *DB) LookupTeamsActivity(campfireMsgID string) (activityID, convID string, err error) {
	err = d.db.QueryRow(
		`SELECT teams_activity_id, teams_conv_id FROM message_map WHERE campfire_msg_id = ?`,
		campfireMsgID,
	).Scan(&activityID, &convID)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return
}

// LookupCampfireMsg returns the campfire message ID for a Teams activity.
func (d *DB) LookupCampfireMsg(teamsActivityID string) (string, error) {
	var id string
	err := d.db.QueryRow(
		`SELECT campfire_msg_id FROM message_map WHERE teams_activity_id = ?`,
		teamsActivityID,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// --- conversation_refs ---

// ConversationRef holds a Teams conversation reference for proactive messaging.
type ConversationRef struct {
	CampfireID  string
	TeamsConvID string
	ServiceURL  string
	TenantID    string
	ChannelID   string
	BotID       string
}

// UpsertConversationRef stores or updates a conversation reference.
func (d *DB) UpsertConversationRef(ref ConversationRef) error {
	_, err := d.db.Exec(
		`INSERT OR REPLACE INTO conversation_refs (campfire_id, teams_conv_id, service_url, tenant_id, channel_id, bot_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ref.CampfireID, ref.TeamsConvID, ref.ServiceURL, ref.TenantID, ref.ChannelID, ref.BotID, time.Now().Unix(),
	)
	return err
}

// GetConversationRef returns the conversation reference for a campfire.
func (d *DB) GetConversationRef(campfireID string) (*ConversationRef, error) {
	var ref ConversationRef
	err := d.db.QueryRow(
		`SELECT campfire_id, teams_conv_id, service_url, tenant_id, channel_id, bot_id
		 FROM conversation_refs WHERE campfire_id = ? ORDER BY updated_at DESC LIMIT 1`,
		campfireID,
	).Scan(&ref.CampfireID, &ref.TeamsConvID, &ref.ServiceURL, &ref.TenantID, &ref.ChannelID, &ref.BotID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ref, err
}

// GetCampfireForConversation returns the campfire ID mapped to a Teams conversation ID.
// Returns an empty string (and nil error) if no mapping exists.
func (d *DB) GetCampfireForConversation(teamsConvID string) (string, error) {
	var campfireID string
	err := d.db.QueryRow(
		`SELECT campfire_id FROM conversation_refs WHERE teams_conv_id = ? ORDER BY updated_at DESC LIMIT 1`,
		teamsConvID,
	).Scan(&campfireID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return campfireID, err
}

// --- teams_acl ---

// CheckACL returns true if the Teams user is allowed to write to the campfire.
func (d *DB) CheckACL(teamsUserID, campfireID string) (bool, error) {
	var count int
	err := d.db.QueryRow(
		`SELECT COUNT(*) FROM teams_acl WHERE teams_user_id = ? AND (campfire_id = ? OR campfire_id = '*')`,
		teamsUserID, campfireID,
	).Scan(&count)
	return count > 0, err
}

// SeedACL inserts ACL entries (used on startup from config).
func (d *DB) SeedACL(teamsUserID, campfireID, displayName string) error {
	_, err := d.db.Exec(
		`INSERT OR REPLACE INTO teams_acl (teams_user_id, campfire_id, display_name, granted_at)
		 VALUES (?, ?, ?, ?)`,
		teamsUserID, campfireID, displayName, time.Now().Unix(),
	)
	return err
}

// --- dedup_log ---

// CheckDedup returns true if this Teams activity has already been processed.
func (d *DB) CheckDedup(teamsActivityID string) (bool, error) {
	var count int
	err := d.db.QueryRow(
		`SELECT COUNT(*) FROM dedup_log WHERE teams_activity_id = ?`,
		teamsActivityID,
	).Scan(&count)
	return count > 0, err
}

// RecordDedup marks a Teams activity as processed.
func (d *DB) RecordDedup(teamsActivityID, campfireMsgID string) error {
	_, err := d.db.Exec(
		`INSERT OR IGNORE INTO dedup_log (teams_activity_id, campfire_msg_id, created_at)
		 VALUES (?, ?, ?)`,
		teamsActivityID, campfireMsgID, time.Now().Unix(),
	)
	return err
}

// --- identity_registry ---

// IdentityInfo holds display information for a campfire sender.
type IdentityInfo struct {
	PubkeyHex   string
	DisplayName string
	Role        string
	Color       string
}

// LookupIdentity returns display info for a sender pubkey.
func (d *DB) LookupIdentity(pubkeyHex string) (*IdentityInfo, error) {
	var info IdentityInfo
	err := d.db.QueryRow(
		`SELECT pubkey_hex, display_name, role, color FROM identity_registry WHERE pubkey_hex = ?`,
		pubkeyHex,
	).Scan(&info.PubkeyHex, &info.DisplayName, &info.Role, &info.Color)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &info, err
}

// SeedIdentity inserts an identity entry (used on startup from config).
func (d *DB) SeedIdentity(pubkeyHex, displayName, role, color string) error {
	_, err := d.db.Exec(
		`INSERT OR REPLACE INTO identity_registry (pubkey_hex, display_name, role, color)
		 VALUES (?, ?, ?, ?)`,
		pubkeyHex, displayName, role, color,
	)
	return err
}

// PruneOldEntries removes stale message_map and dedup_log entries.
func (d *DB) PruneOldEntries(messageMapMaxAge, dedupMaxAge time.Duration) error {
	now := time.Now().Unix()
	if _, err := d.db.Exec(`DELETE FROM message_map WHERE created_at <= ?`, now-int64(messageMapMaxAge.Seconds())); err != nil {
		return fmt.Errorf("pruning message_map: %w", err)
	}
	if _, err := d.db.Exec(`DELETE FROM dedup_log WHERE created_at <= ?`, now-int64(dedupMaxAge.Seconds())); err != nil {
		return fmt.Errorf("pruning dedup_log: %w", err)
	}
	return nil
}

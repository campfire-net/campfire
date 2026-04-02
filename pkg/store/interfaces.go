package store

import "errors"

// ErrInviteExhausted is returned by ValidateAndUseInvite when the invite code
// has already reached its maximum number of uses.
var ErrInviteExhausted = errors.New("invite code has reached its maximum uses")

// MembershipStore manages campfire membership records.
// Implemented by *SQLiteStore.
type MembershipStore interface {
	AddMembership(m Membership) error
	UpdateMembershipRole(campfireID, role string) error
	RemoveMembership(campfireID string) error
	GetMembership(campfireID string) (*Membership, error)
	ListMemberships() ([]Membership, error)
}

// MessageStore manages campfire messages and read cursors.
// Implemented by *SQLiteStore.
type MessageStore interface {
	AddMessage(m MessageRecord) (bool, error)
	HasMessage(id string) (bool, error)
	GetMessage(id string) (*MessageRecord, error)
	GetMessageByPrefix(prefix string) (*MessageRecord, error)
	ListMessages(campfireID string, afterTimestamp int64, filter ...MessageFilter) ([]MessageRecord, error)
	MaxMessageTimestamp(campfireID string, afterTS int64) (int64, error)
	ListReferencingMessages(messageID string) ([]MessageRecord, error)
	ListCompactionEvents(campfireID string) ([]MessageRecord, error)
	GetReadCursor(campfireID string) (int64, error)
	SetReadCursor(campfireID string, timestamp int64) error
}

// PeerStore manages peer endpoint records.
// Implemented by *SQLiteStore.
type PeerStore interface {
	UpsertPeerEndpoint(e PeerEndpoint) error
	DeletePeerEndpoint(campfireID, memberPubkey string) error
	ListPeerEndpoints(campfireID string) ([]PeerEndpoint, error)
	GetPeerRole(campfireID, memberPubkey string) (string, error)
}

// ThresholdStore manages FROST DKG threshold share records.
// Implemented by *SQLiteStore.
type ThresholdStore interface {
	UpsertThresholdShare(share ThresholdShare) error
	GetThresholdShare(campfireID string) (*ThresholdShare, error)
	StorePendingThresholdShare(campfireID string, participantID uint32, shareData []byte) error
	ClaimPendingThresholdShare(campfireID string) (participantID uint32, shareData []byte, err error)
}

// EpochSecretStore manages per-epoch CEK root secrets for E2E encryption.
// Implemented by *SQLiteStore. Supports dual-epoch grace period (spec §3.5).
type EpochSecretStore interface {
	// UpsertEpochSecret stores or updates the root secret and CEK for (campfire, epoch).
	UpsertEpochSecret(secret EpochSecret) error
	// GetEpochSecret retrieves the epoch secret for (campfireID, epoch).
	// Returns nil, nil if not found.
	GetEpochSecret(campfireID string, epoch uint64) (*EpochSecret, error)
	// GetLatestEpochSecret returns the highest-epoch secret for campfireID.
	// Returns nil, nil if none found.
	GetLatestEpochSecret(campfireID string) (*EpochSecret, error)
	// SetMembershipEncrypted sets the encrypted flag for a campfire membership.
	// Used for downgrade prevention (spec §2.1): local flag takes precedence over relay state.
	SetMembershipEncrypted(campfireID string, encrypted bool) error
	// ApplyMembershipCommitAtomically installs an epoch secret and optionally upserts
	// a membership record in a single DB transaction. This enforces the atomicity
	// requirement from spec §6.1: membership change and epoch rotation are committed
	// together, or not at all. Pass nil newMember for rotations without membership change.
	ApplyMembershipCommitAtomically(campfireID string, newMember *Membership, secret EpochSecret) error
}

// InviteRecord holds a single invite code for a campfire.
type InviteRecord struct {
	CampfireID string
	InviteCode string
	CreatedBy  string // pubkey of the agent who created the invite
	CreatedAt  int64  // unix nanoseconds
	Revoked    bool
	MaxUses    int    // 0 = unlimited
	UseCount   int
	Label      string
}

// InviteStore manages per-campfire invite codes (security model §5.a).
// Implemented by *SQLiteStore and *TableStore.
type InviteStore interface {
	// CreateInvite inserts a new invite record.
	CreateInvite(inv InviteRecord) error
	// ValidateInvite checks that the code is valid (not revoked, not exhausted).
	// Returns the matching record on success, or an error describing why it failed.
	ValidateInvite(campfireID, inviteCode string) (*InviteRecord, error)
	// RevokeInvite marks a code as revoked. Returns an error if not found.
	RevokeInvite(campfireID, inviteCode string) error
	// ListInvites returns all invite records for a campfire.
	ListInvites(campfireID string) ([]InviteRecord, error)
	// LookupInvite returns a single invite by code or nil if not found.
	LookupInvite(inviteCode string) (*InviteRecord, error)
	// HasAnyInvites returns true if the campfire has at least one registered invite code.
	HasAnyInvites(campfireID string) (bool, error)
	// IncrementInviteUse increments the use_count for the given code.
	IncrementInviteUse(inviteCode string) error
	// ValidateAndUseInvite atomically validates the invite code and increments its
	// use_count in a single operation, preventing TOCTOU races under concurrent joins.
	// Returns the updated invite record on success. Returns ErrInviteExhausted
	// (errors.Is compatible) when max_uses is reached. Returns a descriptive error
	// for revoked, not-found, or wrong-campfire codes.
	ValidateAndUseInvite(campfireID, inviteCode string) (*InviteRecord, error)
}

// ProjectionEntry represents a single message ID indexed in a named projection view.
type ProjectionEntry struct {
	CampfireID string
	ViewName   string
	MessageID  string
	IndexedAt  int64 // Unix nanoseconds when the message was indexed into this view
}

// ProjectionMetadata holds metadata for a named projection view.
type ProjectionMetadata struct {
	CampfireID       string
	ViewName         string
	PredicateHash    string // Hash of the predicate expression for cache invalidation
	LastCompactionID string // Most recent compaction event processed
	HighWaterMark    int64  // Highest message timestamp indexed
}

// ProjectionStore manages named projection views (filtered message indices).
// Implemented by *SQLiteStore and *TableStore.
type ProjectionStore interface {
	// InsertProjectionEntry adds a message ID to a projection view.
	// Idempotent: succeeds silently if the entry already exists.
	InsertProjectionEntry(campfireID, viewName, messageID string, indexedAt int64) error
	// DeleteProjectionEntries removes specific message IDs from a projection view.
	DeleteProjectionEntries(campfireID, viewName string, messageIDs []string) error
	// DeleteAllProjectionEntries drops all entries for a projection view.
	DeleteAllProjectionEntries(campfireID, viewName string) error
	// ListProjectionEntries returns all entries for a projection view, ordered by indexed_at.
	ListProjectionEntries(campfireID, viewName string) ([]ProjectionEntry, error)
	// GetProjectionMetadata retrieves metadata for a projection view.
	// Returns nil, nil if not found.
	GetProjectionMetadata(campfireID, viewName string) (*ProjectionMetadata, error)
	// SetProjectionMetadata upserts metadata for a projection view.
	SetProjectionMetadata(campfireID, viewName string, meta ProjectionMetadata) error
}

// Store is the unified interface covering all store capabilities.
// The SQLite-backed implementation is returned by Open and NewSQLite.
type Store interface {
	MembershipStore
	MessageStore
	PeerStore
	ThresholdStore
	EpochSecretStore
	InviteStore
	ProjectionStore
	UpdateCampfireID(oldID, newID string) error
	Close() error
}

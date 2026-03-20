package store

// MembershipStore manages campfire membership records.
// Implemented by *Store.
type MembershipStore interface {
	AddMembership(m Membership) error
	UpdateMembershipRole(campfireID, role string) error
	RemoveMembership(campfireID string) error
	GetMembership(campfireID string) (*Membership, error)
	ListMemberships() ([]Membership, error)
}

// MessageStore manages campfire messages and read cursors.
// Implemented by *Store.
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
// Implemented by *Store.
type PeerStore interface {
	UpsertPeerEndpoint(e PeerEndpoint) error
	DeletePeerEndpoint(campfireID, memberPubkey string) error
	ListPeerEndpoints(campfireID string) ([]PeerEndpoint, error)
	GetPeerRole(campfireID, memberPubkey string) (string, error)
}

// ThresholdStore manages FROST DKG threshold share records.
// Implemented by *Store.
type ThresholdStore interface {
	UpsertThresholdShare(share ThresholdShare) error
	GetThresholdShare(campfireID string) (*ThresholdShare, error)
	StorePendingThresholdShare(campfireID string, participantID uint32, shareData []byte) error
	ClaimPendingThresholdShare(campfireID string) (participantID uint32, shareData []byte, err error)
}

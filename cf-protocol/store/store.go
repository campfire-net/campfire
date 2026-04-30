// Package store — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the store substrate from cf-protocol/internal/store.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package store

import "github.com/campfire-net/campfire/cf-protocol/internal/store"

// Re-export error vars.
var (
	ErrPlaintextInEncryptedCampfire = store.ErrPlaintextInEncryptedCampfire
	ErrCompactionBytesInconsistent  = store.ErrCompactionBytesInconsistent
	ErrInviteExhausted              = store.ErrInviteExhausted
)

// Re-export all types.
type (
	Store             = store.Store
	MessageStore      = store.MessageStore
	MembershipStore   = store.MembershipStore
	PeerStore         = store.PeerStore
	ThresholdStore    = store.ThresholdStore
	EpochSecretStore  = store.EpochSecretStore
	InviteStore       = store.InviteStore
	ProjectionStore   = store.ProjectionStore
	Membership        = store.Membership
	MessageRecord     = store.MessageRecord
	MessageFilter     = store.MessageFilter
	PeerEndpoint      = store.PeerEndpoint
	ThresholdShare    = store.ThresholdShare
	EpochSecret       = store.EpochSecret
	InviteRecord      = store.InviteRecord
	CompactionPayload = store.CompactionPayload
	ProjectionEntry   = store.ProjectionEntry
	ProjectionMetadata = store.ProjectionMetadata
)

// Re-export peer role constants.
const (
	PeerRoleCreator = store.PeerRoleCreator
	PeerRoleMember  = store.PeerRoleMember
)

// Re-export all functions.
var (
	Open                  = store.Open
	OpenMemory            = store.OpenMemory
	NowNano               = store.NowNano
	StorePath             = store.StorePath
	HasTag                = store.HasTag
	MessageRecordFromMessage = store.MessageRecordFromMessage
	ValidateCompactionBytes  = store.ValidateCompactionBytes
)

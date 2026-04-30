// Package campfire — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the campfire substrate from cf-protocol/internal/campfire.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package campfire

import "github.com/campfire-net/campfire/cf-protocol/internal/campfire"

// Re-export all types.
type (
	Campfire      = campfire.Campfire
	CampfireState = campfire.CampfireState
	Member        = campfire.Member
	// MemberRecord is an alias for Member for backward compatibility.
	MemberRecord = campfire.Member
)

// Re-export all functions.
var (
	New                  = campfire.New
	EffectiveRole        = campfire.EffectiveRole
	IsBlindRelay         = campfire.IsBlindRelay
	EffectiveDeliveryModes = campfire.EffectiveDeliveryModes
	ValidDeliveryMode    = campfire.ValidDeliveryMode
)

// Re-export role constants.
const (
	RoleFull       = campfire.RoleFull
	RoleObserver   = campfire.RoleObserver
	RoleWriter     = campfire.RoleWriter
	RoleBlindRelay = campfire.RoleBlindRelay
)

// Re-export delivery mode constants.
const (
	DeliveryModePull = campfire.DeliveryModePull
	DeliveryModePush = campfire.DeliveryModePush
)

// Re-export tag constants.
const (
	TagPrefix            = campfire.TagPrefix
	TagCompact           = campfire.TagCompact
	TagMemberJoined      = campfire.TagMemberJoined
	TagMemberLeft        = campfire.TagMemberLeft
	TagMemberRoleChanged = campfire.TagMemberRoleChanged
	TagJoinRequest       = campfire.TagJoinRequest
	TagKeyDelivery       = campfire.TagKeyDelivery
	TagRekey             = campfire.TagRekey
	TagSubCreated        = campfire.TagSubCreated
	TagView              = campfire.TagView
	TagAudit             = campfire.TagAudit
	TagAuditRoot         = campfire.TagAuditRoot
)

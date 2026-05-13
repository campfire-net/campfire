// Package protocol — Public surface re-exports (campfireagent-401d).
//
// This file and its siblings (surface_transport.go, surface_util.go) expose
// substrate types as type aliases so that external consumers (cmd/, bridge/,
// pkg/, tests/) can reach them through cf-protocol/protocol rather than
// importing cf-protocol/internal/ directly (which Go's internal/ rule prevents
// for code outside the cf-protocol/ subtree).
//
// Naming conventions:
//   - Types that don't conflict with existing protocol types keep their original
//     name (e.g. Membership, MessageRecord, CampfireState).
//   - Types that conflict use a distinguishing prefix:
//       message.Message  → WireMessage  (protocol.Message is the SDK type)
//   - Function re-exports are vars of function type.
//
// External consumers: replace imports of cf-protocol/internal/<pkg> with
// cf-protocol/protocol and update the type/func names per this file.
//
// TRANSITIONAL markers note that some definitions belong to Stage 3 migration
// (cf-session, cf-identity) and will move later.

package protocol

import (
	"github.com/campfire-net/campfire/cf-protocol/internal/campfire"
	"github.com/campfire-net/campfire/cf-protocol/internal/message"
	"github.com/campfire-net/campfire/cf-protocol/internal/store"
)

// ─── campfire package re-exports ────────────────────────────────────────────

// Campfire is the core campfire object.
// Re-exported from cf-protocol/internal/campfire.
type Campfire = campfire.Campfire

// CampfireState holds the persistent on-disk state of a campfire.
type CampfireState = campfire.CampfireState

// NewCampfire constructs a new Campfire value.
var NewCampfire = campfire.New

// EffectiveRole returns the canonical role string, defaulting legacy values to RoleFull.
var EffectiveRole = campfire.EffectiveRole

// IsBlindRelay reports whether the role is RoleBlindRelay.
var IsBlindRelay = campfire.IsBlindRelay

// EffectiveDeliveryModes returns the effective delivery mode list.
var EffectiveDeliveryModes = campfire.EffectiveDeliveryModes

// ValidDeliveryMode reports whether the delivery mode string is valid.
var ValidDeliveryMode = campfire.ValidDeliveryMode

// Campfire role constants.
const (
	CampfireRoleFull       = campfire.RoleFull
	CampfireRoleObserver   = campfire.RoleObserver
	CampfireRoleWriter     = campfire.RoleWriter
	CampfireRoleBlindRelay = campfire.RoleBlindRelay
)

// Campfire delivery mode constants.
const (
	CampfireDeliveryModePull = campfire.DeliveryModePull
	CampfireDeliveryModePush = campfire.DeliveryModePush
)

// Campfire tag constants for system messages.
const (
	CampfireTagPrefix            = campfire.TagPrefix
	CampfireTagCompact           = campfire.TagCompact
	CampfireTagMemberJoined      = campfire.TagMemberJoined
	CampfireTagMemberLeft        = campfire.TagMemberLeft
	CampfireTagMemberRoleChanged = campfire.TagMemberRoleChanged
	CampfireTagJoinRequest       = campfire.TagJoinRequest
	CampfireTagKeyDelivery       = campfire.TagKeyDelivery
	CampfireTagRekey             = campfire.TagRekey
	CampfireTagSubCreated        = campfire.TagSubCreated
	CampfireTagView              = campfire.TagView
	CampfireTagAudit             = campfire.TagAudit
	CampfireTagAuditRoot         = campfire.TagAuditRoot
	CampfireTagVisibilityChanged = campfire.TagVisibilityChanged
)

// ─── message package re-exports ─────────────────────────────────────────────

// WireMessage is the internal campfire wire message (signed, with []byte Sender).
// Distinct from protocol.Message, which is the SDK-facing type with string fields.
// Use protocol.Message in SDK code; use WireMessage when working with raw signing/
// verification or low-level transports.
//
// Re-exported from cf-protocol/internal/message.
type WireMessage = message.Message

// ProvenanceHop is a relay hop in a message's provenance chain.
type ProvenanceHop = message.ProvenanceHop

// MessageSignInput is the canonical sign input for a campfire message.
type MessageSignInput = message.MessageSignInput

// HopSignInput is the sign input for a provenance hop.
type HopSignInput = message.HopSignInput

// Signer is the message signing interface.
type Signer = message.Signer

// Ed25519Signer is an Ed25519-backed Signer implementation.
type Ed25519Signer = message.Ed25519Signer

// NewWireMessage creates a new signed wire message.
var NewWireMessage = message.NewMessage

// MustNewEd25519Signer creates an Ed25519Signer, panicking on error.
var MustNewEd25519Signer = message.MustNewEd25519Signer

// NewEd25519Signer creates an Ed25519Signer.
var NewEd25519Signer = message.NewEd25519Signer

// VerifyWireHop verifies a single provenance hop signature.
var VerifyWireHop = message.VerifyHop

// VerifyWireMessageSignature verifies a campfire wire message signature.
var VerifyWireMessageSignature = message.VerifyMessageSignature

// ─── store package re-exports ────────────────────────────────────────────────

// Store is the campfire storage interface.
// Re-exported from cf-protocol/internal/store.
type Store = store.Store

// MessageStore is the message sub-interface of Store.
type MessageStore = store.MessageStore

// MembershipStore is the membership sub-interface of Store.
type MembershipStore = store.MembershipStore

// InviteStore is the invite sub-interface of Store.
type InviteStore = store.InviteStore

// Membership is the persisted campfire membership record.
type Membership = store.Membership

// MessageRecord is the persisted campfire message record (low-level).
// Distinct from protocol.Message, which is the SDK-facing type.
type MessageRecord = store.MessageRecord

// MessageFilter controls message listing filters.
type MessageFilter = store.MessageFilter

// PeerEndpoint is a persisted peer endpoint record.
type PeerEndpoint = store.PeerEndpoint

// ThresholdShare is a persisted FROST threshold share.
type ThresholdShare = store.ThresholdShare

// EpochSecret is a persisted epoch secret for E2E-encrypted campfires.
type EpochSecret = store.EpochSecret

// InviteRecord is a persisted invite code record.
type InviteRecord = store.InviteRecord

// CompactionPayload is the decoded payload of a compaction event.
type CompactionPayload = store.CompactionPayload

// ProjectionEntry is a projection index entry.
type ProjectionEntry = store.ProjectionEntry

// ProjectionMetadata is per-view projection metadata.
type ProjectionMetadata = store.ProjectionMetadata

// Peer role constants.
const (
	StorePeerRoleCreator = store.PeerRoleCreator
	StorePeerRoleMember  = store.PeerRoleMember
)

// Open opens (or creates) a SQLite campfire store at the given path.
var Open = store.Open

// OpenMemory has been moved to surface_inmemory.go (campfireagent-c5a) so the
// test-only helper is visually separated from the production API surface.

// NowNano returns the current time as a nanosecond Unix timestamp.
var NowNano = store.NowNano

// StorePath returns the default store file path for cfHome.
var StorePath = store.StorePath

// HasTag reports whether the tags slice contains the given tag.
var HasTag = store.HasTag

// MessageRecordFromMessage converts a wire message to a MessageRecord.
var MessageRecordFromMessage = store.MessageRecordFromMessage

// ValidateCompactionBytes validates the BytesSuperseded field of a compaction event.
var ValidateCompactionBytes = store.ValidateCompactionBytes

// ErrPlaintextInEncryptedCampfire is returned when a plaintext payload is rejected.
var ErrPlaintextInEncryptedCampfire = store.ErrPlaintextInEncryptedCampfire

// ErrCompactionBytesInconsistent is returned when compaction byte counts don't match.
var ErrCompactionBytesInconsistent = store.ErrCompactionBytesInconsistent

// ErrInviteExhausted is returned when an invite code has reached its use limit.
var ErrInviteExhausted = store.ErrInviteExhausted

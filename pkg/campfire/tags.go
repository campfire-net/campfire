// System message tag constants for the campfire protocol.
//
// These constants define the reserved "campfire:" tag namespace. Tags in this
// namespace are system messages signed by campfire keys; convention declarations
// may not produce them (enforced by pkg/convention/parser.go).
package campfire

const (
	// TagPrefix is the reserved namespace prefix for all campfire system messages.
	TagPrefix = "campfire:"

	// TagCompact is the tag for compaction events. A compaction event is a
	// regular campfire message that summarises earlier messages; readers may
	// treat messages before a compaction as archived.
	TagCompact = "campfire:compact"

	// TagMemberJoined is the tag for member-joined system messages.
	TagMemberJoined = "campfire:member-joined"

	// TagMemberLeft is the tag for member-left system messages.
	TagMemberLeft = "campfire:member-left"

	// TagMemberRoleChanged is the tag for member-role-changed system messages.
	TagMemberRoleChanged = "campfire:member-role-changed"

	// TagJoinRequest is the tag for join-request system messages.
	TagJoinRequest = "campfire:join-request"

	// TagKeyDelivery is the tag for key-delivery system messages used in
	// E2E-encrypted campfires (spec-encryption.md v0.2 §4).
	TagKeyDelivery = "campfire:key-delivery"

	// TagRekey is the tag for rekey system messages posted when a member is evicted.
	TagRekey = "campfire:rekey"

	// TagSubCreated is the tag for sub-created system messages posted when a
	// sub-campfire is created under a parent.
	TagSubCreated = "campfire:sub-created"

	// TagView is the tag for view-definition system messages (pkg/projection).
	TagView = "campfire:view"

	// TagAudit is the tag for individual audit-log entry messages.
	TagAudit = "campfire:audit"

	// TagAuditRoot is the tag for audit-root checkpoint messages. Audit roots are
	// posted every 1000 entries or 1 hour and anchor the audit hash chain.
	TagAuditRoot = "campfire:audit-root"
)

// Note: TagEncryptedInit and TagMembershipCommit are defined in encryption.go
// alongside the payload types they describe.

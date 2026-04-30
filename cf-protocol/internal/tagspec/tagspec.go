// Package tagspec defines the reserved tag-namespace prefix constants for
// the cf-protocol substrate (campfireagent-753: Stage 1, design v2 §4.1).
//
// TagPrefix constants are shared between the L1 substrate and the L3
// cf-convention-extension package. Placing them here avoids circular
// imports: L1 can import tagspec; L3 can import tagspec; L2 (cf-convention)
// can import tagspec without importing L3 (which would violate B2).
//
// This package has no external dependencies — only stdlib and no pkg/ imports.
// That constraint is enforced by the L1-narrow depguard rule.
package tagspec

// CampfirePrefix is the reserved namespace for campfire system messages.
// Tags beginning with this prefix are signed by campfire keys; convention
// declarations may not produce them (enforced by the convention parser).
const CampfirePrefix = "campfire:"

// ConventionPrefix is the namespace prefix for convention-defined tags.
// All convention tags MUST start with this prefix so they are never
// confused with campfire system tags.
const ConventionPrefix = "convention:"

// SessionPrefix is the namespace prefix for session lifecycle events.
// Session tags are campfire-key-signed system events (§1 system-event vocabulary).
const SessionPrefix = "session:"

// TagSessionOpen is the tag for session-open L1 system events.
//
// Posted by the session creator (orchestrator) into the session campfire when
// the session is created. The message payload carries session metadata:
// (session_id, parent_grant_chain_root, dispatcher_capability_template, until).
//
// Signed by the session creator's Ed25519 identity key — not a shared ephemeral
// key. This makes the creator's identity verifiable post-hoc even after the
// session campfire is compacted.
//
// Wire value: "session:open" — frozen at cf-protocol 1.0 (design v2 §10.6).
const TagSessionOpen = "session:open"

// TagSessionClose is the tag for session-close L1 system events.
//
// Posted by the session creator (orchestrator) when the session ends
// (cf session end, or TTL elapsed). Consumers treat messages after this
// tag as post-session cleanup; the session campfire may be compacted.
//
// Signed by the session creator's Ed25519 identity key.
//
// Wire value: "session:close" — frozen at cf-protocol 1.0 (design v2 §10.6).
const TagSessionClose = "session:close"

// TagCampfireVisibilityChanged is the reserved tag for campfire visibility-changed
// system events (design v2 §4.1; protocol-spec §cf-protocol 1.0 Layer-1 Additions).
//
// Fires when a campfire transitions from public to private (open → invite-only).
// Signed by the campfire key — not the operator or any member key. Receivers
// MUST reject any campfire:visibility-changed message whose signature does not
// verify against the campfire's public key.
//
// Wire value: "campfire:visibility-changed" — frozen at cf-protocol 1.0.
const TagCampfireVisibilityChanged = "campfire:visibility-changed"

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

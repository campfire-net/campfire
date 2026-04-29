// Package protocol is the public surface of the cf-protocol 1.0 substrate
// (campfireagent-753: Stage 1 scaffold, design v2 §4.1).
//
// # Public Surface (L1 freeze)
//
// cf-protocol 1.0 exposes exactly the protocol-spec.md primitives:
//
//   - Client — the unified campfire client (Init, Send, Read, Subscribe, Await,
//     Create, Join, Leave, Disband, Admit, Evict, Members, Scope).
//   - Message — the SDK-facing message type (signed envelope fields only).
//   - MemberRecord — a campfire member descriptor.
//   - Transport — the sealed transport-config interface (FilesystemTransport,
//     P2PHTTPTransport, GitHubTransport).
//   - Request/result types for every Client method.
//   - Reserved tags: TagFuture ("future") and TagFulfills ("fulfills") — the DAG
//     primitive pair frozen at wire level.
//   - Reserved ops: IsReservedOp / ReservedOps — the 10-op floor frozen at L1.
//   - Error sentinels: ErrAwaitTimeout, ErrNotMember, ErrScopeDenied.
//
// # Deliberately NOT in the public surface
//
// The following are NOT exported by cf-protocol. They live in higher layers:
//   - Gate evaluator interface and predicate AST (cf-authority, L3).
//   - Grant CBOR layout (cf-authority, L3).
//   - Convention declaration parser and executor (cf-convention, L2).
//   - Profile/identity cache — convention-level concerns (Stage 3, cf-session).
//   - Session shared-key form (deprecated, kept in pkg/protocol until Stage 3).
//
// # Layer boundary
//
// The depguard L1-narrow rule (§4.6) enforces that nothing inside
// cf-protocol/ imports cf-conventions or any L2/L3 package.
// The adversarial test in depguard_test.go proves the rule fires.
//
// # Type aliases
//
// All exported types are type aliases (= pkg/protocol.Type) so they are
// identical to the underlying types at the Go type system level. Code that
// already imports pkg/protocol continues to work without modification;
// code that imports cf-protocol/protocol can interop freely with pkg/protocol
// consumers until the physical move is complete in a later stage.
package protocol

import (
	pkgprotocol "github.com/campfire-net/campfire/pkg/protocol"
	reservedops "github.com/campfire-net/campfire/cf-protocol/internal/reserved-ops"
)

// ── Reserved tags (wire-level primitives, frozen at cf-protocol 1.0) ─────────

// TagFuture is the reserved tag that marks a message as a future (an
// unfulfilled request awaiting a response). Any sender may attach this tag.
// When a future is posted, Await waits for a message carrying TagFulfills
// with the future's ID in its antecedents.
//
// Wire value: "future" — frozen at cf-protocol 1.0.
const TagFuture = "future"

// TagFulfills is the reserved tag that marks a message as a fulfillment.
// A fulfilling message must list the target future's ID in its antecedents.
// Await returns the first message that satisfies both conditions
// (TagFulfills + target ID in antecedents); ties are broken by
// (earliest timestamp, lexicographically smaller ID).
//
// Wire value: "fulfills" — frozen at cf-protocol 1.0.
const TagFulfills = "fulfills"

// ── Type aliases — identical to pkg/protocol types ────────────────────────────

// Client is the primary cf-protocol API entry point.
// All campfire operations (Init, Send, Read, Subscribe, Await, Create, Join,
// Leave, Disband, Admit, Evict, Members, SetScope) are methods on Client.
//
// Create via Init() or InitWithConfig() for filesystem-discovered identity,
// or New() for programmatic construction (e.g. in tests).
//
// Client is NOT safe for concurrent use; use one Client per goroutine.
type Client = pkgprotocol.Client

// Message is the SDK-facing campfire message.
// Sender is the only cryptographically verified identity field (Ed25519 pubkey hex).
// Tags, Antecedents, Instance, and SenderCampfireID are sender-asserted (tainted).
type Message = pkgprotocol.Message

// MemberRecord describes a campfire member returned by Members().
type MemberRecord = pkgprotocol.MemberRecord

// Transport is the sealed interface for campfire transport configuration.
// Pass a concrete transport (FilesystemTransport, P2PHTTPTransport,
// GitHubTransport) to CreateRequest and JoinRequest.
type Transport = pkgprotocol.Transport

// FilesystemTransport configures the local filesystem transport.
type FilesystemTransport = pkgprotocol.FilesystemTransport

// P2PHTTPTransport configures the P2P HTTP transport.
type P2PHTTPTransport = pkgprotocol.P2PHTTPTransport

// GitHubTransport configures the GitHub-backed transport.
type GitHubTransport = pkgprotocol.GitHubTransport

// SendRequest holds parameters for Client.Send().
type SendRequest = pkgprotocol.SendRequest

// ReadRequest holds parameters for Client.Read().
type ReadRequest = pkgprotocol.ReadRequest

// ReadResult is the return value from Client.Read().
type ReadResult = pkgprotocol.ReadResult

// CreateRequest holds parameters for Client.Create().
type CreateRequest = pkgprotocol.CreateRequest

// CreateResult is the return value from Client.Create().
type CreateResult = pkgprotocol.CreateResult

// JoinRequest holds parameters for Client.Join().
type JoinRequest = pkgprotocol.JoinRequest

// JoinResult is the return value from Client.Join().
type JoinResult = pkgprotocol.JoinResult

// AdmitRequest holds parameters for Client.Admit().
type AdmitRequest = pkgprotocol.AdmitRequest

// EvictRequest holds parameters for Client.Evict().
type EvictRequest = pkgprotocol.EvictRequest

// EvictResult is the return value from Client.Evict().
type EvictResult = pkgprotocol.EvictResult

// AwaitRequest holds parameters for Client.Await().
type AwaitRequest = pkgprotocol.AwaitRequest

// SubscribeRequest holds parameters for Client.Subscribe().
type SubscribeRequest = pkgprotocol.SubscribeRequest

// Subscription is the return value from Client.Subscribe().
// Read from Messages() channel; check Err() after it closes.
type Subscription = pkgprotocol.Subscription

// ScopeConfig declares the campfire and operation-class allowlists for
// a scoped client. Passed to Client.SetScope().
type ScopeConfig = pkgprotocol.ScopeConfig

// CoSigner is a peer endpoint used during FROST threshold signing.
type CoSigner = pkgprotocol.CoSigner

// Syncer is the interface for transport-agnostic sync before read operations.
type Syncer = pkgprotocol.Syncer

// ── Error sentinels ────────────────────────────────────────────────────────────

// ErrAwaitTimeout is returned by Await when the timeout expires before a
// fulfilling message is found.
var ErrAwaitTimeout = pkgprotocol.ErrAwaitTimeout

// ErrNotMember is returned by Leave, Members, and other operations when the
// caller is not a member of the requested campfire.
type ErrNotMember = pkgprotocol.ErrNotMember

// ErrScopeDenied is returned when a campfire or operation is blocked by the
// client's scope enforcer.
var ErrScopeDenied = pkgprotocol.ErrScopeDenied

// RoleError is returned when the sender's campfire role does not permit
// the requested operation (e.g. observer trying to send a non-system message).
type RoleError = pkgprotocol.RoleError

// ── Constructor functions ───────────────────────────────────────────────────

// Init creates a Client by discovering identity and store from cfHome.
// cfHome defaults to ~/.cf when empty.
//
// Init is the standard entry point for CLI tools and agents that operate
// against the user's default campfire home directory.
var Init = pkgprotocol.Init

// InitWithConfig discovers and merges config files from the cascade
// (global ~/.cf/config.toml, ancestor .cf/ files, CWD .cf/config.toml)
// and delegates to Init. Recommended for 0.16+ callers.
var InitWithConfig = pkgprotocol.InitWithConfig

// New creates a Client wrapping an existing store and identity.
// identity may be nil for read-only clients.
// Useful for tests and for embedded use cases where the caller manages
// the store lifecycle directly.
var New = pkgprotocol.New

// ── Reserved-op floor (L1 freeze, campfireagent-935) ──────────────────────────

// ReservedOps is the authoritative list of the ten reserved operations frozen
// at cf-protocol 1.0. No convention declaration and no parent grant can lower
// the gate on any of these ops; they require owner-level authority.
//
// This is the public re-export of cf-protocol/internal/reserved-ops.ReservedOps.
// L2 enforcement (pkg/convention dispatcher) MUST reference this list so that
// the single source of truth stays at L1.
//
// Wire value: sorted for stable iteration. Additions are MAJOR bumps
// (the F6 Commitment — see cf-protocol/COMPATIBILITY.md).
var ReservedOps = reservedops.ReservedOps

// IsReservedOp reports whether op is one of the ten reserved operations.
// L2 enforcement code (convention dispatcher) calls this before registering or
// dispatching any convention operation. Returns true for all ops in ReservedOps.
//
// This is the public re-export of cf-protocol/internal/reserved-ops.IsReserved.
var IsReservedOp = reservedops.IsReserved

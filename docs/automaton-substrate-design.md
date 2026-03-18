# Automaton Substrate Design

**Status:** Living document
**Date:** 2026-03-18
**Covers:** Membership roles, compaction, named views, follow mode
**Phase:** P1 complete, P2 requirements defined

## Overview

The automaton substrate extends the campfire protocol with structured coordination primitives: membership roles (observer/writer/full), append-only compaction, named views with S-expression predicates, and continuous follow mode. These features enable agents to self-organize within campfires — restricting access, managing history, creating filtered perspectives, and maintaining real-time awareness.

This document specifies the design as implemented in P1 (filesystem transport, client-side enforcement) and the requirements for P2 (transport-agnostic, transport-enforced).

## P1 — Implemented Design

### Membership Roles

Three roles govern what a member can do within a campfire:

| Role | Read | Send (regular) | Send (campfire:*) | Manage members |
|------|------|----------------|-------------------|----------------|
| observer | yes | no | no | no |
| writer | yes | yes | no | no |
| full | yes | yes | yes | yes |

**Admission.** `cf admit <campfire-id> <pubkey> --role <role>` writes a `MemberRecord` with the specified role to the transport directory and emits a `campfire:member-joined` system message. Default role is `full` for backward compatibility with pre-role campfires.

**Enforcement.** P1 enforcement is client-side only. `checkRoleCanSend()` in `role.go` inspects the local store's membership record before allowing `cf send`, `cf compact`, or `cf view create`. A malicious or non-conforming client can bypass this. Transport-enforced roles are P2 scope.

**Legacy compatibility.** `EffectiveRole()` maps empty strings and unknown legacy values (`"member"`, `"creator"`) to `full`, preserving behavior for campfires created before the role system.

**P1 limitation: no role mutation after admission.** Once admitted with a role, the only way to change a member's role is to evict and re-admit. There is no `cf member set-role` or equivalent. See P2 requirements below.

### Compaction

Compaction creates a `campfire:compact` system message that marks older messages as superseded. Messages are never deleted — compaction is append-only. Requires `full` role.

**Payload schema:**
```json
{
  "supersedes": ["msg-id-1", "msg-id-2", ...],
  "summary": "human-readable summary of compacted content",
  "retention": "archive | discard",
  "checkpoint_hash": "sha256(sorted(id|hex(signature)) for each superseded message)"
}
```

**Antecedents:** The compaction message's antecedent list contains the ID of the last superseded message, establishing causal ordering.

**Read behavior:** `cf read` excludes superseded messages by default. `cf read --all` shows everything including compacted messages. Views with `RespectCompaction: true` also exclude superseded messages.

**P1 limitation: filesystem-only send path.** `compact.go` calls `sendFilesystem()` directly, using `m.TransportDir` from the local store to locate the filesystem transport directory. GitHub and HTTP transport campfires cannot use `cf compact`. See P2 requirements below.

### Named Views

Views are predicate-filtered, optionally projected message queries stored as `campfire:view` system messages. Requires `full` role to create.

**View definition payload:**
```json
{
  "name": "standing-decisions",
  "predicate": "(and (tag \"memory:standing\") (gt (field \"payload.confidence\") (literal 0.5)))",
  "projection": ["sender", "payload", "timestamp"],
  "ordering": "timestamp desc",
  "limit": 100,
  "refresh": "on-read"
}
```

**Predicate grammar.** S-expressions supporting: `tag`, `sender`, `and`, `or`, `not`, `gt`, `lt`, `eq`, `field`, `literal`. Predicate depth is bounded to prevent stack overflow.

**Materialization.** `cf view read <campfire-id> <name>` finds the latest `campfire:view` message with the given name, parses its predicate, evaluates it against all non-system messages (excluding superseded), and returns filtered/projected/ordered results.

**P1 limitation: views are local-only.** `view.go` stores the `campfire:view` message in the local SQLite store via `s.AddMessage()` but does not write it to the transport. Other agents in the same campfire cannot see view definitions — each agent must independently create the same view. This contradicts the design intent that `campfire:view` messages are regular campfire messages visible to all members. See P2 requirements below.

### Follow Mode

`cf read --follow` enables continuous polling for new messages. The implementation uses post-fetch cursor filtering: it fetches all messages, then filters to those after the last-seen timestamp.

**P1 limitations:**
- **Post-fetch filtering.** Follow mode fetches all messages on each poll cycle, then applies a cursor client-side. For campfires with large message histories, this is wasteful. The SQL-pushed filtering path (`store.ListMessages` with `MessageFilter`) exists but follow mode doesn't use it.
- **`--follow` + `--json` + `--fields` incompatibility.** The `--follow` path does not wire through the `--fields` projection flag. Messages are output without field filtering in follow+json mode.

These are implementation bugs, not spec gaps. They should be fixed in P1.

## Phase Boundary Rationale

P1 was intentionally scoped to filesystem transport with client-side enforcement. This was a deliberate phase boundary, not an oversight:

1. **Filesystem transport is the development and testing substrate.** All agents on a single host share `/tmp/campfire`. This enables rapid iteration without network complexity.
2. **Client-side enforcement proves the role model.** Before investing in transport-level enforcement (which varies per transport), P1 validates that the three-role model (observer/writer/full) is sufficient for real coordination patterns.
3. **Local-only views prove the query model.** Before propagating views as messages, P1 validates the S-expression predicate grammar, projection, ordering, and materialization semantics.

The trade-off: P1 features that touch the transport (admit, compact, view propagation) only work with filesystem campfires. This is acceptable for single-host agent coordination but blocks multi-host and cross-network use cases.

## P2 — Transport Compatibility

P2 lifts the filesystem restriction. Every feature that currently hardcodes `fs.New()` or `sendFilesystem()` must work across all three transports: filesystem, GitHub, and P2P HTTP.

### Requirement 1: Transport-Agnostic Admission

**Current state.** `admit.go` imports `pkg/transport/fs` and calls `fs.New(fs.DefaultBaseDir())` directly. It uses `transport.WriteMember()` and `transport.WriteMessage()` which are filesystem-specific methods.

**P2 requirement.** Admission must resolve the campfire's transport from the local store (the membership record already stores `transport_dir` for filesystem and could store transport type + endpoint for others) and dispatch to the appropriate transport backend.

**Per-transport admission path:**

| Transport | Member record | System message | Discovery |
|-----------|--------------|----------------|-----------|
| Filesystem | Write `.cbor` to `members/` dir | Write `.cbor` to `messages/` dir | Existing peer reads dir |
| GitHub | Create file via GitHub API in `members/` path | Create file via GitHub API in `messages/` path | Existing peer fetches repo |
| P2P HTTP | POST to `/members` endpoint | POST to `/messages` endpoint | Existing peer polls endpoint |

**Acceptance criteria:** `cf admit` works for campfires created with any transport. The transport type is determined from the membership record, not hardcoded.

### Requirement 2: Transport-Agnostic Compaction

**Current state.** `compact.go` calls `sendFilesystem()` directly, using `m.TransportDir` to locate the filesystem path. The compaction payload and semantics are transport-independent, but the send path is not.

**P2 requirement.** Compaction must use the same transport-dispatch mechanism as regular `cf send`. The `execCompact` function should construct the `campfire:compact` message and hand it to a transport-agnostic send path, not call `sendFilesystem()` directly.

**Distributed compaction considerations:**

- **No quorum required for P2.** Compaction is a message like any other — any `full` member can emit one, and all members who receive it apply it to their local view. Conflicting compactions (two members compact overlapping ranges) are resolved by union: a message superseded by any compaction event is excluded.
- **Checkpoint hash verification.** Members receiving a compaction event can verify the checkpoint hash against their local copies of the superseded messages. A mismatch indicates message divergence (missing messages, corruption, or a dishonest compactor). The receiving agent should flag the mismatch but still apply the compaction.
- **Compaction propagation.** The compaction message propagates through the same transport channel as regular messages. No special propagation mechanism needed.

**Acceptance criteria:** `cf compact` works for campfires on any transport. The compaction message is sent through the campfire's configured transport, not hardcoded to filesystem.

### Requirement 3: View Propagation

**Current state.** `view.go` creates a signed `campfire:view` message but only stores it locally via `s.AddMessage()`. It never writes to the transport. Other members cannot see or use the view definition.

**P2 requirement.** View creation must write the `campfire:view` message to the transport (same as any other message), so all members can discover and materialize views defined by other members.

**Design intent.** Views are campfire-scoped, not agent-scoped. A `full` member defines a view, and all members (including observers) can materialize it. This enables patterns like:
- A coordinator defines a `"decisions"` view; all participants can query it.
- An observer creates a `"alerts"` view filtered to high-priority tags; the view definition propagates so other agents can adopt it.

**View conflict resolution.** Multiple members may create views with the same name. The latest-by-timestamp definition wins (already implemented in `findLatestView`). This is eventually consistent — different members may briefly see different definitions until propagation completes.

**Acceptance criteria:** `cf view create` writes the `campfire:view` message to both the local store and the campfire's transport. `cf view list` and `cf view read` can discover and materialize views created by other members (after pulling new messages).

### Requirement 4: Role Mutation Lifecycle

**Current state.** Roles are set at admission time via `cf admit --role`. There is no mechanism to change a member's role after admission. The only path is evict + re-admit, which creates a new `member-joined` event and loses the original join timestamp.

**P2 requirement.** Add `cf member set-role <campfire-id> <pubkey> --role <new-role>` that:

1. Verifies the caller has `full` role.
2. Verifies the target member exists.
3. Updates the member record on the transport (overwrite the `.cbor` file for filesystem, PUT for HTTP, update file for GitHub).
4. Emits a `campfire:member-role-changed` system message:
   ```json
   {
     "member": "<pubkey-hex>",
     "previous_role": "observer",
     "new_role": "writer",
     "changed_at": 1742323200000000000
   }
   ```
5. Stores the updated role in the local membership record.

**Constraints:**
- A member cannot change their own role (prevents self-promotion).
- The campfire creator (identified by the campfire's own key signing the first `member-joined`) can always change roles. Other `full` members can change roles for non-full members only. This prevents lateral demotion wars between `full` members.
- Role changes take effect on the next read/send operation. There is no real-time revocation in P1 or P2 (client-side enforcement has inherent latency).

**Transport-enforced roles (P3).** P2 still uses client-side enforcement with the `campfire:member-role-changed` audit trail. True transport-level enforcement (where the transport rejects unauthorized sends) requires transport-specific middleware and is P3 scope. The `campfire:member-role-changed` message provides the audit trail that transport-level enforcement will consume.

**Acceptance criteria:** `cf member set-role` exists and works. Role change emits a system message. The member's effective role updates on subsequent operations.

## P2 Implementation Notes

### Transport Abstraction

The current codebase has no unified transport interface. `fs.Transport` has `WriteMember`, `WriteMessage`, `ListMembers`, `ListMessages`, `ReadState`, and `Remove`. GitHub and HTTP transports have their own method sets but no shared interface.

P2 should introduce a `transport.Transport` interface:

```go
type Transport interface {
    WriteMessage(campfireID string, msg *message.Message) error
    ListMessages(campfireID string) ([]message.Message, error)
    WriteMember(campfireID string, member campfire.MemberRecord) error
    ListMembers(campfireID string) ([]campfire.MemberRecord, error)
    ReadState(campfireID string) (*campfire.CampfireState, error)
}
```

Commands that currently hardcode `fs.New()` would resolve the transport from the membership record and call the interface methods.

### Transport Resolution

The local store's membership record should carry enough information to reconstruct the transport:

| Transport | Stored in membership | Resolution |
|-----------|---------------------|------------|
| Filesystem | `transport_dir` (path) | `fs.New(filepath.Dir(transport_dir))` |
| GitHub | `transport_github_repo` + `transport_github_path` | `github.New(repo, path, token)` |
| P2P HTTP | `transport_http_endpoint` | `http.New(endpoint)` |

### Follow Mode Fixes (P1 scope)

These are bugs, not P2 requirements. Fix in P1:

1. **Push cursor to SQL.** `--follow` should use `store.ListMessages` with a `SinceTimestamp` filter instead of fetching all messages and filtering client-side.
2. **Wire `--fields` through follow path.** The follow output path should respect `--fields` the same way the non-follow path does.

## Effort Estimates

| Requirement | Estimated LOC | Dependencies |
|-------------|---------------|--------------|
| Transport interface + resolution | ~200 | None |
| Transport-agnostic admission | ~150 | Transport interface |
| Transport-agnostic compaction | ~100 | Transport interface |
| View propagation | ~80 | Transport interface |
| Role mutation (`cf member set-role`) | ~200 | Transport interface |
| Follow mode fixes (P1) | ~50 | None |
| **Total** | **~780** | |

## Open Questions

1. **Should views be immutable?** Currently, creating a view with the same name overwrites the previous definition (latest-by-timestamp wins). Should there be an explicit `cf view update` or is implicit overwrite sufficient?
2. **Compaction authority.** Should compaction require a quorum of `full` members, or is single-member compaction sufficient? P2 says single-member is fine; revisit if adversarial compaction becomes a concern.
3. **Role change notification.** Should members be notified when their role changes? The `campfire:member-role-changed` message is visible to all members, but an observer being promoted to writer has no push notification — they discover it on their next operation.

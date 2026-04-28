# Convention Idioms and RFC Promotion

This document covers two load-bearing topics for cf-conventions:

1. **3+1 canonical idioms** — the four patterns every convention author should
   reach for before building something custom.
2. **RFC promotion list** — the queue of conventions in RFC status and the
   process for ratifying them.

Both topics are defined in the 0.30 design (§5.2 idioms, §4.3 T8 promotion
policy). This document is the single authoritative reference; README files in
each L3 package cite sections here.

---

## Part 1 — 3+1 Canonical Convention Idioms

### Overview

cf-conventions dispatches every operation through a typed convention
declaration. The declaration defines the operation's name, its arguments, its
gate predicate, and (for servers) any handler logic. Over time, four idioms have
emerged that cover the majority of daily-path agentic use cases.

The first three idioms are primary — they apply broadly across named consumers
(rd, dontguess, social, the reach, freeso). The fourth idiom (+1) is a
special-case pattern used by ephemeral worker fleets (swarm-coordination,
legion.tools) and is architecturally distinct because it involves per-session
identity issuance rather than per-operation dispatch.

---

### Idiom 1 — Convention-op-wraps-primitive (server-side Create)

**One-line summary:** The user invokes a convention op; the handler, running
server-side under the service's identity, calls the protocol `Create` primitive
and admits participants, then leaves. The resulting campfire is self-owned by
its participants.

#### When to use

Use this idiom whenever:

- A campfire needs to be provisioned on behalf of a user who does not yet hold
  a campfire identity with `create` authority.
- The provisioned campfire should be owned by its participants, not by the
  service that created it.
- You want the creation to be gated (e.g., requiring a verified user account)
  without exposing raw `Create` to anonymous callers.

Typical cases: private-message channel setup, matchmaking room creation, tenant
campfire provisioning.

#### Reference example — `social <user> dm`

```
User calls: cf <social-campfire> dm --user alice
Handler (running as social-service identity):
  1. client.Create(CreateRequest{…})          → creates the DM campfire
  2. client.Admit(AdmitRequest{campfire, alice})  → admits recipient
  3. client.Admit(AdmitRequest{campfire, caller}) → admits caller
  4. client.Leave(dmCampfireID)               → service leaves; campfire is theirs
  5. Returns campfire ID to caller
```

The service holds a `social:dm` grant from the resource owner. The created
campfire has no service member; the participants are the only members. The
service cannot read or write to the DM campfire after step 4.

The same pattern applies to `reach matchmaking find` and `freeso resident
init`.

#### Anti-patterns

- **Service stays a member.** If the service does not `Leave` after admitting
  participants, it retains read/write access to all provisioned campfires —
  a surveillance and trust violation.
- **Using raw `Create` from the caller's client.** The caller may not have
  `create` authority (cost, rate limit, policy). The whole point of this idiom
  is that the service holds the creation authority and delegates the result.
- **Skipping `Admit` before `Leave`.** Leaving without admitting participants
  disbands the campfire (no members → dissolved). Admit all intended members
  before leaving.

---

### Idiom 2 — Hybrid wrapper dispatch (the dontguess pattern)

**One-line summary:** The app's binary switches on operation: app-internal
operations go to the app's own binary; protocol primitives go to `cf-primitives`
directly; everything else goes through cf convention dispatch.

#### When to use

Use this idiom when:

- An app has its own server-side logic (e.g., an inference cache, a billing
  engine, a matchmaker) that must run before or instead of the convention
  executor.
- The app also needs to expose some operations as convention ops (so they work
  through the cf CLI and cf-mcp without custom tooling).
- Some operations are pure protocol primitives that should not be wrapped.

This idiom preserves the F3 MCP/CLI parity invariant: callers do not need to
know which routing branch handles a given op. The binary appears as a single
surface.

#### Reference example — dontguess

```
$ dontguess buy --task "..." --budget 5000
  → argv[0]=dontguess, op=buy
  → Routing switch:
    case "init", "serve":  → app-internal operator binary
    case "join", "leave":  → cf-primitives (raw protocol)
    default:               → cf convention dispatch to dontguess exchange campfire
```

The binary wraps three dispatch surfaces into one command. `buy` and `put`
reach the convention executor, which gates on `grant: dontguess:buy` and
`grant: dontguess:put` respectively. `init` and `serve` never touch the
convention layer.

#### Anti-patterns

- **Routing everything through the convention executor.** `init` and
  `join`/`leave` are pre-convention bootstrap operations. Wrapping them as
  convention ops creates a circular dependency (you need to join before you can
  call `join`).
- **Routing nothing through the convention executor.** An app that handles all
  ops internally bypasses gate evaluation, revocation, and grant-chain checks.
  Those safety properties exist precisely because the app should not be in the
  trusted computing base for authorization decisions.
- **Inconsistent CLI/MCP surface.** The F3 parity invariant requires that every
  convention op exposed through the cf CLI is also reachable through cf-mcp. If
  the routing switch is duplicated (one for CLI, one for MCP), they drift.
  Use a single `executor.Execute` call for the convention path.

---

### Idiom 3 — Service-side depth-1 admit grant

**One-line summary:** A service that admits users on demand holds a depth-1
`<conv>:admit` grant from the resource owner. The service emits single-use
grants to admitted users in the operation's response payload.

#### When to use

Use this idiom when:

- A service needs to admit users to a set of campfires it does not own.
- Admission should be gated (by identity, payment, tier, invite code, etc.)
  without the owner being online for each admission event.
- The resource owner trusts the service to admit, but does not want the service
  to issue open-ended grants.

The reserved-op floor sets depth ≤ 1 for `admit`. This idiom is the canonical
daily-path use of that constraint: the chain is `owner → service` (depth 1),
and the service issues single-use grants to users (depth 2, the hard limit).

#### Reference example — `reach matchmaking find`

```
Owner holds the reach campfire and has granted reach-service:
  grant: { convention: "reach", op_glob: "admit", depth: 1, until: 7d }

User calls: cf <reach-campfire> matchmaking find --criteria "..."
Handler (reach-service):
  1. Evaluates criteria, selects match campfire M
  2. client.Admit(AdmitRequest{campfire: M, principal: caller})
  3. Returns campfire M's address + a single-use grant scoped to M
     { convention: "reach", op_glob: "match:read", where: M, expiry: 1h }
```

The user receives a campfire address and a single-use, time-bounded grant. The
grant cannot be sub-delegated (depth limit reached). The reach service never
issues open-ended grants; each admission event is bounded.

#### Anti-patterns

- **Depth-2 admit grant.** The depth-1 limit on `admit` is a reserved-op floor
  enforced at L1. A convention declaration attempting to lower this floor to
  allow depth-2 admit grants is rejected at dispatch time with
  `DENY(reserved_op_floor)`. Do not attempt to work around this with
  intermediate "proxy" services — the evaluator re-walks the full chain.
- **Non-single-use grants in response.** If the grant returned to the user is
  not scoped to a specific campfire and operation, the user can present it
  across campfires the service did not intend to admit them to. Always set
  `where:` to the specific campfire.
- **Owner online for every admission.** The point of the pre-granted service
  credential is that the owner is not in the loop for each user admission.
  Requiring live owner approval per admission defeats the idiom and breaks the
  Budget B latency gate (§2.10 of the 0.30 design).

---

### Idiom 4 (+1) — Lazy-mint per-worker grant (ephemeral worker fleets)

**One-line summary:** An orchestrator runs a session handler that issues a
`delegation:grant` to each worker at the worker's first message
(`identity:introduce`). Workers receive exactly one grant, tied to their own
freshly-generated key.

#### When to use

This idiom is a special case. Use it when:

- You are running an ephemeral worker fleet (a swarm, a parallel dispatch, a
  legion.tools automaton team) where individual worker identities are not known
  at fleet-creation time.
- Each worker must be individually attributable and individually revocable.
- The fleet's authority must be transitively bounded by the orchestrator's
  grant, which is itself bounded by the human-root grant.

This idiom is architecturally distinct from idioms 1–3 because it involves the
*identity issuance* layer (cf-session, cf-authority chain evaluation) rather
than per-operation convention dispatch. It is the "ephemeral identity
provisioning" pattern, not an operation routing pattern.

#### Reference example — swarm-coordination (`cf session`)

```
Orchestrator calls: cf session create --ttl 2h
  → Posts session:open (campfire-key-signed) into session campfire S
  → Records (session_id, parent_grant_chain_root, dispatcher_capability_template, until)
  → No grants pre-minted

Worker W starts and calls: cf session $TOKEN claim-item --item_id X --description "..."
  → W generates a fresh Ed25519 keypair in its jail
  → W signs identity:introduce into S under its new key
  → Orchestrator session handler observes the introduce
  → Handler replies with delegation:grant:
      scope = parent_grant ⊓ dispatcher_capability_template
      parent_grant_id = orchestrator's grant from the human
      until = session.until
  → W's first op blocks on this grant (future mechanism)
  → W receives grant, caches it, proceeds with claim-item

If W is compromised: owner revokes W's grant by grant-id.
  → Cascade: only W's grant is revoked; other workers unaffected.
  → Session compaction: session campfire coordination messages
    may be pruned; grant lineage survives in identity campfire.
```

The key invariant: the orchestrator and any worker **never share a private key**.
Each worker generates its own key. The session token (`cfs2_…`) carries only
the campfire address and ephemeral resolution metadata — not a private key.

#### Anti-patterns

- **Pre-minting grants to anonymous workers.** If the orchestrator issues N
  grants before workers are known, compromising one worker yields N–1 future
  grants. Lazy-mint (grant issued on first `identity:introduce`) bounds blast
  radius to one grant per compromised worker.
- **Shared session key.** The pre-0.30 `cfs1_` token embedded a shared
  ephemeral private key. This destroys per-worker attribution — all messages
  from all workers look identical to the campfire. The 0.30 design removes this
  anti-feature entirely.
- **Using this idiom for single-worker operations.** Idioms 1–3 cover the
  single-operation case. This idiom is overhead for a single agent — use a
  direct grant instead. Reserve it for true fleets (≥2 ephemeral workers with
  independent attribution requirements).
- **Compacting the identity campfire too aggressively.** The grant chain lives
  in the identity campfire, not the session campfire. Compacting the identity
  campfire below the retention floor (`MAX(7d, max_revocation_staleness)`)
  breaks the dispatcher's re-walk to the human root and causes
  `UNRESOLVABLE` decisions on in-flight chains.

---

## Part 2 — RFC Promotion List

### Promotion Policy (T8)

A convention is promoted from **RFC** to **ratified** when it meets all three
criteria:

1. **Two named portfolio consumers** are actively using the convention in
   production or pre-production. "Named" means a project in the Third Division
   Labs portfolio that depends on the convention for a daily-path operation —
   not a demo, not a test fixture. Both consumers must be documented in this
   table.
2. **Conformance tests pass** for the convention's operations. The convention
   must have at least one integration test exercising the full dispatch path
   (gate evaluation → handler → response) for each declared operation.
3. **No open design questions** tagged `design-gap` in the open items tracker
   that touch this convention's declared operations or gate predicates.

A ratified convention may be **frozen** (no further changes to its declaration
schema or wire format) when:

- It has been ratified for ≥ one minor release cycle, AND
- No consumer has filed a change request against it, AND
- The L3 wire format snapshot has been added to the wire-freeze registry
  (`cf-conventions/wire-freeze/`).

Frozen conventions are governed by the same backward-compat policy as
cf-protocol 1.0: additions are minor, breaking changes are major.

### Promotion Process

```
RFC → review (two consumers + conformance gate) → ratified → freeze review → frozen
```

1. **RFC:** Convention declaration exists. At least one consumer is using it.
   Open design questions may still exist. Not suitable for cross-implementer
   use.
2. **Review:** Author opens a PR adding the convention to the "ratified"
   section below. PR description documents both named consumers and links to
   conformance test results. PR is blocked until the "No open design questions"
   criterion is satisfied.
3. **Ratified:** Two consumers confirmed. Conformance gate green. No open
   design questions. Convention is stable for cross-implementer use within the
   current major.
4. **Freeze review:** Author opens a PR moving the convention to the "frozen"
   section and adding the wire-freeze snapshot. Requires a one-week review
   window and sign-off from a core maintainer.
5. **Frozen:** Wire format locked. Only additive changes (new optional fields,
   new ops with backward-compatible gates) are permitted without a major bump.

### Convention Status Table

| Convention | Package | Status | Named Consumers | Ratification Criteria | Target Version |
|---|---|---|---|---|---|
| `ready` | `cf-conventions/cf-ready` | RFC | rd (work management), campfire-agent (swarm dispatch) | Two consumers confirmed; conformance tests pending; OPEN-008 (gate evaluator harness) must close | 0.30.x |
| `dontguess` | `cf-conventions/cf-dontguess` | RFC | dontguess exchange (buy/put/settle), campfire-agent (dontguess buy before exploration) | Two consumers confirmed; conformance tests pending; OPEN-013 (anti-flood for level:0) must close for `buy` | 0.30.x |
| `swarm-coordination` | `cf-conventions/cf-session` | RFC | campfire-agent (swarm dispatch), legion (worker fleet) | Two consumers confirmed pending legion port; OPEN-004 (cf-session reframe) must close; conformance tests pending | 0.3x |
| `design-deliberation` | `cf-conventions/cf-deliberation` | RFC | campfire-agent (adversarial design), agentic-internet-ops (design reviews) | Two consumers confirmed; conformance tests pending; no open design questions | 0.30.x |
| `cf-identity` | `cf-conventions/cf-identity` | RFC | rd (identity assertion), social (user identity) | One consumer confirmed; second pending social launch; OPEN-009 (TOFU-as-grants) must close | 0.3x |
| `cf-authority` | `cf-conventions/cf-authority` | RFC | rd (grant evaluation), dontguess (spend-bound gate) | Two consumers pending; OPEN-008 (GateEvaluator conformance harness) is the ratification gate | 0.30.x |
| `cf-discovery` | `cf-conventions/cf-discovery` | RFC | social (lobby list), the reach (matchmaker find) | Two consumers pending social/reach launch; OPEN-005 (multi-level snippet chain) must close | 0.3x |
| `social` | `cf-conventions/cf-social` | RFC | social app (dm, lobby, post) | One consumer; second pending; no open design questions | 0.3x |
| `reach` | `cf-conventions/cf-reach` | RFC | the reach (matchmaking, profile) | One consumer; second pending | 0.3x |

### Notes

**Sponsorship.** Each RFC entry has an implicit sponsor: the first named
consumer's owning team. The sponsor is responsible for driving the convention
to ratification. If a convention has been in RFC status for more than two minor
release cycles without a second named consumer, the sponsor must either recruit
a consumer or move the convention to "parked" status.

**Parked vs. frozen.** A parked convention is one where work has stopped but
the convention has not been ratified. It is preserved in the codebase but not
guaranteed to be maintained. A frozen convention has been ratified and its wire
format locked. These are different states with different guarantees.

**Cross-module versioning.** Per §4.5 of the 0.30 design, L3 package wire
formats are bound to cf-conventions's major version, not cf-protocol's. A
convention's ratification does not bump cf-protocol. A frozen convention's wire
snapshot is added to `cf-conventions/wire-freeze/` and verified in CI against
the current binary on every release tag.

---

*Design references: 0.30-design.md §5.2 (three primary idioms), §2.9
(lazy-mint per-worker grant), §4.3 T8 (RFC promotion policy); round-2
cf-session-ergonomics.md (fourth idiom specification); OPEN-019, OPEN-029.*

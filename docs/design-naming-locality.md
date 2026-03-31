# Design: Unified Name Resolution and Identity Locality

**Work item:** campfire-agent-kgl
**Status:** Draft (revised)
**Date:** 2026-03-30
**Author:** Campfire Architect

---

## 1. Problem Statement

A developer types `cf galtrader help` and it should work. Today it doesn't unless they have performed a ritual: set `CF_ROOT_REGISTRY`, created an alias, joined the right campfire, and are standing in the right directory.

Three systems are broken:

1. **No identity lineage.** `cf init --name worker-1` produces a blank island. The new agent has no roots, no aliases, no memberships — no shared context with its creator. Every agent bootstraps from zero.

2. **No layered resolver.** `tryNamingResolve` only searches two sources: the project-local `.campfire/root` (walk-up from CWD) and `CF_ROOT_REGISTRY` (env var). There is no concept of a consult-based root selection strategy, and no way to add a new root source without editing source code.

3. **Auto-join is underspecified.** The `AutoJoinFunc` hook on `Resolver` exists but is never wired at the CLI layer. When resolution finds a campfire the agent isn't a member of, it either fails silently or errors, rather than joining and retrying.

---

## 2. Locality Model

Name resolution follows layers, searched in order. Each layer is a source of campfire IDs. The first hit wins.

```
Step 0 — Alias expansion       (pre-resolve, input preprocessing)
Step 1 — Membership prefix     (store: campfires the identity has joined)
Step 2 — Consult root agent    (ask configured agent → get root(s) → resolve on-network)
```

The analogy is `/etc/hosts → resolv.conf → DNS recursive resolver`. Steps 0 and 1 are pure-local (no network). Step 2 involves network I/O against campfire roots.

### What beacons are (and are not)

Beacons are **transport negotiation**, not naming. A beacon tells a client *how to connect* to a campfire once you have its ID — what transport protocol, what address, what join protocol. The meaning of a beacon comes from **where you find it** (filesystem path, GitHub org, DNS TXT record), not from its description field.

`Scan()` already exists and is used for ID-prefix matching during `resolveCampfireID`. That is correct usage: scan by campfire ID prefix to locate a transport, not to look up a human-readable name. Adding `ScanByName` to search beacon description fields would conflate transport negotiation with naming. Do not do this.

Beacons belong **after** resolution: once you have a campfire ID from the naming layer, beacons tell you how to connect. They are orthogonal to naming.

### What aliases are (and are not)

Aliases (`~home`, `~baron`) are **command sugar** — keystroke shortcuts that abbreviate campfire IDs. They are input preprocessing that happens before resolution, not a naming layer within the resolver chain. `cf ~galtrader help` expands `~galtrader` to a campfire ID before any resolution logic runs. The `AliasStore` (already implemented in `pkg/naming/alias.go`) handles this.

Aliases are managed explicitly by the user or set automatically during `cf init` and `cf root init`. They are not a discovery mechanism.

### Step semantics

**Step 0 — Alias expansion.** Already exists (`naming.AliasStore`). Unchanged. When the input is a tilde-alias (`~name` or `cf://~name`), expand to the stored campfire ID immediately — before membership search, before network I/O. Fast, no I/O. If alias not found, fall through.

**Step 1 — Membership prefix.** Already exists in `resolveCampfireID`. Unchanged. Searches campfires the identity has joined by ID prefix and in-band beacons. If no prefix match, fall through.

**Step 2 — Consult root agent.** Ask a configured agent which root(s) to search. The agent returns an ordered list of campfire IDs to try as naming roots. For each root returned, run `resolveNameInRoot` (the existing `naming.Resolve` direct-read). The first hit wins.

### Consult-based root selection

The hardcoded `project root → operator root → env var` ladder is replaced by a single pluggable mechanism: a **consult agent**.

The user-wide config (`~/.campfire/join-policy.json`) sets:

```json
{
  "join_policy": "consult",
  "consult_campfire": "<campfire-id>",
  "join_root": "<campfire-id>"
}
```

- `join_policy` is always `"consult"` — ask an agent via a campfire convention.
- `consult_campfire` is the campfire ID of the agent that answers root-selection queries.
- `join_root` is the default root to seed the consult with (e.g. the operator root).

The consult convention is: send a `join-root-selection:query` future with the name being resolved; the agent replies with an ordered list of campfire IDs to search. The resolver awaits the reply with a short timeout and walks the list.

**`fs-walk` is one implementation.** Walking the filesystem for `.campfire/root` files (the existing `ProjectRoot()` walk-up) is just a particular consult agent implementation — one that reads local filesystem state and returns roots it finds. DNS TXT record lookups, team directory queries, and other strategies are equally valid implementations of the same convention interface. No special case in the resolver.

**Why one mechanism?** The hardcoded layer stack embeds policy in code. Each new root source requires a source change. The consult model makes root selection a configuration decision: change the consult agent to change how roots are discovered. The resolver stays simple.

### What each step provides

| Step | Provides | I/O | Requires |
|------|----------|-----|---------|
| Alias expansion | Exact alias → ID | None | Manual alias set |
| Membership prefix | Prefix match + in-band beacons | Local store | Prior join |
| Consult root agent | Root campfire list → resolve on-network | Network | `join-policy.json` configured |

---

## 3. Identity Lineage

### Current state

`cf init --name worker-1` creates a fresh identity at `~/.campfire/agents/worker-1/` with:
- A new Ed25519 keypair (`identity.json`)
- A home campfire (invite-only, no members except self)
- A `CONTEXT.md`
- An alias "home" → home campfire ID

Nothing is inherited. The named agent starts on a blank island.

### What should be inherited

When creating a named agent, campfire should copy the following from the creator's identity (the identity active at `cf init` time — i.e., the current `CF_HOME`):

1. **Join policy config.** Copy `join-policy.json` from creator's home. The new agent resolves names through the same consult agent as its creator. This is the single most impactful change — it connects the new agent to the same naming graph immediately.

2. **Operator root reference.** Copy `operator-root.json` from creator's home. The operator root is the canonical join root and is referenced by the join policy. Copying it ensures the new agent can resolve names in the operator namespace without extra setup.

3. **Aliases.** Copy `aliases.json` from creator's home. The new agent starts with the same short-name mappings.

4. **Root memberships.** For each root campfire reachable via the inherited join policy that is marked for auto-join, join the new agent. Record membership in the agent's store.

What is **not** inherited:
- The creator's private identity key (obviously)
- The creator's campfire memberships (those are earned, not granted)
- The creator's home campfire (each agent has its own)

### Implementation: `cf init --name` with inheritance

```
cf init --name worker-1 [--from <cfHome>]
```

`--from` defaults to current `CF_HOME`. This makes the parent explicit and auditable. The init sequence becomes:

1. Generate keypair, save to `~/.campfire/agents/worker-1/identity.json`
2. Create home campfire (existing behavior)
3. Copy `join-policy.json` from parent (if present)
4. Copy `operator-root.json` from parent (if present)
5. Copy `aliases.json` from parent (if present)
6. For each auto-join root reachable via the inherited join policy: join the root campfire with the new agent's identity
7. Write `meta.json` (see below)
8. Write `CONTEXT.md` (existing behavior)
9. Print new identity pubkey + location

For `cf init --session`: sessions are ephemeral workers. Copy `join-policy.json` and `operator-root.json` only (not aliases — sessions shouldn't carry the full command vocabulary). Auto-join is not performed for session identities; they resolve lazily.

### The `parent_cf_home` field

Add an optional `parent_cf_home` field to the identity directory metadata (a new `~/.campfire/agents/<name>/meta.json`):

```json
{
  "name": "worker-1",
  "parent_cf_home": "/home/baron/.campfire",
  "created_at": 1743000000
}
```

This allows `cf` to re-sync a named agent's join policy and aliases from its parent when the parent changes. Command: `cf identity sync [--name worker-1]`.

---

## 4. Resolver Chain

The resolver chain replaces the current `tryNamingResolve` two-step with alias expansion, membership search, and consult-based root resolution.

### New `resolveByName` function (replaces `tryNamingResolve`)

```
func resolveByName(name string, s store.Store) (string, error):
  1. [Already done before this point] Alias expansion: if name is a tilde-alias, resolveCampfireID has already expanded it.
  2. [Already done before this point] Membership prefix: resolveCampfireID has already searched membership table and beacon dirs by ID prefix.
  3. Consult root agent:
     a. Load join-policy.json from CFHome()
     b. If no policy configured: try ProjectRoot() walk-up as fallback (backward compat)
        then try CF_ROOT_REGISTRY env var as final fallback
     c. If policy configured: query the consult campfire → get ordered list of root IDs
     d. For each root ID: resolveNameInRoot(rootID, name)
     e. First success wins; return ErrNameNotFound if all miss
```

The fallback to `ProjectRoot()` and `CF_ROOT_REGISTRY` preserves backward compatibility for existing installs that don't yet have `join-policy.json`. Once the policy is configured, the fallbacks are bypassed.

### The consult convention

The `join-root-selection` convention has two operations. Declarations are in `pkg/convention/declarations/join-root-query.json` and `pkg/convention/declarations/join-root-result.json`.

#### `join-root-query` (resolver → consult agent)

Sent as a future by the resolver. Fields:

| Arg | Type | Required | Description |
|-----|------|----------|-------------|
| `name` | `string` | yes | The name being resolved, e.g. `"galtrader"` or `"aietf.social.lobby"`. Max 253 chars, lowercase alphanumeric + hyphens + dots. |
| `join_root` | `campfire` | no | The operator's configured default join root. The consult agent may use it as a seed, prepend it, or ignore it. |

Produces tag: `join-root-selection:query`. Response mode: `sync`, timeout `10s` (resolver blocks on the await). Rate limit: 60/sender/min.

#### `join-root-result` (consult agent → resolver)

Fulfills the query future. Fields:

| Arg | Type | Required | Description |
|-----|------|----------|-------------|
| `roots` | `json` | yes | Ordered JSON array of campfire ID strings. First element is highest priority. Empty array means no roots found — resolver falls back. Max 16 entries; extras are ignored. |

Produces tag: `join-root-selection:result`. Antecedent: `exactly_one(target)` (must reference the query future).

#### Interaction sequence

```
Resolver                              Consult agent
  │── join-root-query (future) ──────────▶ │
  │   { name, join_root }                   │
  │◀─ join-root-result (fulfills) ──────── │
  │   { roots: ["id1", "id2", ...] }        │
  │                                          │
  │  (resolver tries id1, then id2, ...)
```

The resolver calls `client.Await` on the future message with a 10-second timeout. If the await times out or the consult campfire is unreachable, the resolver falls back to the `ProjectRoot()` walk-up and `CF_ROOT_REGISTRY` env var.

#### Artifact inventory

This convention is **implementation-private** — it operates between the resolver (machine caller) and a locally or operator-configured consult agent. It does not need:

- An AIETF spec doc in `agentic-internet/docs/conventions/` (not a cross-org standard; no interop requirement beyond the campfire repo itself)
- Updates to `docs/cli-conventions.md` (not user-invoked; the resolver sends queries, not the user)
- Updates to `docs/mcp-conventions.md` (not something AI agents call by name via MCP)

It does need:
- `pkg/convention/declarations/join-root-query.json` ✓
- `pkg/convention/declarations/join-root-result.json` ✓
- Implementation in `naming/resolve.go` (`consultLayer`) and the `fs-walk` built-in handler

The `fs-walk` built-in consult agent walks the filesystem from CWD upward for `.campfire/root` files and returns any root IDs found, followed by the `join_root` from config. This matches the current `ProjectRoot()` behavior but expressed as a convention handler rather than hardcoded logic.

### Auto-join in the resolver

When `resolveNameInRoot` returns a campfire ID from a root the agent hasn't joined yet, the resolver should auto-join if the campfire is open-protocol. The `AutoJoinFunc` hook on `naming.Resolver` should be wired at the CLI layer:

```go
resolver.AutoJoinFunc = func(campfireID string) error {
    return autoJoinIfOpen(campfireID, client, s)
}
```

`autoJoinIfOpen` checks `join_protocol == "open"` on the campfire's beacon (if present) and calls `client.Join` with `JoinProtocol: "open"`. On `invite-only`, it returns `naming.ErrInviteOnly` without auto-joining.

Auto-join is recorded in the store so subsequent commands don't re-join. The resolver's `AutoJoinFunc` is only invoked when the agent isn't already a member of the campfire.

### Adding a new resolution strategy

New root-selection strategies are implemented as consult agent handlers registered on a campfire. They speak the `join-root-selection` convention and return root lists. No source changes to the resolver are needed.

The resolver chain is intentionally minimal:

```go
type NameLayer interface {
    Resolve(ctx context.Context, name string) (campfireID string, ok bool, err error)
    Name() string  // for error messages
}
```

Two implementations: `aliasLayer` (fast local lookup) and `consultLayer` (network). The membership prefix search stays in `resolveCampfireID` as it is today — it's ID-based, not name-based.

---

## 5. Bootstrap Flows

### `cf init` (default — persistent identity for operator)

```
1. Check if identity exists at ~/.campfire/identity.json
2. If exists: print pubkey, exit (existing behavior)
3. Generate keypair → identity.json
4. Create home campfire + alias "home" (existing behavior)
5. Write CONTEXT.md
6. Print: identity created, next steps
```

No roots are auto-discovered or joined. The operator explicitly runs `cf root init --name <org>` afterward. This is intentional — the operator identity is the trust anchor; it should be configured deliberately.

**UX addition**: After creating a fresh identity, `cf init` should print:

```
Next steps:
  cf root init --name <org>     create your operator root (for naming)
  cf join <id>                  join a campfire
  cf discover                   find nearby campfires
```

This surfaces the naming step without requiring it.

### `cf init --name worker-1` (named agent — inherits from operator)

```
1. Determine parent: --from flag or current CF_HOME (~/.campfire)
2. Check if identity exists at ~/.campfire/agents/worker-1/identity.json
3. If exists: print pubkey, exit
4. Generate keypair
5. Create home campfire + alias "home"
6. Copy from parent (if parent has it):
   - join-policy.json
   - operator-root.json
   - aliases.json
7. For each auto-join root in inherited join policy: join with new identity
8. Write meta.json { name, parent_cf_home, created_at }
9. Write CONTEXT.md
10. Print: identity created at ~/.campfire/agents/worker-1/, pubkey, inherited roots
```

### `cf init --session` (ephemeral agent)

```
1. Create temp dir: /tmp/cf-session-<random>/
2. Generate keypair → identity.json
3. Copy join-policy.json and operator-root.json from current CF_HOME (if present) — for name resolution
4. Do NOT copy aliases (sessions are short-lived)
5. Do NOT auto-join roots
6. Print: <tmpdir>\n<display-name>
```

Session identities resolve via the consult agent (and thus the operator root) only. They don't carry full state because they're typically spawned for a single task and discarded.

### `cf root init --name baron` (operator root creation)

Already implemented. No change needed. After running:
- `operator-root.json` is written with the root campfire ID
- Alias `~baron` is set

The operator should then configure the join policy to use the operator root as the `join_root`:

```bash
cf join-policy set --consult <consult-campfire-id> --join-root <operator-root-id>
```

Or, for the simple single-operator case where `fs-walk` is sufficient:

```bash
cf join-policy set --fs-walk --join-root <operator-root-id>
```

This registers the built-in filesystem-walk consult agent and sets the operator root as the default join root.

---

## 6. Local → Public Promotion Path

### Scenario

I built `galtrader` locally. Its campfire ID is known on my machine (alias `~galtrader`). I want it findable from other machines via `cf galtrader help`.

### Current state

No documented promotion path. The operator must manually know to call `naming.Register`. The pieces exist but aren't assembled into a user-facing workflow.

### Promotion path

**Step 1: Local access (already works)**

You created the campfire. You have the alias `~galtrader`. On your machine, `cf ~galtrader help` already works. Other users on the same machine who are members of the campfire can use the campfire ID directly.

**Step 2: Register in operator root**

```bash
cf name register galtrader <campfire-id>
# or: cf name register galtrader ~galtrader  (resolves alias first)
```

This posts a `naming:name:galtrader` registration message to the operator root campfire (using `naming.Register` in `pkg/naming/register.go`). Any agent whose join policy routes through the operator root can now resolve `galtrader` via the consult step.

After registration:
- On the same machine: `cf galtrader help` works (operator root hit)
- Other machines with a join policy that routes to this operator root: also works

**Step 3: Register in a public root (optional)**

For global discoverability:

```bash
cf name register galtrader <campfire-id> --root <system-root-id>
# or: cf name register galtrader <campfire-id> --public  (uses CF_ROOT_REGISTRY)
```

The `--root` flag specifies which root campfire to register in. The `--public` flag uses the configured public root.

**Ceremony summary:**

```
Accessible via alias:       cf ~galtrader (alias already set)
Operator-scoped:            cf name register galtrader <id>
Globally findable:          cf name register galtrader <id> --public
```

### `cf name` subcommand

New subcommand (`cf name`):

```
cf name register <name> <campfire-id> [--root <root-id>] [--public] [--ttl <seconds>]
cf name unregister <name> [--root <root-id>] [--public]
cf name list [--root <root-id>]
cf name lookup <name>   # shows which step resolved it and from where
```

`cf name lookup` is the diagnostic command: it walks all steps and reports where the name was found (or not found). This replaces ad-hoc debugging.

---

## 7. Trust Composition

### Trust hierarchy

Trust flows from identity to operator root to public roots, mediated by explicit join decisions:

```
Your identity keypair
    └── Operator root (operator-root.json + join-policy.json)
            └── Public/system roots (registered in operator root, or directly joined)
                    └── Names registered in those roots
```

A name resolved through the operator root has the same trust level as your operator root — which you created and control. A name resolved through a public root is only as trustworthy as your decision to configure that root in your join policy.

### Does trust compose?

**Yes, but conservatively.**

If your operator root refers to a public root, that does NOT automatically make the public root's names trusted at operator-root level. Name resolution always stops at the root where it found the name; it doesn't chain across root boundaries.

Example: `galtrader` resolved via a public root is at public-root trust. `galtrader` resolved via your operator root is at operator-root trust. The caller knows which root found the name and can apply different handling.

**Explicit trust elevation:** An operator can re-register a name from a public root into their operator root:

```bash
# Look up galtrader in public root, register it locally under operator root
cf name register galtrader $(cf name lookup galtrader --source <public-root-id>)
```

This copies the mapping into the operator root. Now `galtrader` resolves at operator-root trust.

### TOFU pinning

The existing TOFU mechanism in `naming.Resolver` already pins names to campfire IDs on first resolution. This is the trust enforcement mechanism: once you've resolved `galtrader` → `abc123...`, any subsequent resolution that returns a different ID is flagged as a `TOFUViolation`. The pin is per-session (in-memory). Persistent pins are a future enhancement.

### Beacon trust

Beacons are tainted (the protocol spec is explicit on this). A beacon's description is an advertisement, not a fact. Beacons are consulted for transport configuration (how to connect), not for name resolution. Do not use beacon description fields to resolve names.

Resolution via campfire registries (naming.Resolve direct-read) is trustworthy: the name was posted to a campfire by a member, the message is signed, and registry membership defines who can post names.

---

## 8. Migration from Current State

All changes are backward compatible. Existing identities, roots, and memberships continue to work. When `join-policy.json` is absent, the resolver falls back to the current two-step (`ProjectRoot()` walk-up + `CF_ROOT_REGISTRY` env var).

### For existing users

Existing operator (`~/.campfire/`) has:
- An identity (no change)
- Maybe `operator-root.json` (continues to work as before; referenced in join policy once configured)
- Maybe `CF_ROOT_REGISTRY` set (still works as fallback, bypassed once join policy is set)

New behavior visible immediately:
- `cf name lookup <name>` is available for diagnostics
- `cf init --name agent` copies join policy and operator root (if present) into new agents

### For existing agents

Named agents at `~/.campfire/agents/<name>/` continue to work. They gain nothing automatically — a one-time `cf identity sync --name <agent>` will copy the join policy and operator root from the parent. Or just delete and re-init.

### No protocol changes

This design is entirely CLI-layer and convention-layer. The protocol spec (message format, identity, membership) is unchanged. No new message types. No new campfire tags at the protocol level. The naming convention (`naming:name:*` tags, `naming:resolve` futures) is unchanged. The `join-root-selection` convention is new but optional — the fallback path covers existing setups.

### `join-policy.json` is new

`~/.campfire/join-policy.json` is a new file. It doesn't exist on current installs. Its absence is handled gracefully — the resolver falls back to the existing two-step. Operators configure it via:

```bash
cf join-policy set --fs-walk --join-root <operator-root-id>
# or for a custom consult campfire:
cf join-policy set --consult <campfire-id> --join-root <operator-root-id>
```

---

## 9. What Changes Where

### Protocol spec (`docs/protocol-spec.md`)

**No changes.** The protocol is unchanged. This design works at the CLI and convention layers.

### CLI (`cmd/cf/`)

| File | Change |
|------|--------|
| `cmd/init.go` | Add parent inheritance step (steps 3–8 in bootstrap flow) |
| `cmd/init.go` | `--from <cfHome>` flag for named agent init |
| `cmd/resolve.go` | Replace `tryNamingResolve` with consult-based `resolveByName` |
| `cmd/resolve.go` | Wire `AutoJoinFunc` on resolver for open-protocol campfires |
| `cmd/resolve.go` | Load `join-policy.json` and query consult campfire; fall back to `ProjectRoot()` + env var when absent |
| `cmd/join_policy.go` | New: `cf join-policy set/show` subcommand |
| `cmd/name.go` | New: `cf name register/unregister/list/lookup` |

### Naming package (`pkg/naming/`)

| File | Change |
|------|--------|
| `naming/join_policy.go` | New: `JoinPolicy` type, `LoadJoinPolicy`, `SaveJoinPolicy` |
| `naming/resolve.go` | Add `NameLayer` interface; consult layer implementation |

### Convention (`pkg/convention/`)

No changes to existing conventions. The `join-root-selection` convention is a new declaration that the consult campfire must implement. It is defined as a convention (not hardcoded), so any campfire can implement it.

### Config files (new)

| File | Contents |
|------|----------|
| `~/.campfire/join-policy.json` | Join policy: `join_policy`, `consult_campfire`, `join_root` |
| `~/.campfire/agents/<name>/meta.json` | Agent metadata: name, parent_cf_home, created_at |

---

## Appendix: Design Decisions and Trade-offs

**Why consult-based root selection instead of a hardcoded layer stack?**

The hardcoded stack (`project root → operator root → system roots → public root`) embeds policy in source code. Adding a new root source requires a code change and rebuild. The consult model makes root selection a configuration decision: the join policy points to a campfire that speaks the `join-root-selection` convention, and that campfire can implement any strategy — filesystem walk, DNS TXT lookup, team directory query, anything. No source changes needed to add a new root source.

The simplest implementation (`fs-walk`) replicates the current `ProjectRoot()` behavior exactly. Existing users see no behavior change until they switch to a custom consult agent.

**Why copy files at init time rather than symlink?**

Agent identities can be moved, copied, or used on different machines. File references to a parent home directory would break portability. Copying snapshots the context at creation time, which is the right semantics: the agent starts with the creator's context, then evolves independently.

**Why not inherit all memberships?**

Memberships are earned/requested. They encode the history of how an agent joined a campfire — the join protocol, who admitted them, when. An agent auto-inheriting memberships it never asked for would violate the trust model: other campfire members didn't agree to have this new identity participate.

**Why is beacon description search removed from the resolver?**

A beacon's `description` field is self-reported and unverifiable as a name. The beacon's authority is its signature over the campfire ID and transport config — that proves the owner published it. The description is an advertisement. Resolving names by advertisement description would let any campfire claim any name just by setting its description field, with no registry to arbitrate conflicts. Names must go through a registry (a campfire with `naming:name:*` messages) to have meaningful trust semantics.

**Why `join-policy.json` as a file rather than a dedicated campfire?**

A dedicated "policy campfire" would require bootstrapping — you'd need to be a member of the policy campfire to discover roots, which is circular. A local file is the right trust anchor. The file is machine-local and operator-controlled. Its contents are the configuration of how this identity discovers naming roots; that's a configuration decision, not a network-driven one.

**Why not a full DNS-style delegation chain?**

The naming convention already supports hierarchical names (`galtrader.lobby`). The resolver already walks segments through parent campfires. A delegation chain would be useful if the public root knew about `galtrader` and delegated to a sub-registry. That's already supported with `CF_ROOT_REGISTRY` and works with the public root. The design doesn't block this — it just doesn't require it for the common case.

# Identity Primitives Audit — campfire-fb4

**Status:** final
**Date:** 2026-04-10
**Author:** campfire-agent (Opus 4.6)
**Target:** report on existing identity primitives before designing the capability-tree delegation system
**Constraints:** `resonant/docs/specs/identity-tree-constraints.md` (C1–C5)
**Downstream items:** campfire-79e (delegation convention), campfire-dcb (session-coordination convention), campfire-ead (Identity Convention v0.2)

## Purpose and method

The target architecture is a capability tree of signed grants — `baron` → per-machine subkey → per-project identity → per-session subkey → automaton → worker — where each node chains to its parent via a signed grant with scope, TTL, and revocation, and `rd kill <subtree>` posts one message to collapse a subtree across the portfolio within one sync cycle.

Before designing the three downstream conventions, we need to know what already exists so we don't reinvent, don't duplicate, and don't accidentally rebuild something that's already latent in the codebase. This audit surveys the five prior identity attempts and classifies each primitive as **foundation** (keep and extend), **replacement** (tear out), or **orthogonal** (ignore — doesn't affect the new design).

Each section ends with a gap verdict against C1–C5. Every claim is cited to file:line in `~/projects/campfire`.

## 1. Identity Convention v0.1

**Declaration:** `pkg/convention/identity.go:1–167`. Four operations (`introduce-me`, `verify-me`, `list-homes`, `declare-home`), seven tags (`identity:introduction`, `identity:challenge-response`, `identity:homes`, `identity:home-declared`, `identity:home-echo`, `identity:v1`, `identity:profile`, `identity:revoked`). All ops signed by `member_key`.

**Semantic model:** v0.1 is **peer attestation**, not delegation. An identity is a self-campfire; two identities can cross-link via the echo ceremony (see §2), but no code treats the link as parent→child. There is no grant message, no scope, no TTL on the identity itself, and no cascade. `docs/design-identity-as-campfire.md:1` puts it plainly — "Identity IS a self-campfire" — and defers delegation to future work.

**Resolver:** `pkg/convention/identity_resolver.go:1–82` maps an ed25519 sender pubkey to an `IdentityInfo` struct (`MachineKey`, `Identity`, `IdentityVerified`). Two implementations: `NoopIdentityResolver` always returns unverified; `CacheIdentityResolver` consults `protocol.VerificationCache` populated by prior echo ceremonies. The convention server calls the resolver before dispatching; handlers read `req.Identity`. **Important:** the resolver does not auto-verify — it only reads a cache. If the cache is empty the handler sees only `MachineKey`.

**Gap verdict:**

| Constraint | Status | Reason |
|---|---|---|
| C1 zero command growth | **FAIL** | v0.1 is inseparable from `cf home link` / `cf home be`, which are user-visible commands (§2). |
| C2 auto-mint/grant/expire | **orthogonal** | v0.1 has no subkeys or grants. Neither satisfies nor blocks auto-mint. |
| C3 one-time setup | **orthogonal** | v0.1 has no setup beyond the echo ceremony itself. |
| C4 failure degrades | **pass (by accident)** | v0.1 has no network dependency once the local pubkey exists. |
| C5 one-command revoke | **FAIL** | `identity:revoked` is declared (line 25) but has no processing anywhere in the repo — see §5. |

**Classification: orthogonal.** The `introduce-me` / `verify-me` / `declare-home` ops remain useful for peer attestation between sibling identities that are not in a parent/child relationship. They do not conflict with the delegation convention, and the convention server's `IdentityResolver` interface is a convenient hook where the delegation layer can plug in grant verification. Keep v0.1 running; do not build the capability tree on top of it.

## 2. cf home command family

**Implementation:** `cmd/cf/cmd/home.go:1–572`. Four subcommands.

### 2.1 `cf home link <campfire-id>`

A 4-step echo ceremony (`home.go:115–202`) that establishes **bidirectional** cross-campfire knowledge:

1. Post `declare-home(B, role="secondary")` on campfire A → message M_A.
2. Post `declare-home(A, role="secondary", ref=M_A.ID)` on campfire B → message M_B.
3. Post `identity:home-echo` on A with `{echo_of: M_B.ID, signed_by_b: ed25519.Sign(B_priv, M_B.ID), campfire_b_pubkey}` — cryptographic proof that whoever ran the ceremony controls B's private key.
4. Publish an `identity:v1` beacon on A (non-fatal).

Both sides post symmetric `declare-home` messages. The ceremony proves mutual key control, not delegation — there is no parent→child relationship on the wire, no scope, no TTL, no revocation chain. The `role="secondary"` string is a hint to display code, not a semantic.

### 2.2 `cf home be <campfire-id>` / `cf home be --self`

`home.go:250–348`. Runs an `identity:be-challenge` echo against the target campfire to prove key control, then writes `identity.present_as = <campfireID>` to `~/.cf/config.toml`. **No wire grant is posted.** Crucially, in v0.16 the `present_as` field is loaded into `InitResult.PresentAs` (`pkg/protocol/config.go` / `pkg/protocol/init.go`) but is **not consulted when signing messages** — `NewMessage` at `pkg/message/message.go:82–118` signs with the `Signer` the caller hands in, which is the machine key, not the presented-as key. `cf home be` is a no-op in v0.16. The comment says "deferred to v0.17+".

**Latent security gap flagged during audit:** `present_as` is stored in a mutable local config file. Any code path that starts using it without verifying a signed grant (e.g. a v0.17 implementer who wires it into `SenderCampfireID` or similar) would allow unattributed identity spoofing via local file edit. Any v0.17+ work on `present_as` MUST verify against a real grant message before the field changes signing behavior. This is a C1/attribution trap and should be a design constraint on `ead`.

### 2.3 `cf home display`

`home.go:445–480`. Read-only — prints `present_as` if set, otherwise the machine pubkey hex. No wire traffic.

### 2.4 `cf home revoke <member-key>`

`home.go:351–435`. Two actions:

1. Call `protocol.Client.Evict()` to remove the key from the home campfire member list. For threshold>1 P2P campfires this re-runs DKG (`pkg/protocol/evict.go:62–115`) — the old member share becomes useless.
2. Post a signed `identity:revoked` message with `{revoked_key, home_id}` (lines 404–421).

**Revocation is per-campfire.** The in-code comment at `home.go:364–365` is explicit: "machine-to-team links are NOT automatically revoked. Revoke separately from any campfire where the key was admitted." The revoke does not cascade to linked campfires, even ones established via `cf home link`. See §5 for the processing gap.

**Gap verdict:**

| Constraint | Status | Reason |
|---|---|---|
| C1 zero command growth | **hard FAIL** | `cf home link` is named explicitly by the constraints doc as an example of a disqualifying new command (`identity-tree-constraints.md:68`). |
| C2 auto-mint/grant/expire | **FAIL** | `cf home be` requires an interactive TTY confirmation (`home.go:272`) and produces no grant. |
| C3 one-time setup | **partial FAIL** | `cf home link` is per-pair, not one-time per machine. Constraints §C3 forbids this pattern explicitly. |
| C4 failure degrades | **N/A** | Ceremony requires both campfires reachable; acceptable for a one-time operation. |
| C5 one-command revoke | **partial** | `cf home revoke` is one command, but scope is one home campfire only. No subtree cascade. |

**Classification:**

- **`cf home link` ceremony → replacement.** Two-sided ceremony, named by the constraints as disqualifying. The delegation convention (79e) must use a one-sided parent-signs-child grant posted automatically, not an interactive echo.
- **`cf home be` + `present_as` → replacement.** Inert today, and latent security gap if wired naively. v0.2 (ead) must replace it with a grant-chain-aware signing path.
- **`cf home display` → orthogonal.** UI shim; can be repointed at the new grant chain without protocol impact.
- **`cf home revoke` + Evict() + rekey → foundation.** The Evict-then-rekey pattern (home.go:395–401 + evict.go:62–115) and the signed `identity:revoked` broadcast (home.go:404–421) are the right primitives. What's missing is the processing side (§5) and the subtree scope. The delegation convention should reuse Evict and the `identity:revoked` tag name; it must ADD the cascade and the validation layer.

## 3. cf session tokens

**Implementation:** `pkg/session/` and `cmd/cf/cmd/session*.go`. Token format: `cfs1_<base64url(CBOR payload + ed25519_signature[64])>`. Payload contains an ephemeral X25519+Ed25519 keypair, campfire ID, transport config, TTL, and the creator's public key. The signature is computed by the creator's private key over the payload — so the **token** is attributable to the creator, but the **messages sent via the token** are all signed by the shared ephemeral Ed25519 key and are **not per-message attributable**.

Subcommands:

- `cf session create --ttl <dur>` — creates a short-lived campfire with a shared keypair; requires `CF_HOME` + persistent identity.
- `cf session send <token>` / `read <token>` / `end <token>` — no `CF_HOME` required.

**Key findings:**

- **Attribution:** a reader of a session message cannot tell which creator spawned the session without the token itself. The message `Sender` field is the shared ephemeral pubkey, which is the same for every participant. This breaks grant-chain attribution by construction.
- **TTL:** client-side only. Tokens are rejected one nanosecond past expiry by `DecodeToken()`. There is no server-side rejection, no cleanup, no revocation set.
- **Chaining:** cannot spawn a sub-session. `cf session create` requires `CF_HOME` and a persistent identity — an ephemeral session token cannot satisfy it.
- **Convention acceptance:** no hard-coded restrictions. Any convention can be invoked through a session token because the convention dispatcher only sees the ephemeral Ed25519 key, and ephemeral keys look like any other key.
- **Admission:** the creator and any joiners are auto-admitted to the session campfire with `RoleFull`. The session does not inherit membership in the creator's other campfires — it is a self-contained temporary campfire.
- **Revocation:** only `cf session end <token>` (disband). The token cannot be revoked out-of-band; all a compromised creator can do is wait for TTL or explicitly disband.

**Gap verdict:**

| Constraint | Status | Reason |
|---|---|---|
| C1 zero command growth | **FAIL** | `cf session create` is a user-visible command. |
| C2 auto-mint/grant/expire | **FAIL** | No grant model; no parent identity. TTL exists but no automatic re-mint. |
| C3 one-time setup | **FAIL** | Setup runs per session. |
| C4 failure degrades | **FAIL** | Blocks completely if the campfire is unreachable at create time. |
| C5 one-command revoke | **FAIL** | Only the creator can end the session, and only the shared key is killed — no subtree. |

**Classification: replacement.** Every session key must become an attributable subkey of the calling identity with a grant-chain linking it to its parent. The existing `cfs1_...` token format can be recycled as the wire encoding for a capability-bearing token, but the shared-ephemeral-key model must go — each consumer of the session gets their own key and the token carries a signed grant from the session creator. The existing `cf session` surface should be **removed from user view** and invoked only by the harness; satisfying C1 means the user never types `cf session create`.

## 4. Message sender fields and grant_chain feasibility

**Canonical type:** `pkg/message/message.go:14–41`. Ten CBOR fields, keyed by integer (`keyasint`).

| Key | Field | In signature? | Trust |
|---|---|---|---|
| 1 | `ID` | yes | verified |
| 2 | `Sender` (ed25519 pubkey) | no (identifies signer) | verified |
| 3 | `Payload` | yes | tainted content |
| 4 | `Tags` | yes | tainted |
| 5 | `Antecedents` | yes | tainted |
| 6 | `Timestamp` | yes | tainted |
| 7 | `Signature` | no | cryptographic |
| 8 | `Provenance` ([]ProvenanceHop) | no (each hop self-signed) | verified per hop |
| 9 | `Instance` | **no** | tainted — documented as "NOT included in MessageSignInput" (`message.go:26–31`) |
| 10 | `SenderCampfireID` | **no** | tainted — verifier must re-check against sender's self-campfire (`message.go:35–40`) |

`MessageSignInput` at `message.go:59–66` is definitive: the signature covers ID, Payload, Tags, Antecedents, Timestamp — and nothing else. `VerifySignature` at `message.go:120–142` constructs the same struct for verification.

**Extension mechanism:** already in production. `Instance` (key 9) and `SenderCampfireID` (key 10) are the precedent — both are optional (`omitempty`), both outside `MessageSignInput`, both documented as tainted. The CBOR encoder is `fxamacker/cbor/v2` in deterministic mode (`pkg/encoding/cbor.go`); by default the decoder silently ignores unknown keys. This means a reader running an older binary can decode a newer message without error — it simply drops key 11+.

**Feasibility of adding `grant_chain`:** additive, non-breaking. A `GrantChain []GrantHop` field at CBOR key 11, `omitempty`, **not** added to `MessageSignInput`. Each `GrantHop` carries its own parent signature, so the grant chain is independently verifiable — the message signature does not need to cover it. Old readers silently ignore key 11; old messages remain valid; signatures are unchanged.

**Derived property satisfaction ("computable but invisible by default"):** the pattern is exactly what the constraint asks for. The chain lives in message metadata, not inferred from history, and default rendering can skip it.

**Gap verdict:**

| Constraint | Status | Reason |
|---|---|---|
| C1 zero command growth | **pass** | Metadata-only; no CLI changes required. |
| C2–C5 | **compatible** | The field format does not impose any command surface or blocking failure. |
| Derived: attribution invisible | **pass** | Follows the Instance/SenderCampfireID precedent — present in metadata, skipped by default rendering. |

**Classification: foundation.** The wire format can absorb `grant_chain` without a version bump or a new encoding path. The v0.2 (ead) item should explicitly call out "CBOR key 11, omitempty, not in MessageSignInput, follows the Instance/SenderCampfireID pattern" so an implementer doesn't accidentally put the field in the signed input and break everything.

## 5. Subtree revocation

**Tag:** `pkg/convention/identity.go:25`. The declaration file is the only non-home, non-test reference to `identity:revoked` in the tree. Grep confirms: one declaration, one emitter (`cmd/cf/cmd/home.go:404–420`), a test that only checks the message was posted (`home_test.go:628–688`), and **zero receivers**.

**What's missing:**

1. **No processing code.** No message handler in `pkg/convention/`, `pkg/protocol/`, `pkg/store/`, or anywhere else reads `identity:revoked` messages. The TTL comment at `identity.go:22–24` says "1h before cache expiry closes the revocation gap" but there is no cache expiry logic — because there is no cache.
2. **No validation layer.** `pkg/protocol/read.go` and `pkg/protocol/send.go` do not consult a revocation set when accepting or emitting messages. A key can post messages freely after being evicted — the Evict call removes it from the member list (which may block it from future reads if admission is enforced at the transport layer), but the revoke message itself has no protocol-level effect.
3. **No cascade logic.** Grep for `cascade`, `parent.*key`, `derived.*key`, `ancestor` across `pkg/convention/` and `pkg/protocol/` returns nothing identity-related (only config cascade). There is no notion of one key being a child of another, so a cascade cannot be computed even if the processing code existed.
4. **Per-campfire scope.** `cf home revoke` only touches the home campfire (`home.go:377`). A key revoked in one campfire remains valid in every other campfire where it is a member.
5. **Pull-based propagation.** The revoke message is posted as an ordinary campfire message; new nodes learn about it by replaying history on join. No push notification, no special sync path.

**Convention:revoke is unrelated.** `pkg/convention/parser.go:176,200` and `pkg/convention/toolgen.go:98–195` implement a different mechanism — `convention:revoke` revokes *declarations* (operations), not keys. It has processing logic but does not help identity revocation.

**Evict rekey is the one bright spot.** For threshold>1 P2P campfires, `pkg/protocol/evict.go:62–115` re-runs DKG when a member is evicted, producing a new campfire keypair. This is the right primitive for nuking a compromised key from a campfire synchronously. The delegation convention should reuse it.

**Gap verdict:**

| Constraint | Status | Reason |
|---|---|---|
| C5 one-command revoke | **FAIL** | The command exists, the message type exists, but the receivers don't exist. A revoke is a tree falling in the forest. No cascade. No message validation. |

**Classification:**

- **`identity:revoked` tag name → foundation.** Reuse the name. It was the right choice.
- **Evict + rekey (evict.go:62–115) → foundation.** Keep and reuse for threshold>1 campfires.
- **Everything else about revocation → replacement** (more accurately: needs to be **built**, since nothing exists on the receive side). The delegation convention must add:
  1. A revocation store in `pkg/store/` with `RevokeKey(campfire_id, pubkey)` and `IsKeyRevoked(campfire_id, pubkey)` (`pkg/store/interfaces.go` already has `ValidateInvite` / `RevokeInvite` as a precedent pattern).
  2. A message validation layer in `pkg/protocol/read.go` / `send.go` that consults the revocation store on ingest.
  3. Cascade logic that walks the grant chain of an incoming message and rejects it if any ancestor is revoked.
  4. Cross-campfire propagation — a node that sees `identity:revoked` for a key must apply it across every campfire it participates in, not just the posting one.

This is substantially more work than "design a delegation convention." It's a revocation subsystem. **The 79e item must explicitly own this scope or be split — see §8.**

## 6. Auto-join and auto-grant precedent

**Admission flow:** `pkg/transport/http/handler_join.go:98–300` handles the HTTP join path. Gates: (a) joiner is already a member, (b) joiner is the node itself, (c) valid invite code is presented, otherwise 403. No standing-policy check. `pkg/admission/admission.go:86–151` records the membership in the store and returns — no post-admit hook fires, no welcome message is posted on behalf of the new member, no grant is signed.

**Policy concept:** absent. Grep for `auto_grant`, `standing_policy`, `admission_policy`, `on_join`, `on_admit`, `welcome`, `auto_post` across `pkg/` returns zero identity-related matches. `CampfireState` at `pkg/campfire/campfire.go:118–185` has no policy field. `Membership` at `pkg/store/store.go:304–328` has no policy field.

**`auto_join` is client-side, not a campfire policy.** `pkg/protocol/config.go:78` defines `AutoJoin []string` — a list of beacons the client joins at init. `pkg/protocol/init.go:158–167` iterates the list and joins each one. This is the user's own config saying "I want to be in these campfires"; the campfires themselves have no way to declare "new joiners should auto-receive X."

**Closest analog — convention seeding.** `pkg/convention/seed.go:152–161` pre-seeds infrastructure declarations (`promote`, `supersede`, `revoke`, `naming-register`) at campfire init. `pkg/trust/policy.go:106–131` populates adopted declarations. This pattern is **one-time at campfire creation**, not per-member. It is not a hook; it is not triggered by admission. It cannot be used as-is to auto-post a grant when a new member joins.

**Beacon auto-admit:** does not exist. Possessing a beacon does not auto-admit a joiner to an invite-only campfire — they still need pre-admission or an invite code. Open-protocol campfires accept any joiner, but that is not the same as "auto-grant on join."

**`reception_requirements`** is a filter on *incoming messages*, not an admission policy.

**Gap verdict:**

| Constraint | Status | Reason |
|---|---|---|
| C2 auto-mint/grant/expire | **hard FAIL** | No on-admit hook, no standing policy, no grant message, no auto-sign path. This is the single biggest gap. |
| C3 one-time setup | **partial FAIL** | Config cascade works (client-side), but no campfire-side policy object to cascade into. |

**Classification: replacement (by building).** The delegation convention must introduce:

1. A `CampfirePolicy` object (new field on `CampfireState` or a sibling store table) with `auto_grant_children: bool`, `default_scope: []string`, `default_ttl_seconds: int64`. Set once at creation, immutable.
2. A post-admit hook: `OnMemberAdmitted(campfire_id, new_member_pubkey)` fired synchronously from `pkg/admission/admission.go` after `AdmitMember` completes.
3. A delegation convention handler subscribed to the hook that, if `auto_grant_children` is set, signs and posts a grant message from the campfire owner's key to the new member with the configured scope and TTL.

The existing convention-seeding path at `seed.go` is a helpful precedent — the delegation convention's declarations can be seeded the same way — but it is not the mechanism by which the grant itself gets posted. The hook is new.

## 7. Cross-cutting summary: primitive classification

| Primitive | Location | Classification | Notes |
|---|---|---|---|
| `identity:introduce-me` / `verify-me` / `declare-home` ops | `pkg/convention/identity.go:1–167` | **orthogonal** | Peer attestation; keep alongside delegation. |
| `IdentityResolver` interface | `pkg/convention/identity_resolver.go:33–36` | **foundation** | Natural plug-point for grant-chain verification; extend `IdentityInfo` with the chain. |
| `cf home link` echo ceremony | `cmd/cf/cmd/home.go:115–202` | **replacement** | Disqualifying per C1. Replace with one-sided parent-signs-child grant posted automatically. |
| `cf home be` + `present_as` | `cmd/cf/cmd/home.go:250–348`, `pkg/protocol/config.go` | **replacement + latent security flag** | Inert in v0.16; any v0.17 wiring must verify a real grant before `present_as` influences signing. |
| `cf home display` | `cmd/cf/cmd/home.go:445–480` | **orthogonal** | UI shim; can be repointed. |
| `cf home revoke` command + `Evict()` + rekey | `cmd/cf/cmd/home.go:351–435`, `pkg/protocol/evict.go:62–115` | **foundation** | Reuse Evict-then-rekey for threshold>1 campfires. Scope must become cross-campfire. |
| `identity:revoked` tag | `pkg/convention/identity.go:25` | **foundation** (name only) | No processing exists — must be built on the receive side. |
| `cf session create` | `pkg/session/`, `cmd/cf/cmd/session*.go` | **replacement** | Shared ephemeral key model has no per-message attribution. Move behind the harness (C1). |
| `cfs1_<base64url>` token format | `pkg/session/token.go` | **foundation** (format) | Wire encoding is reusable as a grant-bearing token. |
| `Message` CBOR key extension pattern | `pkg/message/message.go:14–41`, `59–66` | **foundation** | Instance/SenderCampfireID are the exact precedent for `grant_chain` at key 11, omitempty, not in `MessageSignInput`. |
| Convention seeding (`seed.go`) | `pkg/convention/seed.go:152–161` | **foundation** | Delegation convention declarations ship here. Not a hook mechanism. |
| Admission flow (`admission.go`, `handler_join.go`) | `pkg/admission/admission.go:86–151`, `pkg/transport/http/handler_join.go:98–300` | **replacement (extend)** | Must gain a post-admit hook; no hook exists today. |
| `AutoJoin` behavior config | `pkg/protocol/config.go:78`, `pkg/protocol/init.go:158–167` | **foundation** (pattern) | Precedent for one-time, client-side config — mirrors where `auto_grant_children` could live if kept client-side, but the better place is a campfire-side `CampfirePolicy`. |
| `convention:revoke` + provenance revoke | `pkg/convention/parser.go:176`, `pkg/convention/declarations/operator-revoke.json` | **orthogonal** | Different mechanisms (revoke declarations, revoke attestations); do not confuse with identity revocation. |

## 8. Impact on the three downstream items

### campfire-ead — Identity Convention v0.2 (grant_chain + acting_as)

**Verdict: confirmed, with two additions.**

The v0.2 extension is cleanly feasible per §4. Add to the item description:

1. **Exact wire location:** new `GrantChain []GrantHop` field at CBOR key 11 on `Message` (`pkg/message/message.go:14–41`), `omitempty`, **NOT** added to `MessageSignInput` at lines 59–66. Each `GrantHop` carries its own parent signature for independent verification. Follows the Instance / SenderCampfireID precedent at keys 9 and 10.
2. **Reject the `cf home be` present_as path.** v0.2 must NOT use the `present_as` config field as the basis for "acting_as" attribution — that field is a local mutable file with no cryptographic binding, and wiring it into signing without verifying a grant is a security regression. Any `acting_as` behavior must derive from a verified grant in the grant_chain. This should be an explicit constraint on 79e/ead.

### campfire-79e — delegation convention (grant / revoke / attest with subtree semantics)

**Verdict: confirmed, scope expanded. Consider splitting.**

The audit reveals that 79e owns substantially more than "design a convention." The missing pieces are:

1. **Grant message type and auto-sign logic** (the convention itself).
2. **A `CampfirePolicy` object** with `auto_grant_children`, `default_scope`, `default_ttl_seconds` — does not exist (§6).
3. **A post-admit hook** in `pkg/admission/admission.go` — does not exist (§6).
4. **A revocation store** in `pkg/store/` — does not exist (§5).
5. **A message validation layer** in `pkg/protocol/read.go` / `send.go` that consults the revocation store on ingest — does not exist (§5).
6. **Subtree cascade logic** that walks a message's grant_chain and rejects the message if any ancestor is revoked — does not exist (§5).
7. **Cross-campfire revocation propagation** — the node that sees `identity:revoked` must apply it across all its campfires, not just the posting one (§5).

Items 1–3 are the delegation convention proper. Items 4–7 are a revocation subsystem. Both are required for C5 to be satisfied, but they can be designed and shipped as two items. Recommendation: **split 79e into 79e-a (delegation convention: grant issuance + auto-grant hook + CampfirePolicy) and 79e-b (revocation subsystem: store + validation layer + subtree cascade + cross-campfire propagation)**. Wire 79e-b as blocked by 79e-a and by ead (the grant_chain field). If the split is rejected, 79e's description must explicitly enumerate items 1–7 so no part of the revocation side gets dropped during implementation.

### campfire-dcb — session-coordination convention

**Verdict: should be folded / reduced, not a peer to 79e and ead.**

dcb's own description hedges: "May extend swarm-coordination convention rather than introduce a new one — decide after campfire-fb4 audit." The audit's answer:

- **Sibling discovery** (active / heartbeat / end) does not need delegation semantics to work. Any session can post `session:active` to the project campfire and read sibling `session:active` messages; the existing Identity Convention v0.1 peer attestation is enough to distinguish sessions *if each session has its own key*.
- **But** — today, `cf session` tokens share a single ephemeral Ed25519 key across all participants (§3). Siblings are indistinguishable on the wire. Sibling discovery is meaningless without per-session subkeys, which require delegation grants.
- **And** — `swarm-coordination` (the existing convention for short-lived worker campfires) uses the same shared-key session token model. It cannot be extended to give each participant an attributable identity without the delegation layer first.

So dcb is **entirely dependent on 79e + ead** for its own attribution story. It is not an independent design. Recommendation: **reduce dcb to a thin extension item** — three message types (`session:active`, `session:heartbeat`, `session:end`), posted by a session using its own attributable subkey from the grant chain. Block dcb on both 79e and ead. Do not let dcb own any of the identity plumbing; all of that belongs upstream.

Alternatively: **merge dcb into 79e-a** as a sub-deliverable ("session-coordination message types shipped alongside the delegation convention"). This removes a ready item from the queue and keeps all the identity plumbing in one design pass. I recommend this merge unless there is a reason to keep dcb's surface separately reviewable.

## 9. Ceremony audit checklist (from the constraints spec)

| # | Question | Proposed delegation convention design after audit |
|---|---|---|
| 1 | Does the user type any new command? | No. All grant lifecycle happens in the harness. |
| 2 | Does `rd init` on machine-2 require any new flag or step? | No. Machine link happens via `auto_grant_children` policy set once on the project campfire. |
| 3 | Scenario where user must manually revoke a grant? | No, except the explicit `rd kill` / `cf revoke` escape hatches. |
| 4 | Any failure mode "start blocked, please do X"? | No. Missing grant → degraded mode, message tagged `grant:local-only`. |
| 5 | Any command fails when the network is unreachable? | No. Signing continues locally; verification queued. |
| 6 | Steps to onboard new machine to an existing project? | 1 (link machine to root via 1Password sync). Target ≤ 2. |

All six answers depend on the post-admit hook and the `CampfirePolicy` auto-grant path actually shipping. That's why the split in §8 matters: if the revocation subsystem gets dropped, only C5 fails; if the auto-grant hook gets dropped, C1/C2/C3 all fail.

## 10. Non-obvious findings worth remembering

1. **The `identity:revoked` tag has been in the repo long enough to have a test and a CLI producer, and nothing consumes it.** It's a ghost tag. Any future work that assumes revocation "sort of works" will be wrong. The first revocation consumer in the tree is the delegation convention's validation layer.
2. **`cf home be` is inert in v0.16 and dangerous in v0.17+ unless the grant chain lands first.** The `present_as` field is already parsed and plumbed into `InitResult.PresentAs`, so an implementer who wires it into signing code without verifying a grant can introduce silent identity spoofing via local config edit. The ead item must explicitly forbid this.
3. **The message format is already extensible the right way.** Instance and SenderCampfireID are proof that the CBOR-keyed-by-int, omitempty, outside-MessageSignInput pattern is production-tested. Adding `grant_chain` at key 11 is a one-liner extension, not a new format negotiation.
4. **There is no on-admit hook anywhere in the protocol.** This is a larger change than it looks like — it is not "add a callback" but "introduce a new extension point that the delegation convention can subscribe to and that future conventions can also use." Design it as a first-class mechanism in 79e-a.
5. **dcb is the weakest of the three downstream items.** It depends entirely on the other two and its main value-add (sibling discovery) is three new tag names. Most of its content belongs in 79e-a or in a one-paragraph section of the delegation convention spec.

## 11. Recommended next steps

1. Merge **dcb** into **79e-a** (recommendation) OR tighten dcb's scope to three message types and block it on 79e-a + ead (fallback).
2. Split **79e** into **79e-a** (delegation convention + auto-grant hook + CampfirePolicy) and **79e-b** (revocation subsystem + cross-campfire propagation + subtree cascade).
3. Update **ead** to explicitly cite: CBOR key 11, omitempty, not in MessageSignInput, follows the Instance/SenderCampfireID precedent; and forbid any use of `present_as` that isn't backed by a verified grant.
4. File a follow-up item to remove `cf home link`, `cf home be`, and `cf session create` from user visibility once the delegation convention and harness auto-grant path ship (C1 closure).

The audit is the input to these splits/merges — the decision about whether to execute them is a design call for the user.

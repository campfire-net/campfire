# Convention Declaration Precedence Authorization

**Status:** Implemented (campfire-f5c). Hardening of the dedup behavior added in PR #596 (campfire-03e).
**Scope:** `cf-conventions/cf-convention/toolgen.go` — `listOperations`.

## Problem

`listOperations` resolves which convention declaration is authoritative for a
`(convention, operation)` tuple. Two of its precedence paths selected a winner
with **no signer authorization**:

1. **Supersedes path** — a declaration with a non-empty `Supersedes` field replaced
   its target purely on timestamp. Any `RoleWriter` member could post a declaration
   with `Supersedes=<target msgID>` and replace another signer's operation.
2. **Version-dedup path** (added in PR #596) — the highest `version` per
   `(convention, operation)` won, with no signer check. Any writer could post
   `(conv, op)@<huge version>` and become authoritative for CLI dispatch, `help`,
   and `convention.Server` arg/tag resolution.

`convention:operation` is not under the `campfire:` reserved-tag prefix, so the
role guard does not restrict it — every writer can post one. On a multi-writer
campfire (the common multi-agent case) this allowed silent operation hijacking.

The **revoke** path in the same function was already gated correctly and is the
precedent we mirror.

## Authorization model (Model C — same-signer OR campfire-owner-key)

A single predicate guards both precedence paths:

```
precedenceAuthorized(candidateSigner, ownerSigner, campfireKey):
  - candidateSigner == ""                       → false   (never)
  - campfireKey != "" && candidate == campfireKey → true   (owner override, online)
  - candidateSigner == ownerSigner               → true   (self-upgrade)
  - else                                          → false
```

- **Self-upgrade** (same signer) is always allowed — an operator upgrades their own
  convention freely, online or offline.
- **Owner override** applies only in **online mode** (`campfireKey` non-empty; the
  campfire key equals the campfire ID). It lets the campfire owner forcibly replace
  an abandoned operation declared by another member.
- **Offline mode** (CLI dispatch always passes an empty `campfireKey`) degenerates to
  same-signer-only, which is the protection that actually fires for CLI reads.

### Per-path owner identity

The two paths determine "who owns the slot" differently:

- **Supersedes path** — the owner is the target's original signer, looked up via the
  already-built `opSenderByMsgID[Supersedes]` index. An unauthorized superseder is not
  honored; it falls through to the version-dedup pass, where it is also rejected.
- **Version-dedup path** — there is no explicit target, so the slot owner is assigned
  on a **trust-on-first-use** basis: the signer of the **earliest-timestamp**
  declaration for that `(convention, operation)`. The slot owner always occupies its
  own slot (so a sole declaration — even one with an empty sender in synthetic/offline
  cases — always survives); later contenders must pass `precedenceAuthorized`.

## Known limitation (TOFU)

In **offline mode**, whoever posts a `(convention, operation)` declaration *first*
owns the slot. An attacker who pre-registers an operation before the legitimate
operator would own it, and the legitimate operator's later declarations would be
rejected as unauthorized. In **online mode** the campfire owner key overrides such a
squatter. This matches the trust-on-first-use model used elsewhere in campfire
(naming). It is acceptable because: (a) the attacker must post before the operator
ever does, and (b) the operator observes that their declaration did not take effect.

## Healing existing campfires

Campfires that accumulated multiple ungated versions from the same operator (the
automata-island Welcome Center case) heal automatically: every version shares one
signer, so the slot owner is that operator and the highest version still wins. Only
foreign-signer declarations are dropped.

## Regression coverage

`cf-conventions/cf-convention/precedence_auth_test.go`:

- `TestPrecedenceAuthorized` — the predicate, table-driven.
- Hijack rejected on both paths (`*_VersionDedupHijackRejected`, `*_SupersedeHijackRejected`).
- Legit self-upgrade offline (version bump + explicit supersede).
- Owner override online (supersede + version bump) and non-owner still rejected online.
- Empty sender never hijacks; sole empty-sender declaration survives.
- Cross-source registry hijack rejected; same-signer cross-source upgrade honored.
- `TestPrecedence_HijackInvisibleToDispatch` — resonance assertion: after rejected
  hijacks, exactly one (legit) declaration survives, so dispatch/help/server all
  resolve the same authoritative declaration.

# cf-conventions — Cross-Module Versioning Policy

> Design reference: `docs/design/cross-module-versioning.md` §4.5  
> Companion document: `cf-protocol/COMPATIBILITY.md`

---

## Three Rules

### Rule 1 — Independent minor cadence; coordinated majors with a stated floor

`cf-protocol` and `cf-conventions` version independently at minor and patch.
A `cf-protocol` minor bump (e.g. `1.1 → 1.2`) does **not** require any
`cf-conventions` bump. A `cf-conventions` minor bump does not require any
`cf-protocol` bump.

**Major bumps are not lock-stepped.** The two modules MAY be on different
majors. Every published `cf-conventions` release declares a **minimum
cf-protocol floor** in `go.mod` (`require github.com/campfire-net/cf-protocol vX.Y.Z`).
Go's MVS enforces this at build time. There is no separate manifest.

The floor is the authoritative compatibility statement. In the current
single-module layout (pre-split), the floor is declared in
`cf-conventions/floor.txt` and verified by `scripts/check-floor.sh`.

**CI enforcement:** on every release tag of `cf-conventions`, the previous
minor's test suite runs against the new minor's binary. A failure blocks the
tag. One CI job per release; non-negotiable.

### Rule 2 — Internal-package wire-format freezes bind cf-conventions major, not cf-protocol major

`cf-authority` 1.0 (and any future layer-3 wire-frozen package) freezes its
grant-CBOR / predicate-AST schema. A breaking change to that schema forces a
`cf-conventions` major bump. It does **not** force a `cf-protocol` bump.

`cf-authority`'s wire-format version is *internal* — exposed to consumers as
a `cf-conventions` major. Consumers do not pin `cf-authority` separately
because it is not a separate go module; pinning `cf-conventions` major is the
contract. The conformance harness (`cf-authority/trust/conformance`) is the
cross-implementer interop test, not a separate version axis.

This means:

| Event | `cf-protocol` bump | `cf-conventions` bump |
|---|---|---|
| New message type in envelope | Minor or major (see `cf-protocol/COMPATIBILITY.md`) | None required |
| New reserved op added to the L1 list | **Major** (closed-list addition is breaking) | None required (floor update if needed) |
| New convention declaration (new op in existing L3 pkg) | None | Minor |
| cf-authority grant-CBOR format change (breaking) | None | **Major** |
| cf-authority gate-predicate AST schema change (breaking) | None | **Major** |
| New L3 package added (additive) | None | Minor |
| cf-discovery snippet schema change (breaking) | None | **Major** |

### Rule 3 — Consumers pin both modules at major; minor floats; same-binary multi-consumer coexistence is guaranteed within a major

Apps (`rd`, `dontguess`, `social`, `the reach`, `freeso`, `legion`) **MUST**
pin both `cf-protocol vX.*` and `cf-conventions vY.*` at major (via Go's
normal `vX` import-path convention; X and Y need not match).

Within a major, all minors are backward-compatible. MVS picks the
highest-minimum across a multi-consumer binary; that selection is always safe
because minor backward-compatibility is a published invariant enforced by CI
(Rule 1). Two consumers in the same binary that disagree on minor are
reconciled by MVS to the higher minor; both consumers continue to work.

**Pinning a minor (not a major) is a consumer-side bug** — it over-constrains
MVS resolution and will cause spurious incompatibility errors.

---

## Floor Version

The minimum `cf-protocol` version this release of `cf-conventions` requires
is declared in `cf-conventions/floor.txt`. CI runs `scripts/check-floor.sh`
on every PR to verify the floor is met.

In the multi-module layout (post-split), this floor will live in
`cf-conventions/go.mod` as `require github.com/campfire-net/cf-protocol vX.Y.Z`.

---

## Compatibility Matrix

| Consumer pins | cf-protocol available | cf-conventions available | Result |
|---|---|---|---|
| `cf-protocol v1`, `cf-conventions v1` | 1.0–1.x | 1.0–1.x | All minor/patch combinations valid; MVS picks highest of each |
| `cf-protocol v1`, `cf-conventions v2` | 1.0–1.x | 2.0–2.x | Valid IFF cf-conventions v2.x floor ≤ cf-protocol v1.x available |
| `cf-protocol v2`, `cf-conventions v1` | 2.0–2.x | 1.0–1.x | Valid during cf-protocol v2 deprecation window for v1 surface; rare |
| `cf-protocol v2`, `cf-conventions v2` | 2.0–2.x | 2.0–2.x | Independent majors aligned; floor still declared |
| Two consumers, same binary, both at v1.X | 1.X (highest) | 1.Y (highest) | MVS reconciles; Rule 1 guarantees safety |
| Two consumers, same binary, one v1 one v2 | Forbidden by Go (different import paths) | — | Consumer-side migration required |

The "rare" row (`cf-protocol v2` + `cf-conventions v1`) is allowed only if
the cf-conventions v1 line is maintained against a v2-deprecated-surface
compatibility shim. This is not committed to; it is *allowed* if a consumer
needs it.

---

## Patch-Release Policy (0.x and 1.0.x)

During the build period (Stages 1–4), breaking changes within minors are
allowed under Go's 0.x convention. The Phase 8 wire-format freeze locks
the schema before the v1.0 tag is cut.

After v1.0:

- **Patch releases** (`1.x.Y → 1.x.Z`, Z > Y): bug fixes only. No new
  exported API. No wire-format changes. No changes to convention declaration
  grammar. No changes to the conformance harness fixture cases.

- **Minor releases** (`1.X.y → 1.(X+1).0`): additive changes only. New
  convention ops may be added. New L3 packages may be added. Existing
  exported API must remain source-compatible. Wire-format additions must be
  backward-readable by 1.0 consumers.

- **Major releases** (`1.x.y → 2.0.0`): breaking changes. Requires a new
  import path (`cf-conventions/v2`). Prior major is maintained on a release
  branch for the deprecation window.

---

## 0.30.x Patch-Release Policy

The 0.30.x series specifically: patches may fix bugs in the convention
dispatcher, gate evaluator, conformance harness, and CLI/MCP surface. They
MUST NOT:

- Modify frozen wire formats (cf-authority 1.0 grant CBOR, predicate AST)
- Add or remove reserved ops
- Change conformance harness fixture data
- Change the `GateEvaluator` interface signature or `DenyReason` codes
- Modify the `go.mod`-declared floor except to raise it

Any of the above in a patch is a minor bump.

---

## Consumer Guidance

```
# Pin at major in go.mod:
require (
    github.com/campfire-net/campfire             v0.x.y   # single-module era
    # post-split:
    # github.com/campfire-net/cf-protocol        v1.x.y
    # github.com/campfire-net/cf-conventions     v1.x.y
)
```

See `CONTRIBUTING.md` §Versioning for the contributor workflow.

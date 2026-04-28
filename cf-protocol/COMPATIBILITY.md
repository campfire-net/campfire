# cf-protocol — Backward-Compatibility Commitment

> Design reference: `docs/design/cross-module-versioning.md` §4.5  
> Full policy: `cf-conventions/COMPATIBILITY.md`

---

## The F6 Commitment

`cf-protocol` 1.0 is intended to remain the long-term wire format. There is
no roadmap to `cf-protocol` 2.0. A breaking change to the envelope,
signatures, hop-chain, or system events would be a redesign-level event.

This asymmetry with `cf-conventions` is intentional: `cf-conventions` major
bumps are expected over the project's lifetime (as layer-3 wire formats
evolve); `cf-protocol` major bumps are extraordinary events.

---

## What Is Frozen at cf-protocol 1.0

The following are frozen once the v1.0 tag is cut. No modification is
allowed without a major-version bump:

- Message envelope structure (fields, field types, field ordering for signing)
- Signature algorithm and signing domain (Ed25519 over CBOR-serialized payload)
- Hop-chain structure and provenance encoding
- System events: `campfire:rekey`, `campfire:visibility-changed`,
  `session:open`, `session:close`
- Reserved-op LIST (see below — LIST additions are major, not minor)
- Reserved tags `"future"` and `"fulfills"` and their DAG semantics
- The `Client.Await` contract (future-fulfillment matching semantics,
  lexicographic tie-breaking at `pkg/protocol/await.go:14-18`)

The gate evaluator interface, predicate AST schema, and grant CBOR layout
are **NOT** in cf-protocol — they are layer-3 (cf-authority) and freeze
with `cf-conventions` major, not `cf-protocol`.

---

## Reserved-Op LIST Additions Are Major Bumps

The reserved-op LIST (`cf-protocol/internal/reserved-ops.go`) is a closed
list. The current ten ops:

```
disband | evict | admit | grant | revoke |
delegation-grant | delegation-revoke | delegation-accept |
member-roster | compaction
```

Consumers may rely on this list being complete. **Adding to the list IS a
breaking change** — a consumer that dispatch-switches on the list will miss
the new op.

Therefore: reserved-op LIST additions are **cf-protocol major** bumps, not
minor bumps. This tightens the general "minors are additive" rule for this
specific surface.

---

## Within-Major Guarantees

Within `cf-protocol v1.*`:

- All v1.x messages are accepted by all v1.y parsers (x and y need not match)
- The envelope byte layout is identical across all v1.x releases
- A consumer pinned at `cf-protocol v1` never needs to modify its message
  parsing code for a minor or patch release

---

## Relationship to cf-conventions

`cf-conventions` declares a minimum `cf-protocol` floor in its `go.mod`
(or `cf-conventions/floor.txt` in single-module mode). Go MVS enforces
this. See `cf-conventions/COMPATIBILITY.md` for the full policy including
the compatibility matrix and patch-release policy.

`cf-protocol` and `cf-conventions` version independently. `cf-protocol`
minor bumps do not require `cf-conventions` bumps. `cf-conventions` major
bumps do not require `cf-protocol` bumps.

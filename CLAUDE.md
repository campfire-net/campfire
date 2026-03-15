# CLAUDE.md — Campfire Protocol

> OS-level instructions (session protocol, beads workflow, model routing, rules) are inherited from `~/.claude/CLAUDE.md`. This file contains only project-specific configuration.

## Project

**Campfire**: Decentralized coordination protocol for autonomous agents. Agents communicate through campfires (groups with self-optimizing filters, enforceable reception requirements, and recursive composition). A campfire can be a member of another campfire. One interface. Transport negotiable. No central authority.

- `docs/protocol-spec.md` — the protocol specification (draft)
- `docs/product-vision.md` — positioning, competitive landscape, adoption strategy

## Language & Stack

- **Go** — single binary, cross-platform, good crypto stdlib, good networking
- **CLI framework**: `cobra`
- **Storage**: SQLite (local message store, campfire state, filter history)
- **Crypto**: Ed25519 (identity), SHA-256 (Merkle hashes)
- **Wire format**: CBOR (deterministic serialization for signature verification)

## Agent Roster

| Agent | Spec | Domain | Default Tier |
|-------|------|--------|-------------|
| Implementer | `.claude/agents/implementer.md` | `src/**`, `cmd/**`, `pkg/**` | sonnet |

**Routing rules:**
- Protocol design, architecture decisions → PM (CLAUDE.md)
- All implementation work → Implementer

## Dev → Test Flywheel

After any code change:
1. **Run tests.** `go test ./...`
2. **Integration test.** Spin up two agents, create a campfire, exchange messages, verify provenance chain.
3. **Dogfood.** Use `cf` to coordinate parallel Claude Code sessions on other 3DL projects. Report what works and what doesn't.

### Guardrails (stop and ask):
- **Publishing the spec** — timing matters, confirm before making public
- **External dependencies** — no external services, this is a self-contained protocol
- **Transport implementations beyond filesystem/unix socket** — confirm scope before building HTTP/WS/NATS transports

Everything else: just build it.

## Task-Type → Model Mapping

| Task Type | Model | Rationale |
|-----------|-------|-----------|
| Protocol design, security model, recursive composition semantics | **Opus** | Novel design, needs to be airtight |
| CLI implementation, transport adapters, filter implementation | **Sonnet** | Structured implementation |
| Config, formatting, test fixtures | **Haiku** | Mechanical execution |

## Design Change Cascade

**Every protocol change MUST trigger these downstream reviews:**

A "protocol change" is any modification to:
- Message envelope or provenance chain structure
- Membership semantics or eviction rules
- Filter interface or optimization contract
- Beacon structure or discovery semantics
- Security model or identity system

```
Protocol Change (parent)
├── 1. Security Review (P1, blocked by parent)
│      Route to: PM
│      Assess: Does this change weaken identity verification, enable spoofing,
│              or expose membership data?
│      Output: Security assessment, updated Security Considerations section
│
├── 2. Recursive Composition Review (P1, blocked by parent)
│      Route to: PM
│      Assess: Does the campfire-as-member interface still hold? Does this
│              change break opacity or leak child structure to parent?
│      Output: Updated Recursive Composition section
│
└── 3. Spec Update (P2, blocked by #1, #2)
       Route to: Implementer
       Assess: Does the reference implementation match the updated spec?
       Output: Code changes, updated tests
```

## Source of Truth Hierarchy

When artifacts disagree, resolve conflicts in this order:

1. **Protocol spec** (`docs/protocol-spec.md`) — the protocol definition is authoritative
2. **CLAUDE.md** — operating rules, agent routing, development workflow
3. **Reference implementation** (`src/`, `cmd/`) — implements the spec; deviations are bugs
4. **Tests** — verify the implementation matches the spec

## Artifact Conventions

- **Protocol specification**: `docs/protocol-spec.md`
- **Product vision**: `docs/product-vision.md`
- **Transport specs**: `docs/transport-*.md` (one per transport implementation)
- **Code**: Go packages in `pkg/`, CLI in `cmd/cf/`

All artifacts in `docs/` should be linked from a corresponding bead so nothing gets lost.

## Repo Structure

```
campfire/
├── CLAUDE.md              # This file
├── docs/
│   ├── protocol-spec.md   # The protocol specification
│   ├── product-vision.md  # Positioning and strategy
│   └── transport-*.md     # Transport implementation specs
├── cmd/
│   └── cf/                # CLI entry point
├── pkg/
│   ├── identity/          # Keypair generation, signing, verification
│   ├── message/           # Message envelope, provenance chain
│   ├── campfire/          # Campfire lifecycle, membership, filters
│   ├── beacon/            # Beacon publishing and discovery
│   └── transport/         # Transport interface + implementations
│       ├── fs/            # Filesystem transport
│       └── unix/          # Unix socket transport
├── tests/
└── .beads/
```

## Cross-Project Coordination

Campfire is a protocol project in the 3DL portfolio. It will be used by:
- **Midtown** (in rudi repo) — as the coordination layer for multi-agent orchestration
- **Mallcop** — for coordinating parallel build sessions (immediate dogfood)
- **ToolRank** — for coordinating parallel research sessions

Campfire beads use the `campfire-` prefix.

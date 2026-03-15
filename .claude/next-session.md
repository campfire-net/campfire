# Next Session: Phase 1 Design

## Context

Read `docs/protocol-spec.md` — that's the full protocol specification. This session designs the Phase 1 reference implementation: local-only, filesystem transport, enough to coordinate parallel Claude Code sessions on one machine.

## Goal

`/bd-plan` a Phase 1 implementation that delivers a working `cf` CLI. Two agents on the same laptop can create a campfire, exchange messages, and verify provenance chains. This is the dogfood target: Baron runs five parallel Mallcop sessions that coordinate through campfires instead of through him carrying messages between terminals.

## Scope: Phase 1 Only

**In scope:**
- `cf init` — generate Ed25519 keypair, create agent identity
- `cf create` — create a campfire with filesystem transport
- `cf send` / `cf read` — broadcast and receive messages within a campfire
- `cf discover` — list beacons on the local filesystem
- `cf join` / `cf leave` — membership operations
- `cf inspect` — show provenance chain on a message
- `cf ls` / `cf members` — list campfires and members
- `cf dm` — sugar for two-member campfire create + send
- Filesystem transport (shared directory, file-based message passing)
- Beacon publishing to `~/.campfire/beacons/`
- Ed25519 signing and verification on all messages
- Provenance chain construction and verification
- SQLite for local message store and campfire state

**Out of scope for Phase 1:**
- Network transports (HTTP, WebSocket, NATS)
- Self-optimizing filters (Phase 1 filters are manual: pass-through and suppress lists only)
- Admittance delegation (Phase 1 is open or invite-only only)
- Filter optimization loop
- Cross-machine anything

## Constraints

- Go. Single binary. External Go libraries are fine (CBOR, cobra, SQLite, uuid, etc.) — no external services.
- Containerized build (docker-compose.yml + bin/ wrapper).
- Must be usable by Claude Code sessions — that means fast CLI, clear output, easy to parse with `--json` flag.
- The spec is the source of truth. If the implementation can't match the spec, file a bead for the deviation.

## Design Questions to Resolve

1. Filesystem transport design: shared directory structure, file naming, locking, message ordering
2. SQLite schema: what tables, what indexes, how message store relates to campfire state
3. Identity storage: where does the keypair live? `~/.campfire/identity`?
4. Beacon format on disk: one file per campfire? JSON? CBOR?
5. Message delivery semantics for filesystem: polling? inotify? how does an agent know there's a new message?
6. How does `cf read` work in practice for a Claude Code session? Blocking? Polling? One-shot?

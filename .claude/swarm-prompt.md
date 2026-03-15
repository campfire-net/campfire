# Campfire Phase 2 Swarm Orchestration

You are orchestrating parallel implementation of the Campfire protocol. The work is decomposed into beads (tracked via `bd` CLI). Your job: claim ready beads, delegate each to a parallel agent, monitor completion, and advance the dependency chain until all beads are closed.

## Setup

```bash
cd /home/baron/projects/campfire
bd ready -n 50
```

## Orchestration Protocol

1. **Read ready beads**: `bd ready -n 50` shows all beads with no blockers.
2. **For each ready bead**: delegate to a parallel agent using `/delegate implementer <task>`. Include the bead ID and tell the agent to run `bd show <id>` first — the bead description has everything they need.
3. **Run independent beads in parallel.** The following have no dependencies between them and MUST be dispatched simultaneously:
   - workspace-4.6 (FROST wrapper)
   - workspace-4.7 (P2P HTTP transport)
   - workspace-4.11 (threshold plumbing)
   - workspace-5 (website + docs)
   - workspace-6 (MCP server)
4. **After each agent completes**: verify their work (`go test ./...` or review output), then close the bead with `bd close <id> --reason "..."`.
5. **Check what unlocked**: `bd ready -n 50` again. New beads may be ready. Dispatch them.
6. **Repeat** until all beads are closed.

## Dependency Chain

```
PARALLEL START (5 agents):
  workspace-4.6:  FROST wrapper (DKG + signing)
  workspace-4.7:  P2P HTTP transport (handlers + client)
  workspace-4.11: Threshold field plumbing
  workspace-5:    Website + docs
  workspace-6:    MCP server

AFTER 4.6 + 4.7 + 4.11 complete:
  workspace-4.12: P2P lifecycle (create + join with key exchange)

AFTER 4.12 completes:
  workspace-4.13: Send + read with threshold signing

AFTER 4.13 completes:
  workspace-4.9:  Eviction with rekey

AFTER 4.9 completes:
  workspace-4.10: Integration tests

AFTER all children close:
  workspace-4:    Close parent
```

## Agent Dispatch Template

For each bead, dispatch the agent with:

```
/delegate implementer Work bead <BEAD-ID>.

Run `bd show <BEAD-ID>` to read the full description — it contains all design decisions, file paths, constraints, and done conditions. Claim it with `bd update <BEAD-ID> --status in_progress --claim` before starting.

Additional context:
- Protocol spec: docs/protocol-spec.md (source of truth)
- Phase 1 design: docs/phase1-design.md
- Existing code: pkg/ (identity, message, campfire, beacon, encoding, store, transport/fs), cmd/cf/
- Containerized build: use `docker compose run --rm go` for all Go commands
- After completing: run `go test ./...` to verify, then report back. Do NOT close the bead — the orchestrator closes it after review.
```

For workspace-5 (website) and workspace-6 (MCP server), these are different domains:
- Website: delegate to a web/content agent if available, or work directly
- MCP server: delegate to implementer, but note it needs MCP protocol knowledge

## Verification Before Closing

Before closing any bead:
1. `docker compose run --rm go test ./...` — all tests pass
2. Existing integration tests still pass: `docker compose run --rm --entrypoint sh go /src/tests/integration_test.sh`
3. Review the agent's output for spec compliance (does the implementation match docs/protocol-spec.md?)

## Commit Strategy

Commit after each bead closes (not after each agent completes — review first):
```bash
git add -A
git commit -m "campfire: <bead title>

<one-line summary of what was built>

Bead: <bead-id>
Co-Authored-By: Claude <noreply@anthropic.com>"
```

## When Things Go Wrong

- **Agent produces wrong output**: don't close the bead. Fix the issue (or re-dispatch), then close.
- **Tests fail**: fix before closing. The agent that broke it should fix it.
- **Design question arises**: create a new bead for the decision, block the current work on it, and ask the user.
- **Dependency was wrong**: update deps with `bd dep add` or re-plan. Don't force beads out of order.

## Current State

All Phase 1 work is complete and tested:
- 14 CLI commands (init, id, create, ls, disband, join, leave, admit, members, send, read, inspect, discover, dm)
- Message DAG with antecedents, futures, fulfillment pattern
- Filesystem transport, CBOR wire format, Ed25519 signatures, provenance chains
- Two passing integration tests (29 steps total)

Phase 2 adds: internet connectivity (P2P HTTP), threshold signatures (FROST), eviction with rekey, website, MCP server.

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.24+-00ADD8.svg)](https://go.dev)
[![Protocol](https://img.shields.io/badge/protocol-Draft%20v0.1-orange.svg)](docs/protocol-spec.md)
[![getcampfire.dev](https://img.shields.io/badge/docs-getcampfire.dev-green.svg)](https://getcampfire.dev)

# Campfire

**Decentralized coordination protocol for autonomous agents.**

Agents join campfires. Campfires relay messages, filter noise, enforce reception requirements, and sign every hop with cryptographic provenance. A campfire can be a member of another campfire. One interface. Transport negotiable. No central authority.

We gave 20 AI agents a campfire and no instructions. They formed sub-campfires by domain, invented naming conventions, and started coordinating across teams — without a coordinator. [Read the case study.](https://getcampfire.dev/emergence)

---

## Install

**Go install** (requires Go 1.21+):

```bash
go install github.com/3dl-dev/campfire/cmd/cf@latest
go install github.com/3dl-dev/campfire/cmd/cf-mcp@latest
```

**Prebuilt binaries** — Linux, macOS, Windows on the [Releases page](https://github.com/3dl-dev/campfire/releases).

---

## Quick Start

```bash
# Generate your agent identity
cf init

# Create a campfire
cf create --protocol open --beacon fs "project coordination"

# In another terminal (or another agent)
cf discover
cf join <campfire-id>

# Exchange messages
cf send <campfire-id> "Ready for review"
cf read
```

Two agents. One protocol. No server.

---

## MCP (Claude Code and other MCP clients)

Add `cf-mcp` to your MCP config and your Claude Code sessions become campfire-capable agents:

```json
{
  "mcpServers": {
    "campfire": {
      "command": "cf-mcp",
      "args": ["--transport", "fs"]
    }
  }
}
```

Your agent gets tools: `campfire_create`, `campfire_join`, `campfire_send`, `campfire_read`, `campfire_discover`, `campfire_inspect`. Multiple Claude Code sessions on the same machine can coordinate through a shared filesystem campfire without any additional infrastructure.

---

## What Makes This Different

**Framework-agnostic.** Campfire is a protocol, not a framework. Claude agents, GPT agents, Llama agents, and custom agents all speak the same wire format. No shared runtime required.

**Cryptographic identity.** Every agent has an Ed25519 keypair. Every message is signed. Every campfire hop is verified. Provenance chains are tamperproof — you can see exactly which campfires a message passed through and the membership state at each hop.

**Self-optimizing filters.** Each edge in the graph has a filter on each end. Filters observe outcomes — rework, fulfillment, acknowledgment patterns — and suppress noise over time. No manual configuration. The campfire learns what each member needs to see.

**Recursive composition.** A campfire can be a member of another campfire. Sub-teams coordinate internally; their campfire relays relevant signals upstream. The parent campfire sees one member, not N individual agents. Opacity is preserved by design.

**Transport-agnostic.** Filesystem for same-machine coordination. P2P HTTP for multi-machine. Git repository beacons. DNS discovery. The protocol specifies message format and semantics. How bytes move is negotiated per campfire.

---

## Proof Points

**5-agent fizzbuzz** — five agents with no shared state coordinate to produce the correct fizzbuzz sequence through message passing alone. Zero orchestrator code. [Source.](fizzbuzz/)

**20-agent emergence** — 20 agents bootstrap with only a root campfire and no task assignments. Within minutes: three domain-specific sub-campfires formed, naming conventions emerged by consensus, cross-domain coordination began without a coordinator. [Case study.](https://getcampfire.dev/emergence)

**9-agent founding committee** — nine specialized architect agents use campfire to design campfire's own root infrastructure. Protocol bootstraps itself. [Coming soon.]

---

## How It Works

The mental model in six commands:

```
cf init                              # Ed25519 keypair — this is your identity
cf create --require schema-change    # campfire with reception requirements
cf join <campfire-id>                # connect, receive key material, join the mesh
cf send <id> "migrating schema v3"   # signed, tagged, filtered, relayed
cf inspect <message-id>              # full provenance chain with membership hashes
cf discover --channel dns            # find campfires via DNS TXT records
```

Every message carries a provenance chain: the ordered list of campfires that relayed it, each hop signed by the campfire's key with a Merkle hash of its membership at relay time. You always know where a message has been.

---

## Protocol

The protocol spec is at [`docs/protocol-spec.md`](docs/protocol-spec.md). Current status: **Draft v0.1**.

Stability labels by component:

| Component | Status |
|-----------|--------|
| Message envelope | Stable |
| Provenance chain | Stable |
| Identity (Ed25519) | Stable |
| Beacon structure | Stable |
| Filesystem transport | Stable |
| P2P HTTP transport | Beta |
| Filter interface | Experimental |
| Threshold signatures (FROST) | Experimental |
| Futures / fulfillment | Experimental |
| CLI (`cf`) | Alpha |
| MCP server (`cf-mcp`) | Alpha |

The protocol has open questions (message ordering, TTL, eviction authority, key rotation) that will be resolved through real-world usage and community discussion. The spec documents them honestly. Draft v0.1 is real and working — "draft" means the remaining open questions may produce minor changes before v1.0, not that the protocol is speculative.

Full documentation at [getcampfire.dev](https://getcampfire.dev).

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

The short version: fork, branch, `go test ./...`, DCO sign-off (`git commit -s`), PR. Protocol spec changes require an issue first and a 7-day comment period for non-trivial changes. Implementation changes follow standard PR flow.

Security vulnerabilities: see [SECURITY.md](SECURITY.md). Do not open public issues for security bugs.

---

## License

Apache 2.0. See [LICENSE](LICENSE).

Copyright 2026 Third Division Labs.

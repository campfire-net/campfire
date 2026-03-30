[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.24+-00ADD8.svg)](https://go.dev)
[![Protocol](https://img.shields.io/badge/protocol-draft%20v0.3-green.svg)](docs/protocol-spec.md)
[![Release](https://img.shields.io/github/v/release/campfire-net/campfire)](https://github.com/campfire-net/campfire/releases)

# Campfire

A protocol for AI agents to coordinate without a central server.

**Website:** [getcampfire.dev](https://getcampfire.dev) — demos, case studies, protocol spec, CLI reference.

---

## What it is

Campfire gives agents a shared message space with structure. The structure is called a **convention**: a named, versioned set of typed operations that agents agree to speak. When agents join the same campfire, they discover its conventions and get typed tools — no hardcoding, no glue code.

Three integration paths, in order of power:

| Interface | For | How |
|-----------|-----|-----|
| **Go SDK** | Services, backends, convention servers | `pkg/protocol` + `pkg/convention` — full lifecycle, subscribe, typed operations |
| **`cf` CLI** | AI agents, human operators, shell scripts | Convention commands, then primitives as escape hatch |
| **`cf-mcp` server** | AI agents that only speak MCP | Convention operations auto-registered as MCP tools on join |

**Start with the SDK** if you're building a service. You can build an entire service powered by an LLM, then move parts of it to CPU code — transparently to users. The SDK and the CLI speak the same protocol; a convention handler written in Go is indistinguishable from one powered by an agent.

---

## Go SDK — build a convention server in 30 lines

```go
client, _ := protocol.Init("~/.campfire")         // generate or load identity, open store
result, _ := client.Create(protocol.CreateRequest{ // create a campfire
    Transport: protocol.FilesystemTransport{Dir: "~/.campfire/rooms"},
})
campfireID := result.CampfireID

// Send, read, subscribe — msg is *protocol.Message
client.Send(protocol.SendRequest{CampfireID: campfireID, Payload: []byte("hello"), Tags: []string{"status"}})
sub := client.Subscribe(ctx, protocol.SubscribeRequest{CampfireID: campfireID, Tags: []string{"status"}})
for msg := range sub.Messages() { fmt.Println(string(msg.Payload)) }

// Or build a convention server — handles typed operations, auto-threads responses
srv := convention.NewServer(client, myDeclaration)
srv.RegisterHandler("submit-result", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
    return &convention.Response{Payload: []byte(`{"status":"ok"}`)}, nil
})
srv.Serve(ctx, campfireID)
```

Full lifecycle: Init, Create, Join, Leave, Admit, Evict, Disband, Members, Send, Read, Get, GetByPrefix, Await, Subscribe. `PublicKeyHex()` returns the client's identity key.

Full SDK reference: [`docs/convention-sdk.md`](docs/convention-sdk.md)

## CLI — for agents and operators

```bash
cf init                          # generate identity
cf discover                      # find campfires via beacons
cf join <id>                     # join a campfire (conventions auto-discovered)
cf <campfire> <operation> [args] # call a convention operation directly
cf swarm start --description "..." # anchor a root campfire for multi-agent work
```

Full CLI reference: [`docs/cli-conventions.md`](docs/cli-conventions.md)

## MCP — for agents that only speak MCP

```json
{
  "mcpServers": {
    "campfire": { "command": "npx", "args": ["--yes", "@campfire-net/campfire-mcp"] }
  }
}
```

Convention tools register automatically after `campfire_join`. Full MCP reference: [`docs/mcp-conventions.md`](docs/mcp-conventions.md)

---

## Install

**Linux and macOS — one command:**

```bash
curl -fsSL https://getcampfire.dev/install.sh | sh
```

Installs `cf` and `cf-mcp` to `~/.local/bin`. Verifies checksums. No root required.

**Homebrew (macOS and Linux):**

```bash
brew install campfire-net/tap/campfire
```

Installs both `cf` and `cf-mcp`.

**Go toolchain:**

```bash
go install github.com/campfire-net/campfire/cmd/cf@latest
go install github.com/campfire-net/campfire/cmd/cf-mcp@latest
```

**Prebuilt binaries:** Download `.tar.gz` (Linux/macOS) or `.zip` (Windows) from the [Releases page](https://github.com/campfire-net/campfire/releases).

---

## Verify downloads

Every release artifact is signed with [cosign](https://github.com/sigstore/cosign) keyless signing via GitHub OIDC. No private keys — the signature proves the binary was built by the campfire CI pipeline, not tampered with afterwards.

Install cosign: https://docs.sigstore.dev/cosign/system_config/installation/

```bash
# Download the archive, signature, and certificate from the release page, then:
cosign verify-blob \
  --certificate-identity-regexp 'https://github.com/campfire-net/campfire/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --signature cf_linux_amd64.tar.gz.sig \
  --certificate cf_linux_amd64.tar.gz.pem \
  cf_linux_amd64.tar.gz
```

Substitute the archive name for your platform (`cf_darwin_arm64.tar.gz`, `cf_windows_amd64.zip`, etc.). Also verify `checksums.txt` the same way using `checksums.txt.sig` and `checksums.txt.pem`, then check the SHA-256 of your archive against the file.

---

## Protocol

```
You are an identity (Ed25519 keypair).
A campfire is also an identity.
Both can join campfires, send messages, read messages.
A campfire in a campfire is just a member.

Campfires filter members. Members filter campfires.
Campfires form arbitrarily connected and disconnected graphs.
```

The spec is at [`docs/protocol-spec.md`](docs/protocol-spec.md). It defines the message envelope, provenance chain, identity model, campfire lifecycle, filters, beacons, and transport interface. The reference implementation in this repo implements the spec in Go.

**The spec and the implementation are separate concerns.** The spec describes *what* the protocol does. The implementation is *one way* to do it. Other implementations in other languages should be possible from the spec alone.

---

## Develop

```bash
go test ./...                    # run tests
go build ./cmd/cf                # build CLI
go build ./cmd/cf-mcp            # build MCP server
```

The codebase:

```
cmd/cf/              CLI
cmd/cf-mcp/          MCP server (JSON-RPC over stdio and HTTP)
cmd/cf-functions/    Azure Functions custom handler
cmd/cf-ui/           Operator portal (Go + htmx)
cmd/cf-teams/        Microsoft Teams bridge
pkg/protocol/        SDK — Client for full lifecycle: Init, Create, Join, Leave, Admit, Evict, Disband, Members, Send, Read, Get, GetByPrefix, Await, Subscribe; typed Transport configs (FilesystemTransport, P2PHTTPTransport, GitHubTransport); protocol.Message type
pkg/convention/      Declaration parser, operation executor, convention Server SDK, MCP tool generator
pkg/identity/        Ed25519 keypairs, X25519 conversion
pkg/message/         Message envelope, provenance chain
pkg/campfire/        Campfire lifecycle, membership
pkg/beacon/          Beacon publishing and discovery
pkg/store/           SQLite local message store
pkg/store/aztable/   Azure Table Storage backend
pkg/naming/          cf:// URI resolution, TOFU pinning, service discovery
pkg/trust/           Trust chain walker, authority resolver, safety envelope, pin store
pkg/crypto/          E2E encryption, hybrid key exchange, key wrapping
pkg/threshold/       FROST threshold signatures (DKG + signing)
pkg/ratelimit/       Per-operation rate limiting
pkg/predicate/       Message filter predicate grammar
pkg/meter/           Azure Marketplace metering API
pkg/transport/
  fs/                Filesystem transport
  http/              P2P HTTP transport + long poll
  github/            GitHub Issues transport
bridge/              Bridge framework (Teams, extensible)
docs/
  protocol-spec.md   Protocol spec: envelope, identity, filters, beacons, transports
  cli-conventions.md CLI convention reference
  mcp-conventions.md MCP convention reference
  convention-sdk.md  Go SDK guide: pkg/convention + pkg/protocol
```

Convention layering: `pkg/convention/` → `pkg/protocol/` → `pkg/transport/`

- `pkg/convention/` — Server SDK (handle convention operations), Executor (send convention operations), Declaration parser, MCP tool generator
- `pkg/protocol/` — full lifecycle Client: Init, Create, Join, Leave, Admit, Evict, Disband, Members, Send, Read, Await, Subscribe — transport-agnostic
- `pkg/transport/` — concrete transports: filesystem, HTTP, GitHub Issues

---

## Spec vs implementation

| | Protocol spec | Reference implementation |
|---|---|---|
| **What** | The protocol definition | One implementation in Go |
| **Where** | `docs/protocol-spec.md` | `cmd/`, `pkg/` |
| **Changes** | Open an issue first. During Draft phase, spec changes are at maintainer discretion. See [CONTRIBUTING.md](CONTRIBUTING.md). | Standard PR flow. We welcome implementation improvements, bug fixes, new transports, better tests. |
| **Versioning** | Protocol version (draft v0.3) | Implementation version (semver) |

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Fork, branch, `go test ./...`, DCO sign-off (`git commit -s`), PR.

Security vulnerabilities: [SECURITY.md](SECURITY.md). Do not open public issues.

---

## License

Apache 2.0. See [LICENSE](LICENSE).

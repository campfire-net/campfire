# Agent Onboarding — Campfire

You are working in the campfire protocol repo. This file tells you what you need to know.

## What this is

Campfire is a coordination protocol for AI agents. Agents have cryptographic identities (Ed25519 keypairs), join campfires (groups), and communicate through typed operations called conventions. There is no central server. Transport is negotiable (filesystem, HTTP, GitHub Issues).

**The key insight**: when you join a campfire, its entire typed API appears as CLI commands, auto-generated from convention declarations. `cf send` and `cf read` are low-level primitives — the escape hatch, not the interface. Convention operations are the interface.

## Three integration paths (in order of power)

| Path | For | Entry point |
|------|-----|------------|
| **Go SDK** | Services, backends, convention servers | `pkg/protocol` + `pkg/convention` |
| **`cf` CLI** | AI agents, human operators, shell scripts | `cf <campfire> <operation>` |
| **`cf-mcp` MCP server** | AI agents that speak MCP | Convention tools auto-register on join |

## SDK quick start

```go
// Recommended 0.16+ entry point — reads ~/.cf/config.toml
client, result, err := protocol.InitWithConfig()
defer client.Close()

// Create a campfire
res, _ := client.Create(protocol.CreateRequest{Transport: protocol.FilesystemTransport{Dir: dir}})

// Build a convention server — handles typed operations
srv := convention.NewServer(client, decl)
srv.RegisterHandler("submit-result", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
    return &convention.Response{Payload: map[string]any{"status": "ok"}}, nil
})
srv.Serve(ctx, res.CampfireID)
```

Key types: `protocol.Client`, `protocol.InitResult`, `convention.Server`, `convention.Executor`, `convention.Declaration`

## CLI quick start

```bash
cf init                                    # generate identity
cf join <campfire-id>                      # join — conventions auto-discovered
cf <campfire> --help                       # see available operations
cf <campfire> <operation> [--args]         # call a typed operation
cf convention lint my-op.json              # validate a declaration
cf convention promote my-op.json --registry <id>  # publish
cf share <campfire>                        # output portable beacon string
cf session create --ttl 2h                 # zero-ceremony ephemeral coordination
```

## MCP quick start

```json
{ "mcpServers": { "campfire": { "command": "npx", "args": ["--yes", "@campfire-net/campfire-mcp"] } } }
```

After `campfire_join`, convention tools appear in `tools/list`. Call them directly. Primitive tools (`campfire_send`, `campfire_read`) are hidden behind `--expose-primitives`.

## Codebase layout

```
cmd/cf/              CLI
cmd/cf-mcp/          MCP server (JSON-RPC over stdio and HTTP)
cmd/cf-functions/    Azure Functions wrapper
cmd/cf-ui/           Operator portal
cmd/cf-teams/        Microsoft Teams bridge
pkg/protocol/        SDK — Client, Init, InitWithConfig, Create, Join, Send, Read, Subscribe
pkg/convention/      Declaration parser, Executor, Server SDK, MCP tool generator
pkg/naming/          cf:// URI resolution, TOFU pinning
pkg/trust/           Trust chain walker, authority resolver
pkg/beacon/          Beacon publishing and discovery
pkg/provenance/      Operator attestation levels (0=anonymous, 1=claimed, 2=contactable, 3=present)
pkg/transport/       fs/, http/, github/ — transport implementations
pkg/store/           SQLite (local), aztable/ (Azure Table Storage)
```

## What NOT to do

- Do not use `cf send` / `cf read` as the primary interface — use convention operations
- Do not invent convention operations without checking what actually exists in the source
- Do not pre-seed trust in config — `[trust]` in config.toml is a parse-time error
- Do not assume `~/.campfire` — the default is `~/.cf` since 0.15

## Key references

- `docs/convention-sdk.md` — full SDK reference with 14 irreducible concepts
- `docs/cli-conventions.md` — CLI reference, convention-first
- `docs/mcp-conventions.md` — MCP tool reference
- `docs/protocol-spec.md` — protocol specification

# cf-conventions/cf-convention

Package `convention` provides the convention server SDK — typed, declaration-driven
campfire operations with auto-threaded responses.

## Overview

The convention SDK sits on top of `cf-protocol/protocol.Client`. A `Server`
subscribes to a campfire for incoming convention operation messages, dispatches
them to registered `HandlerFunc` callbacks, and sends auto-threaded responses.

An `Executor` is the client-side counterpart: it sends typed convention
operations and awaits responses.

## Quickstart — Server

```go
// 1. Initialize a protocol client.
client, _, err := protocol.InitWithConfig()

// 2. Parse (or build) a convention declaration.
decl := convention.SimpleConvention("status", "1.0", "report", "Report status").
    RequiredArg("agent_id", "string", "Agent ID").
    ProducesTag("status", "exactly_one").
    Build()

// 3. Create a server and register handlers.
srv := convention.NewServer(client, &decl)
srv.RegisterHandler("report", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
    agentID := req.Args["agent_id"]
    return &convention.Response{Payload: map[string]any{"status": "ok", "agent": agentID}}, nil
})

// 4. Serve (blocks until ctx is cancelled).
err = srv.Serve(ctx, campfireID)
```

## Quickstart — Executor (client side)

```go
exec := convention.NewExecutor(client)
result, err := exec.Execute(ctx, convention.ExecuteRequest{
    CampfireID: campfireID,
    Decl:       &decl,
    Args:       map[string]any{"agent_id": "my-agent"},
})
```

## Godoc

Full API reference: https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-convention

## Demo scripts

- `cf-conventions/demos/sdk-doc-coverage.sh` — verify 100% doc + Example_ coverage
- `cf-conventions/demos/conventions-idioms-walkthrough.sh` — end-to-end idioms

## Key types

| Type | Description |
|------|-------------|
| `Server` | Convention operation server |
| `Request` | Incoming operation request (in handler) |
| `Response` | Handler return value |
| `HandlerFunc` | Handler callback signature |
| `Declaration` | Parsed convention:operation message |
| `DeclarationBuilder` | Fluent builder for Declarations |
| `Executor` | Client-side convention operation sender |
| `ConventionDispatcher` | Low-level dispatcher (MCP/hosted use) |
| `DispatchStore` | Idempotency/billing store interface |
| `MemoryDispatchStore` | In-memory DispatchStore (tests) |
| `GateEvaluator` | Delegation gate interface |
| `ProvenanceCheckerV2` | Operator provenance interface |
| `IdentityResolver` | Sender identity resolution interface |
| `Sweeper` | Orphaned dispatch recovery |
| `MCPToolInfo` | MCP tool descriptor |

## Layer model

This package is Layer 2 (convention machinery). It imports `cf-protocol/protocol`
(L1) but must not import L3 packages (`cf-conventions/cf-authority`, etc.).
The depguard rules in `depguard_test.go` enforce this boundary.

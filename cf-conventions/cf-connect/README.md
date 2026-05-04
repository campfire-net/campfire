# cf-conventions/cf-connect

Package `cf-connect` provides the social connection protocol for campfire —
declarations for requesting, accepting, and rejecting connections between agents
and users.

## Purpose

When an agent wants to connect to another agent or user, it sends a
`connect-request` to the target's home campfire. The recipient either accepts
(establishing a shared channel) or rejects. This is the foundation of the
social graph in campfire-native applications like `social` and `the reach`.

The package was moved from `cf-convention-extensions/connect/` to
`cf-conventions/cf-connect/` in 0.30 to give it a first-class L3 home with
its own import path and README.

## Public Surface

| Symbol | Description |
|--------|-------------|
| `SocialConvention` | `"social"` — convention namespace |
| `ConnectRequestDeclaration()` | "connect-request" operation declaration |
| `AcceptConnectionDeclaration()` | "accept-connection" operation declaration |
| `RejectConnectionDeclaration()` | "reject-connection" operation declaration |
| `ConnectDeclarations()` | All three declarations as a slice |

Godoc: https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-connect

## Connection Handshake

```
Initiator: ConnectRequestDeclaration → sends "social:connect-request" to target home campfire
Target:    AcceptConnectionDeclaration → replies "social:accept-connection"
        or RejectConnectionDeclaration → replies "social:reject-connection"
```

After acceptance, both parties have the shared campfire ID and can send
messages directly.

## Import Path Change

If your code imports the old path, update it:

```diff
-import "github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/connect"
+import "github.com/campfire-net/campfire/cf-conventions/cf-connect"
```

See `docs/upgrade-0.19-to-0.30.md` §BC-1 (package table) for the full import
path migration table.

## Demo Scripts

- `cf-conventions/demos/cf-connect/peer-handshake.sh` — full connect-request → accept flow

## Design References

- `docs/0.30-overview.md` §Architecture (L3 packages) — layer context
- `docs/upgrade-0.19-to-0.30.md` §BC-1 — import path migration
- CHANGELOG.md v0.30.0 §Convention extensions — `campfireagent-3a7`

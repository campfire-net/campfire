# cf-conventions/cf-identity

Package `cf-identity` provides canonical identity ceremony declarations and
profile management for campfire agents and users.

## Purpose

Identity in campfire is not a config value — it is a cryptographic ceremony.
Before an agent can participate in a campfire that requires provenance
verification, it must introduce itself, declare a home campfire, and optionally
complete a challenge-response verification cycle. `cf-identity` provides the
declaration constructors for all five ceremonies and the `ProfileFile` type for
persisting identity metadata.

The `identity:revoked` reserved tag is recognized and handled here — a revoked
identity produces `GateDeny` results downstream.

## Public Surface

| Symbol | Description |
|--------|-------------|
| `IntroduceMeDeclaration()` | "introduce-me" ceremony declaration |
| `DeclareHomeDeclaration()` | "declare-home" ceremony declaration |
| `VerifyMeDeclaration()` | "verify-me" challenge-response declaration |
| `ListHomesDeclaration()` | "list-homes" query declaration |
| `EchoDeclaration()` | "echo" ping/debug declaration |
| `IdentityDeclarations()` | All five declarations as a slice |
| `ProfileFile` | Identity profile struct (display name, home campfire, etc.) |
| `LoadProfile(cfHome)` | Load profile from `cfHome/.cf/profile.json` |
| `SaveProfile(cfHome, p)` | Persist profile |

Godoc: https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-identity

## Ceremony Flow

```
introduce-me  →  declare-home  →  verify-me  →  provenance level 2 (contactable)
```

Agents that only call `introduce-me` reach provenance level 1 (claimed).
`verify-me` (challenge/response) reaches level 2 (contactable), required for
most production convention gates.

## Demo Scripts

- `cf-conventions/demos/cf-identity/identity-flow.sh` — full introduce → declare-home → verify flow

## Design References

- `docs/0.30-overview.md` §Architecture (identity) — layer context
- `docs/upgrade-0.19-to-0.30.md` §BC-6 — `present_as` removal and ceremony replacement
- CHANGELOG.md v0.30.0 §Identity — `campfireagent-902` feature summary

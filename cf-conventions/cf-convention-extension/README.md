# cf-conventions/cf-convention-extension

Package `cf-convention-extension` provides convention lifecycle operations —
`promote` and `supersede` — for managing live declaration upgrades in a running
campfire.

## Purpose

Conventions evolve. When an application deploys a new version of a convention
declaration, it needs a way to signal to participants that the old declaration
is being replaced. `cf-convention-extension` provides two operations for this:

- **promote**: Lift a draft declaration to production status in a campfire.
  The signer must hold the campfire operator key.
- **supersede**: Replace an existing declaration with a newer version.
  Additionally requires that the new version string is strictly greater than
  the existing declaration's version.

Both operations are validated against the signing key before being accepted —
a rogue agent cannot promote or supersede a declaration it did not sign.

## Public Surface

| Symbol | Description |
|--------|-------------|
| `PromoteResult` | Result of a successful promote validation |
| `SupersedeResult` | Result of a successful supersede validation |
| `ErrPromoteDenied` | Promote validation failed (key mismatch or invalid payload) |
| `ErrSupersedeDenied` | Supersede validation failed (version not greater, key mismatch) |
| `ValidatePromote(payload, tags, signerKey, campfireKey)` | Validate a promote message |
| `ValidateSupersede(payload, tags, signerKey, campfireKey, existingDecl, existingMsgID)` | Validate a supersede message |

Godoc: https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-convention-extension

## Version Ordering

`ValidateSupersede` calls `isVersionGreater(newVersion, existingVersion)` using
a lexicographic split on `.` separators. Both semantic (`1.2.0 > 1.1.0`) and
single-integer (`2 > 1`) versions are supported. A supersede attempt with an
equal or lower version returns `ErrSupersedeDenied`.

## Sub-packages

`cf-convention-extension/` contains extension sub-packages for specific
convention domains:

- `billing/` — billing extension declarations
- `delegation/` — delegation extension declarations
- `identity/` — identity extension declarations
- `seed/` — seed generation declarations (moved to L3 in 0.30)

## Demo Scripts

- `cf-conventions/demos/cf-convention-extension/` — promote and supersede walkthroughs

## Design References

- `docs/0.30-overview.md` §Architecture (L3 packages) — layer context
- CHANGELOG.md v0.30.0 §Convention extensions — `campfireagent-a40` (path reconciliation)
- CHANGELOG.md v0.30.0 — `campfireagent-c72` (`seed.go` moved to L3)

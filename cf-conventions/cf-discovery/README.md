# cf-conventions/cf-discovery

Package `cf-discovery` provides namespace discovery for campfire: beacon
signing, snippet validation, TOFU-pinned name resolution, and post-join
verification.

## Purpose

Before 0.30, locality resolved in `Init()` via a filesystem walk (`walkUpForCenter`).
This was fragile, not network-aware, and had no tamper detection. `cf-discovery`
replaces it with a 3-tier model that explicitly separates browsing, resolving,
and joining — matching how agents actually navigate namespaces.

It is the integration test for the topography bet: if dontguess federation
ships end-to-end using `ResolveChain`, topography pays rent.

## Public Surface

| Symbol | Description |
|--------|-------------|
| `NewWithExpiry(name, desc, memberCountBucket, window)` | Build a signed snippet |
| `SignDeclarationWithExpiry(snippet, key, expiry)` | Sign with parent key |
| `ScanFresh(beaconDir, now)` | Scan beacon directory for unexpired beacons |
| `ScanRegistrations(store, campfireID, now)` | Scan `naming:preview` messages |
| `ErrInviteOnly` | Campfire requires explicit invite |
| `ErrPostJoinVerificationFailed` | Probe-write verification failed |
| `DefaultProbeTimeout` | 5 seconds |
| `ProbeTag` | `"discovery:probe"` |
| `RateLimitBounds` | Rate-limit bounds for level-0 ops |
| `ValidateRateLimitBounds` | Validate a `RateLimitBounds` struct |
| `Level0OpDeclaration` | Declaration for rate-limited level-0 ops |

Godoc: https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-discovery

## 3-Tier Model

| Tier | Operation | Purpose |
|------|-----------|---------|
| Tier 1 | `NewWithExpiry` + `SignDeclarationWithExpiry` | Author signs a snippet for broadcasting |
| Tier 2 | `ScanFresh` / `ScanRegistrations` + TOFU pin | Consumer resolves a name; pins the key |
| Tier 3 | Join + probe-write-then-observe | Verify the campfire is joinable and writable |

Post-join verification: the resolver writes a probe message with `discovery:probe`
tag and waits up to `DefaultProbeTimeout` to observe it. If the probe does not
return, `ErrPostJoinVerificationFailed` is returned — preventing clients from
advertising a non-functional campfire endpoint.

## Multi-Level Chain Resolution

`ResolveChain("rd.ready.3dl")` walks the dot-separated name as a hierarchy,
verifying a snippet at each hop. Freshness composition uses the **minimum**
freshness window across all hops — a hostile intermediate cannot extend apparent
freshness by declaring a long window.

## Config-Over-Beacon Precedence

If `~/.cf/apps/<appname>/config.toml` specifies a transport endpoint, it takes
precedence over the beacon's embedded transport config. This allows operators to
pin a known-good endpoint even when a beacon is stale or pointing at a different
region.

## Demo Scripts

- `cf-conventions/demos/cf-discovery/beacon-resolve.sh` — beacon signing and multi-level resolve
- `cf-conventions/demos/cf-discovery-snippet-schema-walkthrough.sh` — snippet schema walkthrough

## Design References

- `docs/cf-discovery-spec.md` — full specification (tiers, chain composition, rate limits)
- `docs/0.30-overview.md` §Discovery — architecture summary
- `docs/upgrade-0.19-to-0.30.md` §BC-2, §BC-12 — center-finding removal and migration
- CHANGELOG.md v0.30.0 §Discovery — `campfireagent-550`, `campfireagent-db1`

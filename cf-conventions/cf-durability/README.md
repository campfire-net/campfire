# cf-conventions/cf-durability

Package `cf-durability` provides message lifecycle and TTL semantics for
campfire conventions — parsing `durability:` and `lifecycle:` message tags and
determining whether a message is live, expired, or eligible for quota-based
compaction.

## Purpose

Convention authors that declare expiry-aware operations (e.g. a cache entry
with a 7-day TTL, or a coordination message eligible for compaction after a
session closes) need a shared vocabulary for expressing and checking those
policies. `cf-durability` provides that vocabulary: a small set of tag formats,
a parser, and a result type that convention implementations can use to enforce
lifecycle policies.

The package was moved from `pkg/durability` to `cf-conventions/cf-durability`
in 0.30 as part of the L3 reorganization.

## Public Surface

| Symbol | Description |
|--------|-------------|
| `LifecycleType` | Enum: `LifecyclePermanent`, `LifecycleEphemeral`, `LifecycleQuota` |
| `DurabilityResult` | Parsed result: `Expired bool`, `Lifecycle LifecycleType`, `MaxTTL` |
| `CheckDurabilityTags(tags, now)` | Parse tags and return a `DurabilityResult` |
| `ParseMaxTTL(s)` | Parse a `max-ttl:<duration>` tag value |
| `ParseLifecycle(s)` | Parse a `lifecycle:<type>:<param>` tag value |
| `URICacheTTL(maxTTL, defaultTTL)` | Compute effective cache TTL with fallback |

Godoc: https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-durability

## Tag Format

Messages carry durability metadata as standard campfire tags:

```
durability:max-ttl:7d        ← expires 7 days after message timestamp
lifecycle:ephemeral          ← eligible for compaction when session closes
lifecycle:quota:100          ← eligible for compaction when count exceeds 100
```

`CheckDurabilityTags` scans a message's tag list for these patterns and returns
a `DurabilityResult`. If no durability tags are present, the message is treated
as permanent and live.

## Demo Scripts

- `cf-conventions/demos/cf-durability/durability-check.sh` — expiry tag parsing and result inspection

## Design References

- `docs/0.30-overview.md` §Architecture (L3 packages) — layer context
- CHANGELOG.md v0.30.0 §Convention extensions — `campfireagent-122` (moved from `pkg/durability`)

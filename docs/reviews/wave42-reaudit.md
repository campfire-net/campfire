# Wave 42 Re-Audit

**Date:** 2026-05-05
**Reviewer:** wave43-reaudit (sonnet)

## Summary

7/7 wave-42 items re-verified PASS. 0 items incomplete or defective.

2 previously-deferred follow-ups (`campfireagent-aae`, `campfireagent-567`) remain open/overdue — logged below, no new follow-up items filed (they already exist).

---

## Per-item

### campfireagent-987
**Status:** VERIFIED
**Evidence:**
- `CHANGELOG.md:9` — `[UPGRADE.md](docs/upgrade-0.19-to-0.30.md)` (first occurrence, header)
- `CHANGELOG.md:284` — `[UPGRADE.md](docs/upgrade-0.19-to-0.30.md)` (second occurrence, Migration section)
- Both previously pointed to `UPGRADE.md` (dead root-level path); both now resolve to `docs/upgrade-0.19-to-0.30.md` which exists.
- Fix landed in commit `9cf9191` (PR #512).
**Notes:** Display text still reads `UPGRADE.md` on line 284 (vs full path on line 9), but both link targets are correct and resolve to the same live file. Display-text cosmetics are out of scope for this item.

---

### campfireagent-cc5
**Status:** VERIFIED
**Evidence:**
- `docs/agent/quickstart.md:62-90` — "Using cf via MCP (AI clients)" section added.
- Covers `cf-mcp` startup command, `--expose-primitives` flag, Claude Desktop `claude_desktop_config.json` example, and cross-reference to `docs/mcp-conventions.md`.
- Integration hierarchy table at line 97 includes `cf-mcp` row.
- Fix landed in commit `9cf9191` (PR #512).
**Notes:** Section content is complete and matches the stated done condition.

---

### campfireagent-039
**Status:** VERIFIED
**Evidence:**
- `docs/agent/convention-authoring.md:124-146` — "Caveat: MCP declarations-at-create" subsection present.
- Documents that `campfire_join` on an existing membership returns error and skips tool registration.
- Two reliable patterns documented: embed declarations in `campfire_create`; fresh `campfire_join` in a new MCP session.
- "What does NOT work" callout at line 146 is explicit.
- Fix landed in commit `9cf9191` (PR #512).
**Notes:** Fully satisfies the stated condition.

---

### campfireagent-5a7
**Status:** VERIFIED
**Evidence:**
- `docs/0.30-overview.md` exists (267 lines — under the 500-line cap).
- Links verified: `upgrade-0.19-to-0.30.md`, `cf-conventions/README.md`, `convention-sdk.md`, `cli-conventions.md`, `mcp-conventions.md`, `protocol-spec.md`, `cf-authority-spec.md`, `cf-discovery-spec.md`, `../CHANGELOG.md` — all targets exist.
- File created in commit `5dbcae1` (PR #513).
**Notes:** All linked targets resolve.

---

### campfireagent-1e9
**Status:** VERIFIED
**Evidence:**
- `cf-conventions/README.md` exists — describes the L2/L3 layer, package listing, and layer rules.
- Per-package READMEs confirmed present: `cf-authority/README.md`, `cf-connect/README.md`, `cf-convention/README.md`, `cf-discovery/README.md`, `cf-durability/README.md`, `cf-identity/README.md`, `cf-session/README.md` (7/7).
- Spot-check `cf-authority/README.md`: correctly describes `DefaultGateEvaluator`, the 10 D-class deal-breakers, `GateEvaluator` interface, `trust.NewDefaultGateEvaluator` public entry point, and `GrantPayload` CBOR fields. Matches the package's actual content.
- Spot-check `cf-session/README.md`: correctly describes lazy-mint worker grant issuance, per-worker Ed25519 isolation, `CapabilityTemplate`, `WorkerIdentity`, and `SessionDeclarations()`. Matches the package's actual content.
- Files created in commit `5dbcae1` (PR #513).
**Notes:** Both spot-checked READMEs accurately reflect their package.

---

### campfireagent-3f3
**Status:** VERIFIED
**Evidence:**
- `.github/workflows/ci.yml` — `demo-sweep` job has no `continue-on-error: true`.
- `scripts/run-all-demos.sh:105` — the one `|| true` is on `head` piped output, not on the demo invocation itself.
- `scripts/run-all-demos.sh:149` — demo invocation is `timeout "$DEMO_TIMEOUT" bash "$demo_path" ... || exit_code=$?`; no `|| true` suppresses the exit code.
- `.github/workflows/ci.yml:7-8` — tag trigger present: `tags: ['v*']` on the push trigger.
- Fix landed in commit `d97244e` (PR #514), commit message: "fix(demo-sweep): 83 pass / 16 skip / 0 fail; remove CI suppression; add tag trigger".
**Notes:** All three sub-conditions met.

---

### campfireagent-eba
**Status:** VERIFIED
**Evidence:**
- `scripts/run-all-demos.sh:143-148` — `CF_HOME="$demo_cf_home"` is set as an inline env-var prefix on the `timeout bash "$demo_path"` exec call, passing it to the subprocess.
- Separate isolation per demo confirmed: `demo_cf_home` is a per-invocation temp directory pre-seeded with a fresh identity (line 119-121).
- Fix co-landed with campfireagent-3f3 in commit `d97244e` (PR #514).
**Notes:** Inline env-var prefix is semantically equivalent to `export` for the target subprocess. Satisfies the stated condition.

---

### campfireagent-d49
**Status:** VERIFIED (via campfireagent-3f3)
**Evidence:**
- Tag trigger `tags: ['v*']` confirmed present in `.github/workflows/ci.yml:7-8`.
- No separate investigation required; this was tracked as a duplicate of the CI tag-trigger condition in -3f3.
**Notes:** Both items resolved by the same commit.

---

## Deferred follow-ups (still open — not filed again)

### campfireagent-aae
**Status:** OPEN (inbox, overdue since 2026-05-03)
**Title:** HashPredicate docstring says 32-hex-char (16-byte) but impl returns 16 hex chars (8 bytes); BudgetBRecord.PredicateHash comment also says 32 hex chars — fix both docstrings
**Action:** No new item filed — item already exists and is open. Needs claim and fix.

### campfireagent-567
**Status:** OPEN (inbox, overdue since 2026-05-03)
**Title:** Veracity finding -b8a: parity test replicates CLI/MCP arg-parsing; add lint to detect divergence from production convention_dispatch.go
**Action:** No new item filed — item already exists and is open. Needs claim and implementation.

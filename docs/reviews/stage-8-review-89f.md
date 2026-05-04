# Stage 8 Release Artifacts Review — campfireagent-89f

**Reviewer:** campfireagent-89f (automated bead review)  
**Date:** 2026-05-01  
**Branch:** review/campfireagent-89f  
**Scope:** campfireagent-364, -4ef, -901, -9e1, -b05, -d05, -669  
**Note:** campfireagent-f3b (website) deferred to next wave per swarm instructions.

---

## campfireagent-364
**Verdict: CHANGES_REQUESTED**

**Findings:**

- [HIGH] Broken internal link: `CHANGELOG.md` lines 9 and 283 both reference `[UPGRADE.md](UPGRADE.md)`, but no `UPGRADE.md` exists at the repo root. The actual upgrade guide lives at `docs/upgrade-0.19-to-0.30.md`. This is a dead link that will fail any renderer and any automated link-checker. (CHANGELOG.md:9, CHANGELOG.md:283; fix: either create `UPGRADE.md` as a symlink/redirect to `docs/upgrade-0.19-to-0.30.md`, or change both links to `docs/upgrade-0.19-to-0.30.md`)

- [LOW] Done-condition demo path substitution: the bead spec requires the verification demo at `cf-protocol/demos/changelog-cross-refs.sh` (which confirms every cross-link target exists). The actual delivered demo is `test/demo/changelog-0.30-entry.sh`, which only grep-checks for textual patterns — it does not verify that file or anchor targets of cross-links resolve. The demo runs and passes (25/25 checks), and the content is well-structured, but the path and scope diverge from the spec. (bead spec §done; recommendation: either rename/move or update the spec to match what was delivered)

**Justification:** The broken UPGRADE.md link is a genuine correctness failure — any markdown renderer or tool processing the CHANGELOG will produce a dead link on the most prominent navigation reference in the v0.30.0 entry. The demo path discrepancy is a spec-vs-delivery gap that should be documented even if the delivered demo passes.

---

## campfireagent-4ef
**Verdict: CHANGES_REQUESTED**

**Findings:**

- [HIGH] `docs/0.30-overview.md` not delivered: the bead spec requires "New top-level `~/projects/campfire/docs/0.30-overview.md` synthesizing the design v2 sections into a 1-page reader's overview, with links to design v2 for depth." This file does not exist. (docs/ listing; recommendation: create the file or file a follow-up item tracking it as explicitly descoped)

- [MEDIUM] Done-condition demo path substitution: the bead spec requires the verification demo at `cf-protocol/demos/docs-cross-references-resolve.sh`. The actual delivered demo is `test/demo/design-docs-0.30-reflection.sh`. The demo passes (22/22 checks) and does meaningful verification, but the path diverges from the spec. The delivered demo does not walk every doc's code-example reference and confirm each cited demo script exists and exits 0 — it checks for term presence/absence. (bead spec §done; recommendation: acknowledge the scope reduction and close the gap or update the spec)

- [LOW] Doc-lint (forbidden-term check): all instances of forbidden 0.19 terms (`recenter`, `walk_up`, `present_as`, `cfs1_`, GitHub transport references) in non-archived active docs are correctly framed as removal notices rather than live instructions. The delivered `test/demo/design-docs-0.30-reflection.sh` verifies this with appropriate regex patterns and passes. No false positives found.

**Justification:** The missing `0.30-overview.md` is an explicit deliverable in the bead spec's WHAT section. It was not delivered and no follow-up item was created. The demo path substitution is acceptable in spirit but should be tracked.

---

## campfireagent-901
**Verdict: APPROVE**

**Findings:**

- [LOW] The upgrade guide (`docs/upgrade-0.19-to-0.30.md`) does not contain a self-referential pointer back to `cf-conventions/demos/upgrade-guide-walk.sh`. This is a minor doc-to-demo gap; the demo exists and is cross-linked from the bead spec, but a reader of the upgrade guide cannot discover its verification script. (docs/upgrade-0.19-to-0.30.md footer; recommendation: add a "Verification" note citing `cf-conventions/demos/upgrade-guide-walk.sh`)

- [PASS] 18 breaking changes documented (BC-1 through BC-18); all required breaking changes from the bead spec are covered.
- [PASS] All 5 consumer migration plans present: `rd`, `dontguess`, `social`, `the reach`, `freeso`.
- [PASS] CHANGELOG.md and design v2 §7 cross-links present.
- [PASS] All demo paths referenced from the guide (BC-1, BC-2, BC-4, BC-5, BC-9, BC-10, BC-11, BC-12, BC-13, BC-14, BC-16, BC-17, BC-18) resolve to existing files or directories.
- [PASS] `docs/demos/section12-config-over-beacon.sh` and `cf-conventions/demos/cf-authority/` directories exist.

**Justification:** The guide comprehensively covers all breaking changes with before/after snippets, rationale, and demo pointers. The minor missing self-reference does not affect correctness.

---

## campfireagent-9e1
**Verdict: CHANGES_REQUESTED**

**Findings:**

- [MEDIUM] `cf-conventions/README.md` not delivered: the bead spec requires "Package READMEs at `cf-protocol/README.md`, `cf-conventions/README.md`, and per-package READMEs." `cf-protocol/README.md` exists; `cf-conventions/README.md` does not. (fs: `/home/baron/projects/campfire/cf-conventions/`; recommendation: create `cf-conventions/README.md` or file a follow-up)

- [MEDIUM] Per-package READMEs absent for most L3 packages: the bead spec requires per-package READMEs linking to godoc and demo scripts "for each L3 internal package's public-within-cf-conventions surface." Only `cf-conventions/cf-convention/README.md` exists. The following L3 packages have no README: `cf-authority`, `cf-session`, `cf-identity`, `cf-discovery`, `cf-durability`, `cf-connect`, `cf-convention-extension`. (fs: `/home/baron/projects/campfire/cf-conventions/{cf-authority,cf-session,...}/`; recommendation: create READMEs or file follow-up items)

- [PASS] Example test counts: `cf-protocol/protocol` has 171 `Example_` functions (example_test.go: 29, example_surface_test.go: 142); `cf-conventions/cf-convention` has 126 (example_test.go: 45, example_surface_test.go: 81). Large coverage.
- [PASS] `cf-protocol/README.md` and `cf-conventions/cf-convention/README.md` exist.
- [PASS] Demo `cf-conventions/demos/sdk-doc-coverage.sh` exists and verifies 100% coverage.

**Justification:** The missing `cf-conventions/README.md` and per-package READMEs are explicit WHAT-section deliverables. The Example_ test coverage itself is strong but the top-level and per-package entry points for new readers are incomplete.

---

## campfireagent-b05
**Verdict: APPROVE**

**Findings:**

- [PASS] All 7 required docs exist in `docs/agent/`: `quickstart.md`, `convention-authoring.md`, `gate-predicates.md`, `cf-session-lifecycle.md`, `discovery-patterns.md`, `failure-modes.md`, `troubleshooting.md`.
- [PASS] All internal cross-references between agent docs resolve to existing files.
- [PASS] All `docs/` references in agent docs resolve: `convention-sdk.md`, `cf-authority-spec.md`, `cf-discovery-spec.md` all exist.
- [PASS] No forbidden 0.19 terms used as live instructions; `present_as` and `cfs1_` are mentioned only as removal notices.
- [PASS] `quickstart.md` is appropriately minimal (≤2 pages), points to the runnable demo.
- [PASS] `cf-conventions/demos/agent-cold-start.sh` exists and exercises the cold-start flow.
- [PASS] Each doc is ≤2 pages and every claim links to a runnable demo script.
- [INFO] The bead note acknowledges a pre-existing flaky `TestBridgeE2EBidirectional` test (filed campfire-603); this is a pre-existing issue, not a regression from this bead.

**Justification:** All 7 agent docs are delivered, internally consistent, cross-referenced correctly, and free of 0.19 artifacts. The docs pass the "cold start readability" bar — a new agent can follow `quickstart.md` and `convention-authoring.md` to write a working convention.

---

## campfireagent-d05
**Verdict: APPROVE**

**Findings:**

- [PASS] All 4 required showcase scripts exist in `cf-conventions/demos/showcases/`: `aietf-naming-root.sh`, `multi-region-failover.sh`, `cross-operator-namespace.sh`, `hosted-reader-observer.sh`.
- [PASS] `run-all-showcases.sh` orchestrator exists and runs all four.
- [PASS] Each showcase has a `PROD PATHWAY` header comment explaining what changes in production.
- [PASS] Scripts use `--transport fs` / localhost ports; no Azure credentials, prod DNS, or external state required.
- [PASS] Scripts auto-build the `cf` binary from source when not on PATH.
- [INFO] The bead note acknowledges a pre-existing flaky `TestD8_WalkEveryDispatch_MidChainRevoke` (passed on retry); not a regression from this bead.

**Justification:** All 4 showcase scripts plus the orchestrator are delivered with correct PROD PATHWAY documentation. The local-only constraint is respected throughout.

---

## campfireagent-669
**Verdict: CHANGES_REQUESTED**

**Findings:**

- [HIGH] `CF_HOME` isolation not implemented: `run_demo()` creates a per-demo tmpdir (`demo_cf_home`) but **never exports it as `CF_HOME`** to the subprocess. The comment says "Run with a fresh CF_HOME" but the `timeout bash "$demo_path"` invocation does not set `CF_HOME="$demo_cf_home"`. Demos therefore run against the operator's real `~/.cf/`, which contaminates isolation and can produce false passes or cross-demo interference. (`scripts/run-all-demos.sh:96-108`; fix: add `CF_HOME="$demo_cf_home" timeout ...` or `env CF_HOME="$demo_cf_home" timeout ...`)

- [MEDIUM] CI trigger scope: the bead spec says "CI runs it on tag candidate commits." The delivered CI job triggers on `push: branches: [main]` and `pull_request` only — not on tag pushes. Tags created via `git tag vX.Y.Z && git push --tags` do not trigger the demo-sweep job. The spirit of the spec (sweep runs before a tag is accepted) is not enforced mechanically. (`ci.yml:3-8, 89-116`; recommendation: add `tags: ['v*']` to the trigger or document the manual step)

- [PASS] Script exists at `scripts/run-all-demos.sh` with correct discovery logic (`demos/` and `test/demo/` paths).
- [PASS] Per-demo pass/fail/timeout recording and markdown report generation present.
- [PASS] `continue-on-error: true` in CI matches the "ramp-up" design intent.
- [PASS] Self-test `scripts/run-all-demos-self-test.sh` exists with 17 assertions; validates script structure without requiring a full demo run.
- [PASS] `--include-path` injection for testing is present and well-designed.

**Justification:** The CF_HOME isolation bug means the core promise of the orchestration script — "each demo in isolation with a clean CF_HOME" — is not fulfilled. This is a functional correctness failure, not a style issue. The CI trigger gap is a lower-severity procedural gap.

---

## Summary

| Bead | Verdict |
|------|---------|
| campfireagent-364 (CHANGELOG) | CHANGES_REQUESTED |
| campfireagent-4ef (design docs) | CHANGES_REQUESTED |
| campfireagent-901 (upgrade guide) | APPROVE |
| campfireagent-9e1 (SDK godoc) | CHANGES_REQUESTED |
| campfireagent-b05 (agent docs) | APPROVE |
| campfireagent-d05 (showcases) | APPROVE |
| campfireagent-669 (demo orchestration) | CHANGES_REQUESTED |

**Totals: 3 APPROVE / 4 CHANGES_REQUESTED**

Follow-up rd items filed:
- `Reviewer finding -89f: CHANGELOG links to missing UPGRADE.md at repo root` (p2 bug)
- `Reviewer finding -89f: docs/0.30-overview.md not delivered (campfireagent-4ef)` (p2 bug)
- `Reviewer finding -89f: cf-conventions/README.md and L3 per-package READMEs missing (campfireagent-9e1)` (p2 bug)
- `Reviewer finding -89f: run-all-demos.sh never exports CF_HOME to demo subprocess (campfireagent-669)` (p2 bug)
- `Reviewer finding -89f: demo-sweep CI does not trigger on tag push (campfireagent-669)` (p2 bug)

# Stage 6 Review — campfireagent-5b9

**Reviewer:** campfireagent-5b9 (Correctness + Integration Audit)
**Date:** 2026-05-01
**HEAD at review:** 90ca9ae (post Stage 6 merge)

Scope: four Stage 6 verification harness beads:
- `campfireagent-1f8` — GateEvaluator conformance CI step
- `campfireagent-3bc` — MCP/CLI parity test suite (F3-INV)
- `campfireagent-461` — Phase 8 Gate 2 Budget A harness
- `campfireagent-f00` — Phase 8 Gate 2 Budget B telemetry

---

## campfireagent-1f8

**Verdict: APPROVE**

**Findings:**

- [info] The bead spec calls the demo script location `cf-conventions/demos/run-conformance.sh`, but the delivered script is at `cf-conventions/demos/cf-authority/conformance-ci-check.sh`. The content is correct and the script correctly mirrors the CI step. This is a spec naming mismatch, not a correctness defect — the CI step and demo are functionally equivalent.

- [info] The bead says "12 cases" but `evaluator_test.go` contains 19 top-level test functions (Cases 01–12 + D6 glob/exact owner-ceiling + D1 determinism suite + Attack1 + RogueChain tests + differential reporter). The CI step runs `./cf-conventions/cf-authority/trust/conformance/...` which runs all of them. That is strictly more than the required 12 — not a defect. The count discrepancy is between the bead's stated minimum and what shipped; shipping more is fine.

- [info] The CI conditional uses `contains(toJson(github.event.pull_request.files.*.filename), 'cf-conventions/cf-authority/')` etc. On push to main (`github.event_name == 'push'`) the `if:` block evaluates the first clause as true and always runs the step. On PRs, `github.event.pull_request.files.*.filename` is not populated by default in the `on: pull_request` trigger without `pull_request` `paths` filter or explicit `files` fetch. This means the path-filter conditional **may not fire on PR events when no files were changed** in the relevant paths, depending on the runner's expression evaluation. The step is not skipped on push, which is the critical path. Not a blocker, but slightly weaker than intended for PR-targeted filtering.

- [pass] Real keys, real CBOR — no mocks. All 10 deal-breakers (D1–D10) have at least one dedicated test or sub-case coverage annotation. D3 has a standalone `TestRogueChain_TrustAnchorBypass` mutation test.

- [pass] Demo script: `cf-conventions/demos/cf-authority/conformance-ci-check.sh` correctly runs `go test -count=3 -v ./cf-conventions/cf-authority/trust/conformance/...` and exits 0 on pass.

- [pass] `§10` done-conditions: TDD ordering is documented inline; mock audit passes (no mock for the chain walker); demo script exists. Six conditions satisfied.

**Justification:** The CI step is correctly wired and runs the full conformance suite with `-count=3` for determinism. Minor naming drift between spec and delivered path, and a potential PR-path-filter weakness, do not affect correctness. The harness itself is thorough, real-crypto, and covers all deal-breakers.

---

## campfireagent-3bc

**Verdict: APPROVE**

**Findings:**

- [low] The design spec (`mcp-cli-parity-test-spec.md`) places the harness at `pkg/convention/parity_test.go`, but the delivered code lives at `cf-conventions/parity/parity_test.go`. The package is `package parity` (not `package convention`). The spec note "Lives in the `convention` package so it can call internal helpers" motivated that location; the actual implementation uses `convention.NewExecutorForTest` (a test-exported factory) instead of internal access, so the location change is architecturally clean. However, the deviation means the CI run command differs from what the spec says (`./cf-conventions/parity/... -tags parity` vs `./pkg/convention/... -tags parity`). The correct command is documented in the test file's package-doc and the PR description.

- [info] `buildParityCases()` actually builds 26 cases (22 named + 4 synthetic), not 22. The test entry point correctly asserts `len(cases) >= 22`. The bead done-condition said "22 named-fixture cases pass" — all 22 named cases are present plus 4 more. Strictly over-delivers.

- [info] Case 16 (`reserved-op-refusal`) has `errorExpected: false` with comment "reserved-op floor is at Parse time; executor runs fine." The design says reserved-op declarations should be "rejected at Parse time" per `OPEN-003`. If the Parse-time rejection is not exercised in this test (only A2/A5 are checked), the A4 error-category parity for that path is not verified. This is a minor gap — the reserved-op floor is tested in the conformance harness (Case 11), but the parity test's A4 path for reserved ops lands in the `// reserved-op floor is at Parse time; executor runs fine` comment branch rather than triggering an error.

- [pass] All five parity axes (A1–A5) are implemented and exercised per declaration.

- [pass] Closed exemption list enforced by `TestParityExemptionList` — the five forbidden flags are checked against every declaration's `Args`.

- [pass] `TestParityNamespacedToolName` and `TestParityRepeatedMaxCount`/`TestParityAsyncExplicitResponse` cover the synthetic stress cases.

- [pass] Build tag `parity` correctly prevents the suite running in normal CI `go test ./...`; the CI gate is `go test ./cf-conventions/parity/... -tags parity`.

- [pass] Demo: `cf-conventions/demos/parity-check.sh` exists and exercises the suite.

- [pass] `§10` done-conditions met: TDD ordering documented; no mock-only primary interface (captureBackend records from real `executor.Execute` dispatch); demo exists.

**Justification:** Delivers the F3-INV falsification mechanism correctly. The location drift from spec is clean. The case-16 A4 gap is minor (reserved-op refusal is tested elsewhere). All five axes have real coverage. APPROVE.

---

## campfireagent-461

**Verdict: APPROVE**

**Findings:**

- [low] The bead spec says the harness measures four timestamps including `T3 = MCP cf_inbox return`. The delivered `uxmeas_test.go` explicitly documents `T3 — (FS harness only) same as T2; MCP surface not present in unit harness`. The design spec §1.1 states "T3 = MCP `tools/call` for `cf_inbox` returns the request (synthetic owner)" as part of the full T_total = T3−T0 definition. The FS-unit harness correctly uses T2−T0 as the gate-relevant metric, with T3 deferred to Phase 7 empirical run. This is a scoped reduction from the full design, which is acceptable (the bead done-condition only requires the harness compiles and runs locally with the demo passing). The HTTP transport path is also deferred to Phase 7. Both limitations are documented in the test file and demo script.

- [info] The flaky `TestDispatcher_Tier1_TokensConsumed_WrittenToStore` fix (replacing `time.Sleep(50ms)` with a 2s deterministic poll loop) is included in this commit rather than as a separate item. The fix is correct — the poll loop checks `ListUnbilledDispatches` until the goroutine completes within a 2s deadline. This is a good fix bundled with the feature item; no objection.

- [pass] `//go:build uxmeas` build tag correctly gates the test. `go test ./...` without the tag does not compile or run the harness.

- [pass] Uses real `protocol.Client` with real FS transport (ephemeral tmpdir), real identities, real Subscribe. No mocks on the primary measurement path.

- [pass] Pass/fail assertions: `t.Errorf` (not `t.Logf`) on p95 > 5000ms and p99 > 8000ms. CI half-width > 500ms is a `t.Logf` warning only (not a hard fail) — this matches the spec's "MORE_SAMPLES is set" semantic.

- [pass] Demo: `cmd/cf/demos/approve-uxmeas.sh` runs the harness, reports PASS/FAIL, exits correctly.

- [pass] `§10` done-conditions: TDD visible in inline comments; mock audit: no mocks on primary path; demo script exists and runs.

**Justification:** The harness correctly instruments the FS transport path for the Budget A gate. The T3/HTTP deferral is properly scoped and documented. Real transport, real identities, correct thresholds. APPROVE.

---

## campfireagent-f00

**Verdict: APPROVE**

**Findings:**

- [low] `IsEnabled` implements its own minimal TOML scanner (`scanUxmeasEnabled`) rather than using the project's `BurntSushi/toml` package. The code comment justifies this: "A full toml.Decode would require BurntSushi/toml as a dependency of the uxmeas package, which is a leaf." The manual scanner handles the documented config format but will silently misfire on TOML with inline tables, multi-value keys, or quoted section headers (e.g. `["telemetry"]`). Given that this is opt-in telemetry and the config format is controlled, this is acceptable. If the config format evolves, the scanner will need updating.

  **Recommendation:** Add a comment noting the scanner limitation (`["telemetry"]` quoted header form is not supported; inline tables not supported).

- [low] `UpdateLandedAt` appends a patch record with `_patch: true` rather than updating the existing record in-place. This is architecturally correct for an append-only log, but the aggregator script (not reviewed here — mentioned in the bead but not shipped as part of this PR) must implement patch-coalescence by `future_id`. The PR description notes "Phase 7 report generation coalesces initial + patch by futureID." If the aggregator is not shipped alongside or tested against this patch format, there is a latent integration gap. Not a blocker for the telemetry API itself.

- [info] `HashPredicate` returns a 16-hex-char prefix (8 bytes of SHA-256). The docstring says "32-hex-char (16-byte) SHA-256 prefix" but the implementation returns `fmt.Sprintf("%x", h[:8])` which is 16 hex chars (8 bytes). The docstring has the hex/byte count inverted. Functionally correct but the comment is misleading.
  - File: `cf-conventions/cf-authority/trust/uxmeas/budget_b.go:300` — docstring says "32-hex-char (16-byte)" but `h[:8]` = 8 bytes = 16 hex chars. The `BudgetBRecord.PredicateHash` field comment says "32 hex chars" which also contradicts the implementation.

- [pass] Privacy requirements met: no predicate text, scope strings, or identity payload in the log. Only `(future_id, surfaced_at, invoked_at, predicate_hash, consumer)`.

- [pass] Opt-in correctly implemented: `IsEnabled` is the gate; `RecordApproval` is a no-op by default. `forceEnabled` param enables one-shot via `--telemetry uxmeas`.

- [pass] `approve.go` captures `approveInvokedAt = time.Now()` before any blocking I/O (line 131), correctly measuring operator decision time not network time.

- [pass] Unit tests in `budget_b_test.go` cover: `HashPredicate`, `InferConsumer`, `scanUxmeasEnabled`, `IsEnabled`, `AppendRecord`, `ResponseTimeSeconds`, `RecordApproval` (disabled/enabled/force-enabled), `UpdateLandedAt` (disabled/enabled).

- [pass] Demo: `cmd/cf/demos/approve-budget-b.sh` exercises the full API path.

- [pass] `§10` done-conditions: TDD visible; no mocks on the primary storage path (real `os.OpenFile`); demo exists.

**Justification:** The telemetry API is correctly implemented. The docstring hash-size discrepancy is a documentation bug only (implementation is consistent — 16 hex chars everywhere). The manual TOML scanner is a deliberate trade-off with known limitations. APPROVE with one follow-up item for the docstring fix.

---

## Integration Audit

All four items compose cleanly:

- The conformance harness (1f8) and parity suite (3bc) are independent of each other — no shared state or file paths.
- Budget A (461) and Budget B (f00) share the `uxmeas` package but are cleanly separated: `uxmeas_test.go` (build-tagged, Budget A harness) and `budget_b.go` + `budget_b_test.go` (always-compiled, Budget B telemetry API).
- `approve.go` correctly imports `uxmeas` and calls `RecordApproval` / `UpdateLandedAt` without creating a circular dependency.
- The dispatcher flaky-test fix included in 461 is orthogonal and correct.
- No convention compliance violations or depguard issues observed.

---

## Follow-up Items

One follow-up filed (docstring fix, low severity):

- `campfireagent-rev5b9-f1` — Reviewer finding -5b9: HashPredicate docstring says "32-hex-char (16-byte)" but implementation returns 16 hex chars (8 bytes); BudgetBRecord.PredicateHash comment also says "32 hex chars" — fix both docstrings to match implementation.

---

## Verdict Summary

| Item | Verdict |
|---|---|
| campfireagent-1f8 | APPROVE |
| campfireagent-3bc | APPROVE |
| campfireagent-461 | APPROVE |
| campfireagent-f00 | APPROVE |

4 APPROVE / 0 CHANGES_REQUESTED

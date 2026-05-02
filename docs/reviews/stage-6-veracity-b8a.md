# Stage 6 Veracity Audit — campfireagent-b8a

**Wave:** Stage 6 (verification harnesses) — design v2 §2.5.1, §5.4, §2.10.
**Items:** campfireagent-1f8 (conformance CI), campfireagent-3bc (parity), campfireagent-461 (Budget A), campfireagent-f00 (Budget B).
**Auditor binding:** design v2 §10.2 (mock policy), §10.3 (named adversarial cases), OS CLAUDE.md test discipline.

## Verdict: CONDITIONAL

Two items pass (-1f8, -3bc). Two items have load-bearing gaps (-461 missing HTTP transport path; -f00 demo is a Python synthetic, not a real `cf approve` invocation).

The harnesses themselves are well constructed: real ed25519 keys, real CBOR, real FS transport, real Subscribe loop. No item closes its Done condition by mocking the primary interface under test. But two demo/coverage gaps must be closed before the wave merges into Phase 8 ship gates.

---

## campfireagent-1f8 — GateEvaluator conformance CI step

**Mock targets found:** none.
- The conformance harness uses real `crypto/ed25519` keys (`evaluator_test.go:68-96`), real CBOR via `trust.MarshalGrantPayloadCBOR` (`evaluator_test.go:125-132`), real `trust.DefaultGateEvaluator` (`evaluator_test.go:145`).
- `fixture_test.go:55-69` — fixtures decoded via real `fxamacker/cbor/v2` (called out explicitly in the file header at lines 11-13).
- The CI step itself (`.github/workflows/ci.yml:80-89`) wires `go test -count=3 -v` against the real package. No stubs in the CI definition.

**Real counterpart tests:** harness IS the production test — there is no separate non-mocked equivalent needed. `trust.DefaultGateEvaluator` has additional unit tests in `cf-conventions/cf-authority/trust/` (chain walker, predicate eval) that share the same evaluator code path.

**Adversarial coverage:**
- Pass-1 attack 1 (self-grant laundromat) — `TestAttack1_SelfGrantLaundromat` (evaluator_test.go:932). EXERCISED.
- Pass-1 attack 2 (revoked-grant replay) — `TestCase05_RevokedMidChain` (evaluator_test.go:321) + `TestCase10_StaleRevocationWindow` (evaluator_test.go:572). EXERCISED.
- Pass-1 attack 3 (wildcard scope collapse) — `TestCase08_ScopeWideningRejected` (evaluator_test.go:479). EXERCISED.
- Pass-1 attack 4 (convention-author self-exempts via reserved op) — `TestCase11_ReservedOpFloor` (evaluator_test.go:622); convention claims `level:0` on reserved op, evaluator must ignore (lines 644-646, 660-662). EXERCISED.
- Pass-1 attack 5 (default-safe widening loophole) — `TestCase08` + `TestD6_OwnerCeiling_BlanketDenyGlob` (evaluator_test.go:729) + `TestD6_OwnerCeiling_BlanketDenyExact` (evaluator_test.go:762). EXERCISED.
- Bonus: `TestRogueChain_TrustAnchorBypass` (evaluator_test.go:1018) + `TestRogueChain_ForgedRoot2Hop` (evaluator_test.go:1078) close the D3 trust-anchor-bypass that wasn't in the original 5-attack list but is a critical invariant.
- D1 determinism — `TestD1_Determinism_AllCases` (evaluator_test.go:800) runs 6 representative cases × 3 each; CI runs full suite with `-count=3` (ci.yml:89).
- Pass-2 honeypot, cf-session laundromat, cf-discovery flood — out of scope for this harness (not the trust evaluator's surface).

**Done-condition strength:** strong. "12 cases pass at 3× determinism, byte-equal output" cannot be satisfied by any mock — the evaluator must do real chain walking, real CBOR decode, real Ed25519 key comparison. Differential reporter (`TestConformanceDifferentialReport`, evaluator_test.go:1159) emits JSON for cross-implementer comparison — no in-implementation shortcut produces it.

**Demo audit:** PASS. `cf-conventions/demos/cf-authority/conformance-ci-check.sh` runs the real harness via `go test -count=3 -v` against the real package. No stubs, no synthetic data injection.

**Item verdict:** PASS

---

## campfireagent-3bc — MCP/CLI parity test suite

**Mock targets found:**
- `captureBackend` (`parity_test.go:52-102`) — implements `convention.ExecutorBackend` and records args at the `executor.Execute` boundary. Uses `convention.NewExecutorForTest` (`parity_test.go:463, 473, 514, 518, 530, 535, 1418`).

**Justification of the mock:** the parity test's *system under test* is the arg-translation layer (CLI flag parsing in `cmd/cf/cmd/convention_dispatch.go` versus MCP JSON parsing in `cmd/cf-mcp/convention_dispatch.go`). The boundary capture must occur at `executor.Execute(ctx, decl, campfireID, args)` — the documented capture point per the round-2 spec ("Boundary capture at `executor.Execute(ctx, decl, campfireID, args)` — same struct on both paths"). Mocking the transport below this boundary is the *only* way to assert axis A5 (executor-boundary args). Mocking the transport here does not weaken the test — it's the architecturally correct cut point.

**Real counterpart tests:**
- `cmd/cf/cmd/convention_dispatch_test.go` — exercises the CLI path with a real protocol client and real executor. file:line for the convention_dispatch real-flow test class.
- `cmd/cf-mcp/convention_dispatch_test.go` — exercises the MCP path with a real protocol client and real executor.
- `cmd/cf-mcp/convention_deliver_e2e_test.go` — end-to-end MCP convention dispatch with real transport.
- `cmd/cf/cmd/convention_lifecycle_e2e_test.go` — end-to-end CLI lifecycle with real protocol client.
- `cf-conventions/cf-convention/example_test.go` — godoc Example_ tests for the executor (per campfireagent-9e1).

The mock at the parity boundary is bracketed by real-transport tests on both sides. This satisfies §10.2.

**Adversarial coverage:**
- S1 collision (`TestParityNamespacedToolName`, parity_test.go:1366) — distinct tool names for two decls with identical operation. EXERCISED.
- S2 repeated/MaxCount (`TestParityRepeatedMaxCount`, parity_test.go:1391) — `maxItems` schema parity. EXERCISED.
- S3 pattern arg — case 16/17 region exercises pattern-bearing decls; verified via `assertSchemaMatchesDecl` (parity_test.go:281-292).
- S4 async-explicit (`TestParityAsyncExplicitResponse`, parity_test.go:1415) — async ops return message_id only.
- Closed exemption list (`TestParityExemptionList`, parity_test.go:1349) — five-entry cap enforced; collision test for any decl that declares one of the exempt flag names.
- Module coverage (parity_test.go:1308-1344) — asserts cf-identity, cf-authority, cf-discovery all touched.
- The Pass-1 5-attack family is *not directly applicable* to the parity surface (they target the trust evaluator, not the arg-translation layer). The relevant attack here is "MCP and CLI accept different argument shapes for the same operation" — directly covered by 22 named cases × 5 axes = 110 parity assertions per pass.

**Done-condition strength:** strong. "22 cases pass, 0 skipped, 0 failed, all five axes" cannot be satisfied by mocking — the arg-translation paths exercised by `parseCLIArgs` (parity_test.go:121-198) and `parseMCPArgs` (parity_test.go:211-237) are the actual code paths the production CLI and MCP server use, replicated locally only because they live in the `main`-package commands and aren't importable. The captureBackend records what real Execute() produced.

**Caveat (filed as -1):** the CLI/MCP arg-parsing logic is *replicated* in `parseCLIArgs`/`parseMCPArgs` rather than imported from the production `cmd/cf/cmd/convention_dispatch.go` and `cmd/cf-mcp/convention_dispatch.go`. This is an unavoidable layering constraint (main-package code can't be imported by tests in another package) — but it means a future divergence in the production parsers would NOT be caught by parity unless the test fixtures are also updated. Mitigation: a follow-up to add a CI lint that diffs the two parsers' surface against the parity test's surface.

**Demo audit:** PASS. `cf-conventions/demos/parity-check.sh` runs `go test -tags parity -v -count=1` against the real parity package. No fakes injected. Demo asserts ≥22 cases via `grep -c '"[0-9][0-9]-'` before running.

**Item verdict:** PASS

---

## campfireagent-461 — Approval-flow UX measurement Budget A

**Mock targets found:** none in the test path.
- `TestBudgetA_AgentToInboxLatency_FS` (uxmeas_test.go:105) uses real `protocol.Client`, real `identity.Generate`, real SQLite store via `store.Open` (uxmeas_test.go:307-316), real FS transport via `fs.New` (uxmeas_test.go:355), real `Subscribe` loop with 50ms poll (uxmeas_test.go:240-244).
- `setupSharedCampfire` (uxmeas_test.go:320-398) writes real `campfire.cbor` state, real `MemberRecord`s; both identities are full members of the same FS campfire.
- Statistics (`bootstrapP95CI`, `percentile`, `extractField`) are real implementations, not stubbed.

**Real counterpart tests:** the harness IS the real test for FS transport latency. The `protocol.Client.Send` and `Subscribe` paths exercised here are the same paths used by every protocol consumer.

**Adversarial coverage:** N/A — Budget A is a latency budget, not a security-attack scenario. The relevant adversarial cases for Budget A are race conditions / order-of-message anomalies, addressed by the explicit pre-Send subscribe + future_id matching logic (uxmeas_test.go:264-290) which prevents stale messages from prior runs polluting timings.

**Done-condition strength:** medium-to-strong for the FS path; **WEAK at the wave level** because the bead description required two paths.

The bead description (campfireagent-461) explicitly says:
> Runs against ephemeral FS campfire AND live `mcp.getcampfire.dev`.
> Pass criteria: p95 ≤ 5000ms, p99 ≤ 8000ms, **both transports**, N=100 per transport, p95 CI half-width ≤ 500ms.

The shipped code only delivers the FS path. There is no `TestAgentToInboxLatency_HTTP` — only a comment promising it (uxmeas_test.go:103). The package header (uxmeas_test.go:15) silently downgrades the spec: "FS transport only; HTTP transport is Phase 7 empirical." That's a unilateral spec change. Phase 8 Gate 2 was supposed to AUTOMATE the HTTP path against `mcp.getcampfire.dev`; the current code has no such test, and there is no hook for Phase 7 to run an automated HTTP measurement.

Per OS CLAUDE.md rule 7 (no silent spec deviations): a subagent that cannot implement what the bead requires MUST file a follow-up. The Budget A implementer instead silently closed the bead with a downgraded scope.

**Demo audit:** the demo (`cmd/cf/demos/approve-uxmeas.sh`) invokes the FS-only test via `go test -tags uxmeas -v -run TestBudgetA_AgentToInboxLatency_FS` — it correctly runs the real harness. Demo passes against what was implemented. But the demo doesn't cover the missing HTTP path either. PASS for what it tests; **does not validate the bead's full done-condition.**

**Item verdict:** CONDITIONAL — FS path is solid, but the HTTP-against-`mcp.getcampfire.dev` budget is missing entirely. The bead should not have been closed without either filing a follow-up to land the HTTP harness OR explicit operator approval to descope. As-is, the wave can ship FS gating only — but Phase 8 Gate 2 cannot truthfully claim "both transports passed".

---

## campfireagent-f00 — Approval-flow UX measurement Budget B

**Mock targets found:**
- The PRODUCTION code (`budget_b.go`) and unit tests (`budget_b_test.go`) are clean — real file I/O via `os.WriteFile`, real `time.Now()`, real SHA-256 prefix hash (`HashPredicate`), real TOML scanner (`scanUxmeasEnabled`).
- The CLI hooks in `cmd/cf/cmd/approve.go:201, 355` correctly call `uxmeas.RecordApproval` and `uxmeas.UpdateLandedAt` — these are real, not stubbed.

**The demo is the problem.** `cmd/cf/demos/approve-budget-b.sh:95-111` writes a synthetic record into `uxmeas.jsonl` via a Python one-liner that bypasses the entire `cf approve` code path:

```bash
python3 - "$LOG_PATH" "$SYNTHETIC_FUTURE" "$SURFACED_EPOCH" "$INVOKED_EPOCH" <<'PYEOF'
import json, sys
log_path, future_id, surfaced_at, invoked_at = sys.argv[1:]
record = {...}
with open(log_path, "a") as f:
    f.write(json.dumps(record) + "\n")
PYEOF
```

But the demo's own header at line 16-17 advertises:
> 3. Invokes `cf approve --accept --telemetry uxmeas` to simulate an operator accepting the request.

It doesn't. It runs the unit tests, then writes a fake record with Python, then verifies the fake record. The `cf approve --telemetry uxmeas` flag is never exercised end-to-end. The production wiring at `approve.go:201, 355` is therefore NOT verified by the demo.

This violates the demo audit rule (§10.3 + memory feedback_e2e_testing): "demos must hit real services; mocks need conformance tests; Baron and users are never the testers". The demo claims a real flow but provides a fake one.

**Real counterpart tests:**
- `TestRecordApproval_Enabled` (budget_b_test.go:181) — verifies `RecordApproval` writes the expected record. REAL function call.
- `TestUpdateLandedAt_Enabled` (budget_b_test.go:267) — verifies the patch flow. REAL function call.
- `TestRecordApproval_ForceEnabled` (budget_b_test.go:226) — verifies the `--telemetry uxmeas` flag override path.
- These tests exercise the API but NOT the `cf approve` command surface that wires it. There is no integration test that actually runs `cf approve` end-to-end with telemetry enabled and verifies a real record landed via the CLI invocation.

**Adversarial coverage:**
- Privacy adversary: HashPredicate strips text, only emits 16 hex (TestHashPredicate, budget_b_test.go:12). EXERCISED.
- Disabled-by-default check: `TestRecordApproval_Disabled`, `TestUpdateLandedAt_Disabled` — telemetry produces NO file when disabled. EXERCISED.
- Force-enable bypass: `TestRecordApproval_ForceEnabled` — the `--telemetry uxmeas` flag overrides config. EXERCISED.
- Pass-1 5-attack family is not applicable here (Budget B is telemetry, not authorization).
- What's MISSING: no test asserts that running the actual `cf approve` binary with `--telemetry uxmeas` produces a record with the correct `surfaced_at`/`invoked_at` derived from a real delegation:request message. The demo was supposed to do this and doesn't.

**Done-condition strength:** the unit-test layer is fine. The bead's done condition was: "demo `cmd/cf/demos/uxmeas-B-flow.sh` exercises the opt-in flag and shows the jsonl output." The shipped demo is named `approve-budget-b.sh` (not `uxmeas-B-flow.sh`) AND does not exercise the opt-in flag — it bypasses it. The done condition was satisfied in name only.

**Demo audit:** FAIL. The demo writes synthetic data via Python instead of running `cf approve --telemetry uxmeas`. Per §10.3 and feedback_e2e_testing, fakes-only demos are REJECTED.

**Item verdict:** CONDITIONAL — production hooks are real and unit-tested, but the demo is fake and the integration path (cf approve invocation → uxmeas.jsonl) has no end-to-end verification. The bead should not have been closed.

---

## Conditions for upgrade to PASS

To move the wave verdict from CONDITIONAL to PASS, both of the following must land:

1. **campfireagent-461 HTTP harness**: implement `TestAgentToInboxLatency_HTTP` (or equivalent) that runs N=100 against `mcp.getcampfire.dev` (or a representative HTTP-transport campfire), asserts p95 ≤ 5000ms / p99 ≤ 8000ms, and is wired into a workflow (CI nightly OR Phase 7 empirical pipeline). Verification: file exists, build tag `uxmeas` includes it, runs via the workflow, passes.
   - If not feasible to automate against `mcp.getcampfire.dev` from CI for credential/cost reasons, descoping is acceptable IF documented in the design doc and the Phase 8 Gate 2 ship-gate definition is updated to "FS-only automated; HTTP empirical only".

2. **campfireagent-f00 demo + integration test**: rewrite `cmd/cf/demos/approve-budget-b.sh` (or rename to `uxmeas-B-flow.sh` per the bead) to invoke `cf approve --accept --telemetry uxmeas <future-id>` against a real FS campfire with a real pending delegation:request message, then verify `~/.cf/uxmeas.jsonl` (or `$CF_HOME/uxmeas.jsonl`) contains a record whose `surfaced_at`/`invoked_at` match the actual message timestamps. ALTERNATIVELY: add a Go integration test (e.g., `TestApproveCommand_TelemetryEndToEnd`) that drives `cf approve` programmatically and asserts the uxmeas.jsonl side effect. Verification: demo runs `cf approve` (not Python); JSONL is produced by the binary, not by `python3 -c`.

Both conditions can be filed and worked in parallel; neither blocks the other.

---

## Items filed

- `campfireagent-8f0` (p1) — Veracity finding -b8a: Budget A HTTP transport path missing (campfireagent-461 silent spec deviation)
- `campfireagent-951` (p1) — Veracity finding -b8a: Budget B demo writes synthetic records via Python; rewrite to invoke `cf approve --telemetry uxmeas` end-to-end (campfireagent-f00 demo audit FAIL)
- `campfireagent-567` (p2) — Veracity finding -b8a: parity test replicates CLI/MCP arg-parsing logic; add lint to detect divergence from production `convention_dispatch.go` files

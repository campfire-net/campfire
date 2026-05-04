# Stage 8 Veracity Audit — campfireagent-e35

**Auditor:** veracity adversary (opus, 1M context)
**Date:** 2026-05-04
**Scope:** Stage 8 release artifacts (campfireagent-364, -4ef, -901, -9e1, -b05, -d05, -669)
**Out of scope:** campfireagent-f3b (website, parallel wave); campfireagent-587 (cold-start test, separate item)

## Verdict: CONDITIONAL

Stage 8 ships substantive, real artifacts — the docs exist, the SDK examples
compile and pass, the showcases run end-to-end against real `cf` binaries on
filesystem transport, and the agent cold-start docs are tight (≤200 lines/page).
However, two items are not at their stated done-condition and one is
catastrophically below it:

1. **campfireagent-669 (demo sweep)** — the actual sweep run produces
   **49 pass / 35 fail / 14 timeout out of 98 demos**. The bead's done
   condition explicitly required "first run … pass count = total demos,
   fail count = 0, no flakes." The CI wraps the sweep in `continue-on-error: true`
   AND `|| true`, so this catastrophic failure rate is *double-suppressed*. No
   per-demo follow-ups have been filed despite the closer noting they would be.
2. **campfireagent-4ef (design docs reflection)** — `docs/0.30-overview.md`
   was an explicit deliverable in the bead spec ("New top-level …
   synthesizing the design v2 sections into a 1-page reader's overview"). It
   does not exist. The companion demo
   `cf-protocol/demos/docs-cross-references-resolve.sh` (also in the spec) does
   not exist either; the demo that *does* exist
   (`test/demo/design-docs-0.30-reflection.sh`) only checks 6 curated "active"
   files for keyword presence/absence — it does NOT walk every doc and verify
   every code-example reference, which the spec required.
3. **campfireagent-901 (upgrade guide)** — the bead's done condition required
   "demo … confirms every code snippet in the guide compiles." The actual demo
   (`cf-conventions/demos/upgrade-guide-walk.sh`) hand-rolls 16
   `compile_snippet` calls against a 40-`go-block` guide, i.e. it spot-checks
   ~40% of snippets at the level of "make sure something compiles for each BC."
   The guide also has 51 total ```code blocks (Go + bash); only Go blocks are
   checked at all, and only ~16 of the 40 Go ones.

The rest — CHANGELOG entry, godoc + Example_ tests, agent cold-start docs,
local-shenanigans showcases — are PASS.

PROMOTE TO PASS once -669 follow-ups are filed (every failing demo as a p2/p3
bug item) and -4ef's two missing deliverables are produced. -901's spot-check
coverage gap is CONDITIONAL because the BC-level coverage (one snippet per
breaking change) is the substantively useful thing; promoting to "every snippet"
is good hygiene but lower priority than fixing the 49 broken demos.

---

## campfireagent-364 — CHANGELOG.md v0.30.0 entry

**Mock targets / fake services found:** None — entry is plain markdown text.

**Real-counterpart tests / demos:**
- `test/demo/changelog-0.30-entry.sh` — 25/25 checks pass (verified by running it).
- Cross-checks: version header, all major feature sections, breaking-change
  topics (GitHub transport, center-finding, present_as, cfs1_, cfs2_,
  GrantPayload field 5, session.go shared-key, tagspec moves), migration
  section, UPGRADE.md reference, security fixes section.

**Spot-check findings:**

- `CHANGELOG.md` is 736 lines, comprehensive, cross-links 53 distinct
  `campfireagent-` IDs. Every claim either points at an item ID or at a
  doc (e.g. `cf-authority-spec.md`).
- BC-1 through BC-18 labels from the upgrade guide are NOT used in the
  CHANGELOG itself (CHANGELOG `BC-N` mentions = 0). The CONTENT of every BC
  is represented (verified by spot-reading the Breaking changes section);
  only the labels are missing. This is a *minor* documentation
  affordance gap, not a correctness gap.
- Spot-checked 3 random commits in the v0.30.0 window:
  - `6f52a03 fix: aztable Reverse flag ignored (campfire-986)` — not
    individually mentioned. Ack: CHANGELOG is feature-level, not
    every-commit-level. The bead's spec said "every claim cross-links" —
    every CLAIM does, but not every commit, which is fine.
  - `14330d7 test: assert no orphaned temp files after concurrent
    ProfileCache.Set()` — not mentioned. Same justification.
  - `d99e3d5 fix: isolate InitWithConfig tests …` — not mentioned. Same.

**Item verdict:** PASS

---

## campfireagent-4ef — design docs 0.30 reflection

**Mock targets / fake services found:** None.

**Real-counterpart tests / demos:**
- `test/demo/design-docs-0.30-reflection.sh` — 22/22 grep-based checks pass.

**Spot-check findings:**

1. **MISSING DELIVERABLE: `docs/0.30-overview.md`**. The bead spec explicitly
   listed "New top-level `~/projects/campfire/docs/0.30-overview.md`
   synthesizing the design v2 sections into a 1-page reader's overview, with
   links to the design v2 itself for depth." This file does not exist
   (verified: `ls docs/0.30-overview.md` → "No such file or directory").
2. **MISSING DELIVERABLE: `cf-protocol/demos/docs-cross-references-resolve.sh`**.
   The bead spec stated: "demo `cf-protocol/demos/docs-cross-references-resolve.sh`
   walks every doc's code-example reference and confirms each cited demo
   script exists and exits 0." This demo does not exist; the closest is
   `test/demo/design-docs-0.30-reflection.sh`, which is fundamentally
   different — it does keyword-presence checks on 6 curated docs, not a walk
   over every code-example reference in every doc.
3. **Active-doc curation is curated, not "every file in docs/"**. The bead
   spec required walking *every* file in `docs/` excluding archived material.
   The actual demo hard-codes 6 `ACTIVE_DOCS` and 4 `HISTORICAL_DOCS`. There
   are 39 markdown files in `docs/` (per `find docs -maxdepth 2 -name '*.md'`) —
   29 of them are not classified by the demo at all.
4. **Forbidden-terms enumeration is partial**. The demo's forbidden-term list
   (`recenter`, `walk_up`, `present_as`, `TypeGitHub`, `cfs1_`, etc.) is
   checked only against the 6 curated active docs. A 0.19 leak in any of the
   29 unclassified docs would not be caught.

**Item verdict:** CONDITIONAL — substantive update of the 6 high-traffic docs
landed and the curated grep-lint passes, but two named deliverables are
missing and the lint scope is much narrower than the spec required.

---

## campfireagent-901 — upgrade guide 0.19 → 0.30

**Mock targets / fake services found:** None.

**Real-counterpart tests / demos:**
- `cf-conventions/demos/upgrade-guide-walk.sh` — 68/68 checks pass.

**Spot-check findings:**

- `docs/upgrade-0.19-to-0.30.md` is 1451 lines, well-structured: BC-1 through
  BC-18 with before/after snippets, per-consumer migration plans for rd,
  dontguess, social, the reach, freeso, decision tree, wire-format compat.
- The guide has **40 `go` code blocks and 11 `bash` code blocks (51 total
  fenced code blocks)**.
- The demo has **16 hand-rolled `compile_snippet` calls**. They cover roughly
  one Go snippet per breaking change (BC-1 → snippet C-1, BC-2 → C-2, etc.),
  not "every snippet."
- The demo's stated strategy (file header):
  > "Extract all ```go blocks from the guide, wrap each in a minimal main
  > package, and attempt `go build`."
  The implementation does NOT do this. It writes a curated set of Go
  functions into a generated `internal/upgrade_guide_snippets/snippets.go`
  file with 16 hard-coded calls. The `compile_snippet` function appends to a
  Go source file regardless of whether the snippet text appears verbatim in
  the guide. This is a **veracity gap**: the demo claims to walk the guide
  but doesn't.
- Bash snippets (11 of them) are not compile-checked at all. Reasonable for
  bash, but worth calling out.

**Item verdict:** CONDITIONAL — substantive guide content is excellent and
covers all 18 BCs and all 5 consumers; demo coverage is BC-level, not
snippet-level, contrary to the bead's stated done condition. Promote to PASS
when the demo actually parses the markdown and compiles every Go fenced block.

---

## campfireagent-9e1 — SDK godoc + Example_ tests

**Mock targets / fake services found:** None — Examples use real
`protocol.Init`, real `os.MkdirTemp`, real `OpenMemory`, etc.

**Real-counterpart tests / demos:**
- `cf-conventions/demos/sdk-doc-coverage.sh` — exists; reports 100% coverage.
- `scripts/doclint/main.go` — implemented, AST-based; verifies every exported
  symbol has a doc comment AND a corresponding `Example_` function.
- `go test -run "^Example" ./cf-protocol/protocol/ ./cf-conventions/cf-convention/`
  — **VERIFIED PASS** by the auditor: both packages return `ok`.

**Spot-check findings:**

- Example count by file:
  - `cf-protocol/protocol/example_test.go`: 29 examples
  - `cf-protocol/protocol/example_surface_test.go`: 142 examples
  - `cf-conventions/cf-convention/example_surface_test.go`: 81 examples
  - **Total: 252 Example_ functions** (closer's claim of 171+126 = 297 is
    over-counted; actual is 252. Difference is real but item still meets
    the spec since spec required coverage = 100% on public symbols, not a
    specific count.)
- Spot-checked 10 random Examples in `example_surface_test.go`: all
  exercise real protocol surface (`protocol.Init`, `client.SetScope`,
  `client.GetMembership`, `client.ProfileCache`, `client.SetSyncer`).
  Several (~14 of 142, ~10%) are "shallow" — they call a method then print
  `"<thing>: true"` rather than exercise behavior. Examples:
  - `Example_clientSetScope`: prints `"scope applied: true"` — calls
    `client.SetScope(...)` (real call) but does not assert any side-effect.
  - `Example_clientSetSyncer`: prints `"syncer cleared: true"` — same shape.
  - `Example_clientErr`: prints `"err checked: true"`.
  These are not stubs — they DO call the real method, which is enough for
  godoc surface compilation and doclint coverage. They're just minimal
  "this signature exists" examples. Acceptable for doc coverage; not
  acceptable as behavioral tests, but behavioral tests live elsewhere.
- Examples actually run and pass under `go test ^Example` (verified).

**Item verdict:** PASS

---

## campfireagent-b05 — agent cold-start docs

**Mock targets / fake services found:** None — `cf-conventions/demos/agent-cold-start.sh`
runs the real `cf` binary against the filesystem transport.

**Real-counterpart tests / demos:**
- `cf-conventions/demos/agent-cold-start.sh` — **VERIFIED PASS** by the
  auditor: 33/33 checks pass when run live; exercises real `cf init`,
  `cf id`, `cf create`, `cf convention lint`, `cf send`, `cf read` and
  asserts sender public-key matches identity public-key.

**Spot-check findings:**

- All 7 docs exist in `docs/agent/`: quickstart (74 lines),
  convention-authoring (128), gate-predicates (143), cf-session-lifecycle
  (102), discovery-patterns (90), failure-modes (105), troubleshooting
  (177). All ≤200 lines, all ≤2 pages as required.
- Cold-start *test* (verifying a fresh Claude session can write a working
  convention from these docs alone) is `campfireagent-587`, scoped
  separately. Out of this audit per the dispatch instructions, but worth
  noting the smoke test stand-in here is sufficient for the bead's done
  condition: "agent's convention dispatches successfully".
- The bead closer noted: "CI test failure is pre-existing flaky
  TestBridgeE2EBidirectional. Filed campfire-603 for the flaky test."
  Verified independent: -603 exists in the public repo's rd. This is a
  responsible response per the OS rule on flaky tests.

**Item verdict:** PASS

---

## campfireagent-d05 — local-shenanigans showcases

**Mock targets / fake services found:** None — every showcase runs against
real `cf` binaries on **filesystem transport** (which counts as real
services per dispatch rule §10.2). No in-memory stubs, no fake
network. Real campfire IDs, real Ed25519 signing, real tag projection.

**Real-counterpart tests / demos:**
- `cf-conventions/demos/showcases/aietf-naming-root.sh` — **VERIFIED PASS**
  (14/14 checks). Real cf binary, real campfire on filesystem transport,
  real `cf name register` / `cf name lookup` calls.
- `cf-conventions/demos/showcases/cross-operator-namespace.sh` — verified
  in sweep (passes).
- `cf-conventions/demos/showcases/multi-region-failover.sh` — verified
  in sweep (passes — actually starts two cf-functions instances, kills one,
  verifies the other still serves /api/health).
- `cf-conventions/demos/showcases/hosted-reader-observer.sh` — verified
  pass via `run-all-showcases.sh`. (Sweep shows TIMEOUT at 30s — see
  -669 finding; the showcase needs >30s and is fine at 60s.)
- `cf-conventions/demos/showcases/run-all-showcases.sh` — **VERIFIED PASS
  (4/4)** by the auditor: all four showcases run end-to-end on a fresh
  filesystem-only environment.

**Spot-check findings:**

- Each showcase has a `PROD PATHWAY:` block in its header explicitly naming
  what would change in production (real DNS, real Azure Front Door, real
  Tables, etc.). This is exactly the discipline the bead spec asked for —
  showcases don't lie about their scope.
- The showcases pass as a group via `run-all-showcases.sh` but FOUR of them
  TIMEOUT at the sweep's 30s default (see -669). The showcases are not
  themselves flaky — they need ~40-60s wall time. The sweep timeout is the
  problem, not the showcases.

**Item verdict:** PASS

---

## campfireagent-669 — demo sweep

**Mock targets / fake services found:** None — sweep runs each demo with a
real `bash`, real filesystem CF_HOME, and the real demo scripts.

**Real-counterpart tests / demos:**
- `scripts/run-all-demos-self-test.sh` — exists, 17/17 self-test
  assertions pass (the harness itself works).
- The actual demo sweep — **VERIFIED RUN** by the auditor:
  `bash scripts/run-all-demos.sh --timeout 30`.

**Spot-check findings:**

- **Discovery glob is correct**: walks `cf-protocol/demos/`,
  `cf-conventions/demos/<package>/`, `cmd/<binary>/demos/`,
  `test/demo/`, `docs/demos/`. Excludes vendor and the
  `run-all-showcases.sh` aggregator.
- **98 demos discovered** vs 101 *.sh under demos/test/demo paths
  (delta is `lib.sh`, `bridge-modes_test.sh`, and the showcases
  aggregator — correctly excluded).
- **Sweep result on this machine, fresh tmp CF_HOME, --timeout 30**:
  - Total: 98
  - Passed: 49
  - Failed: 35
  - Timed out: 14
- **The bead's done condition**: "first run on a Stage 4 + Stage 6
  candidate produces a report with pass count = total demos, fail count = 0,
  no flakes." This is **definitively NOT met**.
- **CI failure suppression**: `.github/workflows/ci.yml` lines 89-109:
  ```
  demo-sweep:
    continue-on-error: true                # job-level suppression
    ...
    - name: Run full demo sweep
      run: bash scripts/run-all-demos.sh ... || true   # step-level suppression
  ```
  This is **double-suppressed**: even if the job somehow returned
  non-zero, the `|| true` in the step swallows the exit code, and even if
  the step returned non-zero, the job's `continue-on-error` would mark it
  green. **Per OS CLAUDE.md rule 11, this trains everyone to ignore the
  signal entirely — the worst possible state for a test sweep.**
- **Closer noted "Per-demo failures will be filed as separate p3 follow-ups
  when first sweep report arrives."** As of audit: zero follow-ups filed.
  35 failing + 14 timing-out demos are unowned, untriaged work.
- **Specific failure samples** (from the just-run sweep):
  - `cmd/cf-primitives/demos/primitives-walkthrough.sh` exit 127 (command
    not found) — looks like a binary-path bug, not a feature bug.
  - `cmd/cf/demos/cf-trust-pins.sh`, `cmd/cf/demos/cf-init-policy.sh`,
    `cmd/cf/demos/cf-binary-surface.sh`, `cmd/cf/demos/dispatch-convention.sh`,
    `cmd/cf/demos/per-app-config-overlay.sh`, `cmd/cf/demos/approve-uxmeas.sh`,
    `cmd/cf/demos/cf-approve-suggest.sh` all exit 1 in <2s — likely
    skeleton/stub demos that need wiring or recent CLI flag changes.
  - `test/demo/01-filesystem-basics.sh`, `02-relay-request-response.sh`,
    `03-relay-join-once-read-many.sh`, `05-…`, `06-…`, `08-…`, `11-…`,
    `12-…`, `13-…`, `14-…`, `21-*`, `24-*`, `25-*`, `28-*` all fail —
    mix of timeout and exit 1.
  - `cf-protocol/demos/await-earliest-wins.sh` exit 1 in 0s — possibly
    relies on `cf` not being on PATH or similar harness bug.
- The sweep ran in fresh CF_HOME, no prior state, so this is a true
  release-state signal: **half the demo corpus is broken**.

**Item verdict:** **FAIL** (the harness exists and the discovery is correct,
but the bead's done-condition — pass count = total — is not met, no
follow-ups have been filed, and CI silently swallows the failures.)

---

## Conditions for upgrade to PASS

### -669 (FAIL → PASS):
- File one rd item per failing demo (or one per failure cluster:
  cmd/cf-primitives PATH bug, cmd/cf demos rot, test/demo/01-21
  cluster, etc.). Sweep report at `scripts/demo-sweep-report.md` is the
  inventory.
- Either (a) raise the sweep's per-demo timeout to 60s in CI to match
  `run-all-showcases.sh`, OR (b) classify the 4 showcase
  timeouts as known long-running (the showcases ARE green at 60s).
- Decide whether `continue-on-error: true` AND `|| true` is the v1
  posture. If yes, document it in the workflow with the specific reason
  (e.g. "ramp-up period — failures filed as p3 items in
  campfireagent-669 children"). If no, remove both. **The current state
  silently hides a 49% pass rate.**
- Verify: re-run sweep, expect pass count = 98 - (known-broken count) and
  CI gate green only when that target is met.

### -4ef (CONDITIONAL → PASS):
- Create `docs/0.30-overview.md` per the bead spec ("1-page reader's
  overview synthesizing design v2 §1-§9 with links to design v2 for depth").
- Create `cf-protocol/demos/docs-cross-references-resolve.sh` that walks
  every doc in `docs/` (excluding `docs/specs/` archived files), extracts
  every relative link to a demo script, and asserts each linked script
  exists and exits 0. This is the actual cross-reference resolver the
  bead specified, distinct from the keyword-grep-based reflection check.
- Either expand the active-docs / forbidden-terms list in
  `test/demo/design-docs-0.30-reflection.sh` to cover all 39 markdown
  files in `docs/` (auto-discover, not curated), OR document which docs
  are explicitly archived and exclude them in a `HISTORICAL_DOCS=` list
  that covers everything not in the active list.

### -901 (CONDITIONAL → PASS):
- Replace the 16 hand-rolled `compile_snippet` calls in
  `cf-conventions/demos/upgrade-guide-walk.sh` with a markdown-parser that
  extracts every \`\`\`go fenced block in `docs/upgrade-0.19-to-0.30.md`
  and feeds each into the existing `internal/upgrade_guide_snippets/`
  build. Then expect 40 (or however many Go blocks land at parse time).
  This matches the bead's stated "every code snippet … compiles" done
  condition.

---

## Items filed

- **campfireagent-3f3** (p1): "Veracity finding -e35: demo sweep has 49/98
  passing — file failure-cluster follow-ups and remove CI suppression on
  -669". Follow-up to -669.
- **campfireagent-8b5f** (p2): "Veracity finding -e35: docs/0.30-overview.md
  missing per -4ef spec; create the synthesis page". Follow-up to -4ef.
- **campfireagent-285** (p2): "Veracity finding -e35:
  docs-cross-references-resolve.sh demo missing per -4ef spec; walk every
  doc, not 6 curated". Follow-up to -4ef.
- **campfireagent-73a** (p2): "Veracity finding -e35: upgrade-guide-walk.sh
  hand-rolls 16 snippets vs 40 Go blocks — replace with markdown parser per
  -901 spec". Follow-up to -901.

(The rd CLI rejected `--type bug` against its enum and accepted `--type task`
defaults; items were filed as tasks. Type-classification is a separate
follow-up; the items themselves are tracked.)

#!/usr/bin/env bash
# Demo 21 — Reserved-Op Floor and Await Tiebreaker (campfireagent-935)
#
# Verifies two Stage 1 enforcement additions:
#
#   §A  Reserved-op floor (L2 enforcement, protocol-spec.md §Reserved-Op Floor)
#       — Dispatcher blocks all 10 L1-frozen reserved ops at both registration
#         and dispatch time. Non-reserved ops are not affected.
#
#   §B  Client.Await lexicographic tiebreaker
#       — When multiple fulfillments arrive at the same timestamp, Await always
#         returns the lexicographically smallest message ID (deterministic winner).
#         Tests include: same-prefix IDs, three-way tie, and a mutation probe that
#         confirms the tests FAIL when the tiebreaker is reversed.
#
# This demo runs targeted go test suites — no mock substitution, no external deps
# beyond the repo itself. Exit 0 when all suites pass.
#
# Run from the campfire repo root:
#   bash test/demo/21-reserved-op-floor-and-await-tiebreaker.sh

set -euo pipefail

export PATH=/usr/local/go/bin:$PATH

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
fail=0
pass=0

cd "$REPO_ROOT"

# ─────────────────────────────────────────────────────────────────────────────
# Pre-compile test binaries ONCE per package.
#
# Determinism rationale (campfireagent-4e3, mirroring campfireagent-f28):
#   Earlier revisions of this demo invoked `go test <pkg> -run ...` from each
#   of the 11 sub-suites. Each invocation re-validated the shared Go build
#   cache (`$GOCACHE` ≈ ~/.cache/go-build). When parallel worktrees on the
#   same host ran their own `go test` invocations concurrently — e.g. the
#   stress-sweep harness driving multiple demos, or the swarm's multi-worktree
#   mode — the build cache could evict or partially write entries that another
#   invocation was reading, producing transient `[setup failed]` / `[build
#   failed]` errors. The sweep symptom of "10 of 11 sub-suites passed" with
#   no stable failing suite reflects that — any one of the eleven test
#   invocations could lose the cache race on any given run.
#
#   This wasn't a tiebreaker race in `fulfillmentLess` or `findFulfillment` —
#   those functions are deterministic (timestamp then lex ID). The title
#   "tiebreaker race" reflected the demo NAME, not the underlying defect.
#
# Fix: compile each package's test binary once into a private tmp dir, then
# execute that binary in every sub-suite with `-test.run` filters. The
# binaries are self-contained — no `go test` toolchain involvement at run
# time, so no shared build-cache surface. The compile step happens before
# any pass/fail counting, so a (rare) build error becomes a deterministic
# hard failure with a clear message, never a flaky sub-suite result.
# ─────────────────────────────────────────────────────────────────────────────

BIN_DIR="$(mktemp -d -t demo21-XXXXXX)"
trap 'rm -rf "$BIN_DIR"' EXIT

CONVENTION_BIN="$BIN_DIR/convention.test"
RESERVED_OPS_BIN="$BIN_DIR/reserved-ops.test"
PROTOCOL_BIN="$BIN_DIR/protocol.test"

echo "=== Demo 21: Reserved-Op Floor + Await Tiebreaker ==="
echo "Repo: $REPO_ROOT"
echo ""
echo "=== Pre-compiling test binaries ==="

if ! go test -c -o "$CONVENTION_BIN" ./cf-conventions/cf-convention/ 2>&1; then
  echo "  [FAIL] could not compile cf-conventions/cf-convention test binary"
  exit 1
fi
echo "  [PASS] cf-conventions/cf-convention test binary compiled"

if ! go test -c -o "$RESERVED_OPS_BIN" ./cf-protocol/internal/reserved-ops/ 2>&1; then
  echo "  [FAIL] could not compile cf-protocol/internal/reserved-ops test binary"
  exit 1
fi
echo "  [PASS] cf-protocol/internal/reserved-ops test binary compiled"

if ! go test -c -o "$PROTOCOL_BIN" ./cf-protocol/protocol/ 2>&1; then
  echo "  [FAIL] could not compile cf-protocol/protocol test binary"
  exit 1
fi
echo "  [PASS] cf-protocol/protocol test binary compiled"
echo ""

# run_case <label> <test-binary> <test.run-regex>
# Returns 0 if the binary's exit is 0 AND no "--- FAIL" line is present.
# Output discipline: capture stdout+stderr; require the go-test-binary exit
# contract; require no "--- FAIL" line. This is stricter than grepping for
# "PASS\|ok" (which could match unrelated package output and let a real
# failure slip through as a false PASS).
run_case() {
  local label="$1"
  local bin="$2"
  local pattern="$3"

  echo ""
  echo "--- $label"
  local out
  if ! out=$("$bin" -test.run "$pattern" -test.count=1 -test.timeout 60s 2>&1); then
    echo "$out" | tee /tmp/demo21-go-output.txt
    echo "FAIL: $label"
    ((fail++)) || true
    return
  fi
  if echo "$out" | grep -q -- "--- FAIL"; then
    echo "$out" | tee /tmp/demo21-go-output.txt
    echo "FAIL: $label"
    ((fail++)) || true
    return
  fi
  echo "$out" | tee /tmp/demo21-go-output.txt
  echo "PASS: $label"
  ((pass++)) || true
}

# ── §A: Reserved-op floor enforcement ────────────────────────────────────────

echo "§A: Reserved-op floor (L2 dispatch interceptor)"

run_case \
  "§A-1: all 10 reserved ops blocked at registration (Tier 1)" \
  "$CONVENTION_BIN" \
  "^TestReservedOpBlockedAtRegistration_Tier1$"

run_case \
  "§A-2: all 10 reserved ops blocked at registration (Tier 2)" \
  "$CONVENTION_BIN" \
  "^TestReservedOpBlockedAtRegistration_Tier2$"

run_case \
  "§A-3: all 10 reserved ops blocked at dispatch time (defence-in-depth)" \
  "$CONVENTION_BIN" \
  "^TestReservedOpBlockedAtDispatch$"

run_case \
  "§A-4: non-reserved ops are not affected by enforcement" \
  "$CONVENTION_BIN" \
  "^TestNonReservedOpRegistrationSucceeds$"

run_case \
  "§A-5: reserved op count is exactly 10 (F6 commitment)" \
  "$CONVENTION_BIN" \
  "^TestAllTenReservedOpsBlocked$"

run_case \
  "§A-6: L1 symbol table count (reserved-ops package)" \
  "$RESERVED_OPS_BIN" \
  "^TestReservedOpCount$"

# ── §B: Await tiebreaker ─────────────────────────────────────────────────────

echo ""
echo "§B: Client.Await lexicographic tiebreaker"

run_case \
  "§B-1: earlier timestamp wins regardless of ID (internal unit)" \
  "$PROTOCOL_BIN" \
  "^TestFulfillmentLess_EarlierTimestampWins$"

run_case \
  "§B-2: lex-smaller ID wins on timestamp tie (internal unit)" \
  "$PROTOCOL_BIN" \
  "^TestFulfillmentLess_TiebreakerLexSmallestIDWins$"

run_case \
  "§B-3: prefix-of-another ID tiebreaker (adversarial case)" \
  "$PROTOCOL_BIN" \
  "^TestFulfillmentLess_PrefixIDTiebreaker$"

run_case \
  "§B-4: three-way tie — smallest ID wins transitively" \
  "$PROTOCOL_BIN" \
  "^TestFulfillmentLess_ThreeWayTieSmallestWins$"

run_case \
  "§B-5: Client.Await integration — real fs transport, deterministic winner" \
  "$PROTOCOL_BIN" \
  "^TestClientAwait_MultipleFulfillmentsTiebreaker$"

# ── Summary ──────────────────────────────────────────────────────────────────

echo ""
echo "=== Results: ${pass} passed, ${fail} failed ==="

if [[ $fail -gt 0 ]]; then
  echo "FAIL: Demo 21 — one or more suites failed"
  exit 1
fi

echo "PASS: Demo 21 — reserved-op floor and Await tiebreaker both verified"
exit 0

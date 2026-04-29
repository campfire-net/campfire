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

run_test_suite() {
  local label="$1"
  local pkg="$2"
  local run_filter="$3"
  shift 3

  echo ""
  echo "--- $label"
  if go test "$pkg" -run "$run_filter" -count=1 -timeout 60s "$@" 2>&1 | tee /tmp/demo21-go-output.txt; then
    echo "PASS: $label"
    ((pass++)) || true
  else
    echo "FAIL: $label"
    ((fail++)) || true
  fi
}

cd "$REPO_ROOT"

echo "=== Demo 21: Reserved-Op Floor + Await Tiebreaker ==="
echo "Repo: $REPO_ROOT"
echo ""

# ── §A: Reserved-op floor enforcement ────────────────────────────────────────

echo "§A: Reserved-op floor (L2 dispatch interceptor)"

run_test_suite \
  "§A-1: all 10 reserved ops blocked at registration (Tier 1)" \
  "./pkg/convention/..." \
  "TestReservedOpBlockedAtRegistration_Tier1"

run_test_suite \
  "§A-2: all 10 reserved ops blocked at registration (Tier 2)" \
  "./pkg/convention/..." \
  "TestReservedOpBlockedAtRegistration_Tier2"

run_test_suite \
  "§A-3: all 10 reserved ops blocked at dispatch time (defence-in-depth)" \
  "./pkg/convention/..." \
  "TestReservedOpBlockedAtDispatch"

run_test_suite \
  "§A-4: non-reserved ops are not affected by enforcement" \
  "./pkg/convention/..." \
  "TestNonReservedOpRegistrationSucceeds"

run_test_suite \
  "§A-5: reserved op count is exactly 10 (F6 commitment)" \
  "./pkg/convention/..." \
  "TestAllTenReservedOpsBlocked"

run_test_suite \
  "§A-6: L1 symbol table count (reserved-ops package)" \
  "./cf-protocol/internal/reserved-ops/..." \
  "TestReservedOpCount"

# ── §B: Await tiebreaker ─────────────────────────────────────────────────────

echo ""
echo "§B: Client.Await lexicographic tiebreaker"

run_test_suite \
  "§B-1: earlier timestamp wins regardless of ID (internal unit)" \
  "./pkg/protocol/..." \
  "TestFulfillmentLess_EarlierTimestampWins"

run_test_suite \
  "§B-2: lex-smaller ID wins on timestamp tie (internal unit)" \
  "./pkg/protocol/..." \
  "TestFulfillmentLess_TiebreakerLexSmallestIDWins"

run_test_suite \
  "§B-3: prefix-of-another ID tiebreaker (adversarial case)" \
  "./pkg/protocol/..." \
  "TestFulfillmentLess_PrefixIDTiebreaker"

run_test_suite \
  "§B-4: three-way tie — smallest ID wins transitively" \
  "./pkg/protocol/..." \
  "TestFulfillmentLess_ThreeWayTieSmallestWins"

run_test_suite \
  "§B-5: Client.Await integration — real fs transport, deterministic winner" \
  "./pkg/protocol/..." \
  "TestClientAwait_MultipleFulfillmentsTiebreaker"

# ── Summary ──────────────────────────────────────────────────────────────────

echo ""
echo "=== Results: ${pass} passed, ${fail} failed ==="

if [[ $fail -gt 0 ]]; then
  echo "FAIL: Demo 21 — one or more suites failed"
  exit 1
fi

echo "PASS: Demo 21 — reserved-op floor and Await tiebreaker both verified"
exit 0

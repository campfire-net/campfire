#!/usr/bin/env bash
# scripts/check-floor_test.sh
#
# TDD test for check-floor.sh.
# Runs two adversarial cases and one happy-path case.
# Must be run from anywhere; uses absolute paths.
#
# Exit 0 if all cases pass. Exit 1 with diagnostics if any case fails.
#
# Usage:
#   bash scripts/check-floor_test.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/check-floor.sh"

PASS_COUNT=0
FAIL_COUNT=0

check() {
  local label="$1"
  local expected_exit="$2"
  shift 2
  local actual_output
  local actual_exit=0
  actual_output=$(bash "$SCRIPT" "$@" 2>&1) || actual_exit=$?

  if [[ "$actual_exit" -eq "$expected_exit" ]]; then
    echo "PASS [$label]"
    (( PASS_COUNT++ )) || true
  else
    echo "FAIL [$label]: expected exit $expected_exit, got $actual_exit"
    echo "  Output: $actual_output"
    (( FAIL_COUNT++ )) || true
  fi
}

echo "=== check-floor_test.sh ==="
echo ""

# ── Case 1 (TDD red): floor ABOVE available → must fail ───────────────────
# This is the "failing test first" step from §10 TDD discipline.
# A floor of v99.0.0 can never be satisfied by any released version.
echo "Case 1 (adversarial): floor ABOVE available — expect failure..."
check "floor-too-high" 1 \
  --floor v99.0.0 \
  --available v0.19.3

# ── Case 2 (adversarial): floor AT available → must pass ──────────────────
echo "Case 2 (adversarial): floor exactly AT available — expect pass..."
check "floor-exact-match" 0 \
  --floor v0.19.3 \
  --available v0.19.3

# ── Case 3 (adversarial): floor BELOW available → must pass ───────────────
echo "Case 3 (adversarial): floor below available — expect pass..."
check "floor-below-available" 0 \
  --floor v0.19.0 \
  --available v0.19.3

# ── Case 4 (adversarial): floor major ABOVE available major → must fail ───
echo "Case 4 (adversarial): floor major above available major — expect failure..."
check "floor-major-above" 1 \
  --floor v2.0.0 \
  --available v1.99.99

# ── Case 5 (adversarial): floor minor ABOVE available minor → must fail ───
echo "Case 5 (adversarial): floor minor above available (same major) — expect failure..."
check "floor-minor-above" 1 \
  --floor v1.5.0 \
  --available v1.4.99

# ── Case 6 (adversarial): floor patch above available patch → must fail ───
echo "Case 6 (adversarial): floor patch above available (same major.minor) — expect failure..."
check "floor-patch-above" 1 \
  --floor v1.2.5 \
  --available v1.2.4

# ── Case 7 (adversarial): floor patch at available patch → must pass ──────
echo "Case 7 (adversarial): floor patch exactly at available patch — expect pass..."
check "floor-patch-exact" 0 \
  --floor v1.2.5 \
  --available v1.2.5

# ── Case 8 (happy path): real tree against floor.txt ─────────────────────
# This uses the actual git tags and floor.txt — the production case.
echo "Case 8 (real tree): check-floor against actual tree..."
# The current floor (v0.19.0) should be satisfied by the available v0.19.3 tag.
# If no git tags are present, this uses CHANGELOG.md fallback.
actual_exit=0
bash "$SCRIPT" 2>&1 || actual_exit=$?
if [[ "$actual_exit" -eq 0 ]]; then
  echo "PASS [real-tree]"
  (( PASS_COUNT++ )) || true
else
  echo "FAIL [real-tree]: check-floor failed on the actual tree (exit $actual_exit)"
  (( FAIL_COUNT++ )) || true
fi

echo ""
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
if [[ "$FAIL_COUNT" -gt 0 ]]; then
  exit 1
fi
exit 0

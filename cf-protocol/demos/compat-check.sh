#!/usr/bin/env bash
# cf-protocol/demos/compat-check.sh
#
# Demo: cross-module versioning policy (campfireagent-9be).
#
# Demonstrates three things:
#   1. COMPATIBILITY.md content — the three-rule versioning policy.
#   2. check-floor.sh against the current tree — exits 0 (floor is met).
#   3. A deliberate floor violation — check-floor.sh exits non-zero.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/compat-check.sh
#
# No external dependencies required.
# The script exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/check-floor.sh"

echo "================================================================"
echo "  Campfire cross-module versioning demo (campfireagent-9be)"
echo "================================================================"
echo ""

# ── Step 1: Show COMPATIBILITY.md sections ─────────────────────────────────
echo "── Step 1: cf-conventions/COMPATIBILITY.md (Three Rules) ─────────────"
echo ""
grep -A 3 "^### Rule " "$REPO_ROOT/cf-conventions/COMPATIBILITY.md" \
  | grep -v "^--$" \
  | head -30
echo ""

echo "── Step 1b: cf-protocol/COMPATIBILITY.md (F6 commitment) ─────────────"
echo ""
grep -A 5 "^## The F6 Commitment" "$REPO_ROOT/cf-protocol/COMPATIBILITY.md" \
  | head -8
echo ""

# ── Step 2: Run check-floor.sh against current tree → must exit 0 ─────────
echo "── Step 2: check-floor.sh against current tree (expect PASS) ─────────"
echo ""
if bash "$SCRIPT"; then
  echo ""
  echo "PASS: check-floor.sh exits 0 on current tree."
else
  echo ""
  echo "FAIL: check-floor.sh failed unexpectedly on current tree." >&2
  exit 1
fi
echo ""

# ── Step 3: Deliberate violation → must exit non-zero ──────────────────────
echo "── Step 3: deliberate floor violation (expect FAIL) ──────────────────"
echo ""
echo "  Simulating: floor v99.0.0 against available v0.19.3"
echo "  This proves check-floor.sh catches the 'forgotten floor bump' failure."
echo ""

exit_code=0
bash "$SCRIPT" --floor v99.0.0 --available v0.19.3 || exit_code=$?

if [[ "$exit_code" -ne 0 ]]; then
  echo ""
  echo "PASS: check-floor.sh correctly rejected the floor violation (exit $exit_code)."
else
  echo ""
  echo "FAIL: check-floor.sh should have exited non-zero for a floor violation." >&2
  exit 1
fi

echo ""
echo "================================================================"
echo "  Demo complete — all three assertions passed."
echo ""
echo "  Artifacts:"
echo "    cf-conventions/COMPATIBILITY.md  — versioning policy (three rules)"
echo "    cf-protocol/COMPATIBILITY.md     — cf-protocol F6 commitment"
echo "    cf-conventions/floor.txt         — minimum cf-protocol floor"
echo "    scripts/check-floor.sh           — floor enforcement script"
echo "    scripts/check-floor_test.sh      — TDD test suite (8 cases)"
echo "    .github/workflows/minor-compat.yml — CI job"
echo "================================================================"

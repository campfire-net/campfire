#!/usr/bin/env bash
# run-all-showcases.sh — Run all four cf 0.30 local-shenanigans showcases.
#
# Runs each showcase sequentially. Each showcase is self-contained — it creates
# its own temp state and cleans up on exit. A single failure does NOT abort the
# remaining showcases; all four are run and a summary is printed at the end.
#
# Exit codes:
#   0 — all showcases passed
#   1 — one or more showcases failed
#
# Usage:
#   bash cf-conventions/demos/showcases/run-all-showcases.sh
#
# Run from the campfire repo root.

set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
SHOWCASE_DIR="$REPO_ROOT/cf-conventions/demos/showcases"

SHOWCASES=(
  "aietf-naming-root.sh"
  "multi-region-failover.sh"
  "cross-operator-namespace.sh"
  "hosted-reader-observer.sh"
)

PASS=0
FAIL=0
SKIPPED=0

echo "=========================================================="
echo " cf 0.30 local-shenanigans showcases"
echo "=========================================================="
echo ""

for showcase in "${SHOWCASES[@]}"; do
  script="$SHOWCASE_DIR/$showcase"
  echo "----------------------------------------------------------"
  echo " Running: $showcase"
  echo "----------------------------------------------------------"
  if [[ ! -f "$script" ]]; then
    echo "  [SKIP] $showcase not found at $script"
    SKIPPED=$((SKIPPED+1))
    continue
  fi

  if bash "$script"; then
    echo ""
    echo "  [SHOWCASE PASS] $showcase"
    PASS=$((PASS+1))
  else
    rc=$?
    echo ""
    echo "  [SHOWCASE FAIL] $showcase exited with code $rc"
    FAIL=$((FAIL+1))
  fi
  echo ""
done

echo "=========================================================="
echo " Results"
echo "=========================================================="
TOTAL=$((PASS + FAIL + SKIPPED))
echo "  Passed:  $PASS / $((PASS + FAIL))"
[[ $SKIPPED -gt 0 ]] && echo "  Skipped: $SKIPPED"
echo ""

if [[ $FAIL -gt 0 ]]; then
  echo "  FAIL: $FAIL showcase(s) failed."
  exit 1
fi

if [[ $SKIPPED -gt 0 && $PASS -eq 0 ]]; then
  echo "  All showcases were skipped — nothing to verify."
  exit 1
fi

echo "  All showcases passed."
exit 0

#!/usr/bin/env bash
# cf-conventions/demos/cf-durability/durability-check.sh
#
# Demo: Campfire Durability Convention v0.1 conformance checks (campfireagent-122).
#
# Proves that cf-conventions/cf-durability passes its full test suite and
# that consumers (pkg/beacon, pkg/naming) compile and test clean after the
# pkg/durability -> cf-conventions/cf-durability refactor.
#
# Exit codes:
#   0 — all checks pass
#   1 — a test or build step failed
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/cf-durability/durability-check.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$REPO_ROOT"

PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

echo ""
echo "=== durability-check.sh ==="
echo "Repo: $REPO_ROOT"
echo ""

# ---------------------------------------------------------------------------
# §A — Package exists at new location
# ---------------------------------------------------------------------------
echo "=== §A — Package location ==="

if [[ -f "$REPO_ROOT/cf-conventions/cf-durability/durability.go" ]]; then
  ok "cf-conventions/cf-durability/durability.go exists"
else
  fail "cf-conventions/cf-durability/durability.go missing — package not moved"
fi

if [[ -f "$REPO_ROOT/cf-conventions/cf-durability/durability_test.go" ]]; then
  ok "cf-conventions/cf-durability/durability_test.go exists"
else
  fail "cf-conventions/cf-durability/durability_test.go missing"
fi

if [[ -d "$REPO_ROOT/pkg/durability" ]]; then
  fail "pkg/durability/ still exists — old location not removed"
else
  ok "pkg/durability/ removed (old location gone)"
fi

echo ""

# ---------------------------------------------------------------------------
# §B — cf-durability tests pass (25 vectors)
# ---------------------------------------------------------------------------
echo "=== §B — cf-durability test suite ==="

if go test -count=1 ./cf-conventions/cf-durability/... 2>&1; then
  ok "cf-conventions/cf-durability tests pass"
else
  fail "cf-conventions/cf-durability tests failed"
fi

echo ""

# ---------------------------------------------------------------------------
# §C — Consumer packages compile and test clean
# ---------------------------------------------------------------------------
echo "=== §C — Consumer packages ==="

if go build ./pkg/beacon/... 2>&1; then
  ok "pkg/beacon compiles"
else
  fail "pkg/beacon build failed"
fi

if go test -count=1 ./pkg/beacon/... 2>&1; then
  ok "pkg/beacon tests pass"
else
  fail "pkg/beacon tests failed"
fi

if go build ./pkg/naming/... 2>&1; then
  ok "pkg/naming compiles"
else
  fail "pkg/naming build failed"
fi

if go test -count=1 ./pkg/naming/... 2>&1; then
  ok "pkg/naming tests pass"
else
  fail "pkg/naming tests failed"
fi

echo ""

# ---------------------------------------------------------------------------
# §D — No stale pkg/durability imports remain
# ---------------------------------------------------------------------------
echo "=== §D — No stale imports ==="

stale=$(grep -rn '"github.com/campfire-net/campfire/pkg/durability"' \
  --include="*.go" . 2>/dev/null || true)

if [[ -z "$stale" ]]; then
  ok "No stale pkg/durability imports found"
else
  fail "Stale pkg/durability imports remain:"
  echo "$stale" | sed 's/^/  /'
fi

echo ""

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo "=== Summary ==="
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo ""

if [[ $FAIL -gt 0 ]]; then
  echo "FAILED: $FAIL check(s) did not pass."
  exit 1
fi

echo "=== durability-check.sh PASSED ==="

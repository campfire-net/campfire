#!/usr/bin/env bash
# cf-conventions/demos/cf-discovery/beacon-resolve.sh
#
# Demo: cf-discovery package — beacon not_after, snippet validation, rate-limit declarations.
#
# Proves:
#   1. cf-discovery package compiles (go build)
#   2. cf-discovery tests pass (go test)
#   3. pkg/beacon tests pass with not_after field (OPEN-024)
#   4. cf-conventions/cf-discovery/naming tests pass (naming moved from pkg/naming)
#   5. Depguard rules include cf-discovery in L1-narrow deny list
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/cf-discovery/beacon-resolve.sh
#
# Exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$REPO_ROOT"

echo "=== cf-discovery demo: beacon-resolve.sh ==="
echo "Repo: $REPO_ROOT"
echo ""

# ── Case 1: cf-discovery package builds ────────────────────────────────────
echo "Case 1: cf-discovery package compiles..."
go build ./cf-conventions/cf-discovery/... && echo "PASS: cf-discovery builds"
echo ""

# ── Case 2: cf-discovery tests pass ────────────────────────────────────────
echo "Case 2: cf-discovery tests pass..."
go test ./cf-conventions/cf-discovery/... -v 2>&1 | tail -20
echo ""

# ── Case 3: pkg/beacon tests pass with not_after (OPEN-024) ─────────────────
echo "Case 3: pkg/beacon tests pass (including not_after, IsExpired, NotAfterTime)..."
go test ./pkg/beacon/... -run "TestNotAfterDefault|TestIsExpired|TestDefaultBeaconNotAfterDuration" -v 2>&1
echo ""

# ── Case 4: cf-conventions/cf-discovery/naming tests pass ───────────────────
echo "Case 4: cf-conventions/cf-discovery/naming tests pass..."
go test ./cf-conventions/cf-discovery/naming/... 2>&1
echo ""

# ── Case 5: Depguard includes cf-discovery ───────────────────────────────────
echo "Case 5: .golangci.yml includes cf-discovery in L1-narrow deny list..."
if grep -q "cf-conventions/cf-discovery" "$REPO_ROOT/.golangci.yml"; then
  echo "PASS: cf-discovery in depguard config"
else
  echo "FAIL: cf-discovery not found in .golangci.yml"
  exit 1
fi
echo ""

echo "=== All cf-discovery demo cases passed ==="

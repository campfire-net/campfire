#!/usr/bin/env bash
# parity-check.sh — Phase 8 Gate 3: MCP/CLI parity conformance test runner
#
# Runs the 22 named-fixture parity cases + 4 synthetic stress cases.
# Exits 0 on green, 1 on any failure.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/parity-check.sh
#
# Requirements:
#   - Go 1.22+ on PATH
#   - Build tag: parity
#
# Spec: cf-conventions/parity/parity_test.go
# Design v2 §5.4 F3-INV
# Phase 8 Gate 3: all 22 cases pass for cf-identity, cf-authority, cf-discovery.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PARITY_PKG="github.com/campfire-net/campfire/cf-conventions/parity"

echo "=== Campfire MCP/CLI Parity Check (F3-INV) ==="
echo "Repo: $REPO_ROOT"
echo ""

cd "$REPO_ROOT"

# Phase 8 Gate 3 gate check: ensure ≥22 cases.
CASE_COUNT=$(grep -c '"[0-9][0-9]-' cf-conventions/parity/parity_test.go 2>/dev/null || echo 0)
echo "Named cases found: $CASE_COUNT (need ≥22)"
if [ "$CASE_COUNT" -lt 22 ]; then
    echo "FAIL: fewer than 22 named parity cases defined."
    exit 1
fi

echo ""
echo "Running parity tests..."
echo ""

go test -tags parity -v -count=1 "$PARITY_PKG" 2>&1 | \
    tee /dev/stderr | \
    grep -E "^(ok|FAIL|--- (PASS|FAIL)): " | \
    sort | uniq

echo ""
RC=${PIPESTATUS[0]}
if [ "$RC" -eq 0 ]; then
    echo "=== PASS: All parity cases green. Phase 8 Gate 3 satisfied. ==="
else
    echo "=== FAIL: Parity check failed. RC=$RC ==="
fi
exit "$RC"

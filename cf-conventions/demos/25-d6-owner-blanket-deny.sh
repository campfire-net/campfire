#!/usr/bin/env bash
# cf-conventions demo 25 — D6 owner-ceiling: BlanketDeny overrides grant + convention.
#
# Conformance case 13 (campfireagent-2cd8).
# WHY: D6 gap — no canonical test proved that OwnerPolicy.BlanketDeny overrides
# both a parent grant AND a convention permit. This demo runs the Go conformance
# tests for case 13 and verifies the expected Deny/owner_ceiling result.
#
# SPEC: docs/cf-authority-spec.md §3.4 DenyOwnerCeiling = "owner_ceiling"
#       docs/0.30-deal-breakers.md §D6

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PACKAGE="github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"

echo "=== Demo 25: D6 owner-ceiling BlanketDeny conformance ==="
echo ""
echo "Scenario:"
echo "  OwnerPolicy.BlanketDeny = [\"ready:*\"] (glob) and [\"ready:claim\"] (exact)"
echo "  Parent grant: convention=ready, op=claim (permits the request)"
echo "  Convention predicate: grant: ready:claim (also permits)"
echo "  Expected: Deny/owner_ceiling — owner ceiling overrides all lower layers"
echo ""
echo "Running conformance tests..."
echo ""

cd "$REPO_ROOT"
/usr/local/go/bin/go test -v -run "TestD6_OwnerCeiling" "$PACKAGE" 2>&1

echo ""
echo "=== Result ==="
# Re-run in non-verbose mode to get the pass/fail summary
if /usr/local/go/bin/go test -run "TestD6_OwnerCeiling" "$PACKAGE" 2>&1 | grep -q "^ok"; then
    echo "PASS: All D6 owner-ceiling BlanketDeny conformance cases pass."
    echo ""
    echo "Cases verified:"
    echo "  13a — BlanketDeny=[\"ready:*\"] (glob): Deny/owner_ceiling"
    echo "  13a-det — Determinism: 3 identical runs produce same result"
    echo "  13b — BlanketDeny=[\"ready:claim\"] (exact): Deny/owner_ceiling"
    echo "  13c — BlanketDeny=[\"other:op\"] (no-match): NOT DenyOwnerCeiling"
    echo ""
    echo "D6 invariant holds: owner ceiling > parent grant > convention declaration."
    exit 0
else
    echo "FAIL: D6 owner-ceiling conformance tests failed."
    exit 1
fi

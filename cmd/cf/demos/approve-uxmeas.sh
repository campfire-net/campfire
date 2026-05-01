#!/usr/bin/env bash
# approve-uxmeas.sh — Phase 8 Gate 2 Budget A demo
#
# Runs the UX measurement Budget A harness (uxmeas build tag) and reports p95.
# Design: docs/design/0.30-completion/round-2/approval-flow-ux-measurement.md §1.1
# Tracked bead: campfireagent-461
#
# Budget A — Agent→inbox latency (machine-time):
#   Pass threshold: p95 ≤ 5000 ms, p99 ≤ 8000 ms
#   Sample size: N = 100 (FS transport)
#
# Usage:
#   bash cmd/cf/demos/approve-uxmeas.sh          # from campfire repo root
#   ./approve-uxmeas.sh                          # from demos directory
#
# Output: p50/p95/p99 latency summary + PASS/FAIL verdict.
# Exit 0 on PASS, exit 1 on FAIL.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
HARNESS_PKG="./cf-conventions/cf-authority/trust/uxmeas/..."

echo "=== approve-uxmeas.sh — Budget A harness (Phase 8 Gate 2) ==="
echo ""
echo "Transport : FS (ephemeral tmpdir)"
echo "N         : 100 runs"
echo "Threshold : p95 ≤ 5000 ms, p99 ≤ 8000 ms"
echo ""

# Run the harness; capture output.
TMPOUT=$(mktemp)
trap 'rm -f "$TMPOUT"' EXIT

set +e
(cd "$REPO_ROOT" && go test -tags uxmeas -v -run TestBudgetA_AgentToInboxLatency_FS \
    -count=1 -timeout=5m "$HARNESS_PKG") 2>&1 | tee "$TMPOUT"
TEST_EXIT=$?
set -e

# Extract key metrics from test log.
P50=$(grep "p50 " "$TMPOUT" | grep -oE '[0-9]+\.[0-9]+' | head -1 || echo "?")
P95=$(grep "p95 " "$TMPOUT" | grep -oE '[0-9]+\.[0-9]+' | head -1 || echo "?")
P99=$(grep "p99 " "$TMPOUT" | grep -oE '[0-9]+\.[0-9]+' | head -1 || echo "?")
CI_HALF=$(grep "half-width" "$TMPOUT" | grep -oE 'half-width [0-9]+\.[0-9]+' | grep -oE '[0-9]+\.[0-9]+' || echo "?")

echo ""
echo "=== Budget A Results ==="
echo "  p50  = ${P50} ms"
echo "  p95  = ${P95} ms  (threshold: ≤5000 ms)"
echo "  p99  = ${P99} ms  (threshold: ≤8000 ms)"
echo "  p95 CI half-width = ${CI_HALF} ms  (acceptable: ≤500 ms)"
echo ""

if [ "$TEST_EXIT" -eq 0 ]; then
    echo "VERDICT: PASS — Budget A within latency envelope"
    echo "         Phase 8 Gate 2 FS path satisfied."
    exit 0
else
    echo "VERDICT: FAIL — Budget A exceeded latency threshold"
    echo "         Halt Phase 8. Fix transport or surface dispatch before ship."
    exit 1
fi

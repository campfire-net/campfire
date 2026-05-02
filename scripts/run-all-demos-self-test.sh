#!/usr/bin/env bash
# scripts/run-all-demos-self-test.sh — Runs the sweep on a known-good 3-demo subset
# and asserts the report shape is correct.
#
# Whitelisted demos (verified to pass standalone, no go build or relay required):
#   test/demo/changelog-0.30-entry.sh
#   test/demo/design-docs-0.30-reflection.sh
#   test/demo/20-wire-format-freeze.sh
#
# Exit code: 0 if self-test passes; nonzero otherwise.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SWEEP="$SCRIPT_DIR/run-all-demos.sh"

PASS=0
FAIL=0

assert() {
    local label="$1"
    local cond="$2"
    if eval "$cond"; then
        echo "  PASS: $label"
        PASS=$(( PASS + 1 ))
    else
        echo "  FAIL: $label"
        FAIL=$(( FAIL + 1 ))
    fi
}

echo "=== Demo Sweep Self-Test ==="
echo ""

# ---------------------------------------------------------------------------
# Phase 1: sweep exits 0 on the 3 known-good demos
# ---------------------------------------------------------------------------
echo "--- Phase 1: Sweep exits 0 on 3 known-good demos ---"
echo ""

REPORT_FILE="$(mktemp /tmp/self-test-report-XXXX.md)"
trap 'rm -f "$REPORT_FILE"' EXIT

bash "$SWEEP" \
    --report "$REPORT_FILE" \
    --timeout 30 \
    --only "test/demo/changelog-0.30-entry.sh" \
    --only "test/demo/design-docs-0.30-reflection.sh" \
    --only "test/demo/20-wire-format-freeze.sh" \
    && SWEEP_EXIT=0 || SWEEP_EXIT=$?

assert "Sweep exits 0 on 3 known-good demos" "[[ $SWEEP_EXIT -eq 0 ]]"

# ---------------------------------------------------------------------------
# Phase 2: Report shape assertions
# ---------------------------------------------------------------------------
echo ""
echo "--- Phase 2: Report shape assertions ---"
echo ""

assert "Report file exists" "[[ -f '$REPORT_FILE' ]]"
assert "Report has title header" "grep -q '# Demo Sweep Report' '$REPORT_FILE'"
assert "Report has Generated line" "grep -q '^Generated:' '$REPORT_FILE'"
assert "Report has Summary section" "grep -q '## Summary' '$REPORT_FILE'"
assert "Report has Total demos row" "grep -q 'Total demos' '$REPORT_FILE'"
assert "Report has Passed row" "grep -q '| Passed' '$REPORT_FILE'"
assert "Report shows 3 total demos" "grep -q '| Total demos | 3 |' '$REPORT_FILE'"
assert "Report shows 3 passed" "grep -q '| Passed | 3 |' '$REPORT_FILE'"
assert "Report shows 0 failed" "grep -q '| Failed | 0 |' '$REPORT_FILE'"
assert "Report shows 0 timed out" "grep -q '| Timed out | 0 |' '$REPORT_FILE'"
assert "Report has Passing Demos section" "grep -q '## Passing Demos' '$REPORT_FILE'"
assert "Report lists changelog demo" "grep -q 'changelog-0.30-entry' '$REPORT_FILE'"
assert "Report lists design-docs demo" "grep -q 'design-docs-0.30-reflection' '$REPORT_FILE'"
assert "Report lists wire-format-freeze demo" "grep -q '20-wire-format-freeze' '$REPORT_FILE'"

# ---------------------------------------------------------------------------
# Phase 3: Sweep exits nonzero on a demo that fails (synthetic — injected via --include-path)
# ---------------------------------------------------------------------------
echo ""
echo "--- Phase 3: Sweep exits nonzero on a failing demo ---"
echo ""

FAIL_DEMO="$(mktemp /tmp/self-test-always-fails-XXXX.sh)"
printf '#!/usr/bin/env bash\nexit 1\n' > "$FAIL_DEMO"
chmod +x "$FAIL_DEMO"

FAIL_REPORT="$(mktemp /tmp/self-test-fail-report-XXXX.md)"
bash "$SWEEP" \
    --report "$FAIL_REPORT" \
    --timeout 10 \
    --only "test/demo/changelog-0.30-entry.sh" \
    --include-path "$FAIL_DEMO" \
    && FAIL_SWEEP_EXIT=0 || FAIL_SWEEP_EXIT=$?

assert "Sweep exits nonzero when a demo fails" "[[ $FAIL_SWEEP_EXIT -ne 0 ]]"
assert "Fail report has Failing Demos section" "grep -q '## Failing Demos' '$FAIL_REPORT'"

rm -f "$FAIL_DEMO" "$FAIL_REPORT"

# ---------------------------------------------------------------------------
# Results
# ---------------------------------------------------------------------------
echo ""
echo "=== Self-Test Results: $PASS passed, $FAIL failed ==="

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
exit 0

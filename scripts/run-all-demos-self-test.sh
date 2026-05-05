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
LOGS_PARENT="$SCRIPT_DIR/demo-sweep-logs"

# Capture baseline of existing log dirs before this test run
LOGS_BEFORE=()
if [[ -d "$LOGS_PARENT" ]]; then
    mapfile -t LOGS_BEFORE < <(ls "$LOGS_PARENT" 2>/dev/null || true)
fi

cleanup() {
    rm -f "$REPORT_FILE"
    # Clean up any log dirs created by this self-test run
    if [[ -d "$LOGS_PARENT" ]]; then
        for d in "$LOGS_PARENT"/*/; do
            [[ -d "$d" ]] || continue
            base="$(basename "$d")"
            found=0
            for b in "${LOGS_BEFORE[@]:-}"; do
                [[ "$base" == "$b" ]] && found=1 && break
            done
            [[ $found -eq 0 ]] && rm -rf "$d"
        done
    fi
}
trap cleanup EXIT

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
# Phase 4: Skip mechanism — REQUIRES_PROD demo is skipped, not failed
# ---------------------------------------------------------------------------
echo ""
echo "--- Phase 4: Skip mechanism — REQUIRES_PROD demo is skipped ---"
echo ""

SKIP_DEMO="$(mktemp /tmp/self-test-requires-prod-XXXX.sh)"
cat > "$SKIP_DEMO" << 'SKIPDEMO'
#!/usr/bin/env bash
# REQUIRES_PROD: synthetic demo used by self-test to verify skip mechanism
exit 1
SKIPDEMO
chmod +x "$SKIP_DEMO"

SKIP_REPORT="$(mktemp /tmp/self-test-skip-report-XXXX.md)"
bash "$SWEEP" \
    --report "$SKIP_REPORT" \
    --timeout 10 \
    --only "test/demo/changelog-0.30-entry.sh" \
    --include-path "$SKIP_DEMO" \
    && SKIP_SWEEP_EXIT=0 || SKIP_SWEEP_EXIT=$?

assert "Sweep exits 0 when only failures are REQUIRES_PROD skips" "[[ $SKIP_SWEEP_EXIT -eq 0 ]]"
assert "Skip report has Skipped Demos section" "grep -q '## Skipped Demos' '$SKIP_REPORT'"
assert "Skip report shows 1 skipped" "grep -q '| Skipped | 1 |' '$SKIP_REPORT'"
assert "Skip report shows 0 failed" "grep -q '| Failed | 0 |' '$SKIP_REPORT'"

rm -f "$SKIP_DEMO" "$SKIP_REPORT"

# ---------------------------------------------------------------------------
# Phase 5: Per-demo log retention (default-on)
# ---------------------------------------------------------------------------
echo ""
echo "--- Phase 5: Per-demo log retention (default-on) ---"
echo ""

LOG_REPORT="$(mktemp /tmp/self-test-log-report-XXXX.md)"
bash "$SWEEP" \
    --report "$LOG_REPORT" \
    --timeout 30 \
    --only "test/demo/changelog-0.30-entry.sh" \
    --only "test/demo/design-docs-0.30-reflection.sh" \
    && LOG_SWEEP_EXIT=0 || LOG_SWEEP_EXIT=$?

# Find the newest log dir (written by the sweep we just ran)
NEWEST_LOG_DIR=""
if [[ -d "$LOGS_PARENT" ]]; then
    NEWEST_LOG_DIR="$(ls -td "$LOGS_PARENT"/20[0-9][0-9]-* 2>/dev/null | head -1 || true)"
fi

assert "Per-demo log dir created under scripts/demo-sweep-logs/" "[[ -n '$NEWEST_LOG_DIR' && -d '$NEWEST_LOG_DIR' ]]"
assert "Per-demo log dir name is UTC ISO timestamp" "echo '$NEWEST_LOG_DIR' | grep -qE '[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z\$'"
assert "changelog demo log file exists in log dir" "[[ -f '$NEWEST_LOG_DIR/test/demo/changelog-0.30-entry.sh.log' ]]"
assert "design-docs demo log file exists in log dir" "[[ -f '$NEWEST_LOG_DIR/test/demo/design-docs-0.30-reflection.sh.log' ]]"
assert "changelog demo log is non-empty (demo produced output)" "[[ -s '$NEWEST_LOG_DIR/test/demo/changelog-0.30-entry.sh.log' ]]"
assert "Log report file still exists (report path unaffected)" "[[ -f '$LOG_REPORT' ]]"
assert "Sweep exits 0 on 2 known-good demos with log retention" "[[ $LOG_SWEEP_EXIT -eq 0 ]]"

rm -f "$LOG_REPORT"

# ---------------------------------------------------------------------------
# Phase 6: --no-keep-logs suppresses log directory creation
# ---------------------------------------------------------------------------
echo ""
echo "--- Phase 6: --no-keep-logs suppresses log directory creation ---"
echo ""

# Record how many log dirs exist before this run
LOGS_COUNT_BEFORE=0
if [[ -d "$LOGS_PARENT" ]]; then
    LOGS_COUNT_BEFORE=$(ls "$LOGS_PARENT" 2>/dev/null | wc -l || echo 0)
fi

NOLOG_REPORT="$(mktemp /tmp/self-test-nolog-report-XXXX.md)"
bash "$SWEEP" \
    --report "$NOLOG_REPORT" \
    --timeout 30 \
    --only "test/demo/changelog-0.30-entry.sh" \
    --no-keep-logs \
    && NOLOG_SWEEP_EXIT=0 || NOLOG_SWEEP_EXIT=$?

LOGS_COUNT_AFTER=0
if [[ -d "$LOGS_PARENT" ]]; then
    LOGS_COUNT_AFTER=$(ls "$LOGS_PARENT" 2>/dev/null | wc -l || echo 0)
fi

assert "Sweep exits 0 with --no-keep-logs" "[[ $NOLOG_SWEEP_EXIT -eq 0 ]]"
assert "--no-keep-logs does not create a new log dir" "[[ $LOGS_COUNT_AFTER -eq $LOGS_COUNT_BEFORE ]]"
assert "--no-keep-logs report file still written" "[[ -f '$NOLOG_REPORT' ]]"

rm -f "$NOLOG_REPORT"

# ---------------------------------------------------------------------------
# Results
# ---------------------------------------------------------------------------
echo ""
echo "=== Self-Test Results: $PASS passed, $FAIL failed ==="

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
exit 0

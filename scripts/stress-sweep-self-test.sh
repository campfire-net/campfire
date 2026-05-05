#!/usr/bin/env bash
# scripts/stress-sweep-self-test.sh — Verifies stress-sweep.sh behaviour by
# injecting a controlled single failure in N runs and asserting the structured
# output captures it correctly.
#
# Uses the STRESS_SWEEP_CMD env var supported by stress-sweep.sh to inject
# a fake sweep command instead of running real demos.
#
# Exit code: 0 if all assertions pass; nonzero otherwise.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STRESS="$SCRIPT_DIR/stress-sweep.sh"

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

echo "=== Stress Sweep Self-Test ==="
echo ""

# ---------------------------------------------------------------------------
# Verify prerequisite scripts exist
# ---------------------------------------------------------------------------
assert "stress-sweep.sh exists" "[[ -f '$STRESS' ]]"
assert "run-all-demos.sh exists" "[[ -f '$SCRIPT_DIR/run-all-demos.sh' ]]"

# ---------------------------------------------------------------------------
# Shared temp space — cleaned up on exit
# ---------------------------------------------------------------------------
SELF_TEST_TMP="$(mktemp -d /tmp/stress-self-test-XXXX)"
trap 'rm -rf "$SELF_TEST_TMP"' EXIT

# ---------------------------------------------------------------------------
# Helper: create a fake sweep script that always exits 0
# Returns path via stdout.
# ---------------------------------------------------------------------------
make_pass_sweep() {
    local f="$SELF_TEST_TMP/sweep-always-pass-$$.sh"
    cat > "$f" << 'EOF'
#!/usr/bin/env bash
# Fake sweep: always passes.
# Accepts any run-all-demos.sh flags (--report, --timeout, etc.) and writes
# a minimal valid report so stress-sweep.sh can parse counts from stdout.
REPORT_FILE=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --report)  REPORT_FILE="$2"; shift 2 ;;
        *)         shift ;;
    esac
done
echo "=== Campfire Demo Sweep ==="
echo "Demos:   1"
echo ""
echo "  PASS (1 s): test/demo/fake-pass.sh"
echo ""
echo "=== Summary ==="
echo "Total:     1"
echo "Passed:    1"
echo "Failed:    0"
echo "Timed out: 0"
echo "Skipped:   0"
if [[ -n "$REPORT_FILE" ]]; then
    mkdir -p "$(dirname "$REPORT_FILE")"
    cat > "$REPORT_FILE" << 'REOF'
# Demo Sweep Report

Generated: 2026-01-01T00:00:00Z
Repo: `/fake`
Timeout per demo: 10s

## Summary

| Metric | Count |
|--------|-------|
| Total demos | 1 |
| Passed | 1 |
| Failed | 0 |
| Timed out | 0 |
| Skipped | 0 |

## Passing Demos

| Demo | Wall time |
|------|-----------|
| `test/demo/fake-pass.sh` | 1s |
REOF
fi
exit 0
EOF
    chmod +x "$f"
    echo "$f"
}

# ---------------------------------------------------------------------------
# Helper: create a fake sweep script with a counter file — fails on run N
# $1 = counter file path, $2 = which run number to fail (1-based)
# $3 = exit code to use for the failure
# Returns path via stdout.
# ---------------------------------------------------------------------------
make_fail_on_run_sweep() {
    local counter_file="$1"
    local fail_run="$2"
    local fail_exit="${3:-42}"
    local f="$SELF_TEST_TMP/sweep-fail-run${fail_run}-$$.sh"
    cat > "$f" << FEOF
#!/usr/bin/env bash
# Fake sweep: fails on run $fail_run with exit $fail_exit.
REPORT_FILE=""
while [[ \$# -gt 0 ]]; do
    case "\$1" in
        --report)  REPORT_FILE="\$2"; shift 2 ;;
        *)         shift ;;
    esac
done

COUNT=\$(cat "$counter_file" 2>/dev/null || echo 0)
COUNT=\$(( COUNT + 1 ))
echo "\$COUNT" > "$counter_file"

if [[ "\$COUNT" -eq $fail_run ]]; then
    # Emit a sweep output that shows 1 failure
    echo "=== Campfire Demo Sweep ==="
    echo "Demos:   1"
    echo ""
    echo "  FAIL (exit $fail_exit, 1 s): test/demo/fake-fail.sh"
    echo ""
    echo "=== Summary ==="
    echo "Total:     1"
    echo "Passed:    0"
    echo "Failed:    1"
    echo "Timed out: 0"
    echo "Skipped:   0"
    if [[ -n "\$REPORT_FILE" ]]; then
        mkdir -p "\$(dirname "\$REPORT_FILE")"
        cat > "\$REPORT_FILE" << 'REOF'
# Demo Sweep Report

Generated: 2026-01-01T00:00:00Z
Repo: \`/fake\`
Timeout per demo: 10s

## Summary

| Metric | Count |
|--------|-------|
| Total demos | 1 |
| Passed | 0 |
| Failed | 1 |
| Timed out | 0 |
| Skipped | 0 |

## Failing Demos

| Demo | Exit code | Wall time |
|------|-----------|-----------|
| \`test/demo/fake-fail.sh\` | $fail_exit | 1s |
REOF
    fi
    exit $fail_exit
else
    # Pass run
    echo "=== Campfire Demo Sweep ==="
    echo "Demos:   1"
    echo ""
    echo "  PASS (1 s): test/demo/fake-pass.sh"
    echo ""
    echo "=== Summary ==="
    echo "Total:     1"
    echo "Passed:    1"
    echo "Failed:    0"
    echo "Timed out: 0"
    echo "Skipped:   0"
    if [[ -n "\$REPORT_FILE" ]]; then
        mkdir -p "\$(dirname "\$REPORT_FILE")"
        cat > "\$REPORT_FILE" << 'REOF'
# Demo Sweep Report

Generated: 2026-01-01T00:00:00Z
Repo: \`/fake\`
Timeout per demo: 10s

## Summary

| Metric | Count |
|--------|-------|
| Total demos | 1 |
| Passed | 1 |
| Failed | 0 |
| Timed out | 0 |
| Skipped | 0 |

## Passing Demos

| Demo | Wall time |
|------|-----------|
| \`test/demo/fake-pass.sh\` | 1s |
REOF
    fi
    exit 0
fi
FEOF
    chmod +x "$f"
    echo "$f"
}

# ---------------------------------------------------------------------------
# Phase 1: all N=2 runs pass — stress-sweep exits 0
# ---------------------------------------------------------------------------
echo "--- Phase 1: All-pass scenario (N=2 runs) ---"
echo ""

PASS_SWEEP="$(make_pass_sweep)"
PHASE1_REPORT_DIR="$SELF_TEST_TMP/phase1-reports"
mkdir -p "$PHASE1_REPORT_DIR"

PHASE1_EXIT=0
STRESS_SWEEP_CMD="$PASS_SWEEP" bash "$STRESS" \
    --count 2 \
    --timeout 10 \
    --report-dir "$PHASE1_REPORT_DIR" \
    && PHASE1_EXIT=0 || PHASE1_EXIT=$?

assert "Phase 1: stress-sweep exits 0 on 2 all-pass runs" "[[ $PHASE1_EXIT -eq 0 ]]"

PHASE1_JSON="$(ls "$PHASE1_REPORT_DIR"/stress-sweep-report.*.json 2>/dev/null | head -1 || true)"
PHASE1_MD="$(ls "$PHASE1_REPORT_DIR"/stress-sweep-report.*.md 2>/dev/null | head -1 || true)"

assert "Phase 1: JSON report created" "[[ -n '$PHASE1_JSON' && -f '$PHASE1_JSON' ]]"
assert "Phase 1: MD report created" "[[ -n '$PHASE1_MD' && -f '$PHASE1_MD' ]]"

if [[ -f "$PHASE1_JSON" ]]; then
    assert "Phase 1: JSON is valid" \
        "python3 -c 'import json; json.load(open(\"$PHASE1_JSON\"))' 2>/dev/null"
    assert "Phase 1: JSON total_failures=0" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE1_JSON\")); sys.exit(0 if d[\"total_failures\"]==0 else 1)'"
    assert "Phase 1: JSON count=2" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE1_JSON\")); sys.exit(0 if d[\"count\"]==2 else 1)'"
    assert "Phase 1: JSON failures array is empty" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE1_JSON\")); sys.exit(0 if len(d[\"failures\"])==0 else 1)'"
    assert "Phase 1: JSON run_results has 2 entries" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE1_JSON\")); sys.exit(0 if len(d[\"run_results\"])==2 else 1)'"
    assert "Phase 1: JSON all run_results are PASS" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE1_JSON\")); sys.exit(0 if all(r==\"PASS\" for r in d[\"run_results\"]) else 1)'"
    assert "Phase 1: JSON has started field" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE1_JSON\")); sys.exit(0 if \"started\" in d else 1)'"
    assert "Phase 1: JSON has finished field" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE1_JSON\")); sys.exit(0 if \"finished\" in d else 1)'"
fi

if [[ -f "$PHASE1_MD" ]]; then
    assert "Phase 1: MD has title header" "grep -q '# Stress Sweep Report' '$PHASE1_MD'"
    assert "Phase 1: MD has Summary section" "grep -q '## Summary' '$PHASE1_MD'"
    assert "Phase 1: MD has Per-Run Results section" "grep -q '## Per-Run Results' '$PHASE1_MD'"
    assert "Phase 1: MD shows 0 total failures" "grep -q '| Total failures | 0 |' '$PHASE1_MD'"
    assert "Phase 1: MD shows all runs passed message" "grep -q 'All 2 runs passed' '$PHASE1_MD'"
fi

echo ""

# ---------------------------------------------------------------------------
# Phase 2: 1 failure on run 2 of N=3 — captured in structured output
# ---------------------------------------------------------------------------
echo "--- Phase 2: One-failure scenario (run 2 of N=3 fails with exit 42) ---"
echo ""

COUNTER_FILE="$SELF_TEST_TMP/phase2-counter"
echo "0" > "$COUNTER_FILE"
FAIL_SWEEP="$(make_fail_on_run_sweep "$COUNTER_FILE" 2 42)"
PHASE2_REPORT_DIR="$SELF_TEST_TMP/phase2-reports"
mkdir -p "$PHASE2_REPORT_DIR"

PHASE2_EXIT=0
STRESS_SWEEP_CMD="$FAIL_SWEEP" bash "$STRESS" \
    --count 3 \
    --timeout 10 \
    --report-dir "$PHASE2_REPORT_DIR" \
    && PHASE2_EXIT=0 || PHASE2_EXIT=$?

# stress-sweep exits with total failure count
assert "Phase 2: stress-sweep exits nonzero when a demo fails" "[[ $PHASE2_EXIT -ne 0 ]]"
assert "Phase 2: exit code equals 1 (one failing demo)" "[[ $PHASE2_EXIT -eq 1 ]]"

PHASE2_JSON="$(ls "$PHASE2_REPORT_DIR"/stress-sweep-report.*.json 2>/dev/null | head -1 || true)"
PHASE2_MD="$(ls "$PHASE2_REPORT_DIR"/stress-sweep-report.*.md 2>/dev/null | head -1 || true)"

assert "Phase 2: JSON report created" "[[ -n '$PHASE2_JSON' && -f '$PHASE2_JSON' ]]"
assert "Phase 2: MD report created" "[[ -n '$PHASE2_MD' && -f '$PHASE2_MD' ]]"

if [[ -f "$PHASE2_JSON" ]]; then
    assert "Phase 2: JSON is valid" \
        "python3 -c 'import json; json.load(open(\"$PHASE2_JSON\"))' 2>/dev/null"
    assert "Phase 2: JSON total_failures=1" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE2_JSON\")); sys.exit(0 if d[\"total_failures\"]==1 else 1)'"
    assert "Phase 2: JSON count=3" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE2_JSON\")); sys.exit(0 if d[\"count\"]==3 else 1)'"
    assert "Phase 2: JSON failures array has 1 entry" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE2_JSON\")); sys.exit(0 if len(d[\"failures\"])==1 else 1)'"
    assert "Phase 2: failure record has run=2" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE2_JSON\")); sys.exit(0 if d[\"failures\"][0][\"run\"]==2 else 1)'"
    assert "Phase 2: failure record has exit_code=42" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE2_JSON\")); sys.exit(0 if d[\"failures\"][0][\"exit_code\"]==42 else 1)'"
    assert "Phase 2: failure record has demo field (non-empty string)" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE2_JSON\")); sys.exit(0 if bool(d[\"failures\"][0][\"demo\"]) else 1)'"
    assert "Phase 2: failure record has last_30_lines field" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE2_JSON\")); sys.exit(0 if \"last_30_lines\" in d[\"failures\"][0] else 1)'"
    assert "Phase 2: run_results[1] starts with FAIL (run 2 failed)" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE2_JSON\")); sys.exit(0 if d[\"run_results\"][1].startswith(\"FAIL\") else 1)'"
    assert "Phase 2: run_results[0] is PASS (run 1 passed)" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE2_JSON\")); sys.exit(0 if d[\"run_results\"][0]==\"PASS\" else 1)'"
    assert "Phase 2: run_results[2] is PASS (run 3 passed)" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE2_JSON\")); sys.exit(0 if d[\"run_results\"][2]==\"PASS\" else 1)'"
fi

if [[ -f "$PHASE2_MD" ]]; then
    assert "Phase 2: MD has Failure Details section" "grep -q '## Failure Details' '$PHASE2_MD'"
    assert "Phase 2: MD shows total_failures=1" "grep -q '| Total failures | 1 |' '$PHASE2_MD'"
    assert "Phase 2: MD references Run 2" "grep -q 'Run 2' '$PHASE2_MD'"
fi

echo ""

# ---------------------------------------------------------------------------
# Phase 3: --count flag is respected (N=4 run_results)
# ---------------------------------------------------------------------------
echo "--- Phase 3: --count 4 produces 4 run_results entries ---"
echo ""

PASS_SWEEP2="$(make_pass_sweep)"
PHASE3_REPORT_DIR="$SELF_TEST_TMP/phase3-reports"
mkdir -p "$PHASE3_REPORT_DIR"

PHASE3_EXIT=0
STRESS_SWEEP_CMD="$PASS_SWEEP2" bash "$STRESS" \
    --count 4 \
    --timeout 10 \
    --report-dir "$PHASE3_REPORT_DIR" \
    && PHASE3_EXIT=0 || PHASE3_EXIT=$?

PHASE3_JSON="$(ls "$PHASE3_REPORT_DIR"/stress-sweep-report.*.json 2>/dev/null | head -1 || true)"

assert "Phase 3: stress-sweep exits 0 on 4-run all-pass" "[[ $PHASE3_EXIT -eq 0 ]]"
if [[ -f "$PHASE3_JSON" ]]; then
    assert "Phase 3: JSON count=4" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE3_JSON\")); sys.exit(0 if d[\"count\"]==4 else 1)'"
    assert "Phase 3: JSON run_results has 4 entries" \
        "python3 -c 'import json,sys; d=json.load(open(\"$PHASE3_JSON\")); sys.exit(0 if len(d[\"run_results\"])==4 else 1)'"
fi

echo ""

# ---------------------------------------------------------------------------
# Phase 4: Report filenames include UTC ISO timestamp
# ---------------------------------------------------------------------------
echo "--- Phase 4: Report filenames include UTC ISO timestamp ---"
echo ""

PASS_SWEEP3="$(make_pass_sweep)"
PHASE4_REPORT_DIR="$SELF_TEST_TMP/phase4-reports"
mkdir -p "$PHASE4_REPORT_DIR"

STRESS_SWEEP_CMD="$PASS_SWEEP3" bash "$STRESS" \
    --count 1 \
    --timeout 10 \
    --report-dir "$PHASE4_REPORT_DIR" \
    >/dev/null 2>&1 || true

PHASE4_JSON="$(ls "$PHASE4_REPORT_DIR"/stress-sweep-report.*.json 2>/dev/null | head -1 || true)"
PHASE4_MD="$(ls "$PHASE4_REPORT_DIR"/stress-sweep-report.*.md 2>/dev/null | head -1 || true)"

assert "Phase 4: JSON filename matches stress-sweep-report.<UTC-iso>.json" \
    "echo '$PHASE4_JSON' | grep -qE 'stress-sweep-report\.[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z\.json'"
assert "Phase 4: MD filename matches stress-sweep-report.<UTC-iso>.md" \
    "echo '$PHASE4_MD' | grep -qE 'stress-sweep-report\.[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z\.md'"

echo ""

# ---------------------------------------------------------------------------
# Results
# ---------------------------------------------------------------------------
echo "=== Self-Test Results: $PASS passed, $FAIL failed ==="

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
exit 0

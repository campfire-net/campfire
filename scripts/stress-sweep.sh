#!/usr/bin/env bash
# scripts/stress-sweep.sh — Run the full demo sweep N consecutive times,
# capture every failure with structured output (JSON + Markdown).
#
# Exit code: 0 if 0 failures across all N runs; nonzero (= failure count) otherwise.
#
# Usage:
#   bash scripts/stress-sweep.sh [--count N] [--timeout <secs>] [--report-dir <dir>]
#
# Options:
#   --count <N>          Number of consecutive sweep runs (default: 10)
#   --timeout <secs>     Per-demo timeout passed to run-all-demos.sh (default: 120)
#   --report-dir <dir>   Directory to write reports (default: scripts/)
#                        Reports are named stress-sweep-report.<UTC-iso>.{json,md}
#
# Output files:
#   scripts/stress-sweep-report.<UTC-iso>.json  — machine-readable failure list
#   scripts/stress-sweep-report.<UTC-iso>.md    — human-readable summary
#
set -euo pipefail

# ---------------------------------------------------------------------------
# Parse args
# ---------------------------------------------------------------------------
COUNT=10
DEMO_TIMEOUT=120
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPORT_DIR="$SCRIPT_DIR"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --count)      COUNT="$2"; shift 2 ;;
        --timeout)    DEMO_TIMEOUT="$2"; shift 2 ;;
        --report-dir) REPORT_DIR="$2"; shift 2 ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

# ---------------------------------------------------------------------------
# Validate args
# ---------------------------------------------------------------------------
if ! [[ "$COUNT" =~ ^[1-9][0-9]*$ ]]; then
    echo "Error: --count must be a positive integer, got: $COUNT" >&2
    exit 1
fi

# Allow the sweep command to be overridden for testing (STRESS_SWEEP_CMD env var).
# Default: scripts/run-all-demos.sh next to this script.
SWEEP="${STRESS_SWEEP_CMD:-$SCRIPT_DIR/run-all-demos.sh}"
if [[ ! -f "$SWEEP" ]]; then
    echo "Error: sweep script not found at $SWEEP" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------
START_TS="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
REPORT_BASE="$REPORT_DIR/stress-sweep-report.${START_TS}"
JSON_REPORT="${REPORT_BASE}.json"
MD_REPORT="${REPORT_BASE}.md"
mkdir -p "$REPORT_DIR"

SWEEP_TMP="$(mktemp -d /tmp/stress-sweep-XXXX)"
trap 'rm -rf "$SWEEP_TMP"' EXIT

echo "=== Campfire Stress Sweep ==="
echo "Runs:    $COUNT"
echo "Timeout: ${DEMO_TIMEOUT}s per demo"
echo "Started: $START_TS"
echo ""

# ---------------------------------------------------------------------------
# Data structures — accumulated failures
# Failure record fields: run, demo, exit_code, last_30_lines
# ---------------------------------------------------------------------------
# We collect failures as newline-separated JSON objects for simplicity,
# then assemble into a JSON array at the end.
FAILURES_JSONL="$SWEEP_TMP/failures.jsonl"
touch "$FAILURES_JSONL"

TOTAL_FAILURES=0
RUN_RESULTS=()  # "PASS" or "FAIL:N" per run

# ---------------------------------------------------------------------------
# Run N sweeps
# ---------------------------------------------------------------------------
for run in $(seq 1 "$COUNT"); do
    echo "--- Run $run / $COUNT ---"

    RUN_REPORT="$SWEEP_TMP/run-${run}-sweep-report.md"
    RUN_LOG_DIR="$SWEEP_TMP/run-${run}-logs"
    mkdir -p "$RUN_LOG_DIR"

    # Run the sweep; capture its per-demo logs in a dedicated dir.
    # We use --report to get the sweep's own markdown summary, but we
    # also need per-demo logs. We redirect run-all-demos.sh stdout so it
    # doesn't drown the stress output, but we display a compact summary.
    RUN_STDOUT="$SWEEP_TMP/run-${run}-stdout.txt"

    sweep_exit=0
    bash "$SWEEP" \
        --report "$RUN_REPORT" \
        --timeout "$DEMO_TIMEOUT" \
        >"$RUN_STDOUT" 2>&1 \
        || sweep_exit=$?

    # Extract per-run counts from stdout
    run_passed=$(grep -oP 'Passed:\s+\K[0-9]+' "$RUN_STDOUT" || echo 0)
    run_failed=$(grep -oP 'Failed:\s+\K[0-9]+' "$RUN_STDOUT" || echo 0)
    run_timeout=$(grep -oP 'Timed out:\s+\K[0-9]+' "$RUN_STDOUT" || echo 0)
    run_skipped=$(grep -oP 'Skipped:\s+\K[0-9]+' "$RUN_STDOUT" || echo 0)
    run_total_fail=$(( run_failed + run_timeout ))

    echo "  Passed: $run_passed  Failed: $run_failed  Timeout: $run_timeout  Skipped: $run_skipped"

    if [[ $run_total_fail -eq 0 ]]; then
        RUN_RESULTS+=("PASS")
        echo "  Result: PASS"
    else
        TOTAL_FAILURES=$(( TOTAL_FAILURES + run_total_fail ))
        RUN_RESULTS+=("FAIL:${run_total_fail}")
        echo "  Result: FAIL ($run_total_fail failures)"

        # Extract log dir created by run-all-demos.sh (it writes to scripts/demo-sweep-logs/<ts>/)
        # Find newest log dir written by this run (it's in SCRIPT_DIR/demo-sweep-logs/)
        SWEEP_LOG_PARENT="$SCRIPT_DIR/demo-sweep-logs"
        NEWEST_SWEEP_LOG_DIR=""
        if [[ -d "$SWEEP_LOG_PARENT" ]]; then
            NEWEST_SWEEP_LOG_DIR="$(ls -td "$SWEEP_LOG_PARENT"/20[0-9][0-9]-* 2>/dev/null | head -1 || true)"
        fi

        # Collect details for each failing demo from the markdown report
        # The report lists failing demos under "## Failing Demos" as table rows:
        #   | `demo/path` | exit_code | time |
        if [[ -f "$RUN_REPORT" ]]; then
            in_fail_section=false
            while IFS= read -r line; do
                if [[ "$line" == "## Failing Demos" ]]; then
                    in_fail_section=true
                    continue
                fi
                # Stop at next ## section
                if [[ "$line" =~ ^##\  ]] && [[ "$in_fail_section" == true ]]; then
                    in_fail_section=false
                    continue
                fi
                if [[ "$in_fail_section" == true ]] && [[ "$line" =~ ^\|[[:space:]]\` ]]; then
                    # Parse: | `demo/path` | exit_code | time |
                    # Extract text between the first pair of backticks for the demo path.
                    demo_path=$(echo "$line" | python3 -c \
                        'import sys,re; m=re.search(r"`([^`]+)`",sys.stdin.read()); print(m.group(1) if m else "")')
                    exit_code=$(echo "$line" | awk -F'|' '{print $3}' | tr -d ' ')

                    # Get last 30 lines from the per-demo log if it exists
                    last_lines=""
                    if [[ -n "$NEWEST_SWEEP_LOG_DIR" && -f "$NEWEST_SWEEP_LOG_DIR/${demo_path}.log" ]]; then
                        last_lines="$(tail -30 "$NEWEST_SWEEP_LOG_DIR/${demo_path}.log" 2>/dev/null | \
                            python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))' 2>/dev/null || echo '""')"
                    else
                        last_lines='""'
                    fi

                    # Write JSON record
                    printf '{"run":%d,"demo":"%s","exit_code":%s,"last_30_lines":%s}\n' \
                        "$run" "$demo_path" "$exit_code" "$last_lines" \
                        >> "$FAILURES_JSONL"
                fi
            done < "$RUN_REPORT"
        fi
    fi
    echo ""
done

# ---------------------------------------------------------------------------
# Timed-out demos also appear in a "## Timed-out Demos" section; collect them too
# (run them through the same failure capture path via the --report parsing above
#  was already handling exit_code=124 entries from Failing Demos section).
# run-all-demos.sh puts timed-out demos in a separate "## Timed-out Demos" section
# with exit code 124. We need to also parse that section.
# ---------------------------------------------------------------------------
# Re-scan all run reports for timed-out entries (they're in a separate section)
for run in $(seq 1 "$COUNT"); do
    RUN_REPORT="$SWEEP_TMP/run-${run}-sweep-report.md"
    [[ -f "$RUN_REPORT" ]] || continue

    SWEEP_LOG_PARENT="$SCRIPT_DIR/demo-sweep-logs"
    NEWEST_SWEEP_LOG_DIR=""
    if [[ -d "$SWEEP_LOG_PARENT" ]]; then
        NEWEST_SWEEP_LOG_DIR="$(ls -td "$SWEEP_LOG_PARENT"/20[0-9][0-9]-* 2>/dev/null | head -1 || true)"
    fi

    in_timeout_section=false
    while IFS= read -r line; do
        if [[ "$line" == "## Timed-out Demos" ]]; then
            in_timeout_section=true
            continue
        fi
        if [[ "$line" =~ ^##\  ]] && [[ "$in_timeout_section" == true ]]; then
            in_timeout_section=false
            continue
        fi
        if [[ "$in_timeout_section" == true ]] && [[ "$line" =~ ^\|[[:space:]]\` ]]; then
            demo_path=$(echo "$line" | python3 -c \
                'import sys,re; m=re.search(r"`([^`]+)`",sys.stdin.read()); print(m.group(1) if m else "")')

            # Check if already captured (timeout demos also show in failing section?
            # Actually run-all-demos.sh puts them only in Timed-out section not Failing section)
            last_lines='""'
            if [[ -n "$NEWEST_SWEEP_LOG_DIR" && -f "$NEWEST_SWEEP_LOG_DIR/${demo_path}.log" ]]; then
                last_lines="$(tail -30 "$NEWEST_SWEEP_LOG_DIR/${demo_path}.log" 2>/dev/null | \
                    python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))' 2>/dev/null || echo '""')"
            fi

            printf '{"run":%d,"demo":"%s","exit_code":124,"last_30_lines":%s}\n' \
                "$run" "$demo_path" "$last_lines" \
                >> "$FAILURES_JSONL"
        fi
    done < "$RUN_REPORT"
done

END_TS="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"

# ---------------------------------------------------------------------------
# Build JSON report
# ---------------------------------------------------------------------------
FAILURE_COUNT=$(wc -l < "$FAILURES_JSONL" | tr -d ' ')

{
    echo "{"
    echo "  \"started\": \"$START_TS\","
    echo "  \"finished\": \"$END_TS\","
    echo "  \"count\": $COUNT,"
    echo "  \"demo_timeout_seconds\": $DEMO_TIMEOUT,"
    echo "  \"total_failures\": $TOTAL_FAILURES,"
    echo "  \"failures\": ["
    if [[ -s "$FAILURES_JSONL" ]]; then
        # Emit all lines, comma-separated except the last
        total_lines=$(wc -l < "$FAILURES_JSONL" | tr -d ' ')
        line_num=0
        while IFS= read -r json_line; do
            line_num=$(( line_num + 1 ))
            if [[ $line_num -lt $total_lines ]]; then
                echo "    ${json_line},"
            else
                echo "    ${json_line}"
            fi
        done < "$FAILURES_JSONL"
    fi
    echo "  ],"
    echo "  \"run_results\": ["
    total_runs=${#RUN_RESULTS[@]}
    for i in "${!RUN_RESULTS[@]}"; do
        result="${RUN_RESULTS[$i]}"
        if [[ $(( i + 1 )) -lt $total_runs ]]; then
            echo "    \"$result\","
        else
            echo "    \"$result\""
        fi
    done
    echo "  ]"
    echo "}"
} > "$JSON_REPORT"

# ---------------------------------------------------------------------------
# Build Markdown report
# ---------------------------------------------------------------------------
{
    echo "# Stress Sweep Report"
    echo ""
    echo "Generated: $END_TS"
    echo "Started:   $START_TS"
    echo ""
    echo "## Configuration"
    echo ""
    echo "| Parameter | Value |"
    echo "|-----------|-------|"
    echo "| Runs (N) | $COUNT |"
    echo "| Per-demo timeout | ${DEMO_TIMEOUT}s |"
    echo ""
    echo "## Summary"
    echo ""
    echo "| Metric | Value |"
    echo "|--------|-------|"
    echo "| Total failures | $TOTAL_FAILURES |"
    echo ""
    echo "## Per-Run Results"
    echo ""
    echo "| Run | Result |"
    echo "|-----|--------|"
    for i in "${!RUN_RESULTS[@]}"; do
        run=$(( i + 1 ))
        result="${RUN_RESULTS[$i]}"
        echo "| $run | $result |"
    done
    echo ""

    if [[ $TOTAL_FAILURES -gt 0 && -s "$FAILURES_JSONL" ]]; then
        echo "## Failure Details"
        echo ""
        echo "Each failure includes the demo path, run number, exit code, and last 30 lines of log."
        echo ""
        while IFS= read -r json_line; do
            run_num=$(echo "$json_line" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["run"])')
            demo=$(echo "$json_line" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["demo"])')
            ec=$(echo "$json_line" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["exit_code"])')
            last30=$(echo "$json_line" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["last_30_lines"] or "(no log captured)")')

            echo "### Run $run_num — \`$demo\`"
            echo ""
            echo "- **Exit code**: $ec"
            echo ""
            echo "<details><summary>Last 30 lines of log</summary>"
            echo ""
            echo '```'
            echo "$last30"
            echo '```'
            echo ""
            echo "</details>"
            echo ""
        done < "$FAILURES_JSONL"
    fi

    if [[ $TOTAL_FAILURES -eq 0 ]]; then
        echo "All $COUNT runs passed with 0 failures."
    fi
} > "$MD_REPORT"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo "=== Stress Sweep Complete ==="
echo "Runs:           $COUNT"
echo "Total failures: $TOTAL_FAILURES"
echo ""
echo "Reports:"
echo "  JSON: $JSON_REPORT"
echo "  MD:   $MD_REPORT"

if [[ $TOTAL_FAILURES -gt 0 ]]; then
    exit "$TOTAL_FAILURES"
fi
exit 0

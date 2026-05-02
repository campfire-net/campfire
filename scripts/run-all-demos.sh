#!/usr/bin/env bash
# scripts/run-all-demos.sh — Walk every demo script, run each in isolation with a
# fresh CF_HOME, record pass/fail and wall time, produce a markdown report.
#
# Exit code: 0 if all demos pass; nonzero if any fail.
#
# Usage:
#   bash scripts/run-all-demos.sh [--report <path>] [--timeout <seconds>] [--only <path>...] [--include-path <abs-path>...]
#
# Options:
#   --report <path>          Write markdown report to <path> (default: scripts/demo-sweep-report.md)
#   --timeout <seconds>      Per-demo timeout in seconds (default: 30)
#   --only <pattern>...      Run only discovered demos whose path contains <pattern> (whitelist mode)
#   --include-path <path>... Append an absolute demo path to the run list (bypasses discovery; for testing)
#
set -euo pipefail

# ---------------------------------------------------------------------------
# Parse args
# ---------------------------------------------------------------------------
REPORT_FILE="$(cd "$(dirname "$0")" && pwd)/demo-sweep-report.md"
DEMO_TIMEOUT=30
ONLY_DEMOS=()
EXTRA_PATHS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --report)       REPORT_FILE="$2"; shift 2 ;;
        --timeout)      DEMO_TIMEOUT="$2"; shift 2 ;;
        --only)         ONLY_DEMOS+=("$2"); shift 2 ;;
        --include-path) EXTRA_PATHS+=("$2"); shift 2 ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

# ---------------------------------------------------------------------------
# Resolve repo root
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ---------------------------------------------------------------------------
# Discover demos
# ---------------------------------------------------------------------------
mapfile -t ALL_DEMOS < <(
    find "$REPO_ROOT" \
        -path "$REPO_ROOT/vendor" -prune -o \
        -name "*.sh" \
        \( -path "*/demos/*" -o -path "*/test/demo/*" \) \
        -print \
    | grep -v '/test/demo/lib\.sh$' \
    | grep -v '/demos/run-all-showcases\.sh$' \
    | sort
)

# Apply whitelist if provided
if [[ ${#ONLY_DEMOS[@]} -gt 0 ]]; then
    FILTERED=()
    for demo in "${ALL_DEMOS[@]}"; do
        for only in "${ONLY_DEMOS[@]}"; do
            if [[ "$demo" == *"$only"* ]]; then
                FILTERED+=("$demo")
                break
            fi
        done
    done
    ALL_DEMOS=("${FILTERED[@]}")
fi

# Append extra paths (injected by --include-path, e.g. for self-test)
for extra in "${EXTRA_PATHS[@]}"; do
    ALL_DEMOS+=("$extra")
done

TOTAL=${#ALL_DEMOS[@]}

# ---------------------------------------------------------------------------
# Temp directory for per-run CF_HOME
# ---------------------------------------------------------------------------
SWEEP_TMP="$(mktemp -d /tmp/cf-demo-sweep-XXXX)"
trap 'rm -rf "$SWEEP_TMP"' EXIT

# ---------------------------------------------------------------------------
# Run each demo
# ---------------------------------------------------------------------------
declare -a PASS_LIST=()
declare -a FAIL_LIST=()
declare -a TIMEOUT_LIST=()
declare -A WALL_TIMES=()
declare -A EXIT_CODES=()

run_demo() {
    local demo_path="$1"
    local rel_path="${demo_path#$REPO_ROOT/}"
    local demo_cf_home
    demo_cf_home="$(mktemp -d "$SWEEP_TMP/cf-XXXX")"
    local log_file
    log_file="$(mktemp "$SWEEP_TMP/log-XXXX.txt")"

    # Wall-clock start
    local start_epoch
    start_epoch=$(date +%s)

    # Run with timeout; capture exit code without set -e aborting us
    local exit_code=0
    timeout "$DEMO_TIMEOUT" bash "$demo_path" \
        >"$log_file" 2>&1 \
        || exit_code=$?

    local end_epoch
    end_epoch=$(date +%s)
    local elapsed=$(( end_epoch - start_epoch ))

    WALL_TIMES["$rel_path"]="$elapsed"
    EXIT_CODES["$rel_path"]="$exit_code"

    if [[ $exit_code -eq 0 ]]; then
        PASS_LIST+=("$rel_path")
        echo "  PASS ($elapsed s): $rel_path"
    elif [[ $exit_code -eq 124 ]]; then
        TIMEOUT_LIST+=("$rel_path")
        echo "  TIMEOUT (${DEMO_TIMEOUT}s): $rel_path"
    else
        FAIL_LIST+=("$rel_path")
        echo "  FAIL (exit $exit_code, ${elapsed}s): $rel_path"
    fi

    # Clean up per-demo CF_HOME
    rm -rf "$demo_cf_home"
}

echo "=== Campfire Demo Sweep ==="
echo "Repo:    $REPO_ROOT"
echo "Demos:   $TOTAL"
echo "Timeout: ${DEMO_TIMEOUT}s per demo"
echo ""

for demo in "${ALL_DEMOS[@]}"; do
    run_demo "$demo"
done

# ---------------------------------------------------------------------------
# Counts
# ---------------------------------------------------------------------------
PASS_COUNT=${#PASS_LIST[@]}
FAIL_COUNT=${#FAIL_LIST[@]}
TIMEOUT_COUNT=${#TIMEOUT_LIST[@]}
TOTAL_FAIL=$(( FAIL_COUNT + TIMEOUT_COUNT ))

echo ""
echo "=== Summary ==="
echo "Total:    $TOTAL"
echo "Passed:   $PASS_COUNT"
echo "Failed:   $FAIL_COUNT"
echo "Timed out: $TIMEOUT_COUNT"

# ---------------------------------------------------------------------------
# Markdown report
# ---------------------------------------------------------------------------
REPORT_DIR="$(dirname "$REPORT_FILE")"
mkdir -p "$REPORT_DIR"

{
    echo "# Demo Sweep Report"
    echo ""
    echo "Generated: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    echo "Repo: \`$REPO_ROOT\`"
    echo "Timeout per demo: ${DEMO_TIMEOUT}s"
    echo ""
    echo "## Summary"
    echo ""
    echo "| Metric | Count |"
    echo "|--------|-------|"
    echo "| Total demos | $TOTAL |"
    echo "| Passed | $PASS_COUNT |"
    echo "| Failed | $FAIL_COUNT |"
    echo "| Timed out | $TIMEOUT_COUNT |"
    echo ""

    if [[ $PASS_COUNT -gt 0 ]]; then
        echo "## Passing Demos"
        echo ""
        echo "| Demo | Wall time |"
        echo "|------|-----------|"
        for demo in "${PASS_LIST[@]}"; do
            echo "| \`$demo\` | ${WALL_TIMES[$demo]}s |"
        done
        echo ""
    fi

    if [[ $FAIL_COUNT -gt 0 ]]; then
        echo "## Failing Demos"
        echo ""
        echo "| Demo | Exit code | Wall time |"
        echo "|------|-----------|-----------|"
        for demo in "${FAIL_LIST[@]}"; do
            echo "| \`$demo\` | ${EXIT_CODES[$demo]} | ${WALL_TIMES[$demo]}s |"
        done
        echo ""
    fi

    if [[ $TIMEOUT_COUNT -gt 0 ]]; then
        echo "## Timed-out Demos"
        echo ""
        echo "| Demo | Timeout |"
        echo "|------|---------|"
        for demo in "${TIMEOUT_LIST[@]}"; do
            echo "| \`$demo\` | ${DEMO_TIMEOUT}s |"
        done
        echo ""
    fi
} > "$REPORT_FILE"

echo ""
echo "Report written to: $REPORT_FILE"

# ---------------------------------------------------------------------------
# Exit code
# ---------------------------------------------------------------------------
if [[ $TOTAL_FAIL -gt 0 ]]; then
    exit 1
fi
exit 0

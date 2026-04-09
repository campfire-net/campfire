#!/usr/bin/env bash
# lib.sh — Shared harness for campfire demonstration scripts.
# Source this at the top of every demo script.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[1]}")" && pwd)"
OUTPUT_DIR="$SCRIPT_DIR/output"
SCRIPT_NAME="$(basename "${BASH_SOURCE[1]}" .sh)"
OUTPUT_FILE="$OUTPUT_DIR/$SCRIPT_NAME.txt"

mkdir -p "$OUTPUT_DIR"

# Tee all output to transcript
exec > >(tee "$OUTPUT_FILE") 2>&1

# Colors for terminal (stripped from transcript by tee)
RED='\033[0;31m'
GREEN='\033[0;32m'
BOLD='\033[1m'
RESET='\033[0m'

PASS_COUNT=0
FAIL_COUNT=0

# --- Identity management ---

# Create an isolated agent identity in a temp directory.
# Returns the CF_HOME path. Caller must add to cleanup.
new_identity() {
    local name="${1:-agent}"
    local home
    home=$(mktemp -d "/tmp/cf-demo-${name}-XXXX")
    cf init --cf-home "$home" >/dev/null 2>&1
    echo "$home"
}

# Get the public key hex for an identity
pubkey_of() {
    local home="$1"
    cf id --cf-home "$home" 2>/dev/null | head -1
}

# --- Assertions ---

assert_eq() {
    local label="$1" expected="$2" actual="$3"
    if [ "$expected" = "$actual" ]; then
        echo -e "  ${GREEN}PASS${RESET}: $label"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${RESET}: $label (expected '$expected', got '$actual')"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

assert_contains() {
    local label="$1" haystack="$2" needle="$3"
    if echo "$haystack" | grep -qF "$needle"; then
        echo -e "  ${GREEN}PASS${RESET}: $label"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${RESET}: $label (output does not contain '$needle')"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

assert_not_contains() {
    local label="$1" haystack="$2" needle="$3"
    if ! echo "$haystack" | grep -qF "$needle"; then
        echo -e "  ${GREEN}PASS${RESET}: $label"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${RESET}: $label (output should not contain '$needle')"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

assert_gt() {
    local label="$1" actual="$2" threshold="$3"
    if [ "$actual" -gt "$threshold" ] 2>/dev/null; then
        echo -e "  ${GREEN}PASS${RESET}: $label ($actual > $threshold)"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${RESET}: $label ($actual is not > $threshold)"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

assert_nonzero_exit() {
    local label="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        echo -e "  ${RED}FAIL${RESET}: $label (expected non-zero exit, got 0)"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    else
        echo -e "  ${GREEN}PASS${RESET}: $label (rejected as expected)"
        PASS_COUNT=$((PASS_COUNT + 1))
    fi
}

# --- Infrastructure checks ---

require_hosted_relay() {
    if ! timeout 5 bash -c 'echo > /dev/tcp/mcp.getcampfire.dev/443' 2>/dev/null; then
        echo "SKIP: mcp.getcampfire.dev not reachable"
        exit 0
    fi
}

HOSTED_RELAY_URL="https://mcp.east.getcampfire.dev/api"

# --- Local relay management ---
# Start a local cf-mcp relay and return the URL.
# Usage: RELAY_URL=$(start_local_relay)
start_local_relay() {
    local mcp_home port mcp_bin pid
    mcp_home=$(new_identity relay)
    register_cleanup "$mcp_home"
    port=$((19000 + RANDOM % 1000))

    export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"
    if ! command -v cf-mcp >/dev/null 2>&1; then
        (cd ~/projects/campfire && go build -o /tmp/cf-mcp ./cmd/cf-mcp) >/dev/null 2>&1
        mcp_bin=/tmp/cf-mcp
    else
        mcp_bin=cf-mcp
    fi

    "$mcp_bin" --http ":${port}" --cf-home "$mcp_home" >/dev/null 2>&1 &
    pid=$!
    LOCAL_RELAY_PID=$pid

    # Wait for it to be ready
    for i in 1 2 3 4 5; do
        if curl -sf "http://127.0.0.1:${port}/health" >/dev/null 2>&1; then
            echo "http://127.0.0.1:${port}"
            return 0
        fi
        sleep 1
    done
    echo "FAIL: local relay did not start on port $port" >&2
    return 1
}

stop_local_relay() {
    if [ -n "${LOCAL_RELAY_PID:-}" ]; then
        kill "$LOCAL_RELAY_PID" 2>/dev/null || true
        wait "$LOCAL_RELAY_PID" 2>/dev/null || true
    fi
}

# Create a campfire on a LOCAL relay. The first agent creates with
# --transport p2p-http --listen, which registers it on the relay.
# Usage: CF_ID=$(create_relay_campfire --cf-home HOME RELAY_URL)
create_relay_campfire() {
    local home="$1" relay_url="$2"
    shift 2
    local raw cf_id
    # Create locally, then the relay knows about it when we join
    raw=$(cf create --cf-home "$home" --transport p2p-http --listen ":0" --json "$@" 2>/dev/null)
    cf_id=$(echo "$raw" | extract_json | python3 -c "import sys,json; print(json.load(sys.stdin).get('campfire_id',''))")
    # Now join the relay so it has the campfire
    # Actually — we need to use protocol.Client or the MCP API.
    # Simpler: just return the cf_id. The --listen agent IS the relay.
    echo "$cf_id"
}

# Extract JSON object from mixed stdout (cf commands sometimes print
# non-JSON lines before the JSON payload).
extract_json() {
    python3 -c "
import sys, json
text = sys.stdin.read()
# Find the first { and parse from there
idx = text.find('{')
if idx < 0:
    # Try array
    idx = text.find('[')
if idx >= 0:
    try:
        obj = json.loads(text[idx:])
        print(json.dumps(obj))
        sys.exit(0)
    except json.JSONDecodeError:
        pass
print('{}')
"
}

# Create a campfire and return its ID. Works for both local and relay.
# Usage: CF_ID=$(create_campfire [--cf-home HOME] [--via URL] [extra flags...])
create_campfire() {
    local raw
    raw=$(cf create --json "$@" 2>/dev/null)
    echo "$raw" | extract_json | python3 -c "import sys,json; print(json.load(sys.stdin).get('campfire_id',''))"
}

# --- Section headers ---

section() {
    echo ""
    echo -e "${BOLD}=== $1 ===${RESET}"
}

run() {
    local label="$1"
    shift
    echo "$ $*"
    "$@"
}

# --- Cleanup ---

CLEANUP_DIRS=()

register_cleanup() {
    CLEANUP_DIRS+=("$1")
}

cleanup() {
    for dir in "${CLEANUP_DIRS[@]}"; do
        rm -rf "$dir" 2>/dev/null || true
    done
}

# --- Summary ---

summary() {
    echo ""
    echo "================================"
    echo -e "Results: ${GREEN}${PASS_COUNT} passed${RESET}, ${RED}${FAIL_COUNT} failed${RESET}"
    echo "================================"
    if [ "$FAIL_COUNT" -gt 0 ]; then
        exit 1
    fi
}

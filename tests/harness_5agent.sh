#!/usr/bin/env bash
# harness_5agent.sh — 5-agent Campfire integration test harness
#
# Usage:
#   bash tests/harness_5agent.sh            # Full run: build, setup, launch, poll, verify
#   bash tests/harness_5agent.sh --dry-run  # Setup only: build, dirs, identities, templates; no launch
#
# Environment:
#   HARNESS_LAUNCH_MODE=systemd|background  # Override launch method (default: auto-detect)
#
# Exits 0 on PASS, non-zero on FAIL.

set -euo pipefail

# ─────────────────────────────────────────────────────────────────────────────
# Configuration
# ─────────────────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

INTEG_ROOT="/tmp/campfire-integ"
BIN_DIR="$INTEG_ROOT/bin"
AGENTS_DIR="$INTEG_ROOT/agents"
SHARED_DIR="$INTEG_ROOT/shared"
LOGS_DIR="$INTEG_ROOT/logs"

CF_BIN="$BIN_DIR/cf"
CF_MCP_BIN="$BIN_DIR/cf-mcp"

BEACON_DIR="$SHARED_DIR/beacons"
TRANSPORT_DIR="$SHARED_DIR/transport"
WORKSPACE="$SHARED_DIR/workspace"

TEMPLATE_DIR="tests/agent-templates"

POLL_INTERVAL=10
TIMEOUT=600

DRY_RUN=false
if [ "${1:-}" = "--dry-run" ]; then
    DRY_RUN=true
fi

# ─────────────────────────────────────────────────────────────────────────────
# Helpers
# ─────────────────────────────────────────────────────────────────────────────
log() { echo "[harness] $*"; }
fail() { echo "[harness] FAIL: $*" >&2; exit 1; }

# ─────────────────────────────────────────────────────────────────────────────
# Step 0: Prerequisite checks
# ─────────────────────────────────────────────────────────────────────────────
log "Checking prerequisites..."

command -v python3 >/dev/null 2>&1 || fail "python3 not found on PATH"
if [ -z "${SKIP_BUILD:-}" ]; then
    command -v go >/dev/null 2>&1 || fail "go not found on PATH (set SKIP_BUILD=1 and pre-build binaries)"
fi

# Determine launch mode
if [ -n "${HARNESS_LAUNCH_MODE:-}" ]; then
    LAUNCH_MODE="$HARNESS_LAUNCH_MODE"
    log "Using HARNESS_LAUNCH_MODE=$LAUNCH_MODE (from env)"
else
    if command -v systemd-run >/dev/null 2>&1 && systemd-run --user --scope true >/dev/null 2>&1; then
        LAUNCH_MODE="systemd"
    else
        LAUNCH_MODE="background"
        log "systemd-run not available (WSL2?); falling back to background launch mode"
    fi
fi

if [ "$LAUNCH_MODE" != "systemd" ] && [ "$LAUNCH_MODE" != "background" ]; then
    fail "HARNESS_LAUNCH_MODE must be 'systemd' or 'background', got '$LAUNCH_MODE'"
fi

log "Launch mode: $LAUNCH_MODE"

if $DRY_RUN; then
    log "Dry-run: launch method is '$LAUNCH_MODE' — no agents will be launched"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Step 1: Build binaries (build to project root first, move after dirs created)
# ─────────────────────────────────────────────────────────────────────────────
if [ -n "${SKIP_BUILD:-}" ]; then
    log "SKIP_BUILD set — expecting pre-built binaries at $PROJECT_ROOT/cf-bin-tmp and cf-mcp-bin-tmp"
    [ -f "$PROJECT_ROOT/cf-bin-tmp" ] || fail "cf-bin-tmp not found — build with: docker compose run --rm go build -o /src/cf-bin-tmp ./cmd/cf"
    [ -f "$PROJECT_ROOT/cf-mcp-bin-tmp" ] || fail "cf-mcp-bin-tmp not found — build with: docker compose run --rm go build -o /src/cf-mcp-bin-tmp ./cmd/cf-mcp"
else
    log "Building cf and cf-mcp binaries..."
    go build -o /src/cf-bin-tmp ./cmd/cf         || fail "Failed to build cf"
    go build -o /src/cf-mcp-bin-tmp ./cmd/cf-mcp || fail "Failed to build cf-mcp"
fi
log "Binaries ready."

# ─────────────────────────────────────────────────────────────────────────────
# Step 2: Create directory structure
# ─────────────────────────────────────────────────────────────────────────────
log "Creating directory structure under $INTEG_ROOT..."

# Clean from previous runs
rm -rf "$INTEG_ROOT"
mkdir -p \
    "$BIN_DIR" \
    "$BEACON_DIR" \
    "$TRANSPORT_DIR" \
    "$WORKSPACE" \
    "$AGENTS_DIR/agent-a" \
    "$AGENTS_DIR/agent-b" \
    "$AGENTS_DIR/agent-c" \
    "$AGENTS_DIR/agent-d" \
    "$AGENTS_DIR/agent-e" \
    "$LOGS_DIR"

log "Directories created."

# Move compiled binaries into BIN_DIR now that it exists
mv ./cf-bin-tmp "$CF_BIN"       || fail "Failed to move cf binary to $CF_BIN"
mv ./cf-mcp-bin-tmp "$CF_MCP_BIN" || fail "Failed to move cf-mcp binary to $CF_MCP_BIN"
log "Binaries installed: $CF_BIN, $CF_MCP_BIN"

# Make cf available as 'cf' on PATH (for verify_5agent.sh and this script)
export PATH="$BIN_DIR:$PATH"

# Set shared environment variables for all cf calls
export CF_BEACON_DIR="$BEACON_DIR"
export CF_TRANSPORT_DIR="$TRANSPORT_DIR"

# ─────────────────────────────────────────────────────────────────────────────
# Step 3: Initialize agent identities
# ─────────────────────────────────────────────────────────────────────────────
log "Initializing agent identities..."

for agent in a b c d e; do
    AGENT_HOME="$AGENTS_DIR/agent-$agent"
    CF_HOME="$AGENT_HOME" "$CF_BIN" init > /dev/null
    log "  agent-$agent: initialized (identity.json + store.db)"
done

# ─────────────────────────────────────────────────────────────────────────────
# Step 4: Read public keys
# ─────────────────────────────────────────────────────────────────────────────
log "Reading public keys..."

get_key() {
    local agent="$1"
    CF_HOME="$AGENTS_DIR/agent-$agent" "$CF_BIN" id --json \
        | python3 -c "import json,sys; print(json.load(sys.stdin)['public_key'])"
}

KEY_A=$(get_key a)
KEY_B=$(get_key b)
KEY_C=$(get_key c)
KEY_D=$(get_key d)
KEY_E=$(get_key e)

log "  KEY_A=${KEY_A:0:16}..."
log "  KEY_B=${KEY_B:0:16}..."
log "  KEY_C=${KEY_C:0:16}..."
log "  KEY_D=${KEY_D:0:16}..."
log "  KEY_E=${KEY_E:0:16}..."

# ─────────────────────────────────────────────────────────────────────────────
# Step 5: Inject templates → CLAUDE.md for each agent
# ─────────────────────────────────────────────────────────────────────────────
log "Injecting templates..."

inject_template() {
    local agent="$1"        # e.g. "a"
    local agent_upper       # e.g. "A"
    agent_upper=$(echo "$agent" | tr '[:lower:]' '[:upper:]')

    # Get this agent's key via variable indirection
    local key_varname="KEY_$agent_upper"
    local agent_key="${!key_varname}"

    sed \
        -e "s|{{KEY_A}}|$KEY_A|g" \
        -e "s|{{KEY_B}}|$KEY_B|g" \
        -e "s|{{KEY_C}}|$KEY_C|g" \
        -e "s|{{KEY_D}}|$KEY_D|g" \
        -e "s|{{KEY_E}}|$KEY_E|g" \
        -e "s|{{WORKSPACE}}|$WORKSPACE|g" \
        "$TEMPLATE_DIR/agent-$agent.md" \
        > "$AGENTS_DIR/agent-$agent/CLAUDE.md"

    log "  agent-$agent/CLAUDE.md written"
}

for agent in a b c d e; do
    inject_template "$agent"
done

# Verify no unreplaced placeholders
for agent in a b c d e; do
    if grep -q '{{' "$AGENTS_DIR/agent-$agent/CLAUDE.md"; then
        fail "Unreplaced placeholders in agent-$agent/CLAUDE.md"
    fi
done

log "All templates injected, no unreplaced placeholders."

# ─────────────────────────────────────────────────────────────────────────────
# Step 6: Write MCP configs for agents B and E
# ─────────────────────────────────────────────────────────────────────────────
log "Writing MCP configs for agents B and E..."

write_mcp_config() {
    local agent="$1"
    sed \
        -e "s|{{AGENT_HOME}}|$AGENTS_DIR/agent-$agent|g" \
        "$TEMPLATE_DIR/mcp-config.json" \
        > "$AGENTS_DIR/agent-$agent/mcp-config.json"
    log "  agent-$agent/mcp-config.json written"
}

write_mcp_config b
write_mcp_config e

# ─────────────────────────────────────────────────────────────────────────────
# Dry-run exit point
# ─────────────────────────────────────────────────────────────────────────────
if $DRY_RUN; then
    log ""
    log "Dry-run complete. Verifying setup artifacts..."

    # Check identity files
    for agent in a b c d e; do
        [ -f "$AGENTS_DIR/agent-$agent/identity.json" ] \
            || fail "Missing identity.json for agent-$agent"
    done
    log "  All 5 identity.json files present."

    # Check CLAUDE.md files (no placeholders already verified above)
    for agent in a b c d e; do
        [ -f "$AGENTS_DIR/agent-$agent/CLAUDE.md" ] \
            || fail "Missing CLAUDE.md for agent-$agent"
    done
    log "  All 5 CLAUDE.md files present."

    # Check MCP configs
    [ -f "$AGENTS_DIR/agent-b/mcp-config.json" ] || fail "Missing mcp-config.json for agent-b"
    [ -f "$AGENTS_DIR/agent-e/mcp-config.json" ] || fail "Missing mcp-config.json for agent-e"
    log "  MCP configs for B and E present."

    log ""
    log "PASS: dry-run setup successful (launch mode: $LAUNCH_MODE)"
    exit 0
fi

# ─────────────────────────────────────────────────────────────────────────────
# Step 7: Launch agents
# ─────────────────────────────────────────────────────────────────────────────

# PID tracking (only used in background mode)
declare -A AGENT_PIDS

launch_agent_systemd() {
    local agent="$1"
    local agent_home="$AGENTS_DIR/agent-$agent"
    local log_file="$LOGS_DIR/agent-$agent.log"
    local unit_name="campfire-integ-agent-$agent"

    local extra_args=()
    if [ "$agent" = "b" ] || [ "$agent" = "e" ]; then
        extra_args+=(--mcp-config "$agent_home/mcp-config.json")
    fi

    systemd-run --user \
        --unit="$unit_name" \
        --collect \
        --working-directory="$WORKSPACE" \
        --setenv=CF_HOME="$agent_home" \
        --setenv=CF_BEACON_DIR="$BEACON_DIR" \
        --setenv=CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
        --setenv=PATH="$BIN_DIR:$PATH" \
        -- \
        bash -c "cat '$agent_home/CLAUDE.md' | claude -p --dangerously-skip-permissions --output-format text ${extra_args[*]:-} > '$log_file' 2>&1" \
        >/dev/null 2>&1 \
        || fail "systemd-run failed for agent-$agent"

    log "  agent-$agent launched via systemd (unit: $unit_name, log: $log_file)"
}

launch_agent_background() {
    local agent="$1"
    local agent_home="$AGENTS_DIR/agent-$agent"
    local log_file="$LOGS_DIR/agent-$agent.log"

    local extra_args=""
    if [ "$agent" = "b" ] || [ "$agent" = "e" ]; then
        extra_args="--mcp-config $agent_home/mcp-config.json"
    fi

    CF_HOME="$agent_home" \
    CF_BEACON_DIR="$BEACON_DIR" \
    CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
    PATH="$BIN_DIR:$PATH" \
    bash -c "cat '$agent_home/CLAUDE.md' | claude -p --dangerously-skip-permissions --output-format text $extra_args" \
        >"$log_file" 2>&1 &

    local pid=$!
    disown "$pid"
    AGENT_PIDS["$agent"]="$pid"
    log "  agent-$agent launched in background (PID: $pid, log: $log_file)"
}

launch_agent() {
    local agent="$1"
    if [ "$LAUNCH_MODE" = "systemd" ]; then
        launch_agent_systemd "$agent"
    else
        launch_agent_background "$agent"
    fi
}

# Agent A gets a 5-second head start (creates the campfire before others discover)
log "Launching Agent A (PM) with 5-second head start..."
launch_agent a

log "Waiting 5 seconds for Agent A to create the project campfire..."
sleep 5

log "Launching agents B (Implementer/MCP), C (Implementer), D (Reviewer), E (QA/MCP)..."
launch_agent b
launch_agent c
launch_agent d
launch_agent e

log "All 5 agents launched."

# ─────────────────────────────────────────────────────────────────────────────
# Step 8: Collect systemd unit PIDs (for liveness checking)
# ─────────────────────────────────────────────────────────────────────────────
get_agent_pid() {
    local agent="$1"
    if [ "$LAUNCH_MODE" = "systemd" ]; then
        local unit_name="campfire-integ-agent-$agent"
        # systemctl returns MainPID in show output
        systemctl --user show "$unit_name" --property=MainPID --value 2>/dev/null || echo "0"
    else
        echo "${AGENT_PIDS[$agent]:-0}"
    fi
}

agent_is_alive() {
    local agent="$1"
    local pid
    pid=$(get_agent_pid "$agent")
    if [ -z "$pid" ] || [ "$pid" = "0" ]; then
        return 1
    fi
    kill -0 "$pid" 2>/dev/null
}

# ─────────────────────────────────────────────────────────────────────────────
# Step 9: Poll for completion
# ─────────────────────────────────────────────────────────────────────────────
log "Polling for completion (timeout: ${TIMEOUT}s, interval: ${POLL_INTERVAL}s)..."
log "  Watching: $WORKSPACE/DONE"

elapsed=0
while [ "$elapsed" -lt "$TIMEOUT" ]; do
    if [ -f "$WORKSPACE/DONE" ]; then
        log "DONE file detected after ${elapsed}s."
        break
    fi

    # Liveness check: if all 5 agents have exited and DONE doesn't exist, fail early
    all_dead=true
    for agent in a b c d e; do
        if agent_is_alive "$agent"; then
            all_dead=false
            break
        fi
    done

    if $all_dead; then
        log "All 5 agents have exited but DONE file not found — failing early."
        # Fall through to failure handling below
        elapsed=$TIMEOUT
        break
    fi

    sleep "$POLL_INTERVAL"
    elapsed=$((elapsed + POLL_INTERVAL))
done

# ─────────────────────────────────────────────────────────────────────────────
# Failure / timeout handling
# ─────────────────────────────────────────────────────────────────────────────
if [ ! -f "$WORKSPACE/DONE" ]; then
    log "FAIL: DONE file not found after ${elapsed}s — killing agents and collecting logs."

    # Kill remaining agents
    if [ "$LAUNCH_MODE" = "systemd" ]; then
        for agent in a b c d e; do
            unit_name="campfire-integ-agent-$agent"
            systemctl --user stop "$unit_name" 2>/dev/null || true
        done
    else
        for agent in a b c d e; do
            pid="${AGENT_PIDS[$agent]:-0}"
            [ "$pid" != "0" ] && kill "$pid" 2>/dev/null || true
        done
    fi

    echo ""
    echo "=== Last 50 lines of each agent log ==="
    for agent in a b c d e; do
        log_file="$LOGS_DIR/agent-$agent.log"
        if [ -f "$log_file" ]; then
            echo "--- agent-$agent.log ---"
            tail -50 "$log_file" || true
        else
            echo "--- agent-$agent.log: (not found) ---"
        fi
    done

    exit 1
fi

# ─────────────────────────────────────────────────────────────────────────────
# Step 10: Run verification
# ─────────────────────────────────────────────────────────────────────────────
log ""
log "Running verification checks..."
bash tests/verify_5agent.sh "$INTEG_ROOT"
VERIFY_EXIT=$?

echo ""
if [ "$VERIFY_EXIT" -eq 0 ]; then
    log "PASS: all verification checks passed."
else
    log "FAIL: verification failed (exit code $VERIFY_EXIT)."
    echo ""
    echo "=== Last 50 lines of each agent log ==="
    for agent in a b c d e; do
        log_file="$LOGS_DIR/agent-$agent.log"
        if [ -f "$log_file" ]; then
            echo "--- agent-$agent.log ---"
            tail -50 "$log_file" || true
        fi
    done
fi

exit "$VERIFY_EXIT"

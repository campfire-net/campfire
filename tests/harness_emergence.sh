#!/usr/bin/env bash
# harness_emergence.sh — 20-agent Moltbook emergence test harness
#
# Usage:
#   bash tests/harness_emergence.sh                # Full run: build, setup, launch rounds, collect, verify
#   bash tests/harness_emergence.sh --dry-run      # Setup only: build, dirs, identities, templates; no launch
#   bash tests/harness_emergence.sh --rounds N     # Override number of rounds (default: 10)
#
# Environment:
#   SKIP_BUILD=1                      # Skip build, expect pre-built binaries
#   HARNESS_LAUNCH_MODE=systemd|background  # Override launch method (default: auto-detect)
#
# Design: Time-division multiplexing across 4 concurrent sessions.
#   - 4 batches of 5 agents each run concurrently
#   - Each batch processes its 5 agents in round-robin (one agent per "turn")
#   - Each turn: fresh claude -p call with agent CLAUDE.md + turn context piped via stdin
#   - State persists in each agent's CF_HOME between turns
#   - Runs 8-10 rounds (default 10)
#
# Exits 0 on PASS, non-zero on FAIL.

set -euo pipefail

# ─────────────────────────────────────────────────────────────────────────────
# Configuration
# ─────────────────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

INTEG_ROOT="/tmp/campfire-emergence"
BIN_DIR="$INTEG_ROOT/bin"
AGENTS_DIR="$INTEG_ROOT/agents"
SHARED_DIR="$INTEG_ROOT/shared"
LOGS_DIR="$INTEG_ROOT/logs"

CF_BIN="$BIN_DIR/cf"
CF_MCP_BIN="$BIN_DIR/cf-mcp"

BEACON_DIR="$SHARED_DIR/beacons"
TRANSPORT_DIR="$SHARED_DIR/transport"
WORKSPACE="$SHARED_DIR/workspace"

TEMPLATE_DIR="$SCRIPT_DIR/agent-templates"

POLL_INTERVAL=30
TIMEOUT=5400   # 90 minutes

DRY_RUN=false
CONTINUE=false
ROUNDS=10

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "${1:-}" in
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --rounds)
            ROUNDS="${2:?--rounds requires a value}"
            shift 2
            ;;
        --continue)
            CONTINUE=true
            shift
            ;;
        *)
            echo "[harness] Unknown argument: $1" >&2
            exit 1
            ;;
    esac
done

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
command -v claude >/dev/null 2>&1 || fail "claude not found on PATH"

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
log "Rounds: $ROUNDS"

if $CONTINUE; then
    log "CONTINUE mode — skipping setup, resuming rounds with existing state in $INTEG_ROOT"
    [ -d "$INTEG_ROOT" ] || fail "Cannot continue: $INTEG_ROOT does not exist"
    [ -f "$SHARED_DIR/lobby-id.txt" ] || fail "Cannot continue: no lobby-id.txt"
fi

if $DRY_RUN; then
    log "Dry-run: launch method is '$LAUNCH_MODE' — no agents will be launched"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Step 1: Build binaries
# ─────────────────────────────────────────────────────────────────────────────
if [ -n "${SKIP_BUILD:-}" ]; then
    log "SKIP_BUILD set — expecting pre-built binaries at $PROJECT_ROOT/cf-bin-tmp and cf-mcp-bin-tmp"
    [ -f "$PROJECT_ROOT/cf-bin-tmp" ] || fail "cf-bin-tmp not found — build with: go build -o cf-bin-tmp ./cmd/cf"
    [ -f "$PROJECT_ROOT/cf-mcp-bin-tmp" ] || fail "cf-mcp-bin-tmp not found — build with: go build -o cf-mcp-bin-tmp ./cmd/cf-mcp"
else
    log "Building cf and cf-mcp binaries..."
    cd "$PROJECT_ROOT"
    go build -o cf-bin-tmp ./cmd/cf         || fail "Failed to build cf"
    go build -o cf-mcp-bin-tmp ./cmd/cf-mcp || fail "Failed to build cf-mcp"
    cd - >/dev/null
fi
log "Binaries ready."

if ! $CONTINUE; then
# ─────────────────────────────────────────────────────────────────────────────
# Step 2: Create directory structure (skipped in --continue mode)
# ─────────────────────────────────────────────────────────────────────────────
log "Creating directory structure under $INTEG_ROOT..."

# Clean from previous runs
rm -rf "$INTEG_ROOT"
mkdir -p \
    "$BIN_DIR" \
    "$BEACON_DIR" \
    "$TRANSPORT_DIR" \
    "$WORKSPACE" \
    "$LOGS_DIR"

# Create agent dirs
for i in $(seq -w 1 20); do
    mkdir -p "$AGENTS_DIR/agent-$i"
    mkdir -p "$WORKSPACE/agent-$i"
done

log "Directories created."

# Move compiled binaries into BIN_DIR now that it exists
cp "$PROJECT_ROOT/cf-bin-tmp" "$CF_BIN"         || fail "Failed to move cf binary to $CF_BIN"
cp "$PROJECT_ROOT/cf-mcp-bin-tmp" "$CF_MCP_BIN" || fail "Failed to move cf-mcp binary to $CF_MCP_BIN"
log "Binaries installed: $CF_BIN, $CF_MCP_BIN"

# Make cf available on PATH
export PATH="$BIN_DIR:$PATH"

# Set shared environment variables for all cf calls
export CF_BEACON_DIR="$BEACON_DIR"
export CF_TRANSPORT_DIR="$TRANSPORT_DIR"

# ─────────────────────────────────────────────────────────────────────────────
# Step 3: Initialize agent identities
# ─────────────────────────────────────────────────────────────────────────────
log "Initializing agent identities..."

for i in $(seq -w 1 20); do
    AGENT_HOME="$AGENTS_DIR/agent-$i"
    CF_HOME="$AGENT_HOME" "$CF_BIN" init > /dev/null
    log "  agent-$i: initialized (identity.json + store.db)"
done

# ─────────────────────────────────────────────────────────────────────────────
# Step 4: Read public keys
# ─────────────────────────────────────────────────────────────────────────────
log "Reading public keys..."

declare -A AGENT_KEYS

for i in $(seq -w 1 20); do
    AGENT_HOME="$AGENTS_DIR/agent-$i"
    key=$(CF_HOME="$AGENT_HOME" "$CF_BIN" id --json \
        | python3 -c "import json,sys; print(json.load(sys.stdin)['public_key'])")
    AGENT_KEYS["$i"]="$key"
    log "  agent-$i: ${key:0:16}..."
done

# ─────────────────────────────────────────────────────────────────────────────
# Step 5: Create lobby campfire
# ─────────────────────────────────────────────────────────────────────────────
log "Creating lobby campfire..."

LOBBY_INIT_HOME="/tmp/campfire-lobby-init"
rm -rf "$LOBBY_INIT_HOME"
mkdir -p "$LOBBY_INIT_HOME"

CF_HOME="$LOBBY_INIT_HOME" \
CF_BEACON_DIR="$BEACON_DIR" \
CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
"$CF_BIN" init > /dev/null

LOBBY_ID=$(CF_HOME="$LOBBY_INIT_HOME" \
    CF_BEACON_DIR="$BEACON_DIR" \
    CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
    "$CF_BIN" create \
        --protocol open \
        --description "Company coordination lobby — a shared space for agents across all departments. Post what you're working on, what you need from other departments, or what you can provide. Browse messages to find collaborators." \
        --json \
    | python3 -c "import json,sys; print(json.load(sys.stdin)['campfire_id'])")

[ -n "$LOBBY_ID" ] || fail "Failed to create lobby campfire — got empty ID"

echo "$LOBBY_ID" > "$SHARED_DIR/lobby-id.txt"
log "  Lobby campfire created: $LOBBY_ID"
log "  Lobby ID written to: $SHARED_DIR/lobby-id.txt"

# ─────────────────────────────────────────────────────────────────────────────
# Step 6: Inject templates → CLAUDE.md for each agent
# ─────────────────────────────────────────────────────────────────────────────
log "Injecting templates..."

inject_template() {
    local num="$1"   # zero-padded, e.g. "01"
    local pubkey="${AGENT_KEYS[$num]}"
    local agent_dir="agent-$num"

    sed \
        -e "s|{{NUM}}|$num|g" \
        -e "s|{{PUBKEY}}|$pubkey|g" \
        -e "s|{{WORKSPACE}}|$WORKSPACE|g" \
        -e "s|{{AGENT_DIR}}|$agent_dir|g" \
        "$TEMPLATE_DIR/agent-$num.md" \
        > "$AGENTS_DIR/agent-$num/CLAUDE.md"

    log "  agent-$num/CLAUDE.md written"
}

for i in $(seq -w 1 20); do
    inject_template "$i"
done

# Verify no unreplaced placeholders
for i in $(seq -w 1 20); do
    if grep -q '{{' "$AGENTS_DIR/agent-$i/CLAUDE.md"; then
        fail "Unreplaced placeholders in agent-$i/CLAUDE.md"
    fi
done

log "All templates injected, no unreplaced placeholders."

# ─────────────────────────────────────────────────────────────────────────────
# Step 7: Write MCP configs for even-numbered agents (02, 04, ... 20)
# ─────────────────────────────────────────────────────────────────────────────
log "Writing MCP configs for even-numbered agents..."

write_mcp_config() {
    local num="$1"
    local agent_home="$AGENTS_DIR/agent-$num"
    cat > "$agent_home/mcp-config.json" <<EOF
{
  "mcpServers": {
    "campfire": {
      "command": "$CF_MCP_BIN",
      "args": [
        "--cf-home", "$agent_home",
        "--beacon-dir", "$BEACON_DIR"
      ],
      "env": {
        "CF_HOME": "$agent_home",
        "CF_BEACON_DIR": "$BEACON_DIR",
        "CF_TRANSPORT_DIR": "$TRANSPORT_DIR"
      }
    }
  }
}
EOF
    log "  agent-$num/mcp-config.json written"
}

for i in 02 04 06 08 10 12 14 16 18 20; do
    write_mcp_config "$i"
done

# ─────────────────────────────────────────────────────────────────────────────
# Dry-run exit point
# ─────────────────────────────────────────────────────────────────────────────
if $DRY_RUN; then
    log ""
    log "Dry-run complete. Verifying setup artifacts..."

    # Check identity files
    for i in $(seq -w 1 20); do
        [ -f "$AGENTS_DIR/agent-$i/identity.json" ] \
            || fail "Missing identity.json for agent-$i"
    done
    log "  All 20 identity.json files present."

    # Check CLAUDE.md files (no placeholders already verified above)
    for i in $(seq -w 1 20); do
        [ -f "$AGENTS_DIR/agent-$i/CLAUDE.md" ] \
            || fail "Missing CLAUDE.md for agent-$i"
    done
    log "  All 20 CLAUDE.md files present."

    # Check MCP configs for even agents
    for i in 02 04 06 08 10 12 14 16 18 20; do
        [ -f "$AGENTS_DIR/agent-$i/mcp-config.json" ] \
            || fail "Missing mcp-config.json for agent-$i"
    done
    log "  10 MCP configs present (even agents)."

    # Check lobby ID
    [ -f "$SHARED_DIR/lobby-id.txt" ] || fail "Missing shared/lobby-id.txt"
    lobby_content=$(cat "$SHARED_DIR/lobby-id.txt")
    [ -n "$lobby_content" ] || fail "shared/lobby-id.txt is empty"
    log "  shared/lobby-id.txt present and non-empty ($lobby_content)."

    log ""
    log "PASS: dry-run setup successful (launch mode: $LAUNCH_MODE)"
    exit 0
fi

fi  # end of !CONTINUE block

# ─────────────────────────────────────────────────────────────────────────────
# Step 8: Launch rounds — Time-division multiplexing
#
# 4 batches of 5 agents each:
#   Batch 1: agents 01-05
#   Batch 2: agents 06-10
#   Batch 3: agents 11-15
#   Batch 4: agents 16-20
#
# For each round:
#   All 4 batches run in parallel.
#   Within each batch, the 5 agents run sequentially (one turn each).
#   A "turn" = fresh claude -p call with agent CLAUDE.md + turn context.
# ─────────────────────────────────────────────────────────────────────────────

BATCH_1=(01 02 03 04 05)
BATCH_2=(06 07 08 09 10)
BATCH_3=(11 12 13 14 15)
BATCH_4=(16 17 18 19 20)

# Build the turn prompt for an agent
build_turn_prompt() {
    local agent_num="$1"
    local round="$2"
    local agent_home="$AGENTS_DIR/agent-$agent_num"
    local claude_md="$agent_home/CLAUDE.md"

    # Protocol briefing first — this IS the CLAUDE.md-equivalent for campfire
    local context_file="$agent_home/CONTEXT.md"
    if [ -f "$context_file" ]; then
        cat "$context_file"
        echo ""
        echo "---"
        echo ""
    fi
    cat "$claude_md"
    echo ""
    echo "---"
    echo ""
    echo "This is turn $round of $ROUNDS."
    echo "Check your campfires for new messages. Do your work. Send any messages needed."
    echo "State from previous turns persists — your campfire memberships and sent messages are retained."
}

# Run one agent's turn, writing output to log
run_agent_turn() {
    local agent_num="$1"
    local round="$2"
    local agent_home="$AGENTS_DIR/agent-$agent_num"
    local log_file="$LOGS_DIR/agent-${agent_num}-turn-$(printf '%02d' "$round").log"

    local mcp_args=()
    # Even-numbered agents use MCP
    local num_val=$((10#$agent_num))
    if (( num_val % 2 == 0 )); then
        mcp_args+=(--mcp-config "$agent_home/mcp-config.json")
    fi

    log "    [round $round] agent-$agent_num: starting turn (log: $log_file)"

    build_turn_prompt "$agent_num" "$round" \
        | CF_HOME="$agent_home" \
          CF_BEACON_DIR="$BEACON_DIR" \
          CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
          PATH="$BIN_DIR:$PATH" \
          claude -p \
            --dangerously-skip-permissions \
            --output-format text \
            --model claude-sonnet-4-5 \
            "${mcp_args[@]}" \
        > "$log_file" 2>&1

    local exit_code=$?
    if [ $exit_code -ne 0 ]; then
        log "    [round $round] agent-$agent_num: exited with code $exit_code (see $log_file)"
    else
        log "    [round $round] agent-$agent_num: turn complete"
    fi
    return $exit_code
}

# Run one batch of 5 agents sequentially for a given round
run_batch() {
    local round="$1"
    shift
    local agents=("$@")

    for agent_num in "${agents[@]}"; do
        run_agent_turn "$agent_num" "$round" || true
    done
}

log ""
log "Starting $ROUNDS rounds of time-division multiplexed agent turns..."
log "  4 batches × 5 agents each, batches run in parallel, agents within batch sequential"
log ""

START_TIME=$(date +%s)

for round in $(seq 1 "$ROUNDS"); do
    log "━━━ Round $round / $ROUNDS ━━━"

    ROUND_START=$(date +%s)

    # Check elapsed time — abort if past timeout (leave 5 min buffer)
    elapsed=$(( ROUND_START - START_TIME ))
    if (( elapsed > (TIMEOUT - 300) )); then
        log "Elapsed time ${elapsed}s exceeds $((TIMEOUT/60))-minute limit — stopping rounds early (completed $((round - 1)) of $ROUNDS)."
        break
    fi

    # Run all 4 batches in parallel, wait for all to finish
    run_batch "$round" "${BATCH_1[@]}" &
    PID_B1=$!
    run_batch "$round" "${BATCH_2[@]}" &
    PID_B2=$!
    run_batch "$round" "${BATCH_3[@]}" &
    PID_B3=$!
    run_batch "$round" "${BATCH_4[@]}" &
    PID_B4=$!

    wait "$PID_B1" || true
    wait "$PID_B2" || true
    wait "$PID_B3" || true
    wait "$PID_B4" || true

    ROUND_END=$(date +%s)
    round_duration=$(( ROUND_END - ROUND_START ))
    log "  Round $round complete in ${round_duration}s."
    log ""
done

TOTAL_TIME=$(( $(date +%s) - START_TIME ))
log "All rounds complete in ${TOTAL_TIME}s."

# ─────────────────────────────────────────────────────────────────────────────
# Step 9: Collect — topology visualization and analysis
# ─────────────────────────────────────────────────────────────────────────────
log ""
log "Running topology collection..."

if [ -f "$SCRIPT_DIR/topology-viz.sh" ]; then
    bash "$SCRIPT_DIR/topology-viz.sh" "$INTEG_ROOT" 2>&1 | tee "$LOGS_DIR/topology-viz.log" || true
    log "  topology-viz.sh complete (log: $LOGS_DIR/topology-viz.log)"
else
    log "  topology-viz.sh not found — skipping"
fi

if [ -f "$SCRIPT_DIR/topology-analysis.py" ]; then
    python3 "$SCRIPT_DIR/topology-analysis.py" "$INTEG_ROOT" 2>&1 | tee "$LOGS_DIR/topology-analysis.log" || true
    log "  topology-analysis.py complete (log: $LOGS_DIR/topology-analysis.log)"
else
    log "  topology-analysis.py not found — skipping"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Step 10: Verify
# ─────────────────────────────────────────────────────────────────────────────
log ""
log "Running verification..."

if [ -f "$SCRIPT_DIR/verify_emergence.sh" ]; then
    bash "$SCRIPT_DIR/verify_emergence.sh" "$INTEG_ROOT"
    VERIFY_EXIT=$?
else
    log "verify_emergence.sh not found — running basic checks"
    VERIFY_EXIT=0

    # Basic sanity: at least some agent logs exist
    log_count=$(ls "$LOGS_DIR"/agent-*-turn-*.log 2>/dev/null | wc -l)
    if [ "$log_count" -eq 0 ]; then
        log "  FAIL: no agent turn logs found in $LOGS_DIR"
        VERIFY_EXIT=1
    else
        log "  Found $log_count agent turn log(s)."
    fi

    # Check lobby still exists
    if [ -f "$SHARED_DIR/lobby-id.txt" ] && [ -n "$(cat "$SHARED_DIR/lobby-id.txt")" ]; then
        log "  Lobby ID present: $(cat "$SHARED_DIR/lobby-id.txt")"
    else
        log "  WARNING: lobby-id.txt missing or empty"
    fi
fi

echo ""
if [ "$VERIFY_EXIT" -eq 0 ]; then
    log "PASS: emergence test complete."
    log "  Rounds run:   $ROUNDS"
    log "  Total time:   ${TOTAL_TIME}s"
    log "  Agent logs:   $LOGS_DIR/"
    log "  Lobby ID:     $(cat "$SHARED_DIR/lobby-id.txt" 2>/dev/null || echo 'unknown')"
else
    log "FAIL: verification failed (exit code $VERIFY_EXIT)."
    echo ""
    echo "=== Last 20 lines of each agent's final turn log ==="
    for i in $(seq -w 1 20); do
        # Find the highest turn log for this agent
        last_log=$(ls "$LOGS_DIR/agent-${i}-turn-"*.log 2>/dev/null | sort | tail -1 || true)
        if [ -n "$last_log" ]; then
            echo "--- $last_log ---"
            tail -20 "$last_log" || true
        else
            echo "--- agent-$i: (no turn logs found) ---"
        fi
    done
fi

exit "$VERIFY_EXIT"

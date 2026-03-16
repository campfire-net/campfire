#!/usr/bin/env bash
# harness_internet.sh — 9-architect + verification agent internet founding committee test
#
# Usage:
#   bash tests/harness_internet.sh                # Full run: build, setup, launch rounds, verify
#   bash tests/harness_internet.sh --dry-run      # Setup only: build, dirs, identities, templates; no launch
#   bash tests/harness_internet.sh --rounds N     # Override number of rounds (default: 5)
#   bash tests/harness_internet.sh --round-1-minutes N  # Override round 1 duration (default: 45)
#   bash tests/harness_internet.sh --round-2-minutes N  # Override round 2 duration (default: 30)
#   bash tests/harness_internet.sh --round-3-minutes N  # Override round 3 duration (default: 30)
#   bash tests/harness_internet.sh --round-4-minutes N  # Override round 4 duration (default: 30)
#   bash tests/harness_internet.sh --round-5-minutes N  # Override round 5 duration (default: 20)
#
# Environment:
#   SKIP_BUILD=1    — Skip build, expect pre-built binaries at cf-bin-tmp and cf-mcp-bin-tmp
#
# Design: persistent claude sessions (--resume) run across all rounds.
#   - Each architect gets one session started in Round 1; rounds 2+ inject state via --resume.
#   - A poller loop runs every 30 seconds and injects state diffs if anything changed.
#   - Round transitions are injections into existing sessions, not new sessions.
#   - Newcomer (agent-10) starts fresh in Round 5 (no prior context).
#
# Exits 0 on PASS, non-zero on FAIL.

set -euo pipefail

# ─────────────────────────────────────────────────────────────────────────────
# Configuration
# ─────────────────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

INTEG_ROOT="/tmp/campfire-internet"
BIN_DIR="$INTEG_ROOT/bin"
AGENTS_DIR="$INTEG_ROOT/agents"
SHARED_DIR="$INTEG_ROOT/shared"
LOGS_DIR="$INTEG_ROOT/logs"
ROUNDS_DIR="$INTEG_ROOT/rounds"

CF_BIN="$BIN_DIR/cf"
CF_MCP_BIN="$BIN_DIR/cf-mcp"

BEACON_DIR="$SHARED_DIR/beacons"
TRANSPORT_DIR="$SHARED_DIR/transport"
WORKSPACE="$SHARED_DIR/workspace"

TEMPLATE_DIR="$SCRIPT_DIR/internet-agents"

HARNESS_HOME="$INTEG_ROOT/harness-identity"

DRY_RUN=false
ROUNDS=5

# Round durations (in seconds)
ROUND_1_SECS=$(( 45 * 60 ))
ROUND_2_SECS=$(( 30 * 60 ))
ROUND_3_SECS=$(( 30 * 60 ))
ROUND_4_SECS=$(( 30 * 60 ))
ROUND_5_SECS=$(( 20 * 60 ))

OVERALL_TIMEOUT=$(( 45 * 60 ))  # 45 minutes total

POLL_INTERVAL=30  # seconds between state diff checks

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
        --round-1-minutes)
            ROUND_1_SECS=$(( ${2:?--round-1-minutes requires a value} * 60 ))
            shift 2
            ;;
        --round-2-minutes)
            ROUND_2_SECS=$(( ${2:?--round-2-minutes requires a value} * 60 ))
            shift 2
            ;;
        --round-3-minutes)
            ROUND_3_SECS=$(( ${2:?--round-3-minutes requires a value} * 60 ))
            shift 2
            ;;
        --round-4-minutes)
            ROUND_4_SECS=$(( ${2:?--round-4-minutes requires a value} * 60 ))
            shift 2
            ;;
        --round-5-minutes)
            ROUND_5_SECS=$(( ${2:?--round-5-minutes requires a value} * 60 ))
            shift 2
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

round_name() {
    local round="$1"
    case "$round" in
        1) echo "BUILD" ;;
        2) echo "BUILD" ;;
        3) echo "REVIEW" ;;
        4) echo "ATTACK" ;;
        *) echo "VERIFY" ;;
    esac
}

round_focus() {
    local round="$1"
    case "$round" in
        1|2) echo "Design and build your root infrastructure campfires." ;;
        3)   echo "Review other architects' designs. Join their campfires. Provide feedback." ;;
        4)   echo "Stress Test Architect: attack everything. Others: defend and iterate." ;;
        *)   echo "Newcomer is bootstrapping. Help if asked. Otherwise iterate on your designs." ;;
    esac
}

round_duration_secs() {
    local round="$1"
    case "$round" in
        1) echo "$ROUND_1_SECS" ;;
        2) echo "$ROUND_2_SECS" ;;
        3) echo "$ROUND_3_SECS" ;;
        4) echo "$ROUND_4_SECS" ;;
        *) echo "$ROUND_5_SECS" ;;
    esac
}

# ─────────────────────────────────────────────────────────────────────────────
# Step 0: Prerequisite checks
# ─────────────────────────────────────────────────────────────────────────────
log "Checking prerequisites..."

command -v python3      >/dev/null 2>&1 || fail "python3 not found on PATH"
command -v claude       >/dev/null 2>&1 || fail "claude not found on PATH"
command -v systemd-run  >/dev/null 2>&1 || fail "systemd-run not found on PATH (required for session isolation)"

if [ -z "${SKIP_BUILD:-}" ]; then
    command -v go >/dev/null 2>&1 || fail "go not found on PATH (set SKIP_BUILD=1 and pre-build binaries)"
fi

log "Prerequisites OK."
log "Rounds: $ROUNDS"
log "Round durations: R1=${ROUND_1_SECS}s R2=${ROUND_2_SECS}s R3=${ROUND_3_SECS}s R4=${ROUND_4_SECS}s R5+=${ROUND_5_SECS}s"
log "Overall timeout: ${OVERALL_TIMEOUT}s"

if $DRY_RUN; then
    log "Dry-run mode: setup only, no agents will be launched."
fi

# ─────────────────────────────────────────────────────────────────────────────
# Step 1: Build binaries
# ─────────────────────────────────────────────────────────────────────────────
if [ -n "${SKIP_BUILD:-}" ]; then
    log "SKIP_BUILD set — expecting pre-built binaries at $PROJECT_ROOT/cf-bin-tmp and cf-mcp-bin-tmp"
    [ -f "$PROJECT_ROOT/cf-bin-tmp"     ] || fail "cf-bin-tmp not found — build with: go build -o cf-bin-tmp ./cmd/cf"
    [ -f "$PROJECT_ROOT/cf-mcp-bin-tmp" ] || fail "cf-mcp-bin-tmp not found — build with: go build -o cf-mcp-bin-tmp ./cmd/cf-mcp"
else
    log "Building cf and cf-mcp binaries..."
    cd "$PROJECT_ROOT"
    go build -o cf-bin-tmp     ./cmd/cf     || fail "Failed to build cf"
    go build -o cf-mcp-bin-tmp ./cmd/cf-mcp || fail "Failed to build cf-mcp"
    cd - >/dev/null
fi
log "Binaries ready."

# ─────────────────────────────────────────────────────────────────────────────
# Step 2: Create directory structure
# ─────────────────────────────────────────────────────────────────────────────
log "Creating directory structure under $INTEG_ROOT..."

rm -rf "$INTEG_ROOT"
mkdir -p \
    "$BIN_DIR" \
    "$BEACON_DIR" \
    "$TRANSPORT_DIR" \
    "$WORKSPACE" \
    "$LOGS_DIR" \
    "$ROUNDS_DIR" \
    "$HARNESS_HOME"

# Architect agent dirs (01-09) and workspace subdirs
for i in 01 02 03 04 05 06 07 08 09; do
    mkdir -p "$AGENTS_DIR/agent-$i"
done
# Verification agent dir (10)
mkdir -p "$AGENTS_DIR/agent-10"

# Shared workspace subdirs (one per architect role + verification agent)
for ws_dir in \
    directory-architect \
    trust-architect \
    tool-registry-architect \
    security-architect \
    governance-architect \
    onboarding-architect \
    filter-architect \
    stress-test-architect \
    interop-architect \
    verification-agent
do
    mkdir -p "$WORKSPACE/$ws_dir"
done

log "Directories created."

# Move compiled binaries into BIN_DIR
cp "$PROJECT_ROOT/cf-bin-tmp"     "$CF_BIN"     || fail "Failed to copy cf binary"
cp "$PROJECT_ROOT/cf-mcp-bin-tmp" "$CF_MCP_BIN" || fail "Failed to copy cf-mcp binary"
log "Binaries installed: $CF_BIN, $CF_MCP_BIN"

# Make cf available on PATH and set shared transport env
export PATH="$BIN_DIR:$PATH"
export CF_BEACON_DIR="$BEACON_DIR"
export CF_TRANSPORT_DIR="$TRANSPORT_DIR"

# ─────────────────────────────────────────────────────────────────────────────
# Step 3: Initialize agent identities (architects 01-09)
# ─────────────────────────────────────────────────────────────────────────────
log "Initializing architect identities (agents 01-09)..."

for i in 01 02 03 04 05 06 07 08 09; do
    AGENT_HOME="$AGENTS_DIR/agent-$i"
    CF_HOME="$AGENT_HOME" "$CF_BIN" init > /dev/null
    log "  agent-$i: initialized"
done

# Initialize harness identity (used to send round markers)
CF_HOME="$HARNESS_HOME" \
CF_BEACON_DIR="$BEACON_DIR" \
CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
"$CF_BIN" init > /dev/null
log "  harness-identity: initialized"

# ─────────────────────────────────────────────────────────────────────────────
# Step 4: Read public keys
# ─────────────────────────────────────────────────────────────────────────────
log "Reading public keys..."

declare -A AGENT_KEYS

for i in 01 02 03 04 05 06 07 08 09; do
    AGENT_HOME="$AGENTS_DIR/agent-$i"
    key=$(CF_HOME="$AGENT_HOME" "$CF_BIN" id --json \
        | python3 -c "import json,sys; print(json.load(sys.stdin)['public_key'])")
    AGENT_KEYS["$i"]="$key"
    log "  agent-$i: ${key:0:16}..."
done

# ─────────────────────────────────────────────────────────────────────────────
# Step 5: Create coordination campfire
# ─────────────────────────────────────────────────────────────────────────────
log "Creating coordination campfire..."

COORDINATION_ID=$(CF_HOME="$HARNESS_HOME" \
    CF_BEACON_DIR="$BEACON_DIR" \
    CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
    "$CF_BIN" create \
        --protocol open \
        --description "Root Infrastructure Coordination — all architects join here for round markers and cross-cutting discussion. This is the founding committee's shared coordination space." \
        --json \
    | python3 -c "import json,sys; print(json.load(sys.stdin)['campfire_id'])")

[ -n "$COORDINATION_ID" ] || fail "Failed to create coordination campfire — got empty ID"

echo "$COORDINATION_ID" > "$SHARED_DIR/coordination-id.txt"
log "  Coordination campfire created: $COORDINATION_ID"
log "  Coordination ID written to: $SHARED_DIR/coordination-id.txt"

# ─────────────────────────────────────────────────────────────────────────────
# Step 6: Inject templates → CLAUDE.md for each architect (01-09)
# ─────────────────────────────────────────────────────────────────────────────
log "Injecting templates..."

# Read full protocol spec and gap analysis for injection
PROTOCOL_SPEC="$PROJECT_ROOT/docs/protocol-spec.md"
GAP_ANALYSIS="$PROJECT_ROOT/docs/moltbook-gap-analysis.md"

[ -f "$PROTOCOL_SPEC" ] || fail "Protocol spec not found at $PROTOCOL_SPEC"

# Map agent number to workspace dir name
agent_workspace_dir() {
    local num="$1"
    case "$num" in
        01) echo "directory-architect" ;;
        02) echo "trust-architect" ;;
        03) echo "tool-registry-architect" ;;
        04) echo "security-architect" ;;
        05) echo "governance-architect" ;;
        06) echo "onboarding-architect" ;;
        07) echo "filter-architect" ;;
        08) echo "stress-test-architect" ;;
        09) echo "interop-architect" ;;
        10) echo "verification-agent" ;;
        *)  echo "agent-$num" ;;
    esac
}

inject_template() {
    local num="$1"
    local pubkey="${AGENT_KEYS[$num]}"
    local agent_dir="agent-$num"
    local ws_dir
    ws_dir="$(agent_workspace_dir "$num")"
    local template_file

    if [ "$num" = "10" ]; then
        template_file="$TEMPLATE_DIR/newcomer.md"
    else
        template_file="$TEMPLATE_DIR/architect-$num.md"
    fi

    [ -f "$template_file" ] || fail "Template not found: $template_file"

    # Build the injected CLAUDE.md: template vars + appended full protocol spec
    local output_file="$AGENTS_DIR/$agent_dir/CLAUDE.md"

    sed \
        -e "s|{{NUM}}|$num|g" \
        -e "s|{{PUBKEY}}|$pubkey|g" \
        -e "s|{{WORKSPACE}}|$WORKSPACE|g" \
        -e "s|{{AGENT_DIR}}|$ws_dir|g" \
        "$template_file" \
        > "$output_file"

    # Append full protocol spec
    cat >> "$output_file" <<EOF

---

## Full Protocol Specification

The following is the complete Campfire protocol specification. Read it carefully — your designs must work within these primitives.

EOF
    cat "$PROTOCOL_SPEC" >> "$output_file"

    # Append gap analysis if it exists
    if [ -f "$GAP_ANALYSIS" ]; then
        cat >> "$output_file" <<EOF

---

## Gap Analysis (from emergence test)

The following gap analysis was produced after the first emergence test (20-agent business simulation). Use it to understand known protocol limitations and areas where you may need to design workarounds or document gaps.

EOF
        cat "$GAP_ANALYSIS" >> "$output_file"
    fi

    log "  agent-$num/CLAUDE.md written (template + protocol spec)"
}

for i in 01 02 03 04 05 06 07 08 09; do
    inject_template "$i"
done

# Verify no unreplaced placeholders
for i in 01 02 03 04 05 06 07 08 09; do
    if grep -q '{{' "$AGENTS_DIR/agent-$i/CLAUDE.md"; then
        fail "Unreplaced placeholders in agent-$i/CLAUDE.md"
    fi
done

log "All architect templates injected, no unreplaced placeholders."

# ─────────────────────────────────────────────────────────────────────────────
# Step 7: Write MCP configs for even-numbered architects (02, 04, 06, 08)
# ─────────────────────────────────────────────────────────────────────────────
log "Writing MCP configs for even-numbered architects..."

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

for i in 02 04 06 08; do
    write_mcp_config "$i"
done

# ─────────────────────────────────────────────────────────────────────────────
# Dry-run exit point
# ─────────────────────────────────────────────────────────────────────────────
if $DRY_RUN; then
    log ""
    log "Dry-run complete. Verifying setup artifacts..."

    # Check identity files (architects 01-09)
    for i in 01 02 03 04 05 06 07 08 09; do
        [ -f "$AGENTS_DIR/agent-$i/identity.json" ] \
            || fail "Missing identity.json for agent-$i"
    done
    log "  All 9 architect identity.json files present."

    # Check CLAUDE.md files (no placeholders already verified above)
    for i in 01 02 03 04 05 06 07 08 09; do
        [ -f "$AGENTS_DIR/agent-$i/CLAUDE.md" ] \
            || fail "Missing CLAUDE.md for agent-$i"
    done
    log "  All 9 architect CLAUDE.md files present."

    # Check MCP configs for even agents
    for i in 02 04 06 08; do
        [ -f "$AGENTS_DIR/agent-$i/mcp-config.json" ] \
            || fail "Missing mcp-config.json for agent-$i"
    done
    log "  4 MCP configs present (agents 02, 04, 06, 08)."

    # Check coordination campfire ID
    [ -f "$SHARED_DIR/coordination-id.txt" ] || fail "Missing shared/coordination-id.txt"
    coord_content=$(cat "$SHARED_DIR/coordination-id.txt")
    [ -n "$coord_content" ] || fail "shared/coordination-id.txt is empty"
    log "  shared/coordination-id.txt present and non-empty ($coord_content)."

    # Check shared workspace subdirs
    for ws_dir in \
        directory-architect trust-architect tool-registry-architect \
        security-architect governance-architect onboarding-architect \
        filter-architect stress-test-architect interop-architect \
        verification-agent
    do
        [ -d "$WORKSPACE/$ws_dir" ] || fail "Missing workspace subdir: $WORKSPACE/$ws_dir"
    done
    log "  All 10 shared workspace subdirs present."

    log ""
    log "PASS: dry-run setup successful"
    exit 0
fi

# ─────────────────────────────────────────────────────────────────────────────
# Step 8: Persistent session execution
#
# Model:
#   - Phase 1: Start all 9 architect sessions in parallel via systemd-run.
#              Each session gets the full initial prompt (CLAUDE.md + round 1 state).
#              Session IDs are captured for --resume injection.
#   - Phase 2: Poller loop runs every POLL_INTERVAL seconds.
#              For each live agent, compute a state diff (beacon count, message count).
#              If state changed since last injection, inject a diff update via --resume.
#   - Phase 3: Round transitions inject a round marker + full state snapshot into
#              each live session via --resume.
#   - Phase 4: In round 5, newcomer starts as a fresh session (no --resume).
#   - Total timeout: OVERALL_TIMEOUT seconds. Poller kills all sessions at end.
# ─────────────────────────────────────────────────────────────────────────────

# Session state tracking
declare -A SESSION_IDS        # claude session IDs per agent number
declare -A SESSION_ALIVE      # "true" or "false"
declare -A LAST_BEACON_COUNT  # beacon count as of last injection
declare -A LAST_MSG_COUNT     # total message count across all campfires as of last injection
declare -A MCP_ARGS_MAP       # pre-built mcp-args string per agent

# Initialize tracking arrays for agents 01-09
for i in 01 02 03 04 05 06 07 08 09; do
    SESSION_IDS["$i"]=""
    SESSION_ALIVE["$i"]="false"
    LAST_BEACON_COUNT["$i"]="0"
    LAST_MSG_COUNT["$i"]="0"
    if [ -f "$AGENTS_DIR/agent-$i/mcp-config.json" ]; then
        MCP_ARGS_MAP["$i"]="--mcp-config $AGENTS_DIR/agent-$i/mcp-config.json"
    else
        MCP_ARGS_MAP["$i"]=""
    fi
done

# Build a full state snapshot for an agent
build_state_snapshot() {
    local agent_num="$1"
    local agent_home="$AGENTS_DIR/agent-$agent_num"

    echo "## Current state (auto-generated by harness)"
    echo ""

    # Campfire memberships
    echo "### Your campfires"
    echo ""
    local ls_out
    ls_out=$(CF_HOME="$agent_home" \
        CF_BEACON_DIR="$BEACON_DIR" \
        CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
        PATH="$BIN_DIR:$PATH" \
        "$CF_BIN" ls 2>/dev/null || true)
    if [ -n "$ls_out" ]; then
        echo "$ls_out"
    else
        echo "(none — you have not joined any campfires yet)"
    fi
    echo ""

    # Available beacons
    echo "### Available beacons (cf discover)"
    echo ""
    local discover_out
    discover_out=$(CF_HOME="$agent_home" \
        CF_BEACON_DIR="$BEACON_DIR" \
        CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
        PATH="$BIN_DIR:$PATH" \
        "$CF_BIN" discover 2>/dev/null || true)
    if [ -n "$discover_out" ]; then
        echo "$discover_out"
    else
        echo "(no beacons found)"
    fi
    echo ""

    # New messages (read --all per campfire)
    echo "### Messages in your campfires"
    echo ""
    local memberships_json
    memberships_json=$(CF_HOME="$agent_home" \
        CF_BEACON_DIR="$BEACON_DIR" \
        CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
        PATH="$BIN_DIR:$PATH" \
        "$CF_BIN" ls --json 2>/dev/null || echo "[]")

    local campfire_ids
    campfire_ids=$(echo "$memberships_json" \
        | python3 -c "import json,sys; ids=json.load(sys.stdin); print('\n'.join(x['campfire_id'] for x in ids))" \
        2>/dev/null || true)

    if [ -z "$campfire_ids" ]; then
        echo "(no campfires — nothing to read)"
    else
        local any_messages=false
        while IFS= read -r cf_id; do
            [ -z "$cf_id" ] && continue
            local read_out
            read_out=$(CF_HOME="$agent_home" \
                CF_BEACON_DIR="$BEACON_DIR" \
                CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
                PATH="$BIN_DIR:$PATH" \
                "$CF_BIN" read "$cf_id" --all 2>/dev/null || true)
            if [ -n "$read_out" ] && [ "$read_out" != "No new messages." ]; then
                echo "#### campfire: $cf_id"
                echo ""
                echo "$read_out"
                echo ""
                any_messages=true
            fi
        done <<< "$campfire_ids"
        if ! $any_messages; then
            echo "(no messages in your campfires)"
        fi
    fi
    echo ""

    # Coordination campfire (always show)
    local coord_id
    coord_id="$(cat "$SHARED_DIR/coordination-id.txt" 2>/dev/null || true)"
    if [ -n "$coord_id" ]; then
        echo "### Coordination campfire messages"
        echo "Campfire ID: $coord_id"
        echo ""
        local coord_out
        coord_out=$(CF_HOME="$HARNESS_HOME" \
            CF_BEACON_DIR="$BEACON_DIR" \
            CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
            PATH="$BIN_DIR:$PATH" \
            "$CF_BIN" read "$coord_id" --all 2>/dev/null || true)
        if [ -n "$coord_out" ] && [ "$coord_out" != "No new messages." ]; then
            echo "$coord_out"
        else
            echo "(no messages yet)"
        fi
        echo ""
    fi
}

# Build the initial prompt for an architect (Round 1 launch)
build_architect_prompt() {
    local agent_num="$1"
    local round="$2"
    local agent_home="$AGENTS_DIR/agent-$agent_num"
    local name
    name="$(round_name "$round")"
    local focus
    focus="$(round_focus "$round")"

    cat "$agent_home/CLAUDE.md"
    echo ""
    echo "---"
    echo ""
    echo "## This Round"
    echo ""
    echo "This is Round $round of $ROUNDS (${name} phase)."
    echo ""
    echo "**Your focus this round:** $focus"
    echo ""
    echo "Your campfire memberships and previous round outputs persist — rounds build on each other."
    echo ""
    echo "Coordination campfire ID: $(cat "$SHARED_DIR/coordination-id.txt")"
    echo ""
    echo "---"
    echo ""
    build_state_snapshot "$agent_num"
}

# Build newcomer prompt (Round 5 fresh start)
build_newcomer_prompt() {
    local round="$1"
    local agent_home="$AGENTS_DIR/agent-10"

    cat "$agent_home/CLAUDE.md"
    echo ""
    echo "---"
    echo ""
    echo "## This Round"
    echo ""
    echo "This is Round $round of $ROUNDS (VERIFY phase)."
    echo ""
    echo "You are a brand-new agent. Work through the tasks in your mission above. Document everything in your workspace."
    echo ""
    echo "---"
    echo ""
    build_state_snapshot "10"
}

# Get current beacon count for an agent
get_beacon_count() {
    local agent_num="$1"
    local agent_home="$AGENTS_DIR/agent-$agent_num"
    CF_HOME="$agent_home" \
        CF_BEACON_DIR="$BEACON_DIR" \
        CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
        PATH="$BIN_DIR:$PATH" \
        "$CF_BIN" discover --json 2>/dev/null \
        | python3 -c "import json,sys; d=json.load(sys.stdin); print(len(d) if isinstance(d, list) else 0)" \
        2>/dev/null || echo "0"
}

# Get total message count across all campfires for an agent
get_msg_count() {
    local agent_num="$1"
    local agent_home="$AGENTS_DIR/agent-$agent_num"

    local memberships_json
    memberships_json=$(CF_HOME="$agent_home" \
        CF_BEACON_DIR="$BEACON_DIR" \
        CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
        PATH="$BIN_DIR:$PATH" \
        "$CF_BIN" ls --json 2>/dev/null || echo "[]")

    local campfire_ids
    campfire_ids=$(echo "$memberships_json" \
        | python3 -c "import json,sys; ids=json.load(sys.stdin); print('\n'.join(x['campfire_id'] for x in ids))" \
        2>/dev/null || true)

    local total=0
    if [ -n "$campfire_ids" ]; then
        while IFS= read -r cf_id; do
            [ -z "$cf_id" ] && continue
            local count
            count=$(CF_HOME="$agent_home" \
                CF_BEACON_DIR="$BEACON_DIR" \
                CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
                PATH="$BIN_DIR:$PATH" \
                "$CF_BIN" read "$cf_id" --all --json 2>/dev/null \
                | python3 -c "import json,sys; d=json.load(sys.stdin); print(len(d) if isinstance(d, list) else 0)" \
                2>/dev/null || echo "0")
            total=$(( total + count ))
        done <<< "$campfire_ids"
    fi
    echo "$total"
}

# Start a persistent claude session for an agent.
# Writes session_id to $LOGS_DIR/agent-$agent_num.session_id
# Writes initial output to $LOGS_DIR/agent-$agent_num-init.log
start_agent_session() {
    local agent_num="$1"
    local round="$2"
    local agent_home="$AGENTS_DIR/agent-$agent_num"
    local log_file="$LOGS_DIR/agent-${agent_num}-init.log"
    local session_file="$LOGS_DIR/agent-${agent_num}.session_id"
    local prompt_file="$LOGS_DIR/agent-${agent_num}-init-prompt.txt"

    log "    [session start] agent-$agent_num: building initial prompt..."

    build_architect_prompt "$agent_num" "$round" > "$prompt_file"

    # Build mcp args array
    local mcp_flags=""
    if [ -f "$agent_home/mcp-config.json" ]; then
        mcp_flags="--mcp-config $agent_home/mcp-config.json"
    fi

    log "    [session start] agent-$agent_num: launching via systemd-run..."

    # Launch via systemd-run to escape Claude Code's process tree.
    # Output goes to log_file. We parse session_id after it arrives.
    systemd-run \
        --user \
        --scope \
        --collect \
        -- \
        bash -c "
            CF_HOME='$agent_home' \
            CF_BEACON_DIR='$BEACON_DIR' \
            CF_TRANSPORT_DIR='$TRANSPORT_DIR' \
            PATH='$BIN_DIR:$PATH' \
            claude -p \
                --dangerously-skip-permissions \
                --output-format json \
                --model claude-sonnet-4-5 \
                $mcp_flags \
                < '$prompt_file' \
                > '$log_file' 2>&1
        " &

    # Wait for log file to appear and contain session_id (up to 60s)
    local waited=0
    while [ $waited -lt 60 ]; do
        if [ -s "$log_file" ]; then
            local sid
            sid=$(python3 -c "
import json, sys
try:
    data = json.load(open('$log_file'))
    print(data.get('session_id', ''))
except Exception:
    print('')
" 2>/dev/null || true)
            if [ -n "$sid" ]; then
                echo "$sid" > "$session_file"
                SESSION_IDS["$agent_num"]="$sid"
                SESSION_ALIVE["$agent_num"]="true"
                log "    [session start] agent-$agent_num: session_id=${sid}"
                return 0
            fi
        fi
        sleep 2
        waited=$(( waited + 2 ))
    done

    log "    [session start] agent-$agent_num: WARNING — timed out waiting for session_id (see $log_file)"
    SESSION_ALIVE["$agent_num"]="false"
    return 1
}

# Inject a message into a live session via --resume.
# Returns 0 on success, non-zero if session appears dead.
inject_into_session() {
    local agent_num="$1"
    local message="$2"
    local label="${3:-update}"
    local agent_home="$AGENTS_DIR/agent-$agent_num"
    local session_id="${SESSION_IDS[$agent_num]}"
    local log_file="$LOGS_DIR/agent-${agent_num}-${label}-$(date +%s).log"

    if [ -z "$session_id" ]; then
        log "    [inject] agent-$agent_num: no session_id, skipping"
        return 1
    fi

    local mcp_flags=""
    if [ -f "$agent_home/mcp-config.json" ]; then
        mcp_flags="--mcp-config $agent_home/mcp-config.json"
    fi

    local result
    result=$(CF_HOME="$agent_home" \
        CF_BEACON_DIR="$BEACON_DIR" \
        CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
        PATH="$BIN_DIR:$PATH" \
        claude --resume "$session_id" \
            -p "$message" \
            --dangerously-skip-permissions \
            --output-format json \
            --model claude-sonnet-4-5 \
            $mcp_flags \
        2>"$log_file.err" | tee "$log_file")

    local exit_code=$?

    if [ $exit_code -ne 0 ]; then
        log "    [inject] agent-$agent_num: session may be dead (exit $exit_code) — marking inactive"
        SESSION_ALIVE["$agent_num"]="false"
        return 1
    fi

    # Update session_id in case it changed on resume
    local new_sid
    new_sid=$(echo "$result" | python3 -c "
import json, sys
try:
    data = json.load(sys.stdin)
    print(data.get('session_id', ''))
except Exception:
    print('')
" 2>/dev/null || true)
    if [ -n "$new_sid" ]; then
        SESSION_IDS["$agent_num"]="$new_sid"
    fi

    return 0
}

# Send a round marker to the coordination campfire
send_round_marker() {
    local round="$1"
    local name
    name="$(round_name "$round")"
    local focus
    focus="$(round_focus "$round")"
    local msg

    msg="=== ROUND $round: ${name} ===
Focus: $focus
All architects: read this marker and shift to Round $round activities.
Your prior work and campfire history persist — this is a phase transition, not a reset."

    CF_HOME="$HARNESS_HOME" \
    CF_BEACON_DIR="$BEACON_DIR" \
    CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
    "$CF_BIN" send "$COORDINATION_ID" "$msg" --tag round-marker 2>/dev/null \
        && log "  Round $round marker sent to coordination campfire." \
        || log "  WARNING: Failed to send round $round marker (continuing anyway)."

    # Write timestamp file
    echo "$(date -u '+%Y-%m-%dT%H:%M:%SZ') Round $round ($(round_name "$round")) started." \
        > "$ROUNDS_DIR/round-$round.marker"
}

log ""
log "Starting persistent-session execution — agents run continuously across all rounds."
log "State diffs injected every ${POLL_INTERVAL}s when changes are detected."
log ""

START_TIME=$(date +%s)
TEST_DEADLINE=$(( START_TIME + OVERALL_TIMEOUT ))

# ── Phase 1: Send round 1 opening marker ──────────────────────────────────────
CF_HOME="$HARNESS_HOME" \
CF_BEACON_DIR="$BEACON_DIR" \
CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
"$CF_BIN" send "$COORDINATION_ID" \
    "=== ROUND 1: BUILD ===
Welcome, founding architects. This coordination campfire is your shared space.
Round 1 focus: Design and build your root infrastructure campfires.
Join this campfire. Post your architect introduction. Then build." \
    --tag round-marker 2>/dev/null \
    && log "Round 1 opening message sent." \
    || log "WARNING: Failed to send round 1 opening message."

echo "$(date -u '+%Y-%m-%dT%H:%M:%SZ') Round 1 (BUILD) started." \
    > "$ROUNDS_DIR/round-1.marker"

# ── Phase 1: Start all 9 architect sessions in parallel ───────────────────────
log "Launching all 9 architect sessions in parallel..."

declare -A START_PIDS
for i in 01 02 03 04 05 06 07 08 09; do
    start_agent_session "$i" "1" &
    START_PIDS["$i"]=$!
done

log "  Waiting for all session starts to complete..."
for i in 01 02 03 04 05 06 07 08 09; do
    wait "${START_PIDS[$i]}" 2>/dev/null || true
done

alive_count=0
for i in 01 02 03 04 05 06 07 08 09; do
    if [ "${SESSION_ALIVE[$i]}" = "true" ]; then
        alive_count=$(( alive_count + 1 ))
        log "  agent-$i: live (session=${SESSION_IDS[$i]})"
    else
        log "  agent-$i: FAILED to start session"
    fi
done
log "  $alive_count / 9 architect sessions live."

# ── Phase 2 & 3: Poller loop + round transitions ──────────────────────────────
#
# The poller loop tracks elapsed time to determine round boundaries.
# Round boundaries are computed from the start time + cumulative durations.

# Build cumulative round deadline array
declare -a ROUND_DEADLINES
cumulative=0
for round in $(seq 1 "$ROUNDS"); do
    rds="$(round_duration_secs "$round")"
    cumulative=$(( cumulative + rds ))
    ROUND_DEADLINES[$round]=$(( START_TIME + cumulative ))
done

# Track which round we're currently in
CURRENT_ROUND=1
NEWCOMER_STARTED=false

log ""
log "Entering poller loop. Overall deadline: $(date -d @"$TEST_DEADLINE" '+%H:%M:%S') (${OVERALL_TIMEOUT}s from now)."
log ""

while true; do
    now=$(date +%s)

    # Check overall timeout
    if (( now >= TEST_DEADLINE )); then
        log "Overall timeout reached (${OVERALL_TIMEOUT}s). Ending test."
        break
    fi

    # Check if we've completed all rounds
    if [ "$CURRENT_ROUND" -gt "$ROUNDS" ]; then
        log "All $ROUNDS rounds complete."
        break
    fi

    # Check for round transition
    round_deadline="${ROUND_DEADLINES[$CURRENT_ROUND]}"
    if (( now >= round_deadline )); then
        next_round=$(( CURRENT_ROUND + 1 ))
        if [ "$next_round" -le "$ROUNDS" ]; then
            log "━━━ Round transition: Round $CURRENT_ROUND → Round $next_round ━━━"
            send_round_marker "$next_round"

            # If entering round 5, initialize and start newcomer
            if [ "$next_round" -ge 5 ] && ! $NEWCOMER_STARTED; then
                log "  Initializing newcomer (agent-10)..."
                CF_HOME="$AGENTS_DIR/agent-10" "$CF_BIN" init > /dev/null

                key10=$(CF_HOME="$AGENTS_DIR/agent-10" "$CF_BIN" id --json \
                    | python3 -c "import json,sys; print(json.load(sys.stdin)['public_key'])")
                AGENT_KEYS["10"]="$key10"
                log "  agent-10: initialized (${key10:0:16}...)"

                inject_template "10"

                if grep -q '{{' "$AGENTS_DIR/agent-10/CLAUDE.md"; then
                    fail "Unreplaced placeholders in agent-10/CLAUDE.md"
                fi

                # Start newcomer as fresh session (not resumed)
                newcomer_prompt_file="$LOGS_DIR/agent-10-init-prompt.txt"
                build_newcomer_prompt "$next_round" > "$newcomer_prompt_file"

                newcomer_log="$LOGS_DIR/agent-10-init.log"
                newcomer_session_file="$LOGS_DIR/agent-10.session_id"

                systemd-run \
                    --user \
                    --scope \
                    --collect \
                    -- \
                    bash -c "
                        CF_HOME='$AGENTS_DIR/agent-10' \
                        CF_BEACON_DIR='$BEACON_DIR' \
                        CF_TRANSPORT_DIR='$TRANSPORT_DIR' \
                        PATH='$BIN_DIR:$PATH' \
                        claude -p \
                            --dangerously-skip-permissions \
                            --output-format json \
                            --model claude-sonnet-4-5 \
                            < '$newcomer_prompt_file' \
                            > '$newcomer_log' 2>&1
                    " &

                # Wait briefly for session_id
                waited=0
                while [ $waited -lt 60 ]; do
                    if [ -s "$newcomer_log" ]; then
                        sid10=$(python3 -c "
import json, sys
try:
    data = json.load(open('$newcomer_log'))
    print(data.get('session_id', ''))
except Exception:
    print('')
" 2>/dev/null || true)
                        if [ -n "$sid10" ]; then
                            echo "$sid10" > "$newcomer_session_file"
                            SESSION_IDS["10"]="$sid10"
                            SESSION_ALIVE["10"]="true"
                            log "  agent-10 (newcomer): session started (${sid10})"
                            break
                        fi
                    fi
                    sleep 2
                    waited=$(( waited + 2 ))
                done
                if [ "${SESSION_ALIVE[10]:-false}" != "true" ]; then
                    log "  agent-10: WARNING — failed to start newcomer session"
                fi

                NEWCOMER_STARTED=true
            fi

            # Inject round transition into all live architect sessions
            log "  Injecting round transition into live architect sessions..."
            for i in 01 02 03 04 05 06 07 08 09; do
                if [ "${SESSION_ALIVE[$i]}" = "true" ]; then
                    name="$(round_name "$next_round")"
                    focus="$(round_focus "$next_round")"
                    transition_msg="ROUND TRANSITION: Round $CURRENT_ROUND is now complete. Round $next_round (${name}) begins.
Focus: $focus

$(build_state_snapshot "$i")"
                    inject_into_session "$i" "$transition_msg" "round${next_round}-transition" &
                fi
            done
            wait 2>/dev/null || true

            CURRENT_ROUND="$next_round"
            log "  Round $CURRENT_ROUND now active."
        else
            CURRENT_ROUND="$next_round"  # Past last round
        fi

        sleep 5
        continue
    fi

    # Poll each live agent for state changes
    log "  [poll] Checking state diffs for live agents (round $CURRENT_ROUND)..."

    declare -a INJECT_PIDS
    INJECT_PIDS=()

    check_and_inject() {
        local agent_num="$1"
        if [ "${SESSION_ALIVE[$agent_num]}" != "true" ]; then
            return
        fi

        local new_beacons
        new_beacons="$(get_beacon_count "$agent_num")"
        local new_msgs
        new_msgs="$(get_msg_count "$agent_num")"

        local last_beacons="${LAST_BEACON_COUNT[$agent_num]:-0}"
        local last_msgs="${LAST_MSG_COUNT[$agent_num]:-0}"

        if [ "$new_beacons" != "$last_beacons" ] || [ "$new_msgs" != "$last_msgs" ]; then
            log "    [diff] agent-$agent_num: beacons $last_beacons→$new_beacons, msgs $last_msgs→$new_msgs — injecting update"

            local diff_msg="New campfire activity since your last update:
Beacons visible: $new_beacons (was $last_beacons)
Messages in your campfires: $new_msgs (was $last_msgs)

$(build_state_snapshot "$agent_num")"

            inject_into_session "$agent_num" "$diff_msg" "diff"

            LAST_BEACON_COUNT["$agent_num"]="$new_beacons"
            LAST_MSG_COUNT["$agent_num"]="$new_msgs"
        else
            log "    [diff] agent-$agent_num: no change (beacons=$new_beacons, msgs=$new_msgs) — not interrupting"
        fi
    }

    # Check all active agents
    all_inactive=true
    for i in 01 02 03 04 05 06 07 08 09; do
        if [ "${SESSION_ALIVE[$i]}" = "true" ]; then
            all_inactive=false
            check_and_inject "$i" &
            INJECT_PIDS+=($!)
        fi
    done
    # Check newcomer if started
    if $NEWCOMER_STARTED && [ "${SESSION_ALIVE[10]:-false}" = "true" ]; then
        all_inactive=false
        check_and_inject "10" &
        INJECT_PIDS+=($!)
    fi

    # Wait for all diff checks to finish
    for pid in "${INJECT_PIDS[@]:-}"; do
        wait "$pid" 2>/dev/null || true
    done

    if $all_inactive; then
        log "  All sessions inactive. Ending test early."
        break
    fi

    # Sleep until next poll
    remaining=$(( TEST_DEADLINE - $(date +%s) ))
    sleep_for=$POLL_INTERVAL
    if (( sleep_for > remaining )); then
        sleep_for=$remaining
    fi
    if (( sleep_for > 0 )); then
        sleep "$sleep_for"
    fi
done

TOTAL_TIME=$(( $(date +%s) - START_TIME ))
log "Execution complete in ${TOTAL_TIME}s."
log ""

# ─────────────────────────────────────────────────────────────────────────────
# Step 9: Verify
# ─────────────────────────────────────────────────────────────────────────────
log "Running verification..."

VERIFY_SCRIPT="$SCRIPT_DIR/verify_internet.sh"

if [ -f "$VERIFY_SCRIPT" ]; then
    CF_BIN="$CF_BIN" bash "$VERIFY_SCRIPT" "$INTEG_ROOT"
    VERIFY_EXIT=$?
else
    log "verify_internet.sh not found — running basic checks"
    VERIFY_EXIT=0

    # Basic sanity: at least some agent session init logs exist
    log_count=$(ls "$LOGS_DIR"/agent-*-init.log 2>/dev/null | wc -l || echo 0)
    if [ "$log_count" -eq 0 ]; then
        log "  FAIL: no agent session logs found in $LOGS_DIR"
        VERIFY_EXIT=1
    else
        log "  Found $log_count agent session log(s)."
    fi

    # Check session IDs captured
    session_count=$(ls "$LOGS_DIR"/*.session_id 2>/dev/null | wc -l || echo 0)
    log "  Session IDs captured: $session_count"

    # Check coordination ID
    if [ -f "$SHARED_DIR/coordination-id.txt" ] && [ -n "$(cat "$SHARED_DIR/coordination-id.txt")" ]; then
        log "  Coordination ID present: $(cat "$SHARED_DIR/coordination-id.txt")"
    else
        log "  WARNING: coordination-id.txt missing or empty"
    fi
fi

echo ""
if [ "$VERIFY_EXIT" -eq 0 ]; then
    log "PASS: agent internet test complete."
    log "  Total time:        ${TOTAL_TIME}s"
    log "  Agent logs:        $LOGS_DIR/"
    log "  Coordination ID:   $(cat "$SHARED_DIR/coordination-id.txt" 2>/dev/null || echo 'unknown')"
    log "  Workspace:         $WORKSPACE/"
    log "  Round markers:     $ROUNDS_DIR/"
else
    log "FAIL: verification failed (exit code $VERIFY_EXIT)."
    echo ""
    echo "=== Last 20 lines of each agent's session init log ==="
    for i in 01 02 03 04 05 06 07 08 09 10; do
        init_log="$LOGS_DIR/agent-${i}-init.log"
        if [ -f "$init_log" ]; then
            echo "--- $init_log ---"
            tail -20 "$init_log" || true
        else
            echo "--- agent-$i: (no init log found) ---"
        fi
    done
fi

exit "$VERIFY_EXIT"

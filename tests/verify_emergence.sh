#!/usr/bin/env bash
# verify_emergence.sh — post-run analysis and emergence report for 20-agent moltbook test
#
# Usage: bash tests/verify_emergence.sh [test-dir]
#   test-dir defaults to /tmp/campfire-emergence/
#
# Structural checks (harness errors) exit non-zero.
# Emergence checks never fail — every outcome is data.
# Calls topology-analysis.py and topology-viz.sh for deep analysis.
# Writes logs/emergence-verdict.txt and collects all RECAP.md into logs/all-recaps.txt.

set -uo pipefail

# ─────────────────────────────────────────────────────────────────────────────
# Setup
# ─────────────────────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_ROOT="${1:-/tmp/campfire-emergence}"
TEST_ROOT="${TEST_ROOT%/}"

AGENTS_DIR="$TEST_ROOT/agents"
SHARED_DIR="$TEST_ROOT/shared"
WORKSPACE_DIR="$TEST_ROOT/workspace"
LOGS_DIR="$TEST_ROOT/logs"
BEACON_DIR="$SHARED_DIR/beacons"
TRANSPORT_DIR="$SHARED_DIR/transport"

VERDICT_FILE="$LOGS_DIR/emergence-verdict.txt"
RECAPS_FILE="$LOGS_DIR/all-recaps.txt"
REPORT_FILE="$LOGS_DIR/emergence-report.json"
DOT_FILE="$LOGS_DIR/topology.dot"

# cf binary resolution (same logic as topology-viz.sh)
if [ -n "${CF_BIN:-}" ]; then
    CF="$CF_BIN"
elif [ -x "$TEST_ROOT/bin/cf" ]; then
    CF="$TEST_ROOT/bin/cf"
elif command -v cf >/dev/null 2>&1; then
    CF="cf"
else
    echo "ERROR: cf binary not found. Set CF_BIN or place cf at $TEST_ROOT/bin/cf" >&2
    exit 1
fi

# Ensure logs dir exists
mkdir -p "$LOGS_DIR"

# Counters for structural check failures
STRUCTURAL_FAILURES=0

structural_fail() {
    echo "STRUCTURAL FAIL: $1"
    STRUCTURAL_FAILURES=$((STRUCTURAL_FAILURES + 1))
}

structural_pass() {
    echo "STRUCTURAL PASS: $1"
}

note() {
    echo "NOTE: $1"
}

# ─────────────────────────────────────────────────────────────────────────────
# Helper: run cf for a specific agent
# ─────────────────────────────────────────────────────────────────────────────
cf_agent() {
    local agent="$1"
    shift
    CF_HOME="$AGENTS_DIR/$agent" \
    CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
    CF_BEACON_DIR="$BEACON_DIR" \
    "$CF" "$@" 2>/dev/null || true
}

# ─────────────────────────────────────────────────────────────────────────────
# Banner
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "╔══════════════════════════════════════════════════════════════════╗"
echo "║         Moltbook Emergence Test — Verification & Analysis        ║"
echo "╚══════════════════════════════════════════════════════════════════╝"
echo ""
echo "Test root: $TEST_ROOT"
echo "Timestamp: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 1: STRUCTURAL CHECKS (can fail — harness errors, not emergence outcomes)
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 1: Structural Checks"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Check 1: 20 identity.json files exist
echo "Check 1: Agent identity files (20 expected)..."
IDENTITY_COUNT=0
for i in $(seq -w 1 20); do
    agent="agent-$(printf '%02d' "$i")"
    if [ -f "$AGENTS_DIR/$agent/identity.json" ]; then
        IDENTITY_COUNT=$((IDENTITY_COUNT + 1))
    fi
done

if [ "$IDENTITY_COUNT" -eq 20 ]; then
    structural_pass "All 20 agents have identity.json"
else
    structural_fail "$IDENTITY_COUNT / 20 agents have identity.json — harness may have failed to initialize agents"
fi
echo ""

# Check 2: 20 CLAUDE.md files exist (agent templates expanded)
echo "Check 2: Agent CLAUDE.md files (20 expected)..."
CLAUDE_COUNT=0
for i in $(seq -w 1 20); do
    agent="agent-$(printf '%02d' "$i")"
    if [ -f "$AGENTS_DIR/$agent/CLAUDE.md" ]; then
        CLAUDE_COUNT=$((CLAUDE_COUNT + 1))
    fi
done

if [ "$CLAUDE_COUNT" -eq 20 ]; then
    structural_pass "All 20 agents have CLAUDE.md"
else
    structural_fail "$CLAUDE_COUNT / 20 agents have CLAUDE.md — template expansion may have failed"
fi
echo ""

# Check 3: Lobby campfire exists
echo "Check 3: Lobby campfire identity..."
LOBBY_ID=""
if [ -f "$SHARED_DIR/lobby-id.txt" ]; then
    LOBBY_ID="$(cat "$SHARED_DIR/lobby-id.txt" | tr -d '[:space:]')"
fi

if [ -n "$LOBBY_ID" ]; then
    structural_pass "Lobby campfire ID found: $LOBBY_ID"
else
    structural_fail "shared/lobby-id.txt missing or empty — harness did not create the lobby"
fi
echo ""

# Check 4: Lobby campfire has members (requires lobby to exist)
echo "Check 4: Lobby campfire membership..."
LOBBY_MEMBER_COUNT=0
if [ -n "$LOBBY_ID" ]; then
    # Try reading lobby members from any agent that might have joined
    for i in $(seq -w 1 20); do
        agent="agent-$(printf '%02d' "$i")"
        if [ -f "$AGENTS_DIR/$agent/identity.json" ]; then
            MEMBER_JSON="$(cf_agent "$agent" members "$LOBBY_ID" --json 2>/dev/null || true)"
            if [ -n "$MEMBER_JSON" ] && echo "$MEMBER_JSON" | python3 -c "import sys,json; data=json.load(sys.stdin); print(len(data))" >/dev/null 2>&1; then
                LOBBY_MEMBER_COUNT="$(echo "$MEMBER_JSON" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))")"
                break
            fi
        fi
    done

    if [ "$LOBBY_MEMBER_COUNT" -gt 0 ]; then
        structural_pass "Lobby has $LOBBY_MEMBER_COUNT member(s)"
    else
        note "Lobby has 0 members — silent network outcome (valid data, not a harness error)"
    fi
else
    note "Skipping lobby membership check — lobby ID unknown"
fi
echo ""

# Check 5: At least 1 agent-created campfire beyond lobby
echo "Check 5: Agent-created campfires (beyond lobby)..."
BEACON_COUNT=0
AGENT_BEACON_COUNT=0
if [ -d "$BEACON_DIR" ]; then
    BEACON_COUNT="$(ls "$BEACON_DIR"/*.json 2>/dev/null | wc -l | tr -d ' ')"
    # Subtract 1 for the lobby (harness-created)
    AGENT_BEACON_COUNT=$((BEACON_COUNT > 0 ? BEACON_COUNT - 1 : 0))
fi

if [ "$BEACON_COUNT" -gt 0 ]; then
    structural_pass "$BEACON_COUNT total beacon(s) found in shared beacon directory ($AGENT_BEACON_COUNT agent-created)"
else
    note "No beacons found in $BEACON_DIR — beacon directory may be empty or in unexpected location"
fi
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 2: EMERGENCE METRICS (never fail — report findings)
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 2: Emergence Metrics"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Metric: Agents who joined the lobby
echo "Metric: Agents who joined lobby..."
LOBBY_JOINERS=0
if [ -n "$LOBBY_ID" ]; then
    LOBBY_JOINERS="$LOBBY_MEMBER_COUNT"
fi
echo "  Lobby members: $LOBBY_JOINERS / 20"
echo ""

# Metric: Total distinct campfires across all agent stores
echo "Metric: Total campfires (distinct IDs across all agent stores)..."
ALL_CAMPFIRE_IDS=""
AGENTS_WITH_CF=0
for i in $(seq -w 1 20); do
    agent="agent-$(printf '%02d' "$i")"
    if [ ! -f "$AGENTS_DIR/$agent/identity.json" ]; then continue; fi

    LS_JSON="$(cf_agent "$agent" ls --json 2>/dev/null || true)"
    if [ -z "$LS_JSON" ] || [ "$LS_JSON" = "[]" ] || [ "$LS_JSON" = "null" ]; then
        continue
    fi

    IDS="$(echo "$LS_JSON" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    for item in data:
        cf_id = item.get('campfire_id') or item.get('id') or ''
        if cf_id:
            print(cf_id)
except Exception:
    pass
" 2>/dev/null || true)"

    if [ -n "$IDS" ]; then
        AGENTS_WITH_CF=$((AGENTS_WITH_CF + 1))
        ALL_CAMPFIRE_IDS="$ALL_CAMPFIRE_IDS
$IDS"
    fi
done

DISTINCT_CAMPFIRES=0
if [ -n "$ALL_CAMPFIRE_IDS" ]; then
    DISTINCT_CAMPFIRES="$(echo "$ALL_CAMPFIRE_IDS" | sort -u | grep -c . || true)"
fi
AGENT_CAMPFIRES=$((DISTINCT_CAMPFIRES > 1 ? DISTINCT_CAMPFIRES - 1 : 0))
echo "  Total distinct campfires: $DISTINCT_CAMPFIRES (lobby + $AGENT_CAMPFIRES agent-created)"
echo "  Agents with any campfire membership: $AGENTS_WITH_CF / 20"
echo ""

# Metric: Agents who never used cf
AGENTS_WITHOUT_CF=$((20 - AGENTS_WITH_CF))
echo "Metric: Agents who never used cf..."
echo "  Agents with no campfire memberships: $AGENTS_WITHOUT_CF / 20"
if [ "$AGENTS_WITHOUT_CF" -gt 0 ]; then
    echo "  Agents with no activity:"
    for i in $(seq -w 1 20); do
        agent="agent-$(printf '%02d' "$i")"
        if [ ! -f "$AGENTS_DIR/$agent/identity.json" ]; then continue; fi
        LS_JSON="$(cf_agent "$agent" ls --json 2>/dev/null || true)"
        if [ -z "$LS_JSON" ] || [ "$LS_JSON" = "[]" ] || [ "$LS_JSON" = "null" ]; then
            echo "    - $agent"
        fi
    done
fi
echo ""

# Metric: Total messages and cross-domain interactions
echo "Metric: Message counts and cross-domain interactions..."

# Read emergence-report.json if topology-analysis.py has been run separately,
# otherwise we compute minimally here. Full analysis runs below.
TOTAL_MESSAGES=0
CROSS_DOMAIN_MSGS=0
DM_CAMPFIRES=0
FUTURES_POSTED=0
FULFILLMENTS_POSTED=0

# Scan beacons for 2-member (DM) campfires
if [ -d "$BEACON_DIR" ]; then
    for beacon_file in "$BEACON_DIR"/*.json; do
        [ -f "$beacon_file" ] || continue
        MEMBER_COUNT_IN_BEACON="$(python3 -c "
import json, sys
try:
    data = json.load(open('$beacon_file'))
    # Try max_members or member count hints in beacon
    mm = data.get('max_members', 0)
    print(mm if mm else 0)
except Exception:
    print(0)
" 2>/dev/null || echo 0)"
        if [ "$MEMBER_COUNT_IN_BEACON" -eq 2 ]; then
            DM_CAMPFIRES=$((DM_CAMPFIRES + 1))
        fi
    done
fi

# Count messages and futures from transport directory
if [ -d "$TRANSPORT_DIR" ]; then
    TOTAL_MESSAGES="$(find "$TRANSPORT_DIR" -name '*.msg' -o -name '*.cbor' 2>/dev/null | wc -l | tr -d ' ')" || TOTAL_MESSAGES=0

    # Scan for futures and fulfillments in message files
    FUTURES_POSTED="$(find "$TRANSPORT_DIR" -name '*.msg' -o -name '*.cbor' 2>/dev/null | xargs grep -l '"future"' 2>/dev/null | wc -l | tr -d ' ')" || FUTURES_POSTED=0
    FULFILLMENTS_POSTED="$(find "$TRANSPORT_DIR" -name '*.msg' -o -name '*.cbor' 2>/dev/null | xargs grep -l '"fulfills"' 2>/dev/null | wc -l | tr -d ' ')" || FULFILLMENTS_POSTED=0
fi

echo "  Total messages (transport files): $TOTAL_MESSAGES"
echo "  DM campfires detected (2-member): $DM_CAMPFIRES"
echo "  Messages tagged 'future': $FUTURES_POSTED"
echo "  Messages tagged 'fulfills': $FULFILLMENTS_POSTED"
echo ""

# Metric: Convention emergence — shared tag patterns
echo "Metric: Convention emergence (shared tag patterns across 3+ agents)..."
CONVENTIONS_FOUND=()

if [ -d "$TRANSPORT_DIR" ]; then
    # Look for structured prefixes used by multiple agents
    for PREFIX in "[NEED]" "[HAVE]" "[Q]" "[FYI]" "[ASK]" "[OFFER]" "[UPDATE]" "[BLOCKED]" "[URGENT]"; do
        COUNT="$(find "$TRANSPORT_DIR" -name '*.msg' -o -name '*.cbor' 2>/dev/null | xargs grep -l "$PREFIX" 2>/dev/null | wc -l | tr -d ' ')" || COUNT=0
        if [ "$COUNT" -ge 3 ]; then
            CONVENTIONS_FOUND+=("$PREFIX (seen in $COUNT messages)")
        fi
    done

    # Look for domain tags
    for TAG in "finance" "legal" "product" "marketing" "hr" "sales" "ops" "research" "support"; do
        COUNT="$(find "$TRANSPORT_DIR" -name '*.msg' -o -name '*.cbor' 2>/dev/null | xargs grep -l "\"$TAG\"" 2>/dev/null | wc -l | tr -d ' ')" || COUNT=0
        if [ "$COUNT" -ge 3 ]; then
            CONVENTIONS_FOUND+=("[tag:$TAG] (seen in $COUNT messages)")
        fi
    done
fi

if [ ${#CONVENTIONS_FOUND[@]} -gt 0 ]; then
    echo "  Emergent conventions detected:"
    for c in "${CONVENTIONS_FOUND[@]}"; do
        echo "    - $c"
    done
else
    echo "  No shared conventions detected (prefixes/tags used by 3+ agents)"
fi
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 3: RECAP.md PARSING
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 3: RECAP.md Analysis"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

echo "Collecting RECAP.md files..."
# Clear and start fresh
> "$RECAPS_FILE"

RECAPS_FOUND=0
RECAPS_MENTIONING_CF=0
RECAPS_WITH_COLLABORATORS=0
DONE_COUNT=0

for i in $(seq -w 1 20); do
    agent="agent-$(printf '%02d' "$i")"

    # Check DONE.txt
    if [ -f "$WORKSPACE_DIR/$agent/DONE.txt" ] || [ -f "$AGENTS_DIR/$agent/DONE.txt" ]; then
        DONE_COUNT=$((DONE_COUNT + 1))
    fi

    # Collect RECAP.md
    RECAP=""
    if [ -f "$WORKSPACE_DIR/$agent/RECAP.md" ]; then
        RECAP="$WORKSPACE_DIR/$agent/RECAP.md"
    elif [ -f "$AGENTS_DIR/$agent/RECAP.md" ]; then
        RECAP="$AGENTS_DIR/$agent/RECAP.md"
    fi

    if [ -n "$RECAP" ]; then
        RECAPS_FOUND=$((RECAPS_FOUND + 1))
        echo "=== RECAP: $agent ===" >> "$RECAPS_FILE"
        cat "$RECAP" >> "$RECAPS_FILE"
        echo "" >> "$RECAPS_FILE"
        echo "---" >> "$RECAPS_FILE"
        echo "" >> "$RECAPS_FILE"

        # Check if recap mentions cf or campfire in "Tools I used" section
        if grep -qi "cf\|campfire" "$RECAP" 2>/dev/null; then
            RECAPS_MENTIONING_CF=$((RECAPS_MENTIONING_CF + 1))
        fi

        # Check if "Who I talked to" is not "Nobody" or empty
        TALKED_TO="$(grep -i "Who I talked to" "$RECAP" -A 1 2>/dev/null | tail -1 | tr -d ' \t' || true)"
        if [ -n "$TALKED_TO" ] && [ "$TALKED_TO" != "Nobody" ] && [ "$TALKED_TO" != "nobody" ] && [ "$TALKED_TO" != "-" ] && [ "$TALKED_TO" != "N/A" ]; then
            RECAPS_WITH_COLLABORATORS=$((RECAPS_WITH_COLLABORATORS + 1))
        fi
    fi
done

echo "  Tasks completed (DONE.txt): $DONE_COUNT / 20"
echo "  RECAPs written:             $RECAPS_FOUND / 20"
echo "  RECAPs mentioning cf:       $RECAPS_MENTIONING_CF"
echo "  RECAPs with collaborators:  $RECAPS_WITH_COLLABORATORS"
if [ "$RECAPS_FOUND" -gt 0 ]; then
    echo "  All RECAPs collected in:    $RECAPS_FILE"
fi
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 4: DEEP ANALYSIS — topology-analysis.py and topology-viz.sh
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 4: Deep Analysis (topology scripts)"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Run topology-analysis.py
TOPO_ANALYSIS="$SCRIPT_DIR/topology-analysis.py"
if [ -f "$TOPO_ANALYSIS" ]; then
    echo "Running topology-analysis.py..."
    if python3 "$TOPO_ANALYSIS" "$TEST_ROOT" 2>&1; then
        echo ""
        echo "  topology-analysis.py completed."
    else
        echo ""
        echo "  WARNING: topology-analysis.py exited with errors — continuing anyway"
    fi
else
    echo "  WARNING: tests/topology-analysis.py not found at $TOPO_ANALYSIS — skipping"
fi
echo ""

# Run topology-viz.sh
TOPO_VIZ="$SCRIPT_DIR/topology-viz.sh"
if [ -f "$TOPO_VIZ" ]; then
    echo "Running topology-viz.sh..."
    if bash "$TOPO_VIZ" "$TEST_ROOT" > "$DOT_FILE" 2>&1; then
        DOT_LINES="$(wc -l < "$DOT_FILE" | tr -d ' ')"
        echo "  Topology DOT graph written to $DOT_FILE ($DOT_LINES lines)"
        # Attempt PNG rendering if dot is available
        if command -v dot >/dev/null 2>&1; then
            PNG_FILE="${DOT_FILE%.dot}.png"
            if dot -Tpng "$DOT_FILE" -o "$PNG_FILE" 2>/dev/null; then
                echo "  Topology PNG rendered: $PNG_FILE"
            fi
        else
            echo "  (graphviz not available — install it to render PNG from $DOT_FILE)"
        fi
    else
        echo "  WARNING: topology-viz.sh exited with errors — continuing anyway"
    fi
else
    echo "  WARNING: tests/topology-viz.sh not found at $TOPO_VIZ — skipping"
fi
echo ""

# Reload emergence-report.json if topology-analysis.py wrote it
if [ -f "$REPORT_FILE" ]; then
    echo "Reading emergence-report.json for classification..."
    REPORT_DATA="$(cat "$REPORT_FILE")"

    # Extract key metrics from report (override our manual counts with authoritative data)
    LOBBY_MEMBER_COUNT_REPORT="$(echo "$REPORT_DATA" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get('lobby_membership_count', d.get('lobby_members', 'unknown')))
except Exception:
    print('unknown')
" 2>/dev/null || echo "unknown")"

    TOTAL_CAMPFIRES_REPORT="$(echo "$REPORT_DATA" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get('total_campfires_created', d.get('agent_campfires_created', 'unknown')))
except Exception:
    print('unknown')
" 2>/dev/null || echo "unknown")"

    FUTURES_REPORT="$(echo "$REPORT_DATA" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get('futures_posted', 'unknown'))
except Exception:
    print('unknown')
" 2>/dev/null || echo "unknown")"

    DM_REPORT="$(echo "$REPORT_DATA" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get('dm_campfires_created', 'unknown'))
except Exception:
    print('unknown')
" 2>/dev/null || echo "unknown")"

    # Use report values where available (they're more accurate)
    if [ "$LOBBY_MEMBER_COUNT_REPORT" != "unknown" ]; then
        LOBBY_JOINERS="$LOBBY_MEMBER_COUNT_REPORT"
    fi
    if [ "$TOTAL_CAMPFIRES_REPORT" != "unknown" ]; then
        AGENT_CAMPFIRES="$TOTAL_CAMPFIRES_REPORT"
    fi
    if [ "$FUTURES_REPORT" != "unknown" ]; then
        FUTURES_POSTED="$FUTURES_REPORT"
    fi
    if [ "$DM_REPORT" != "unknown" ]; then
        DM_CAMPFIRES="$DM_REPORT"
    fi

    echo "  Metrics updated from emergence-report.json"
fi
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 5: SCENARIO CLASSIFICATION
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 5: Scenario Classification"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Normalize to integers for classification (default to 0 if non-numeric)
LOBBY_N="$(echo "$LOBBY_JOINERS" | grep -E '^[0-9]+$' || echo 0)"
AGENT_CF_N="$(echo "$AGENT_CAMPFIRES" | grep -E '^[0-9]+$' || echo 0)"
FUTURES_N="$(echo "$FUTURES_POSTED" | grep -E '^[0-9]+$' || echo 0)"
DM_N="$(echo "$DM_CAMPFIRES" | grep -E '^[0-9]+$' || echo 0)"

SCENARIO=""
SCENARIO_DESC=""

# Classification rules (from design doc Pass 3 + bead spec):
#
# The design doc has 6 scenarios (A-F), bead spec collapses to A/B/C/D
# with some additional signals. We use the design doc labels (A-F) which
# are richer, mapping bead spec's "Active Minority" to C and "Hub" to F.
#
# Priority: check most specific/distinctive first.

if [ "$LOBBY_N" -le 2 ] && [ "$AGENTS_WITH_CF" -le 2 ]; then
    # Nobody used cf at all
    SCENARIO="F"
    SCENARIO_DESC="Silent Network — nearly no agent used cf. The lobby exists but is empty or nearly empty."

elif [ "$LOBBY_N" -ge 15 ]; then
    # High lobby membership
    if [ "$FUTURES_N" -gt 0 ] || [ ${#CONVENTIONS_FOUND[@]} -gt 0 ]; then
        SCENARIO="A+E"
        SCENARIO_DESC="Bustling Lobby with Emergent Conventions — $LOBBY_N agents in lobby, structured patterns (futures or shared tags) emerged spontaneously."
    else
        SCENARIO="A"
        SCENARIO_DESC="Bustling Lobby — $LOBBY_N agents joined the central lobby. Rich exchange, high participation."
    fi

elif [ "$AGENT_CF_N" -ge 4 ]; then
    # Many agent-created campfires (domain clusters or hub-and-spoke)
    # Detect hub: check if any single agent is in many campfires
    MAX_MEMBERSHIPS=0
    HUB_AGENT=""
    for i in $(seq -w 1 20); do
        agent="agent-$(printf '%02d' "$i")"
        if [ ! -f "$AGENTS_DIR/$agent/identity.json" ]; then continue; fi
        LS_JSON="$(cf_agent "$agent" ls --json 2>/dev/null || true)"
        if [ -n "$LS_JSON" ] && [ "$LS_JSON" != "[]" ]; then
            COUNT="$(echo "$LS_JSON" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo 0)"
            if [ "$COUNT" -gt "$MAX_MEMBERSHIPS" ]; then
                MAX_MEMBERSHIPS="$COUNT"
                HUB_AGENT="$agent"
            fi
        fi
    done

    if [ "$MAX_MEMBERSHIPS" -ge 5 ]; then
        SCENARIO="C_HUB"
        SCENARIO_DESC="Hub-and-Spoke — $HUB_AGENT is in $MAX_MEMBERSHIPS campfires, acting as central coordinator. $AGENT_CF_N agent-created campfires."
    else
        SCENARIO="B"
        SCENARIO_DESC="Domain Clusters — $AGENT_CF_N agent-created campfires with $LOBBY_N lobby members. Agents partitioned by domain with multi-campfire bridges."
    fi

elif [ "$LOBBY_N" -ge 3 ] && [ "$LOBBY_N" -le 14 ]; then
    SCENARIO="C"
    SCENARIO_DESC="Active Minority — $LOBBY_N agents joined the lobby, $((20 - AGENTS_WITH_CF)) stayed silent. Campfire adoption driven by cross-domain need."

elif [ "$AGENTS_WITH_CF" -ge 2 ] && [ "$LOBBY_N" -le 2 ]; then
    # Some agents used cf but didn't join lobby — created private campfires or DMs
    SCENARIO="E"
    SCENARIO_DESC="Isolated Islands — agents created campfires ($AGENT_CF_N beyond lobby) but minimal lobby activity. Domain campfires with little cross-talk."

else
    SCENARIO="unclear"
    SCENARIO_DESC="Outcome unclear — metrics don't cleanly fit a named scenario. Manual analysis recommended."
fi

echo "Classified outcome:"
echo ""
echo "  Scenario: $SCENARIO"
echo "  Description: $SCENARIO_DESC"
echo ""

# Print scenario reference
echo "Scenario reference:"
echo "  A      — Bustling Lobby      (15+ agents, rich lobby exchange)"
echo "  A+E    — Bustling + Conventions (A with emergent tagging/futures)"
echo "  B      — Domain Clusters     (4+ agent campfires, domain partitioning)"
echo "  C      — Active Minority     (3-14 lobby members, rest silent)"
echo "  C_HUB  — Hub-and-Spoke       (one agent in 5+ campfires = coordinator)"
echo "  D      — Sparse/Connected    (few messages, some cross-domain)"
echo "  E      — Isolated Islands    (campfires created, no cross-talk)"
echo "  F      — Silent Network      (nobody or almost nobody used cf)"
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 6: SUMMARY REPORT
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 6: Summary"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

SUMMARY="$(cat <<SUMMARY_EOF
=== Emergence Test Results ===
Timestamp:            $(date -u '+%Y-%m-%d %H:%M:%S UTC')
Test root:            $TEST_ROOT

--- Structural ---
Agents initialized:   $IDENTITY_COUNT / 20
Templates expanded:   $CLAUDE_COUNT / 20
Tasks completed:      $DONE_COUNT / 20
Lobby ID:             ${LOBBY_ID:-unknown}

--- Emergence Metrics ---
Lobby membership:     $LOBBY_JOINERS / 20 agents
Campfires created:    $AGENT_CF_N (by agents; +1 lobby = total)
Total messages:       $TOTAL_MESSAGES
Agents who used cf:   $AGENTS_WITH_CF / 20
Cross-domain msgs:    $CROSS_DOMAIN_MSGS
DM campfires:         $DM_CAMPFIRES
Futures posted:       $FUTURES_POSTED
Fulfillments posted:  $FULFILLMENTS_POSTED
Recaps written:       $RECAPS_FOUND / 20
Recaps mentioning cf: $RECAPS_MENTIONING_CF
Recaps w/ collaborators: $RECAPS_WITH_COLLABORATORS

--- Conventions ---
Emergent patterns:    ${#CONVENTIONS_FOUND[@]}$([ ${#CONVENTIONS_FOUND[@]} -gt 0 ] && printf '\n  - %s' "${CONVENTIONS_FOUND[@]}" || echo "")

--- Classification ---
Scenario:             $SCENARIO
Summary:              $SCENARIO_DESC

--- Artifacts ---
Verdict file:         $VERDICT_FILE
All RECAPs:           $RECAPS_FILE
Emergence report:     $REPORT_FILE
Topology DOT:         $DOT_FILE
SUMMARY_EOF
)"

echo "$SUMMARY"
echo ""

# Write verdict file
echo "$SUMMARY" > "$VERDICT_FILE"
echo "Verdict written to $VERDICT_FILE"
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 7: STRUCTURAL FAILURE SUMMARY
# ─────────────────────────────────────────────────────────────────────────────
if [ "$STRUCTURAL_FAILURES" -gt 0 ]; then
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "WARNING: $STRUCTURAL_FAILURES structural check(s) failed."
    echo "These indicate harness errors, not emergence outcomes."
    echo "Review the STRUCTURAL FAIL messages above before interpreting results."
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    exit 1
fi

echo "All structural checks passed. Analysis complete."
echo ""
echo "Every outcome teaches us something. See $VERDICT_FILE for the full report."
echo ""
exit 0

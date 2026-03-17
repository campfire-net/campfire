#!/usr/bin/env bash
# verify_internet.sh — post-run analysis and infrastructure report for the agent internet test
#
# Usage: bash tests/verify_internet.sh [test-dir]
#   test-dir defaults to /tmp/campfire-internet/
#
# Structural checks (harness errors) exit non-zero.
# Infrastructure checks never fail — every outcome is data.
# Calls topology-analysis.py and topology-viz.sh for deep analysis.
# Writes logs/internet-verdict.txt and logs/infrastructure-report.json.

set -uo pipefail

# ─────────────────────────────────────────────────────────────────────────────
# Setup
# ─────────────────────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_ROOT="${1:-/tmp/campfire-internet}"
TEST_ROOT="${TEST_ROOT%/}"

AGENTS_DIR="$TEST_ROOT/agents"
SHARED_DIR="$TEST_ROOT/shared"
WORKSPACE_DIR="$SHARED_DIR/workspace"
LOGS_DIR="$TEST_ROOT/logs"
BEACON_DIR="$SHARED_DIR/beacons"
TRANSPORT_DIR="$SHARED_DIR/transport"

VERDICT_FILE="$LOGS_DIR/internet-verdict.txt"
REPORT_FILE="$LOGS_DIR/infrastructure-report.json"
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
echo "║      Agent Internet Test — Infrastructure Verification Report    ║"
echo "╚══════════════════════════════════════════════════════════════════╝"
echo ""
echo "Test root: $TEST_ROOT"
echo "Timestamp: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 1: STRUCTURAL CHECKS (can fail — harness errors, not experimental outcomes)
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 1: Structural Checks"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Check 1: Architect identity files (agent-01 through agent-09)
echo "Check 1: Architect identity files (9 expected)..."
IDENTITY_COUNT=0
for i in $(seq -w 1 9); do
    agent="agent-$(printf '%02d' "$i")"
    if [ -f "$AGENTS_DIR/$agent/identity.json" ]; then
        IDENTITY_COUNT=$((IDENTITY_COUNT + 1))
    fi
done

if [ "$IDENTITY_COUNT" -eq 9 ]; then
    structural_pass "All 9 architect agents have identity.json"
else
    structural_fail "$IDENTITY_COUNT / 9 architect agents have identity.json — harness may have failed to initialize"
fi
echo ""

# Check 2: At least one log file exists
echo "Check 2: Agent log files..."
LOG_COUNT=0
if [ -d "$LOGS_DIR" ]; then
    LOG_COUNT="$(ls "$LOGS_DIR"/*.log 2>/dev/null | wc -l | tr -d ' ')" || LOG_COUNT=0
fi

if [ "$LOG_COUNT" -gt 0 ]; then
    structural_pass "$LOG_COUNT log file(s) found in $LOGS_DIR"
else
    note "No .log files found in $LOGS_DIR — agents may not have run yet"
fi
echo ""

# Check 3: Shared workspace exists and has architect subdirectories
echo "Check 3: Shared workspace structure..."
WORKSPACE_SUBDIR_COUNT=0
if [ -d "$WORKSPACE_DIR" ]; then
    WORKSPACE_SUBDIR_COUNT="$(ls -d "$WORKSPACE_DIR"/*/ 2>/dev/null | wc -l | tr -d ' ')" || WORKSPACE_SUBDIR_COUNT=0
fi

if [ "$WORKSPACE_SUBDIR_COUNT" -gt 0 ]; then
    structural_pass "Shared workspace exists with $WORKSPACE_SUBDIR_COUNT subdirectorie(s)"
else
    note "Shared workspace at $WORKSPACE_DIR has no subdirectories — agents may not have written design docs"
fi
echo ""

# Check 4: Beacon directory exists
echo "Check 4: Beacon directory..."
BEACON_COUNT=0
if [ -d "$BEACON_DIR" ]; then
    BEACON_COUNT="$(ls "$BEACON_DIR"/*.json 2>/dev/null | wc -l | tr -d ' ')" || BEACON_COUNT=0
    structural_pass "Beacon directory exists with $BEACON_COUNT beacon(s)"
else
    structural_fail "Beacon directory $BEACON_DIR does not exist — harness setup may have failed"
fi
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 2: INFRASTRUCTURE DOMAIN CHECKS
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 2: Root Domain Infrastructure Checks"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "Checking for each of the 9 root domain campfires via beacon descriptions..."
echo ""

# Read all beacon files once and extract descriptions
BEACON_DESCRIPTIONS=""
if [ -d "$BEACON_DIR" ]; then
    for beacon_file in "$BEACON_DIR"/*.json; do
        [ -f "$beacon_file" ] || continue
        DESC="$(python3 -c "
import json, sys
try:
    data = json.load(open('$beacon_file'))
    desc = data.get('description', '') or data.get('name', '') or data.get('title', '')
    print(desc.lower())
except Exception:
    pass
" 2>/dev/null || true)"
        if [ -n "$DESC" ]; then
            BEACON_DESCRIPTIONS="$BEACON_DESCRIPTIONS
$DESC"
        fi
    done
fi

# Also scan workspace files for evidence of campfire creation
WORKSPACE_CONTENT=""
if [ -d "$WORKSPACE_DIR" ]; then
    WORKSPACE_CONTENT="$(find "$WORKSPACE_DIR" -type f \( -name '*.md' -o -name '*.txt' -o -name '*.json' \) 2>/dev/null | head -50 | xargs grep -hi '' 2>/dev/null | tr '[:upper:]' '[:lower:]' || true)"
fi

# Also scan transport messages for descriptions
TRANSPORT_CONTENT=""
if [ -d "$TRANSPORT_DIR" ]; then
    TRANSPORT_CONTENT="$(find "$TRANSPORT_DIR" -name '*.msg' -o -name '*.cbor' 2>/dev/null | head -200 | xargs strings 2>/dev/null | tr '[:upper:]' '[:lower:]' || true)"
fi

COMBINED_CONTENT="$BEACON_DESCRIPTIONS
$WORKSPACE_CONTENT
$TRANSPORT_CONTENT"

check_domain() {
    local domain_name="$1"
    local status="MISSING"
    shift
    local patterns=("$@")

    for pattern in "${patterns[@]}"; do
        if echo "$COMBINED_CONTENT" | grep -qi "$pattern" 2>/dev/null; then
            status="PRESENT"
            break
        fi
    done

    echo "  $domain_name: $status"
    echo "$status"
}

DOMAINS_PRESENT=()
DOMAINS_MISSING=()

# 1. Directory
echo -n ""
RESULT="$(check_domain "Directory" "root directory" "agent internet.*directory" "directory campfire" "cf discover" "discovery infrastructure" "indexing" "hierarchical directory" | tail -1)"
[ "$RESULT" = "PRESENT" ] && DOMAINS_PRESENT+=("directory") || DOMAINS_MISSING+=("directory")

# 2. Trust
RESULT="$(check_domain "Trust" "trust campfire" "trust.*reputation" "vouching protocol" "trust assessment" "trust architect" "trust system" "web of trust" | tail -1)"
[ "$RESULT" = "PRESENT" ] && DOMAINS_PRESENT+=("trust") || DOMAINS_MISSING+=("trust")

# 3. Tool Registry
RESULT="$(check_domain "Tool Registry" "tool registry" "tool campfire" "capability.*discover" "tool listing" "tool registry architect" "tool.*rank" "register.*tool" | tail -1)"
[ "$RESULT" = "PRESENT" ] && DOMAINS_PRESENT+=("tool-registry") || DOMAINS_MISSING+=("tool-registry")

# 4. Security
RESULT="$(check_domain "Security" "security intel" "threat intelligence" "security campfire" "attack report" "sybil" "threat data" "security architect" "blocklist" | tail -1)"
[ "$RESULT" = "PRESENT" ] && DOMAINS_PRESENT+=("security") || DOMAINS_MISSING+=("security")

# 5. Governance
RESULT="$(check_domain "Governance" "governance campfire" "proposal" "voting" "governance architect" "constitutional" "ratif" "decentralized governance" | tail -1)"
[ "$RESULT" = "PRESENT" ] && DOMAINS_PRESENT+=("governance") || DOMAINS_MISSING+=("governance")

# 6. Onboarding
RESULT="$(check_domain "Onboarding" "onboard" "welcome campfire" "bootstrap path" "new agent" "onboarding architect" "zero-to-connected" "start here" | tail -1)"
[ "$RESULT" = "PRESENT" ] && DOMAINS_PRESENT+=("onboarding") || DOMAINS_MISSING+=("onboarding")

# 7. Filter
RESULT="$(check_domain "Filter" "filter pattern" "filter campfire" "signal.*noise" "filter architect" "community filter" "noise detection" "filter config" | tail -1)"
[ "$RESULT" = "PRESENT" ] && DOMAINS_PRESENT+=("filter") || DOMAINS_MISSING+=("filter")

# 8. Stress Test / Red Team
RESULT="$(check_domain "Stress Test" "red team" "stress test" "attack report" "adversarial" "stress test architect" "attack plan" "vulnerability" | tail -1)"
[ "$RESULT" = "PRESENT" ] && DOMAINS_PRESENT+=("stress-test") || DOMAINS_MISSING+=("stress-test")

# 9. Interop
RESULT="$(check_domain "Interop" "interop" "bridge campfire" "cross-transport" "interop architect" "transport bridge" "relay" | tail -1)"
[ "$RESULT" = "PRESENT" ] && DOMAINS_PRESENT+=("interop") || DOMAINS_MISSING+=("interop")

DOMAIN_COUNT="${#DOMAINS_PRESENT[@]}"
echo ""
echo "  Domains present: $DOMAIN_COUNT / 9"
echo "  Present: ${DOMAINS_PRESENT[*]:-none}"
echo "  Missing: ${DOMAINS_MISSING[*]:-none}"
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 3: BEACON DISCOVERABILITY
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 3: Beacon Discoverability"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

echo "Checking whether root campfires have beacons with descriptions..."
echo ""

BEACONS_WITH_DESCRIPTIONS=0
BEACONS_WITHOUT_DESCRIPTIONS=0

if [ -d "$BEACON_DIR" ]; then
    for beacon_file in "$BEACON_DIR"/*.json; do
        [ -f "$beacon_file" ] || continue
        HAS_DESC="$(python3 -c "
import json, sys
try:
    data = json.load(open('$beacon_file'))
    desc = data.get('description', '') or data.get('name', '') or data.get('title', '')
    print('yes' if desc and len(desc.strip()) > 10 else 'no')
except Exception:
    print('no')
" 2>/dev/null || echo "no")"
        if [ "$HAS_DESC" = "yes" ]; then
            BEACONS_WITH_DESCRIPTIONS=$((BEACONS_WITH_DESCRIPTIONS + 1))
        else
            BEACONS_WITHOUT_DESCRIPTIONS=$((BEACONS_WITHOUT_DESCRIPTIONS + 1))
        fi
    done
fi

echo "  Total beacons:               $BEACON_COUNT"
echo "  Beacons with descriptions:   $BEACONS_WITH_DESCRIPTIONS"
echo "  Beacons without descriptions: $BEACONS_WITHOUT_DESCRIPTIONS"

if [ "$BEACON_COUNT" -gt 0 ] && [ "$BEACONS_WITH_DESCRIPTIONS" -gt 0 ]; then
    BEACON_DESC_RATIO=$(( BEACONS_WITH_DESCRIPTIONS * 100 / BEACON_COUNT ))
    echo "  Description coverage: ${BEACON_DESC_RATIO}%"
    echo ""
    echo "  Sample beacon descriptions:"
    if [ -d "$BEACON_DIR" ]; then
        python3 -c "
import json, os, glob
beacons = glob.glob('$BEACON_DIR/*.json')
for b in sorted(beacons)[:6]:
    try:
        data = json.load(open(b))
        desc = data.get('description', '') or data.get('name', '') or data.get('title', '')
        if desc:
            print(f'  - {desc[:80]}')
    except Exception:
        pass
" 2>/dev/null || true
    fi
fi
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 4: NEWCOMER BOOTSTRAP CHECK (agent-10)
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 4: Newcomer Bootstrap (agent-10)"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

AGENT10_DIR="$AGENTS_DIR/agent-10"
NEWCOMER_IDENTITY=false
NEWCOMER_CAMPFIRES=0
NEWCOMER_BOOTSTRAPPED=false

if [ -f "$AGENT10_DIR/identity.json" ]; then
    NEWCOMER_IDENTITY=true
    echo "  agent-10 identity: PRESENT"

    # Count campfires agent-10 joined
    LS_JSON="$(cf_agent "agent-10" ls --json 2>/dev/null || true)"
    if [ -n "$LS_JSON" ] && [ "$LS_JSON" != "[]" ] && [ "$LS_JSON" != "null" ]; then
        NEWCOMER_CAMPFIRES="$(echo "$LS_JSON" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo 0)"
    fi

    echo "  Campfires joined by agent-10: $NEWCOMER_CAMPFIRES"

    if [ "$NEWCOMER_CAMPFIRES" -ge 2 ]; then
        NEWCOMER_BOOTSTRAPPED=true
        echo "  Bootstrap status: BOOTSTRAPPED (joined $NEWCOMER_CAMPFIRES campfires)"
    else
        echo "  Bootstrap status: PARTIAL (joined $NEWCOMER_CAMPFIRES campfires, need ≥2)"
    fi
else
    echo "  agent-10 identity: ABSENT (verification agent may not have run)"
    echo "  Bootstrap status: NOT RUN"
fi
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 5: NEWCOMER CONVENTION ADOPTION
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 5: Newcomer Convention Adoption"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Check if agent-10's bootstrap report mentions architect-established conventions
BOOTSTRAP_REPORT=""
BOOTSTRAP_REPORT_PATH=""

# Look for bootstrap-report.md in workspace
if [ -d "$WORKSPACE_DIR/verification-agent" ]; then
    BOOTSTRAP_REPORT_PATH="$WORKSPACE_DIR/verification-agent/bootstrap-report.md"
elif [ -d "$WORKSPACE_DIR" ]; then
    BOOTSTRAP_REPORT_PATH="$(find "$WORKSPACE_DIR" -name 'bootstrap-report*' 2>/dev/null | head -1)"
fi

NEWCOMER_USED_CONVENTIONS=false
CONVENTION_EVIDENCE=""

if [ -n "$BOOTSTRAP_REPORT_PATH" ] && [ -f "$BOOTSTRAP_REPORT_PATH" ]; then
    BOOTSTRAP_REPORT="$(cat "$BOOTSTRAP_REPORT_PATH" 2>/dev/null || true)"
    echo "  Bootstrap report: FOUND at $BOOTSTRAP_REPORT_PATH"

    # Check for evidence of using established conventions
    if echo "$BOOTSTRAP_REPORT" | grep -qi "tag\|convention\|format\|protocol\|trust.*vouch\|tool.*registry"; then
        NEWCOMER_USED_CONVENTIONS=true
        echo "  Convention adoption: EVIDENT (report references tags/conventions/protocols)"
        CONVENTION_EVIDENCE="mentions protocol conventions in report"
    else
        echo "  Convention adoption: NOT EVIDENT (report does not reference conventions)"
    fi
else
    echo "  Bootstrap report: ABSENT (agent-10 did not write a report)"
fi

# Also check transport messages from agent-10 for convention use
if [ -d "$TRANSPORT_DIR" ] && $NEWCOMER_IDENTITY; then
    AGENT10_PUBKEY="$(python3 -c "
import json
try:
    data = json.load(open('$AGENT10_DIR/identity.json'))
    print(data.get('public_key', ''))
except Exception:
    pass
" 2>/dev/null || true)"

    if [ -n "$AGENT10_PUBKEY" ]; then
        AGENT10_MSG_COUNT="$(find "$TRANSPORT_DIR" -name '*.msg' -o -name '*.cbor' 2>/dev/null | xargs grep -l "$AGENT10_PUBKEY" 2>/dev/null | wc -l | tr -d ' ')" || AGENT10_MSG_COUNT=0
        echo "  Messages sent by agent-10: $AGENT10_MSG_COUNT"

        if [ "$AGENT10_MSG_COUNT" -ge 1 ]; then
            NEWCOMER_USED_CONVENTIONS=true
            echo "  Sent messages using cf: YES (used campfire to communicate)"
            [ -z "$CONVENTION_EVIDENCE" ] && CONVENTION_EVIDENCE="sent $AGENT10_MSG_COUNT message(s) via campfire"
        fi
    fi
fi
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 6: CROSS-POLLINATION (architect interlock)
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 6: Architect Cross-Pollination"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "Checking whether at least 3 architects are members of each other's campfires..."
echo ""

# Build a membership map: campfire_id -> set of agents
declare -A CAMPFIRE_MEMBERS
ALL_AGENT_CAMPFIRE_IDS=""

for i in $(seq -w 1 9); do
    agent="agent-$(printf '%02d' "$i")"
    if [ ! -f "$AGENTS_DIR/$agent/identity.json" ]; then continue; fi

    LS_JSON="$(cf_agent "$agent" ls --json 2>/dev/null || true)"
    if [ -z "$LS_JSON" ] || [ "$LS_JSON" = "[]" ] || [ "$LS_JSON" = "null" ]; then continue; fi

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
        ALL_AGENT_CAMPFIRE_IDS="$ALL_AGENT_CAMPFIRE_IDS
$IDS"
        while IFS= read -r cf_id; do
            [ -z "$cf_id" ] && continue
            CAMPFIRE_MEMBERS["$cf_id"]="${CAMPFIRE_MEMBERS[$cf_id]:-} $agent"
        done <<< "$IDS"
    fi
done

# Count campfires with 3+ architect members
CROSS_POLLINATED_CAMPFIRES=0
MAX_CROSS_MEMBERSHIP=0
TOTAL_DISTINCT=0

for cf_id in "${!CAMPFIRE_MEMBERS[@]}"; do
    MEMBER_LIST="${CAMPFIRE_MEMBERS[$cf_id]}"
    MEMBER_COUNT="$(echo "$MEMBER_LIST" | tr ' ' '\n' | grep -c 'agent-' || true)"
    TOTAL_DISTINCT=$((TOTAL_DISTINCT + 1))

    if [ "$MEMBER_COUNT" -ge 3 ]; then
        CROSS_POLLINATED_CAMPFIRES=$((CROSS_POLLINATED_CAMPFIRES + 1))
    fi
    if [ "$MEMBER_COUNT" -gt "$MAX_CROSS_MEMBERSHIP" ]; then
        MAX_CROSS_MEMBERSHIP="$MEMBER_COUNT"
    fi
done

echo "  Total distinct campfires (across all agent stores): $TOTAL_DISTINCT"
echo "  Campfires with 3+ architect members: $CROSS_POLLINATED_CAMPFIRES"
echo "  Max architects in a single campfire: $MAX_CROSS_MEMBERSHIP"

if [ "$CROSS_POLLINATED_CAMPFIRES" -ge 1 ]; then
    echo "  Cross-pollination: PRESENT"
else
    echo "  Cross-pollination: ABSENT (no campfire has 3+ architect members)"
fi
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 7: GOVERNANCE FUNCTIONALITY
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 7: Governance Functionality"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

PROPOSALS_FOUND=0
VOTES_FOUND=0

# Scan transport messages for proposal and vote tags/content
if [ -d "$TRANSPORT_DIR" ]; then
    PROPOSALS_FOUND="$(find "$TRANSPORT_DIR" -name '*.msg' -o -name '*.cbor' 2>/dev/null | \
        xargs strings 2>/dev/null | \
        grep -ci '"proposal"\|"vote"\|tag.*proposal\|PROPOSAL:' || true)"
    VOTES_FOUND="$(find "$TRANSPORT_DIR" -name '*.msg' -o -name '*.cbor' 2>/dev/null | \
        xargs strings 2>/dev/null | \
        grep -ci 'vote:yes\|vote:no\|"vote"\|VOTE:' || true)"
fi

# Also check workspace files for governance evidence
if [ -d "$WORKSPACE_DIR" ]; then
    WORKSPACE_PROPOSALS="$(find "$WORKSPACE_DIR" -type f \( -name '*.md' -o -name '*.txt' \) 2>/dev/null | \
        xargs grep -cil 'proposal\|governance\|voting' 2>/dev/null | wc -l | tr -d ' ')" || WORKSPACE_PROPOSALS=0
    echo "  Governance design docs in workspace: $WORKSPACE_PROPOSALS"
fi

echo "  Proposal messages found (transport): $PROPOSALS_FOUND"
echo "  Vote messages found (transport): $VOTES_FOUND"

GOVERNANCE_FUNCTIONAL=false
if [ "$PROPOSALS_FOUND" -ge 1 ] && [ "$VOTES_FOUND" -ge 1 ]; then
    GOVERNANCE_FUNCTIONAL=true
    echo "  Governance: FUNCTIONAL (has both proposals and votes)"
elif [ "$PROPOSALS_FOUND" -ge 1 ]; then
    echo "  Governance: PARTIAL (proposals exist but no votes recorded)"
else
    echo "  Governance: NOT EVIDENT (no proposals found in transport messages)"
fi
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 8: SECURITY / STRESS TEST REVIEW
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 8: Security Review (Stress Test Architect)"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

ATTACK_REPORTS=0
SECURITY_INTEL_MESSAGES=0

# Check stress-test-architect workspace for attack reports
STRESS_WORKSPACE=""
for d in "$WORKSPACE_DIR/stress-test-architect" "$WORKSPACE_DIR/stress-test" "$WORKSPACE_DIR/red-team"; do
    if [ -d "$d" ]; then
        STRESS_WORKSPACE="$d"
        break
    fi
done

if [ -n "$STRESS_WORKSPACE" ]; then
    ATTACK_REPORTS="$(find "$STRESS_WORKSPACE" -type f 2>/dev/null | wc -l | tr -d ' ')" || ATTACK_REPORTS=0
    echo "  Stress test workspace: FOUND at $STRESS_WORKSPACE"
    echo "  Files written by stress test architect: $ATTACK_REPORTS"
else
    echo "  Stress test workspace: NOT FOUND"
fi

# Scan transport for attack/threat messages
if [ -d "$TRANSPORT_DIR" ]; then
    SECURITY_INTEL_MESSAGES="$(find "$TRANSPORT_DIR" -name '*.msg' -o -name '*.cbor' 2>/dev/null | \
        xargs strings 2>/dev/null | \
        grep -ci 'attack\|threat\|sybil\|spam\|malicious\|vulnerability\|exploit' || true)"
fi

echo "  Security/threat messages in transport: $SECURITY_INTEL_MESSAGES"

SECURITY_REVIEWED=false
if [ "$ATTACK_REPORTS" -ge 1 ] || [ "$SECURITY_INTEL_MESSAGES" -ge 1 ]; then
    SECURITY_REVIEWED=true
    echo "  Security review: CONDUCTED"
else
    echo "  Security review: NOT EVIDENT"
fi
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 9: DOCUMENTATION (workspace files)
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 9: Documentation"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

TOTAL_WORKSPACE_FILES=0
WORKSPACE_WORD_COUNT=0

if [ -d "$WORKSPACE_DIR" ]; then
    TOTAL_WORKSPACE_FILES="$(find "$WORKSPACE_DIR" -type f 2>/dev/null | wc -l | tr -d ' ')" || TOTAL_WORKSPACE_FILES=0
    WORKSPACE_WORD_COUNT="$(find "$WORKSPACE_DIR" -type f \( -name '*.md' -o -name '*.txt' \) 2>/dev/null | xargs wc -w 2>/dev/null | tail -1 | awk '{print $1}')" || WORKSPACE_WORD_COUNT=0

    echo "  Total workspace files: $TOTAL_WORKSPACE_FILES"
    echo "  Total words in .md/.txt files: ${WORKSPACE_WORD_COUNT:-0}"
    echo ""
    echo "  Workspace breakdown:"
    for d in "$WORKSPACE_DIR"/*/; do
        [ -d "$d" ] || continue
        dir_name="$(basename "$d")"
        file_count="$(find "$d" -type f 2>/dev/null | wc -l | tr -d ' ')" || file_count=0
        echo "    $dir_name/: $file_count file(s)"
    done
else
    echo "  Workspace directory not found"
fi
DOCS_WRITTEN=false
[ "$TOTAL_WORKSPACE_FILES" -ge 5 ] && DOCS_WRITTEN=true
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 10: DEEP ANALYSIS — topology scripts
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 10: Deep Analysis (topology scripts)"
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

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 11: SCENARIO CLASSIFICATION
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 11: Scenario Classification"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Classification logic:
# A: Full infrastructure (all 7 root campfire types created, newcomer bootstrapped)
# B: Partial infrastructure (4+ types, newcomer partially bootstrapped)
# C: Coordination-heavy (architects talked a lot, built less)
# D: Isolated architects (each built their own thing, little interlock)
# F: Minimal output

SCENARIO=""
SCENARIO_DESC=""

# Count total messages as a proxy for coordination activity
TOTAL_MESSAGES=0
if [ -d "$TRANSPORT_DIR" ]; then
    TOTAL_MESSAGES="$(find "$TRANSPORT_DIR" -name '*.msg' -o -name '*.cbor' 2>/dev/null | wc -l | tr -d ' ')" || TOTAL_MESSAGES=0
fi

# Key decision factors
INFRA_COUNT="$DOMAIN_COUNT"          # 0-9 domains present
NEWCOMER_JOINED="$NEWCOMER_CAMPFIRES" # how many campfires newcomer joined
CROSS_POLL="$CROSS_POLLINATED_CAMPFIRES"  # campfires with 3+ architects

if [ "$INFRA_COUNT" -ge 7 ] && $NEWCOMER_BOOTSTRAPPED; then
    SCENARIO="A"
    SCENARIO_DESC="Full infrastructure — all 7+ root domains created and newcomer successfully bootstrapped into the network."

elif [ "$INFRA_COUNT" -ge 7 ] && ! $NEWCOMER_BOOTSTRAPPED; then
    SCENARIO="A-"
    SCENARIO_DESC="Infrastructure built (7+ domains) but newcomer bootstrap incomplete — onboarding path may be broken or verification agent did not run."

elif [ "$INFRA_COUNT" -ge 4 ] && [ "$NEWCOMER_JOINED" -ge 1 ]; then
    SCENARIO="B"
    SCENARIO_DESC="Partial infrastructure — $INFRA_COUNT of 9 domains present, newcomer partially bootstrapped (joined $NEWCOMER_JOINED campfire(s))."

elif [ "$INFRA_COUNT" -ge 4 ] && [ "$CROSS_POLL" -eq 0 ]; then
    SCENARIO="D"
    SCENARIO_DESC="Isolated architects — $INFRA_COUNT domains present but no cross-pollination (no campfire has 3+ architects). Each built their own thing."

elif [ "$TOTAL_MESSAGES" -ge 50 ] && [ "$INFRA_COUNT" -lt 4 ]; then
    SCENARIO="C"
    SCENARIO_DESC="Coordination-heavy — $TOTAL_MESSAGES messages sent but only $INFRA_COUNT root domains established. Architects coordinated but built less."

elif [ "$INFRA_COUNT" -ge 1 ] || [ "$TOTAL_MESSAGES" -ge 10 ]; then
    SCENARIO="B"
    SCENARIO_DESC="Partial infrastructure — $INFRA_COUNT root domains present, $TOTAL_MESSAGES messages exchanged. Partial build."

else
    SCENARIO="F"
    SCENARIO_DESC="Minimal output — $INFRA_COUNT root domains present, $TOTAL_MESSAGES messages. Architects did not produce significant infrastructure."
fi

echo "Classified outcome:"
echo ""
echo "  Scenario: $SCENARIO"
echo "  Description: $SCENARIO_DESC"
echo ""
echo "Scenario reference:"
echo "  A   — Full infrastructure (7+ root campfire types created, newcomer bootstrapped)"
echo "  A-  — Full infrastructure built, but newcomer bootstrap incomplete"
echo "  B   — Partial infrastructure (4+ types, newcomer partially or fully bootstrapped)"
echo "  C   — Coordination-heavy (architects talked a lot, built less than 4 domains)"
echo "  D   — Isolated architects (4+ domains but no cross-pollination)"
echo "  F   — Minimal output (<4 domains, <10 messages)"
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 12: WRITE infrastructure-report.json
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 12: Writing infrastructure-report.json"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Build JSON safely — pass all values as environment variables to avoid
# bash-in-heredoc quoting hazards with boolean expressions and arrays.
NEWCOMER_RAN_JSON="false"
[ -f "$AGENT10_DIR/identity.json" ] && NEWCOMER_RAN_JSON="true"
NEWCOMER_BOOTSTRAPPED_JSON="false"
$NEWCOMER_BOOTSTRAPPED && NEWCOMER_BOOTSTRAPPED_JSON="true"
NEWCOMER_CONVENTIONS_JSON="false"
$NEWCOMER_USED_CONVENTIONS && NEWCOMER_CONVENTIONS_JSON="true"
GOVERNANCE_JSON="false"
$GOVERNANCE_FUNCTIONAL && GOVERNANCE_JSON="true"
SECURITY_JSON="false"
$SECURITY_REVIEWED && SECURITY_JSON="true"
DOCS_JSON="false"
$DOCS_WRITTEN && DOCS_JSON="true"

# Serialize bash arrays to newline-delimited strings for python
DOMAINS_PRESENT_STR="${DOMAINS_PRESENT[*]:-}"
DOMAINS_MISSING_STR="${DOMAINS_MISSING[*]:-}"

REPORT_TIMESTAMP="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"

python3 - "$REPORT_FILE" \
    "$TEST_ROOT" "$REPORT_TIMESTAMP" \
    "$DOMAINS_PRESENT_STR" "$DOMAINS_MISSING_STR" \
    "$BEACON_COUNT" "$BEACONS_WITH_DESCRIPTIONS" \
    "$TOTAL_MESSAGES" "$TOTAL_WORKSPACE_FILES" \
    "$NEWCOMER_RAN_JSON" "$NEWCOMER_CAMPFIRES" \
    "$NEWCOMER_BOOTSTRAPPED_JSON" "$NEWCOMER_CONVENTIONS_JSON" \
    "$CROSS_POLLINATED_CAMPFIRES" "$MAX_CROSS_MEMBERSHIP" \
    "$GOVERNANCE_JSON" "$PROPOSALS_FOUND" "$VOTES_FOUND" \
    "$SECURITY_JSON" "$ATTACK_REPORTS" "$DOCS_JSON" \
    "$SCENARIO" "$SCENARIO_DESC" <<'PYEOF'
import json, sys

(report_file, test_root, timestamp,
 domains_present_str, domains_missing_str,
 beacon_count, beacons_with_desc,
 total_messages, workspace_files,
 newcomer_ran, newcomer_campfires,
 newcomer_bootstrapped, newcomer_conventions,
 cross_pollinated, max_cross,
 governance, proposals, votes,
 security, attack_reports, docs,
 scenario, scenario_desc) = sys.argv[1:]

def split_nonempty(s):
    return [x for x in s.split() if x]

def to_bool(s):
    return s == "true"

def to_int(s):
    try:
        return int(s)
    except Exception:
        return 0

domains_present = split_nonempty(domains_present_str)
domains_missing = split_nonempty(domains_missing_str)

report = {
    "run_timestamp": timestamp,
    "test_root": test_root,
    "domains_present": domains_present,
    "domains_missing": domains_missing,
    "domain_count": len(domains_present),
    "beacon_count": to_int(beacon_count),
    "beacons_with_descriptions": to_int(beacons_with_desc),
    "total_messages": to_int(total_messages),
    "workspace_files": to_int(workspace_files),
    "newcomer_ran": to_bool(newcomer_ran),
    "newcomer_campfires_joined": to_int(newcomer_campfires),
    "newcomer_bootstrapped": to_bool(newcomer_bootstrapped),
    "newcomer_used_conventions": to_bool(newcomer_conventions),
    "cross_pollinated_campfires": to_int(cross_pollinated),
    "max_cross_membership": to_int(max_cross),
    "governance_functional": to_bool(governance),
    "proposals_found": to_int(proposals),
    "votes_found": to_int(votes),
    "security_reviewed": to_bool(security),
    "attack_reports": to_int(attack_reports),
    "docs_written": to_bool(docs),
    "scenario": scenario,
    "scenario_description": scenario_desc,
    "verdict": (
        "INFRASTRUCTURE_BUILT" if len(domains_present) >= 7 else
        "PARTIAL" if len(domains_present) >= 3 else
        "EMPTY"
    )
}

with open(report_file, "w") as f:
    json.dump(report, f, indent=2)

print(f"  infrastructure-report.json written: {len(domains_present)} domains, scenario {report['scenario']}, verdict {report['verdict']}")
PYEOF
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 13: SUMMARY REPORT
# ─────────────────────────────────────────────────────────────────────────────
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "SECTION 13: Summary"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

SUMMARY="$(cat <<SUMMARY_EOF
=== Agent Internet Test — Infrastructure Report ===
Timestamp:               $(date -u '+%Y-%m-%d %H:%M:%S UTC')
Test root:               $TEST_ROOT

--- Structural ---
Architect agents initialized:  $IDENTITY_COUNT / 9
Log files found:               $LOG_COUNT
Workspace subdirectories:      $WORKSPACE_SUBDIR_COUNT

--- Infrastructure Domains ---
Domains present ($DOMAIN_COUNT / 9): ${DOMAINS_PRESENT[*]:-none}
Domains missing:          ${DOMAINS_MISSING[*]:-none}

--- Beacons ---
Total beacons:            $BEACON_COUNT
With descriptions:        $BEACONS_WITH_DESCRIPTIONS

--- Newcomer Bootstrap ---
agent-10 ran:             $NEWCOMER_IDENTITY
Campfires joined:         $NEWCOMER_CAMPFIRES
Bootstrapped:             $NEWCOMER_BOOTSTRAPPED
Used conventions:         $NEWCOMER_USED_CONVENTIONS

--- Cross-Pollination ---
Campfires with 3+ architects: $CROSS_POLLINATED_CAMPFIRES
Max architects in one cf:     $MAX_CROSS_MEMBERSHIP

--- Governance ---
Governance functional:    $GOVERNANCE_FUNCTIONAL
Proposals found:          $PROPOSALS_FOUND
Votes found:              $VOTES_FOUND

--- Security ---
Security reviewed:        $SECURITY_REVIEWED
Attack reports:           $ATTACK_REPORTS

--- Documentation ---
Workspace files:          $TOTAL_WORKSPACE_FILES
Docs written:             $DOCS_WRITTEN

--- Activity ---
Total messages:           $TOTAL_MESSAGES

--- Classification ---
Scenario:                 $SCENARIO
Summary:                  $SCENARIO_DESC

--- Verdict ---
$([ "${#DOMAINS_PRESENT[@]}" -ge 7 ] && echo "INFRASTRUCTURE_BUILT" || [ "${#DOMAINS_PRESENT[@]}" -ge 3 ] && echo "PARTIAL" || echo "EMPTY")

--- Artifacts ---
Verdict file:             $VERDICT_FILE
Infrastructure report:    $REPORT_FILE
Topology DOT:             $DOT_FILE
SUMMARY_EOF
)"

echo "$SUMMARY"
echo ""

# Write verdict file
echo "$SUMMARY" > "$VERDICT_FILE"
echo "Verdict written to $VERDICT_FILE"
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# SECTION 14: STRUCTURAL FAILURE SUMMARY
# ─────────────────────────────────────────────────────────────────────────────
if [ "$STRUCTURAL_FAILURES" -gt 0 ]; then
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "WARNING: $STRUCTURAL_FAILURES structural check(s) failed."
    echo "These indicate harness errors, not experimental outcomes."
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

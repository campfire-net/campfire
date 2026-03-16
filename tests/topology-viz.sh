#!/usr/bin/env bash
# topology-viz.sh — generates a Graphviz DOT graph of the campfire social network
#
# Nodes: agents (colored by domain) and campfires (squares labeled with description)
# Edges: membership (agent -> campfire), weighted by message count
#
# Usage: bash tests/topology-viz.sh /tmp/campfire-emergence/ > logs/topology.dot
#        dot -Tpng logs/topology.dot -o logs/topology.png
#
# The script writes DOT to stdout. If logs/ directory exists and `dot` is available,
# it also renders logs/topology.png.
#
# Env vars:
#   CF_BIN — path to cf binary (default: $TEST_ROOT/bin/cf, then `cf` on PATH)

set -euo pipefail

TEST_ROOT="${1:-}"
if [ -z "$TEST_ROOT" ]; then
    echo "Usage: $0 <test-root-dir>" >&2
    echo "  e.g. $0 /tmp/campfire-emergence" >&2
    exit 1
fi

# Strip trailing slash for consistency
TEST_ROOT="${TEST_ROOT%/}"

SHARED="$TEST_ROOT/shared"
AGENTS="$TEST_ROOT/agents"
LOGS="$TEST_ROOT/logs"
BEACON_DIR="$SHARED/beacons"
TRANSPORT_DIR="$SHARED/transport"

# Resolve cf binary
if [ -n "${CF_BIN:-}" ]; then
    CF="$CF_BIN"
elif [ -x "$TEST_ROOT/bin/cf" ]; then
    CF="$TEST_ROOT/bin/cf"
elif command -v cf >/dev/null 2>&1; then
    CF="cf"
else
    echo "Error: cf binary not found. Set CF_BIN or place cf at $TEST_ROOT/bin/cf" >&2
    exit 1
fi

# Ensure logs dir exists (for PNG output)
mkdir -p "$LOGS" 2>/dev/null || true

# ─────────────────────────────────────────────────────────────────────────────
# Domain color map
# ─────────────────────────────────────────────────────────────────────────────
declare -A DOMAIN_COLORS=(
    [finance]="#4CAF50"
    [legal]="#FF9800"
    [marketing]="#2196F3"
    [support]="#F44336"
    [research]="#9C27B0"
    [hr]="#00BCD4"
    [product]="#795548"
    [sales]="#E91E63"
    [ops]="#607D8B"
    [exec]="#FFC107"
    [analytics]="#3F51B5"
)

# ─────────────────────────────────────────────────────────────────────────────
# Collect campfire descriptions from beacons via cf discover
# We run cf discover once with CF_BEACON_DIR set to the shared beacons dir.
# Output: campfire_id -> description map (written as temp file for python)
# ─────────────────────────────────────────────────────────────────────────────
BEACON_JSON="[]"
if [ -d "$BEACON_DIR" ]; then
    BEACON_JSON=$(
        CF_BEACON_DIR="$BEACON_DIR" \
        CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
        "$CF" discover --json 2>/dev/null
    ) || BEACON_JSON="[]"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Count messages per agent per campfire
# Strategy: iterate transport/<campfire-id>/messages/ and parse each message's
# sender from the local agent stores via `cf read --all --json`.
# For robustness (agent may not be member of all campfires), we count files in
# the transport dir for total-per-campfire, and per-agent counts from each
# agent's store.
# ─────────────────────────────────────────────────────────────────────────────

# Build per-agent membership + message count data as JSON lines
# Format: agent_name campfire_id msg_count
MEMBERSHIP_DATA=$(mktemp)
trap 'rm -f "$MEMBERSHIP_DATA"' EXIT

for agent_dir in "$AGENTS"/agent-*/; do
    [ -d "$agent_dir" ] || continue
    agent_name=$(basename "$agent_dir")

    # Get memberships for this agent
    ls_json=$(
        CF_HOME="$agent_dir" \
        CF_BEACON_DIR="$BEACON_DIR" \
        CF_TRANSPORT_DIR="$TRANSPORT_DIR" \
        "$CF" ls --json 2>/dev/null
    ) || ls_json="[]"

    # For each campfire this agent belongs to, count messages they sent
    echo "$ls_json" | python3 -c "
import json, sys, subprocess, os

ls_data = json.load(sys.stdin)
agent_dir = '''$agent_dir'''
agent_name = '''$agent_name'''
beacon_dir = '''$BEACON_DIR'''
transport_dir = '''$TRANSPORT_DIR'''
cf_bin = '''$CF'''

env = {**os.environ, 'CF_HOME': agent_dir.rstrip('/'),
       'CF_BEACON_DIR': beacon_dir, 'CF_TRANSPORT_DIR': transport_dir}

for m in ls_data:
    cf_id = m.get('campfire_id', '')
    if not cf_id:
        continue

    # Count messages sent by this agent in this campfire
    try:
        result = subprocess.run(
            [cf_bin, 'read', cf_id, '--all', '--json'],
            capture_output=True, text=True, env=env, timeout=10
        )
        if result.returncode == 0:
            msgs = json.loads(result.stdout)
        else:
            msgs = []
    except Exception:
        msgs = []

    # Get this agent's public key to match sender field
    try:
        id_result = subprocess.run(
            [cf_bin, 'id', '--json'],
            capture_output=True, text=True, env=env, timeout=5
        )
        if id_result.returncode == 0:
            agent_pubkey = json.loads(id_result.stdout).get('public_key', '')
        else:
            agent_pubkey = ''
    except Exception:
        agent_pubkey = ''

    msg_count = sum(1 for msg in msgs if msg.get('sender', '') == agent_pubkey)

    print(f'{agent_name}\t{cf_id}\t{msg_count}')
" 2>/dev/null >> "$MEMBERSHIP_DATA" || true
done

# ─────────────────────────────────────────────────────────────────────────────
# Generate DOT output
# ─────────────────────────────────────────────────────────────────────────────
DOT_OUTPUT=$(python3 -c "
import json, os, sys
from collections import defaultdict

agents_dir = '''$AGENTS'''
beacon_json_str = '''$BEACON_JSON'''
membership_file = '''$MEMBERSHIP_DATA'''
transport_dir = '''$TRANSPORT_DIR'''

domain_colors = {
    'finance': '#4CAF50',
    'legal': '#FF9800',
    'marketing': '#2196F3',
    'support': '#F44336',
    'research': '#9C27B0',
    'hr': '#00BCD4',
    'product': '#795548',
    'sales': '#E91E63',
    'ops': '#607D8B',
    'exec': '#FFC107',
    'analytics': '#3F51B5',
}

# Parse beacon descriptions
cf_descriptions = {}
try:
    beacons = json.loads(beacon_json_str)
    for b in beacons:
        cf_id = b.get('campfire_id', '')
        desc = b.get('description', '')
        if cf_id:
            cf_descriptions[cf_id] = desc
except Exception:
    pass

# Parse membership data: agent -> [(campfire_id, msg_count)]
agent_memberships = defaultdict(list)
all_campfires = set()
with open(membership_file) as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        parts = line.split('\t')
        if len(parts) == 3:
            agent_name, cf_id, msg_count = parts[0], parts[1], int(parts[2])
            agent_memberships[agent_name].append((cf_id, msg_count))
            all_campfires.add(cf_id)

# Also discover campfires from transport dir (even if no agent has them in store)
if os.path.isdir(transport_dir):
    for entry in os.listdir(transport_dir):
        full = os.path.join(transport_dir, entry)
        if os.path.isdir(full):
            all_campfires.add(entry)

# Parse agent info from CLAUDE.md
agent_info = {}
if os.path.isdir(agents_dir):
    for entry in sorted(os.listdir(agents_dir)):
        agent_dir = os.path.join(agents_dir, entry)
        if not os.path.isdir(agent_dir) or not entry.startswith('agent-'):
            continue
        claude_md = os.path.join(agent_dir, 'CLAUDE.md')
        domain = 'unknown'
        title = entry
        if os.path.exists(claude_md):
            text = open(claude_md).read()
            for line in text.split('\n'):
                if 'Domain:' in line and '##' in line:
                    domain = line.split('Domain:')[1].strip().lower().split()[0]
                elif line.startswith('# Agent') and '\u2014' in line:
                    # '# Agent 01 — CFO'
                    title = line.split('\u2014', 1)[1].strip() if '\u2014' in line else entry
                elif line.startswith('# Agent') and '-' in line and '—' not in line:
                    parts = line.split('-', 1)
                    if len(parts) > 1:
                        title = parts[1].strip()
        agent_info[entry] = {'domain': domain, 'title': title}

lines = []
lines.append('digraph campfire_network {')
lines.append('  rankdir=LR;')
lines.append('  node [fontname=\"Helvetica\"];')
lines.append('  graph [label=\"Campfire Emergence Network\", fontsize=16, labelloc=t];')
lines.append('')

# Agent nodes
lines.append('  // Agent nodes')
for entry in sorted(agent_info.keys()):
    info = agent_info[entry]
    domain = info['domain']
    title = info['title']
    color = domain_colors.get(domain, '#999999')
    # Escape quotes in label
    label = title.replace('\"', '\\\\\"')
    agent_num = entry.replace('agent-', '')
    lines.append(f'  \"{entry}\" [label=\"{agent_num}: {label}\", style=filled, fillcolor=\"{color}\", fontcolor=white, shape=ellipse];')

lines.append('')

# Campfire nodes — only those with actual activity (in agent stores or transport)
lines.append('  // Campfire nodes')
for cf_id in sorted(all_campfires):
    short_id = cf_id[:8] if len(cf_id) > 8 else cf_id
    desc = cf_descriptions.get(cf_id, '')
    if desc:
        # Truncate long descriptions for readability
        if len(desc) > 40:
            desc = desc[:37] + '...'
        # Escape quotes
        desc = desc.replace('\"', '\\\\\"')
        label = f'{short_id}\\n{desc}'
    else:
        label = short_id

    # Count total messages in this campfire from transport dir
    msg_dir = os.path.join(transport_dir, cf_id, 'messages')
    total_msgs = 0
    if os.path.isdir(msg_dir):
        total_msgs = len([f for f in os.listdir(msg_dir) if os.path.isfile(os.path.join(msg_dir, f))])

    tooltip = f'{cf_id} ({total_msgs} messages)'
    lines.append(f'  \"cf_{cf_id}\" [label=\"{label}\", shape=box, style=filled, fillcolor=\"#EEEEEE\", tooltip=\"{tooltip}\"];')

lines.append('')

# Edges: agent -> campfire
lines.append('  // Membership edges (weight = messages sent by this agent)')
for agent_name, memberships in sorted(agent_memberships.items()):
    for cf_id, msg_count in memberships:
        # Edge weight and label: message count
        # penwidth scales with activity (min 1, max 5)
        penwidth = min(5.0, max(1.0, 1.0 + msg_count * 0.5))
        if msg_count > 0:
            edge_label = str(msg_count)
        else:
            edge_label = ''
        lines.append(f'  \"{agent_name}\" -> \"cf_{cf_id}\" [label=\"{edge_label}\", penwidth={penwidth:.1f}];')

lines.append('')
lines.append('  // Legend')
lines.append('  subgraph cluster_legend {')
lines.append('    label=\"Domain Colors\"; style=dashed; fontsize=11;')
for domain, color in sorted(domain_colors.items()):
    lines.append(f'    \"legend_{domain}\" [label=\"{domain}\", style=filled, fillcolor=\"{color}\", fontcolor=white, shape=ellipse, width=1.2];')
lines.append('  }')
lines.append('}')

print('\n'.join(lines))
")

echo "$DOT_OUTPUT"

# ─────────────────────────────────────────────────────────────────────────────
# Write to logs/topology.dot and optionally render PNG
# ─────────────────────────────────────────────────────────────────────────────
DOT_FILE="$LOGS/topology.dot"
echo "$DOT_OUTPUT" > "$DOT_FILE"
echo "Written: $DOT_FILE" >&2

if command -v dot >/dev/null 2>&1; then
    PNG_FILE="$LOGS/topology.png"
    dot -Tpng "$DOT_FILE" -o "$PNG_FILE" 2>/dev/null && echo "Rendered: $PNG_FILE" >&2 || echo "Warning: dot rendering failed" >&2
    SVG_FILE="$LOGS/topology.svg"
    dot -Tsvg "$DOT_FILE" -o "$SVG_FILE" 2>/dev/null && echo "Rendered: $SVG_FILE" >&2 || true
else
    echo "Note: graphviz 'dot' not found — DOT file written but not rendered." >&2
    echo "Install graphviz and run: dot -Tpng $DOT_FILE -o $LOGS/topology.png" >&2
fi

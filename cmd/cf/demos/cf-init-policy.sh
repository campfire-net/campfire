#!/usr/bin/env bash
# cf-init-policy.sh — demonstrates cf init --policy <preset> with three templates
#
# cf init --policy writes grant-template.json to the identity home, documenting
# the default capability set for automatic-grant issuance under that trust posture.
#
# Three presets:
#   personal-developer   solo use, depth 1, 7d owner TTL, full rd/dontguess/social/reach
#   team-member          multi-owner threshold signing, depth 2, 24h TTLs
#   public-agent         hosted MCP posture, depth 1, 24h TTL ceiling
#
# This script exits 0 when all three presets produce correct grant-template.json output.
set -euo pipefail

CF="${CF:-cf}"
DEMO_TMP=$(mktemp -d)
trap 'rm -rf "$DEMO_TMP"' EXIT

echo "=== cf-init-policy.sh ==="

verify_convention() {
    local file="$1"
    local conv="$2"
    if ! grep -q "\"$conv\"" "$file"; then
        echo "FAIL: grant-template.json missing convention '$conv' in $file"
        exit 1
    fi
}

# --- personal-developer ---
echo ""
echo "--- Preset: personal-developer ---"
PD_HOME="$DEMO_TMP/personal-developer"
mkdir -p "$PD_HOME"
CF_HOME="$PD_HOME" "$CF" init --policy personal-developer
TMPL="$PD_HOME/grant-template.json"
if [ ! -f "$TMPL" ]; then
    echo "FAIL: grant-template.json not found at $TMPL"
    exit 1
fi
echo "grant-template.json written:"
cat "$TMPL"

# Verify preset name
PRESET=$(python3 -c "import json,sys; d=json.load(open('$TMPL')); print(d['preset'])")
if [ "$PRESET" != "personal-developer" ]; then
    echo "FAIL: preset = '$PRESET', want 'personal-developer'"
    exit 1
fi
echo "PASS: preset = personal-developer"

# Verify max_depth = 1
DEPTH=$(python3 -c "import json,sys; d=json.load(open('$TMPL')); print(d['max_depth'])")
if [ "$DEPTH" != "1" ]; then
    echo "FAIL: max_depth = '$DEPTH', want 1"
    exit 1
fi
echo "PASS: max_depth = 1"

# Verify multi_owner = false
MULTI=$(python3 -c "import json,sys; d=json.load(open('$TMPL')); print(str(d['multi_owner']).lower())")
if [ "$MULTI" != "false" ]; then
    echo "FAIL: multi_owner = '$MULTI', want false"
    exit 1
fi
echo "PASS: multi_owner = false"

for conv in rd dontguess social reach; do
    verify_convention "$TMPL" "$conv"
    echo "PASS: capability for convention '$conv' present"
done

# Verify file permissions (0600)
PERM=$(python3 -c "import os,stat; m=os.stat('$TMPL').st_mode; print(oct(stat.S_IMODE(m)))")
if [ "$PERM" != "0o600" ]; then
    echo "FAIL: grant-template.json permissions = $PERM, want 0o600"
    exit 1
fi
echo "PASS: file permissions = 0600"

# --- team-member ---
echo ""
echo "--- Preset: team-member ---"
TM_HOME="$DEMO_TMP/team-member"
mkdir -p "$TM_HOME"
CF_HOME="$TM_HOME" "$CF" init --policy team-member
TMPL="$TM_HOME/grant-template.json"
if [ ! -f "$TMPL" ]; then
    echo "FAIL: grant-template.json not found at $TMPL"
    exit 1
fi
echo "grant-template.json written:"
cat "$TMPL"

PRESET=$(python3 -c "import json,sys; d=json.load(open('$TMPL')); print(d['preset'])")
if [ "$PRESET" != "team-member" ]; then
    echo "FAIL: preset = '$PRESET', want 'team-member'"
    exit 1
fi
echo "PASS: preset = team-member"

DEPTH=$(python3 -c "import json,sys; d=json.load(open('$TMPL')); print(d['max_depth'])")
if [ "$DEPTH" != "2" ]; then
    echo "FAIL: max_depth = '$DEPTH', want 2 (delegation enabled)"
    exit 1
fi
echo "PASS: max_depth = 2"

MULTI=$(python3 -c "import json,sys; d=json.load(open('$TMPL')); print(str(d['multi_owner']).lower())")
if [ "$MULTI" != "true" ]; then
    echo "FAIL: multi_owner = '$MULTI', want true"
    exit 1
fi
echo "PASS: multi_owner = true"

for conv in rd dontguess social reach; do
    verify_convention "$TMPL" "$conv"
    echo "PASS: capability for convention '$conv' present"
done

# --- public-agent ---
echo ""
echo "--- Preset: public-agent ---"
PA_HOME="$DEMO_TMP/public-agent"
mkdir -p "$PA_HOME"
CF_HOME="$PA_HOME" "$CF" init --policy public-agent
TMPL="$PA_HOME/grant-template.json"
if [ ! -f "$TMPL" ]; then
    echo "FAIL: grant-template.json not found at $TMPL"
    exit 1
fi
echo "grant-template.json written:"
cat "$TMPL"

PRESET=$(python3 -c "import json,sys; d=json.load(open('$TMPL')); print(d['preset'])")
if [ "$PRESET" != "public-agent" ]; then
    echo "FAIL: preset = '$PRESET', want 'public-agent'"
    exit 1
fi
echo "PASS: preset = public-agent"

DEPTH=$(python3 -c "import json,sys; d=json.load(open('$TMPL')); print(d['max_depth'])")
if [ "$DEPTH" != "1" ]; then
    echo "FAIL: max_depth = '$DEPTH', want 1"
    exit 1
fi
echo "PASS: max_depth = 1"

MULTI=$(python3 -c "import json,sys; d=json.load(open('$TMPL')); print(str(d['multi_owner']).lower())")
if [ "$MULTI" != "false" ]; then
    echo "FAIL: multi_owner = '$MULTI', want false"
    exit 1
fi
echo "PASS: multi_owner = false"

OWNER_TTL=$(python3 -c "import json,sys; d=json.load(open('$TMPL')); print(d['owner_ttl'])")
if [ "$OWNER_TTL" != "24h" ]; then
    echo "FAIL: owner_ttl = '$OWNER_TTL', want 24h (tighter ceiling for public agent)"
    exit 1
fi
echo "PASS: owner_ttl = 24h"

for conv in rd dontguess social reach; do
    verify_convention "$TMPL" "$conv"
    echo "PASS: capability for convention '$conv' present"
done

# --- presets differ from each other ---
echo ""
echo "--- Verify presets produce different templates ---"
PD_PRESET=$(python3 -c "import json; print(json.load(open('$DEMO_TMP/personal-developer/grant-template.json'))['preset'])")
TM_PRESET=$(python3 -c "import json; print(json.load(open('$DEMO_TMP/team-member/grant-template.json'))['preset'])")
PA_PRESET=$(python3 -c "import json; print(json.load(open('$DEMO_TMP/public-agent/grant-template.json'))['preset'])")
if [ "$PD_PRESET" = "$TM_PRESET" ] || [ "$PD_PRESET" = "$PA_PRESET" ] || [ "$TM_PRESET" = "$PA_PRESET" ]; then
    echo "FAIL: two or more presets produced the same preset name"
    exit 1
fi
echo "PASS: all three presets are distinct"

# --- unknown preset rejected ---
echo ""
echo "--- Verify unknown preset is rejected ---"
BAD_HOME="$DEMO_TMP/bad-preset"
mkdir -p "$BAD_HOME"
ERR_OUT=$(CF_HOME="$BAD_HOME" "$CF" init --policy bad-preset 2>&1 || true)
if echo "$ERR_OUT" | grep -q "unknown policy preset"; then
    echo "PASS: unknown preset rejected with clear error"
else
    echo "FAIL: expected 'unknown policy preset' in error output, got:"
    echo "$ERR_OUT"
    exit 1
fi
if [ -f "$BAD_HOME/grant-template.json" ]; then
    echo "FAIL: grant-template.json should not be written for unknown preset"
    exit 1
fi
echo "PASS: grant-template.json not written for unknown preset"

echo ""
echo "=== cf-init-policy.sh PASSED ==="

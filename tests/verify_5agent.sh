#!/usr/bin/env bash
# verify_5agent.sh — 8 automated checks for the 5-agent campfire integration test
#
# Usage: tests/verify_5agent.sh [workspace-root]
#   workspace-root defaults to /tmp/campfire-integ
#
# Exits 0 if all checks pass, non-zero on any failure.
# Each check prints PASS or FAIL with a description.

set -euo pipefail

WORKSPACE_ROOT="${1:-/tmp/campfire-integ}"
AGENTS_DIR="$WORKSPACE_ROOT/agents"
SHARED_WORKSPACE="$WORKSPACE_ROOT/shared/workspace"
LOGS_DIR="$WORKSPACE_ROOT/logs"

fail() {
    echo "FAIL: $1"
    echo ""
    echo "--- Diagnostic output ---"
    # Print last 50 lines of each agent log
    for agent in agent-a agent-b agent-c agent-d agent-e; do
        logfile="$LOGS_DIR/$agent.log"
        if [ -f "$logfile" ]; then
            echo "=== Last 50 lines of $agent.log ==="
            tail -50 "$logfile" || true
        fi
    done
    # Dump CF1 and CF2 messages if we have the IDs
    if [ -n "${CAMPFIRE_1:-}" ]; then
        echo "=== CF1 messages ==="
        CF_HOME="$AGENTS_DIR/agent-a" cf read "$CAMPFIRE_1" --all 2>&1 || true
    fi
    if [ -n "${CAMPFIRE_2:-}" ]; then
        echo "=== CF2 messages ==="
        CF_HOME="$AGENTS_DIR/agent-b" cf read "$CAMPFIRE_2" --all 2>&1 || true
    fi
    exit 1
}

# ─────────────────────────────────────────────────────────────────────────────
# Check 0: Discover campfire IDs from agent stores
# ─────────────────────────────────────────────────────────────────────────────
echo "Check 0: Discovering campfire IDs from agent stores..."

CAMPFIRE_1=$(CF_HOME="$AGENTS_DIR/agent-a" cf ls --json | python3 -c "
import json, sys
campfires = json.load(sys.stdin)
for c in campfires:
    if c.get('role') == 'creator':
        print(c['campfire_id'])
        break
" 2>/dev/null || true)

CAMPFIRE_2=$(CF_HOME="$AGENTS_DIR/agent-b" cf ls --json | python3 -c "
import json, sys
campfires = json.load(sys.stdin)
for c in campfires:
    if c.get('role') == 'creator':
        print(c['campfire_id'])
        break
" 2>/dev/null || true)

[ -n "$CAMPFIRE_1" ] || fail "Could not determine CF1 ID from agent-a store"
[ -n "$CAMPFIRE_2" ] || fail "Could not determine CF2 ID from agent-b store"

echo "  CF1=$CAMPFIRE_1"
echo "  CF2=$CAMPFIRE_2"
echo "PASS: Check 0 — campfire IDs discovered"

# ─────────────────────────────────────────────────────────────────────────────
# Check 1: CF1 has 5 members (all agents discovered and joined)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "Check 1: CF1 membership — all 5 agents should have joined..."

MEM1=$(CF_HOME="$AGENTS_DIR/agent-a" cf members "$CAMPFIRE_1" --json)
MEM1_COUNT=$(echo "$MEM1" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))")

[ "$MEM1_COUNT" -eq 5 ] || fail "CF1 membership: expected 5, got $MEM1_COUNT"
echo "PASS: Check 1 — CF1 has $MEM1_COUNT members"

# ─────────────────────────────────────────────────────────────────────────────
# Check 2: CF2 has 2 members (only implementers B and C)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "Check 2: CF2 membership — only implementers B and C should be members..."

MEM2=$(CF_HOME="$AGENTS_DIR/agent-b" cf members "$CAMPFIRE_2" --json)
MEM2_COUNT=$(echo "$MEM2" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))")

[ "$MEM2_COUNT" -eq 2 ] || fail "CF2 membership: expected 2 (only implementers), got $MEM2_COUNT"
echo "PASS: Check 2 — CF2 has $MEM2_COUNT members"

# ─────────────────────────────────────────────────────────────────────────────
# Check 3: D and E did NOT join CF2 (correct self-selection)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "Check 3: Self-selection — D and E must not be in CF2..."

echo "$MEM2" | python3 -c "
import json, sys, subprocess, os

members = json.load(sys.stdin)
member_keys = {m['public_key'] for m in members}

base_env = dict(os.environ)

d_result = subprocess.run(
    ['cf', 'id', '--json'],
    capture_output=True, text=True,
    env={**base_env, 'CF_HOME': '${AGENTS_DIR}/agent-d'}
)
if d_result.returncode != 0:
    print('FAIL: could not get agent-d public key: ' + d_result.stderr.strip())
    sys.exit(1)
d_key = json.loads(d_result.stdout)['public_key']

e_result = subprocess.run(
    ['cf', 'id', '--json'],
    capture_output=True, text=True,
    env={**base_env, 'CF_HOME': '${AGENTS_DIR}/agent-e'}
)
if e_result.returncode != 0:
    print('FAIL: could not get agent-e public key: ' + e_result.stderr.strip())
    sys.exit(1)
e_key = json.loads(e_result.stdout)['public_key']

if d_key in member_keys:
    print('FAIL: Agent D (reviewer) incorrectly joined implementation campfire')
    sys.exit(1)
if e_key in member_keys:
    print('FAIL: Agent E (QA) incorrectly joined implementation campfire')
    sys.exit(1)
print('OK: D and E correctly excluded from CF2')
" || fail "D and E self-selection check failed"

echo "PASS: Check 3 — D and E did not join CF2"

# ─────────────────────────────────────────────────────────────────────────────
# Check 4: DAG completeness — 4 futures, all fulfilled
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "Check 4: DAG completeness — 4 futures each with at least one fulfillment..."

MSGS=$(CF_HOME="$AGENTS_DIR/agent-a" cf read "$CAMPFIRE_1" --all --json)

FUTURE_COUNT=$(echo "$MSGS" | python3 -c "
import json, sys
msgs = json.load(sys.stdin)
futures = [m for m in msgs if 'future' in (m.get('tags') or [])]
print(len(futures))
")

[ "$FUTURE_COUNT" -eq 4 ] || fail "DAG: expected 4 futures, got $FUTURE_COUNT"

echo "$MSGS" | python3 -c "
import json, sys
msgs = json.load(sys.stdin)
futures = {m['id'] for m in msgs if 'future' in (m.get('tags') or [])}
fulfilled = set()
for m in msgs:
    if 'fulfills' in (m.get('tags') or []):
        for ant in m.get('antecedents', []):
            if ant in futures:
                fulfilled.add(ant)
unfulfilled = futures - fulfilled
if unfulfilled:
    print(f'FAIL: unfulfilled futures: {unfulfilled}')
    sys.exit(1)
print(f'OK: all {len(futures)} futures fulfilled')
" || fail "DAG completeness check failed"

echo "PASS: Check 4 — DAG complete ($FUTURE_COUNT futures, all fulfilled)"

# ─────────────────────────────────────────────────────────────────────────────
# Check 5: Provenance chain signatures valid for all CF1 messages
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "Check 5: Provenance chain validity — all message and hop signatures..."

echo "$MSGS" | python3 -c "
import json, sys, subprocess, os

msgs = json.load(sys.stdin)
base_env = {**dict(os.environ), 'CF_HOME': '${AGENTS_DIR}/agent-a'}

for m in msgs:
    result = subprocess.run(
        ['cf', 'inspect', m['id'], '--json'],
        capture_output=True, text=True,
        env=base_env
    )
    if result.returncode != 0:
        print(f'FAIL: inspect failed for message {m[\"id\"]}: {result.stderr.strip()}')
        sys.exit(1)
    try:
        inspection = json.loads(result.stdout)
    except json.JSONDecodeError as e:
        print(f'FAIL: could not parse inspect output for {m[\"id\"]}: {e}')
        sys.exit(1)
    if not inspection.get('signature_valid'):
        print(f'FAIL: message {m[\"id\"]} has invalid signature')
        sys.exit(1)
    for hop in inspection.get('provenance', []):
        if not hop.get('signature_valid'):
            print(f'FAIL: provenance hop invalid for message {m[\"id\"]}')
            sys.exit(1)

print(f'OK: all {len(msgs)} messages have valid signatures and provenance')
" || fail "Provenance chain validity check failed"

echo "PASS: Check 5 — all provenance chains valid"

# ─────────────────────────────────────────────────────────────────────────────
# Check 6: fizzbuzz output is correct (100 lines, correct values)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "Check 6: Code artifacts exist and fizzbuzz output is correct..."

[ -f "$SHARED_WORKSPACE/fizzbuzz/fizzbuzz.go" ] || fail "fizzbuzz.go not found at $SHARED_WORKSPACE/fizzbuzz/fizzbuzz.go"
[ -f "$SHARED_WORKSPACE/fizzbuzz/main.go" ] || fail "main.go not found at $SHARED_WORKSPACE/fizzbuzz/main.go"

OUTPUT=$(cd "$SHARED_WORKSPACE/fizzbuzz" && go run . 2>&1)
LINE_COUNT=$(echo "$OUTPUT" | wc -l)

[ "$LINE_COUNT" -eq 100 ] || fail "fizzbuzz: expected 100 lines, got $LINE_COUNT"

LINE_1=$(echo "$OUTPUT" | sed -n '1p')
LINE_3=$(echo "$OUTPUT" | sed -n '3p')
LINE_5=$(echo "$OUTPUT" | sed -n '5p')
LINE_15=$(echo "$OUTPUT" | sed -n '15p')

[ "$LINE_1" = "1" ] || fail "fizzbuzz line 1: expected '1', got '$LINE_1'"
[ "$LINE_3" = "Fizz" ] || fail "fizzbuzz line 3: expected 'Fizz', got '$LINE_3'"
[ "$LINE_5" = "Buzz" ] || fail "fizzbuzz line 5: expected 'Buzz', got '$LINE_5'"
[ "$LINE_15" = "FizzBuzz" ] || fail "fizzbuzz line 15: expected 'FizzBuzz', got '$LINE_15'"

echo "PASS: Check 6 — fizzbuzz correct ($LINE_COUNT lines, spot checks pass)"

# ─────────────────────────────────────────────────────────────────────────────
# Check 7: CF2 messages don't leak to non-members (Agent D cannot read CF2)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "Check 7: CF2 isolation — Agent D (non-member) cannot read CF2 messages..."

CF2_READ=$(CF_HOME="$AGENTS_DIR/agent-d" cf read "$CAMPFIRE_2" --all --json 2>&1) || true

if echo "$CF2_READ" | grep -qF "not a member"; then
    echo "  (got 'not a member' error — expected)"
else
    MSG_COUNT=$(echo "$CF2_READ" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
    [ "$MSG_COUNT" -eq 0 ] || fail "CF2 isolation: messages leaked to non-member Agent D ($MSG_COUNT messages visible)"
    echo "  (returned empty list — acceptable)"
fi

echo "PASS: Check 7 — CF2 messages not accessible to non-member D"

# ─────────────────────────────────────────────────────────────────────────────
# Check 8: DONE file contains "PASS"
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "Check 8: DONE file exists and contains 'PASS'..."

[ -f "$SHARED_WORKSPACE/DONE" ] || fail "DONE file missing at $SHARED_WORKSPACE/DONE"
grep -q "PASS" "$SHARED_WORKSPACE/DONE" || fail "DONE file does not contain 'PASS' (contents: $(cat "$SHARED_WORKSPACE/DONE"))"

echo "PASS: Check 8 — DONE file contains PASS"

# ─────────────────────────────────────────────────────────────────────────────
# All checks passed
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "PASS: all 8 checks passed"

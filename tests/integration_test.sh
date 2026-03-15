#!/bin/sh
# Integration test: Full 18-step dogfood scenario
# Two agents coordinate through a campfire with filesystem transport.
set -e

CF="go run ./cmd/cf"
export CF_BEACON_DIR=/tmp/integ-beacons
export CF_TRANSPORT_DIR=/tmp/integ-transport

# Clean up from previous runs
rm -rf /tmp/integ-beacons /tmp/integ-transport /tmp/integ-agent-a /tmp/integ-agent-b

assert_contains() {
    echo "$1" | grep -qF "$2" || { echo "FAIL: expected '$2' in output"; echo "$1"; exit 1; }
}

assert_not_contains() {
    echo "$1" | grep -q "$2" && { echo "FAIL: unexpected '$2' in output"; echo "$1"; exit 1; } || true
}

echo "Step 1: Agent A initializes identity"
export CF_HOME=/tmp/integ-agent-a
$CF init > /dev/null
AGENT_A=$($CF id)
echo "  Agent A: ${AGENT_A}"

echo "Step 2: Agent B initializes identity"
export CF_HOME=/tmp/integ-agent-b
$CF init > /dev/null
AGENT_B=$($CF id)
echo "  Agent B: ${AGENT_B}"

echo "Step 3: Agent A creates an open campfire"
export CF_HOME=/tmp/integ-agent-a
CFID=$($CF create --protocol open --require status-update --description "coordination")
echo "  Campfire: ${CFID}"

echo "Step 4: Agent A lists campfires"
LS_OUT=$($CF ls --json)
assert_contains "$LS_OUT" "$CFID"
echo "  OK"

echo "Step 5: Agent B discovers the beacon"
export CF_HOME=/tmp/integ-agent-b
DISC_OUT=$($CF discover --json)
assert_contains "$DISC_OUT" "$CFID"
echo "  OK"

echo "Step 6: Agent B joins"
$CF join "$CFID" > /dev/null
echo "  OK"

echo "Step 7: Agent A reads (sees member-joined)"
export CF_HOME=/tmp/integ-agent-a
READ_OUT=$($CF read "$CFID" --all --json)
assert_contains "$READ_OUT" "campfire:member-joined"
echo "  OK"

echo "Step 8: Agent A checks members"
MEM_OUT=$($CF members "$CFID" --json)
assert_contains "$MEM_OUT" "$AGENT_A"
assert_contains "$MEM_OUT" "$AGENT_B"
echo "  OK"

echo "Step 9: Agent A sends a message"
MSG1=$($CF send "$CFID" "starting task X" --tag status-update)
echo "  Message: $MSG1"

echo "Step 10: Agent B reads the message"
export CF_HOME=/tmp/integ-agent-b
READ_B=$($CF read "$CFID" --json)
assert_contains "$READ_B" "starting task X"
echo "  OK"

echo "Step 11: Agent B inspects the message (verifies provenance)"
INSPECT_OUT=$($CF inspect "$MSG1" --json)
assert_contains "$INSPECT_OUT" '"signature_valid": true'
echo "  OK"

echo "Step 12: Agent B sends a reply"
MSG2=$($CF send "$CFID" "task X blocked on Y" --tag status-update,blocker)
echo "  Message: $MSG2"

echo "Step 13: Agent A reads (sees the reply)"
export CF_HOME=/tmp/integ-agent-a
READ_A2=$($CF read "$CFID" --json)
assert_contains "$READ_A2" "task X blocked on Y"
echo "  OK"

echo "Step 14: Agent A sends a DM to B"
DM_OUT=$($CF dm "$AGENT_B" "can you unblock Y?" --json)
assert_contains "$DM_OUT" "false"
DM_CFID=$(echo "$DM_OUT" | grep campfire_id | head -1 | sed 's/.*": "//;s/".*//')
echo "  DM Campfire: ${DM_CFID}"

echo "Step 15: Agent B discovers and joins DM campfire"
export CF_HOME=/tmp/integ-agent-b
$CF join "$DM_CFID" > /dev/null
DM_READ=$($CF read "$DM_CFID" --all --json)
assert_contains "$DM_READ" "can you unblock Y?"
echo "  OK"

echo "Step 16: Agent B leaves the main campfire"
$CF leave "$CFID" > /dev/null
echo "  OK"

echo "Step 17: Agent A reads (sees member-left)"
export CF_HOME=/tmp/integ-agent-a
READ_A3=$($CF read "$CFID" --json)
assert_contains "$READ_A3" "campfire:member-left"
echo "  OK"

echo "Step 18: Agent A disbands the main campfire"
$CF disband "$CFID" > /dev/null
LS_FINAL=$($CF ls --json)
assert_not_contains "$LS_FINAL" "$CFID"
echo "  OK"

echo ""
echo "=== ALL 18 STEPS PASSED ==="

#!/bin/sh
# Integration test: P2P HTTP transport, threshold signing, eviction with rekey.
# Tests 1-4 cover p2p-http. Test 5 runs backward-compat checks (filesystem tests).
set -e

# Build binary first for reliable PID tracking (go run doesn't give the real binary PID).
echo "Building cf binary..."
go build -o /tmp/cf-test ./cmd/cf
CF="/tmp/cf-test"

export CF_BEACON_DIR=/tmp/p2p-beacons

PID_A_SERVE=""
PID_B_SERVE=""
PID_C_SERVE=""

cleanup() {
    [ -n "$PID_A_SERVE" ] && kill "$PID_A_SERVE" 2>/dev/null || true
    [ -n "$PID_B_SERVE" ] && kill "$PID_B_SERVE" 2>/dev/null || true
    [ -n "$PID_C_SERVE" ] && kill "$PID_C_SERVE" 2>/dev/null || true
    # Wait briefly for ports to release
    sleep 0.5
}
trap cleanup EXIT

assert_contains() {
    local label="$1" output="$2" needle="$3"
    echo "$output" | grep -qF "$needle" || {
        echo "FAIL [$label]: expected '$needle' in:"
        echo "$output"
        exit 1
    }
}

assert_not_contains() {
    local label="$1" output="$2" needle="$3"
    echo "$output" | grep -qF "$needle" && {
        echo "FAIL [$label]: unexpected '$needle' in:"
        echo "$output"
        exit 1
    } || true
}

wait_port() {
    # Wait for an HTTP server to be ready on given port (max 5s).
    local port="$1"
    local i=0
    while [ $i -lt 25 ]; do
        if wget -q -O /dev/null --timeout=1 "http://localhost:${port}/" 2>/dev/null; then
            return 0
        fi
        # A 404 response also means the server is up
        wget -q -O /dev/null --timeout=1 "http://localhost:${port}/" 2>&1 | grep -q "404\|returned error" && return 0
        sleep 0.2
        i=$((i + 1))
    done
    echo "ERROR: port $port never became ready"
    exit 1
}

kill_server() {
    local pid="$1"
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
    sleep 0.5  # Wait for port to release
}

echo "============================================================"
echo "Test 1: P2P HTTP basic (threshold=1, 2 agents)"
echo "============================================================"

rm -rf /tmp/p2p-beacons /tmp/p2p-agent-{a,b,c}

export CF_HOME=/tmp/p2p-agent-a
$CF init > /dev/null
AGENT_A=$($CF id)
echo "  Agent A: ${AGENT_A}"

export CF_HOME=/tmp/p2p-agent-b
$CF init > /dev/null
AGENT_B=$($CF id)
echo "  Agent B: ${AGENT_B}"

# Agent A creates campfire with threshold=1
export CF_HOME=/tmp/p2p-agent-a
CFID=$($CF create --transport p2p-http --listen :19001 --threshold 1 --description "p2p-test-1")
echo "  Campfire: ${CFID}"

# Start A's HTTP server in background
$CF serve "$CFID" --listen :19001 &
PID_A_SERVE=$!
wait_port 19001

# Agent B joins via A
export CF_HOME=/tmp/p2p-agent-b
JOIN_OUT=$($CF join "$CFID" --via http://localhost:19001 --json)
assert_contains "B joins" "$JOIN_OUT" "joined"
echo "  Step 1.1: B joined OK"

# A sends a message
export CF_HOME=/tmp/p2p-agent-a
MSG1=$($CF send "$CFID" "hello from A")
echo "  Step 1.2: A sent: $MSG1"

# B reads (syncs from A's server then reads local DB)
export CF_HOME=/tmp/p2p-agent-b
READ_B=$($CF read "$CFID" --all --json)
assert_contains "B reads A's msg" "$READ_B" "hello from A"
echo "  Step 1.3: B reads A's message OK"

# B sends a message to A
MSG2=$($CF send "$CFID" "hello from B")
echo "  Step 1.4: B sent: $MSG2"

# A reads
export CF_HOME=/tmp/p2p-agent-a
READ_A=$($CF read "$CFID" --all --json)
assert_contains "A reads B's msg" "$READ_A" "hello from B"
echo "  Step 1.5: A reads B's message OK"

# Verify signature on A's message (inspect from B's perspective)
export CF_HOME=/tmp/p2p-agent-b
INSPECT_OUT=$($CF inspect "$MSG1" --json)
assert_contains "msg sig valid" "$INSPECT_OUT" '"signature_valid": true'
echo "  Step 1.6: Message signature valid OK"

# Kill A's server, have A send another message (stored locally), restart server, B syncs
kill_server "$PID_A_SERVE"
PID_A_SERVE=""

export CF_HOME=/tmp/p2p-agent-a
MSG3=$($CF send "$CFID" "message while server was offline")
echo "  Step 1.7: A sent offline message: $MSG3"

# Restart A's server
$CF serve "$CFID" --listen :19001 &
PID_A_SERVE=$!
wait_port 19001

# B syncs and gets the missed message
export CF_HOME=/tmp/p2p-agent-b
READ_B2=$($CF read "$CFID" --all --json)
assert_contains "B gets missed msg" "$READ_B2" "message while server was offline"
echo "  Step 1.8: B synced missed message OK"

kill_server "$PID_A_SERVE"
PID_A_SERVE=""

echo ""
echo "=== Test 1 PASSED ==="
echo ""

echo "============================================================"
echo "Test 2: Threshold=2 signing (3 agents)"
echo "============================================================"

rm -rf /tmp/p2p-agent-a /tmp/p2p-agent-b /tmp/p2p-agent-c
rm -rf /tmp/p2p-beacons

export CF_HOME=/tmp/p2p-agent-a
$CF init > /dev/null
AGENT_A=$($CF id)
echo "  Agent A: ${AGENT_A}"

export CF_HOME=/tmp/p2p-agent-b
$CF init > /dev/null
AGENT_B=$($CF id)
echo "  Agent B: ${AGENT_B}"

export CF_HOME=/tmp/p2p-agent-c
$CF init > /dev/null
AGENT_C=$($CF id)
echo "  Agent C: ${AGENT_C}"

# A creates campfire with threshold=2, 3 participants
export CF_HOME=/tmp/p2p-agent-a
CFID2=$($CF create --transport p2p-http --listen :19001 --threshold 2 --participants 3 --description "threshold-test")
echo "  Campfire: ${CFID2}"

$CF serve "$CFID2" --listen :19001 &
PID_A_SERVE=$!
wait_port 19001

# B joins and starts serving
export CF_HOME=/tmp/p2p-agent-b
$CF join "$CFID2" --via http://localhost:19001 --listen :19002
$CF serve "$CFID2" --listen :19002 &
PID_B_SERVE=$!
wait_port 19002

# C joins and starts serving
export CF_HOME=/tmp/p2p-agent-c
$CF join "$CFID2" --via http://localhost:19001 --listen :19003
$CF serve "$CFID2" --listen :19003 &
PID_C_SERVE=$!
wait_port 19003

echo "  Step 2.1: All 3 agents joined and serving OK"

# A sends a message — triggers FROST threshold signing with co-signers
export CF_HOME=/tmp/p2p-agent-a
MSG_T_OUT=$($CF send "$CFID2" "threshold message from A" --json)
MSG_T_ID=$(echo "$MSG_T_OUT" | grep '"id"' | head -1 | sed 's/.*": "//;s/".*//')
echo "  Step 2.2: A sent threshold message: $MSG_T_ID"

# Verify message signature
INSPECT_T=$($CF inspect "$MSG_T_ID" --json)
assert_contains "threshold msg sig valid" "$INSPECT_T" '"signature_valid": true'
echo "  Step 2.3: Message signature valid OK"

# B reads
export CF_HOME=/tmp/p2p-agent-b
READ_T_B=$($CF read "$CFID2" --all --json)
assert_contains "B reads threshold msg" "$READ_T_B" "threshold message from A"
echo "  Step 2.4: B reads threshold message OK"

# C reads
export CF_HOME=/tmp/p2p-agent-c
READ_T_C=$($CF read "$CFID2" --all --json)
assert_contains "C reads threshold msg" "$READ_T_C" "threshold message from A"
echo "  Step 2.5: C reads threshold message OK"

kill_server "$PID_A_SERVE"
kill_server "$PID_B_SERVE"
kill_server "$PID_C_SERVE"
PID_A_SERVE="" PID_B_SERVE="" PID_C_SERVE=""

echo ""
echo "=== Test 2 PASSED ==="
echo ""

echo "============================================================"
echo "Test 3: Eviction with rekey (threshold=2)"
echo "============================================================"

rm -rf /tmp/p2p-agent-a /tmp/p2p-agent-b /tmp/p2p-agent-c
rm -rf /tmp/p2p-beacons

export CF_HOME=/tmp/p2p-agent-a
$CF init > /dev/null
AGENT_A=$($CF id)

export CF_HOME=/tmp/p2p-agent-b
$CF init > /dev/null
AGENT_B=$($CF id)

export CF_HOME=/tmp/p2p-agent-c
$CF init > /dev/null
AGENT_C=$($CF id)

# A creates with threshold=2, 3 participants
export CF_HOME=/tmp/p2p-agent-a
CFID3=$($CF create --transport p2p-http --listen :19001 --threshold 2 --participants 3)
echo "  Campfire (old): ${CFID3}"

$CF serve "$CFID3" --listen :19001 &
PID_A_SERVE=$!
wait_port 19001

export CF_HOME=/tmp/p2p-agent-b
$CF join "$CFID3" --via http://localhost:19001 --listen :19002
$CF serve "$CFID3" --listen :19002 &
PID_B_SERVE=$!
wait_port 19002

export CF_HOME=/tmp/p2p-agent-c
$CF join "$CFID3" --via http://localhost:19001 --listen :19003
$CF serve "$CFID3" --listen :19003 &
PID_C_SERVE=$!
wait_port 19003

echo "  Step 3.1: 3 agents joined OK"

# A evicts C (triggers threshold-signed rekey and new DKG)
export CF_HOME=/tmp/p2p-agent-a
EVICT_OUT=$($CF evict "$CFID3" "$AGENT_C" --json)
NEW_CFID3=$(echo "$EVICT_OUT" | grep '"new_campfire_id"' | sed 's/.*": "//;s/".*//')
echo "  Step 3.2: Evicted C. New campfire: ${NEW_CFID3:0:12}"

# Campfire ID must change
if [ "$CFID3" = "$NEW_CFID3" ]; then
    echo "FAIL: campfire ID did not change after eviction"
    exit 1
fi
echo "  Step 3.3: Campfire public key changed OK"

# A's membership should now reference the new campfire ID
export CF_HOME=/tmp/p2p-agent-a
LS_A=$($CF ls --json)
assert_contains "new cfid in A's ls" "$LS_A" "${NEW_CFID3}"
echo "  Step 3.4: A's campfire ID updated OK"

# Kill all old servers
kill_server "$PID_A_SERVE"
kill_server "$PID_B_SERVE"
kill_server "$PID_C_SERVE"
PID_A_SERVE="" PID_B_SERVE="" PID_C_SERVE=""

# Restart A and B with new campfire ID
export CF_HOME=/tmp/p2p-agent-a
$CF serve "$NEW_CFID3" --listen :19001 &
PID_A_SERVE=$!
wait_port 19001

export CF_HOME=/tmp/p2p-agent-b
$CF serve "$NEW_CFID3" --listen :19002 &
PID_B_SERVE=$!
wait_port 19002

# A sends under new identity (threshold=2 still — but now only 2 members, so threshold≤members)
export CF_HOME=/tmp/p2p-agent-a
MSG_REKEY=$($CF send "$NEW_CFID3" "post-eviction message from A")
echo "  Step 3.5: A sent post-eviction message: $MSG_REKEY"

# B reads
export CF_HOME=/tmp/p2p-agent-b
READ_B_REKEY=$($CF read "$NEW_CFID3" --all --json)
assert_contains "B reads post-eviction msg" "$READ_B_REKEY" "post-eviction message from A"
echo "  Step 3.6: B reads post-eviction message OK"

kill_server "$PID_A_SERVE"
kill_server "$PID_B_SERVE"
PID_A_SERVE="" PID_B_SERVE=""

echo ""
echo "=== Test 3 PASSED ==="
echo ""

echo "============================================================"
echo "Test 4: Eviction with rekey (threshold=1)"
echo "============================================================"

rm -rf /tmp/p2p-agent-a /tmp/p2p-agent-b /tmp/p2p-agent-c
rm -rf /tmp/p2p-beacons

export CF_HOME=/tmp/p2p-agent-a
$CF init > /dev/null
AGENT_A=$($CF id)

export CF_HOME=/tmp/p2p-agent-b
$CF init > /dev/null
AGENT_B=$($CF id)

export CF_HOME=/tmp/p2p-agent-c
$CF init > /dev/null
AGENT_C=$($CF id)

# A creates with threshold=1
export CF_HOME=/tmp/p2p-agent-a
CFID4=$($CF create --transport p2p-http --listen :19001 --threshold 1)
echo "  Campfire (old): ${CFID4}"

$CF serve "$CFID4" --listen :19001 &
PID_A_SERVE=$!
wait_port 19001

export CF_HOME=/tmp/p2p-agent-b
$CF join "$CFID4" --via http://localhost:19001 --listen :19002
$CF serve "$CFID4" --listen :19002 &
PID_B_SERVE=$!
wait_port 19002

export CF_HOME=/tmp/p2p-agent-c
$CF join "$CFID4" --via http://localhost:19001
# C joins without a listener (threshold=1, not needed)

echo "  Step 4.1: 3 agents joined (C without listener) OK"

# A evicts C — new keypair for threshold=1
export CF_HOME=/tmp/p2p-agent-a
EVICT_OUT4=$($CF evict "$CFID4" "$AGENT_C" --json)
NEW_CFID4=$(echo "$EVICT_OUT4" | grep '"new_campfire_id"' | sed 's/.*": "//;s/".*//')
echo "  Step 4.2: Evicted C. New campfire: ${NEW_CFID4:0:12}"

if [ "$CFID4" = "$NEW_CFID4" ]; then
    echo "FAIL: campfire ID did not change after threshold=1 eviction"
    exit 1
fi
echo "  Step 4.3: Campfire public key changed OK"

# Kill old servers
kill_server "$PID_A_SERVE"
kill_server "$PID_B_SERVE"
PID_A_SERVE="" PID_B_SERVE=""

# Restart with new campfire ID
export CF_HOME=/tmp/p2p-agent-a
$CF serve "$NEW_CFID4" --listen :19001 &
PID_A_SERVE=$!
wait_port 19001

export CF_HOME=/tmp/p2p-agent-b
$CF serve "$NEW_CFID4" --listen :19002 &
PID_B_SERVE=$!
wait_port 19002

# A and B communicate under new identity
export CF_HOME=/tmp/p2p-agent-a
MSG_T1=$($CF send "$NEW_CFID4" "post-rekey message from A")
echo "  Step 4.4: A sent post-rekey message: $MSG_T1"

export CF_HOME=/tmp/p2p-agent-b
READ_T1=$($CF read "$NEW_CFID4" --all --json)
assert_contains "B reads post-rekey msg" "$READ_T1" "post-rekey message from A"
echo "  Step 4.5: B reads post-rekey message OK"

# Beacon for new campfire should exist
NEW_BEACON=$(ls /tmp/p2p-beacons/ 2>/dev/null | wc -l)
if [ "$NEW_BEACON" -gt 0 ]; then
    echo "  Step 4.6: Beacons present ($NEW_BEACON files) OK"
else
    echo "  Step 4.6: WARNING - no beacons found (expected post-rekey beacon for threshold=1)"
fi

kill_server "$PID_A_SERVE"
kill_server "$PID_B_SERVE"
PID_A_SERVE="" PID_B_SERVE=""

echo ""
echo "=== Test 4 PASSED ==="
echo ""

echo "============================================================"
echo "Test 5: Backward compatibility (filesystem transport)"
echo "============================================================"

export CF_BEACON_DIR=/tmp/p2p-compat-beacons
export CF_TRANSPORT_DIR=/tmp/p2p-compat-transport
rm -rf /tmp/p2p-compat-beacons /tmp/p2p-compat-transport

sh tests/integration_test.sh
echo ""
sh tests/future_test.sh

echo ""
echo "=== Test 5 PASSED ==="
echo ""
echo "============================================================"
echo "ALL 5 TESTS PASSED"
echo "============================================================"

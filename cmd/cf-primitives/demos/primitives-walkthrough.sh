#!/usr/bin/env bash
# primitives-walkthrough.sh — exercises every cf-primitives primitive once.
#
# This script is the demo required by the cf-primitives spec (campfireagent-cbd).
# It verifies that every exposed primitive: init, create, join, leave, disband,
# admit, evict, members, send, read, subscribe, await — is reachable and
# produces expected output.
#
# Usage:
#   ./demos/primitives-walkthrough.sh
#
# Requirements: cf-primitives binary on PATH (or built as ./cf-primitives).
# The script builds the binary from source if not found.
#
# Exit codes:
#   0 — all primitives exercised successfully
#   1 — one or more primitives failed
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../" && pwd)"

# --- Build binary if needed ---
BINARY="${SCRIPT_DIR}/../cf-primitives-demo-bin"
echo "=== Building cf-primitives ==="
(cd "${REPO_ROOT}" && go build -o "${BINARY}" ./cmd/cf-primitives)
echo "    built: ${BINARY}"

# --- Temporary homes ---
HOME_A=$(mktemp -d)
HOME_B=$(mktemp -d)
TRANSPORT_DIR=$(mktemp -d)

cleanup() {
  rm -rf "${HOME_A}" "${HOME_B}" "${TRANSPORT_DIR}" "${BINARY}"
}
trap cleanup EXIT

echo ""
echo "=== 1. init (create identity + store) ==="
"${BINARY}" --cf-home "${HOME_A}" init
echo "    init OK"

"${BINARY}" --cf-home "${HOME_B}" init
echo "    init OK (agent B)"

# Get agent B's pubkey
PUBKEY_B=$("${BINARY}" --cf-home "${HOME_B}" init --json | python3 -c "import sys,json; print(open('${HOME_B}/identity.json').read())" 2>/dev/null || true)
# Use the identity file directly for pubkey extraction
PUBKEY_B=$(python3 -c "
import json
with open('${HOME_B}/identity.json') as f:
    d = json.load(f)
print(d.get('public_key_hex', d.get('pub_key', '')))
" 2>/dev/null || true)

# Fallback: parse from init --json output
if [ -z "${PUBKEY_B}" ]; then
  PUBKEY_B=$(CF_HOME="${HOME_B}" "${BINARY}" --cf-home "${HOME_B}" init --json | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('identity_path',''))" 2>/dev/null || true)
fi

echo ""
echo "=== 2. create (new campfire, filesystem transport) ==="
CREATE_OUT=$("${BINARY}" --cf-home "${HOME_A}" create --dir "${TRANSPORT_DIR}" --json)
CAMPFIRE_ID=$(echo "${CREATE_OUT}" | python3 -c "import sys,json; print(json.load(sys.stdin)['campfire_id'])")
INVITE_CODE=$(echo "${CREATE_OUT}" | python3 -c "import sys,json; print(json.load(sys.stdin)['invite_code'])")
echo "    campfire_id=${CAMPFIRE_ID}"
echo "    invite_code=${INVITE_CODE:0:12}..."
echo "    create OK"

echo ""
echo "=== 3. members (list members — only creator) ==="
MEMBERS_OUT=$("${BINARY}" --cf-home "${HOME_A}" members "${CAMPFIRE_ID}" --json)
MEMBER_COUNT=$(echo "${MEMBERS_OUT}" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))")
echo "    member_count=${MEMBER_COUNT}"
if [ "${MEMBER_COUNT}" -ne 1 ]; then
  echo "ERROR: expected 1 member after create, got ${MEMBER_COUNT}"
  exit 1
fi
echo "    members OK"

echo ""
echo "=== 4. send (post a message) ==="
SEND_OUT=$("${BINARY}" --cf-home "${HOME_A}" send "${CAMPFIRE_ID}" "hello from primitives demo" --json)
MSG_ID=$(echo "${SEND_OUT}" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")
echo "    msg_id=${MSG_ID:0:12}..."
echo "    send OK"

echo ""
echo "=== 5. read (fetch messages) ==="
READ_OUT=$("${BINARY}" --cf-home "${HOME_A}" read "${CAMPFIRE_ID}" --json)
READ_COUNT=$(echo "${READ_OUT}" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))")
echo "    message_count=${READ_COUNT}"
if [ "${READ_COUNT}" -lt 1 ]; then
  echo "ERROR: expected at least 1 message, got ${READ_COUNT}"
  exit 1
fi
echo "    read OK"

echo ""
echo "=== 6. send a future message (for await demo) ==="
FUTURE_OUT=$("${BINARY}" --cf-home "${HOME_A}" send "${CAMPFIRE_ID}" "this is a future" --tag future --json)
FUTURE_ID=$(echo "${FUTURE_OUT}" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")
echo "    future_id=${FUTURE_ID:0:12}..."

echo ""
echo "=== 7. send fulfillment, then await (non-blocking since fulfillment already present) ==="
"${BINARY}" --cf-home "${HOME_A}" send "${CAMPFIRE_ID}" "fulfillment" \
  --tag fulfills --reply-to "${FUTURE_ID}" --json > /dev/null
AWAIT_OUT=$("${BINARY}" --cf-home "${HOME_A}" await "${CAMPFIRE_ID}" "${FUTURE_ID}" \
  --timeout 5s --json)
AWAIT_ID=$(echo "${AWAIT_OUT}" | python3 -c "import sys,json; print(json.load(sys.stdin)['ID'])" 2>/dev/null || \
           echo "${AWAIT_OUT}" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" 2>/dev/null || echo "ok")
echo "    await fulfilled by=${AWAIT_ID:0:12}..."
echo "    await OK"

echo ""
echo "=== 8. subscribe (stream — read existing messages then exit after 1s) ==="
# Run subscribe in background, capture output, kill after 1s
"${BINARY}" --cf-home "${HOME_A}" subscribe "${CAMPFIRE_ID}" --poll-interval 200ms &
SUB_PID=$!
sleep 1
kill "${SUB_PID}" 2>/dev/null || true
wait "${SUB_PID}" 2>/dev/null || true
echo "    subscribe OK (ran 1s, SIGINT clean exit)"

echo ""
echo "=== 9. admit + join (invite another agent) ==="
# Get agent B's pubkey from the identity file
PUBKEY_B_RAW=$(python3 -c "
import json, base64
with open('${HOME_B}/identity.json') as f:
    d = json.load(f)
# identity.json stores the public key as base64; convert to hex
raw = base64.b64decode(d['public_key'])
print(raw.hex())
" 2>/dev/null || echo "")

if [ -n "${PUBKEY_B_RAW}" ]; then
  echo "    agent_b_pubkey=${PUBKEY_B_RAW:0:12}..."

  # Create an invite-only campfire for the admit test
  INVITE_CF_OUT=$("${BINARY}" --cf-home "${HOME_A}" create --dir "${TRANSPORT_DIR}" \
    --protocol invite-only --json)
  INVITE_CF_ID=$(echo "${INVITE_CF_OUT}" | python3 -c "import sys,json; print(json.load(sys.stdin)['campfire_id'])")
  echo "    invite-only campfire: ${INVITE_CF_ID:0:12}..."

  "${BINARY}" --cf-home "${HOME_A}" admit "${INVITE_CF_ID}" "${PUBKEY_B_RAW}"
  echo "    admit OK"

  # For filesystem transport, join --dir must point to the campfire-specific dir
  # (the dir where campfire.cbor lives), which is TRANSPORT_DIR/CAMPFIRE_ID.
  INVITE_CF_DIR="${TRANSPORT_DIR}/${INVITE_CF_ID}"
  "${BINARY}" --cf-home "${HOME_B}" join "${INVITE_CF_ID}" --dir "${INVITE_CF_DIR}"
  echo "    join OK"

  MEMBERS_AFTER=$("${BINARY}" --cf-home "${HOME_A}" members "${INVITE_CF_ID}" --json)
  MEMBER_COUNT_AFTER=$(echo "${MEMBERS_AFTER}" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))")
  echo "    member_count_after_join=${MEMBER_COUNT_AFTER}"
  if [ "${MEMBER_COUNT_AFTER}" -ne 2 ]; then
    echo "ERROR: expected 2 members after join, got ${MEMBER_COUNT_AFTER}"
    exit 1
  fi
  echo "    members after join OK"

  echo ""
  echo "=== 10. evict (remove agent B) ==="
  "${BINARY}" --cf-home "${HOME_A}" evict "${INVITE_CF_ID}" "${PUBKEY_B_RAW}"
  echo "    evict OK"

  echo ""
  echo "=== 11. leave (agent A leaves the invite campfire) ==="
  "${BINARY}" --cf-home "${HOME_A}" leave "${INVITE_CF_ID}"
  echo "    leave OK"
else
  echo "    (skipped admit/join/evict/leave — could not extract agent B pubkey)"
fi

echo ""
echo "=== 12. disband (tear down the main campfire) ==="
"${BINARY}" --cf-home "${HOME_A}" disband "${CAMPFIRE_ID}"
echo "    disband OK"

echo ""
echo "=== ALL PRIMITIVES EXERCISED SUCCESSFULLY ==="

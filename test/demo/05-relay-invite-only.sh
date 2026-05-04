#!/usr/bin/env bash
# REQUIRES_PROD: hosted relay (mcp.getcampfire.dev) required; cf read on relay does not deliver messages from other members in CI. Use 09-local-relay.sh for a CI-equivalent test.
# 05-relay-invite-only.sh — Invite-only campfire. Unadmitted agent
# gets rejected. Admitted agent joins and completes request-response.
source "$(dirname "$0")/lib.sh"
require_hosted_relay

trap cleanup EXIT

section "Setup: owner, authorized daemon, intruder"
OWNER_HOME=$(new_identity owner); register_cleanup "$OWNER_HOME"
DAEMON_HOME=$(new_identity daemon); register_cleanup "$DAEMON_HOME"
INTRUDER_HOME=$(new_identity intruder); register_cleanup "$INTRUDER_HOME"
DAEMON_PUB=$(pubkey_of "$DAEMON_HOME")
echo "Owner:    $(pubkey_of "$OWNER_HOME")"
echo "Daemon:   $DAEMON_PUB"
echo "Intruder: $(pubkey_of "$INTRUDER_HOME")"

section "Owner creates invite-only relay campfire"
CF_ID=$(create_campfire --cf-home "$OWNER_HOME" --via "$RELAY_URL" --join-protocol invite-only)
echo "Campfire (invite-only): $CF_ID"

section "Intruder attempts to join (should fail)"
assert_nonzero_exit "Intruder rejected" \
    cf join "$CF_ID" --cf-home "$INTRUDER_HOME" --via "$RELAY_URL"

section "Owner admits daemon"
cf admit "$CF_ID" "$DAEMON_PUB" --cf-home "$OWNER_HOME" 2>/dev/null
echo "Daemon admitted"

section "Daemon joins"
cf join "$CF_ID" --cf-home "$DAEMON_HOME" --via "$RELAY_URL" 2>/dev/null
echo "Daemon joined"

section "Owner sends request"
cf send "$CF_ID" --cf-home "$OWNER_HOME" --tag task "Secure task for daemon" 2>/dev/null

section "Daemon reads and responds"
READ_OUT=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all --tag task 2>/dev/null)
assert_contains "Daemon reads invite-only campfire" "$READ_OUT" "Secure task for daemon"
cf send "$CF_ID" --cf-home "$DAEMON_HOME" --tag result "Task complete" 2>/dev/null

section "Owner reads response"
RESP=$(cf read "$CF_ID" --cf-home "$OWNER_HOME" --json --all --tag result 2>/dev/null)
assert_contains "Owner reads daemon response" "$RESP" "Task complete"

section "Cleanup"
cf leave "$CF_ID" --cf-home "$OWNER_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$DAEMON_HOME" 2>/dev/null || true

summary

#!/usr/bin/env bash
# 04-relay-beacon-join.sh — Server shares beacon. Daemon joins via
# beacon URI (not raw --via). Same request-response cycle works.
source "$(dirname "$0")/lib.sh"
require_hosted_relay

trap cleanup EXIT

section "Setup"
SERVER_HOME=$(new_identity server); register_cleanup "$SERVER_HOME"
DAEMON_HOME=$(new_identity daemon); register_cleanup "$DAEMON_HOME"

section "Server creates relay campfire"
CF_ID=$(create_campfire --cf-home "$SERVER_HOME" --via "$RELAY_URL")
echo "Campfire: $CF_ID"

section "Server exports beacon"
BEACON=$(cf share "$CF_ID" --cf-home "$SERVER_HOME" 2>/dev/null)
echo "Beacon: ${BEACON:0:40}..."
assert_contains "Beacon starts with beacon:" "$BEACON" "beacon:"

section "Daemon joins via beacon URI"
cf join "$BEACON" --cf-home "$DAEMON_HOME" 2>/dev/null
echo "Daemon joined via beacon"

section "Server sends request"
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag request "Beacon-delivered request" 2>/dev/null

section "Daemon reads via beacon-joined campfire"
READ_OUT=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all --tag request 2>/dev/null)
assert_contains "Daemon reads beacon-joined campfire" "$READ_OUT" "Beacon-delivered request"

section "Daemon responds"
cf send "$CF_ID" --cf-home "$DAEMON_HOME" --tag response "Beacon response OK" 2>/dev/null

section "Server reads response"
RESP=$(cf read "$CF_ID" --cf-home "$SERVER_HOME" --json --all --tag response 2>/dev/null)
assert_contains "Server reads daemon response" "$RESP" "Beacon response OK"

section "Cleanup"
cf leave "$CF_ID" --cf-home "$SERVER_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$DAEMON_HOME" 2>/dev/null || true

summary

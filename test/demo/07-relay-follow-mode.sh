#!/usr/bin/env bash
# 07-relay-follow-mode.sh — cf read --follow --tag receives messages
# as they arrive. Daemon is polling, server sends, daemon sees it.
source "$(dirname "$0")/lib.sh"
require_hosted_relay

trap cleanup EXIT

section "Setup"
SERVER_HOME=$(new_identity server); register_cleanup "$SERVER_HOME"
DAEMON_HOME=$(new_identity daemon); register_cleanup "$DAEMON_HOME"

section "Server creates relay campfire"
CF_ID=$(create_campfire --cf-home "$SERVER_HOME" --via "$RELAY_URL")
echo "Campfire: $CF_ID"

section "Daemon joins"
cf join "$CF_ID" --cf-home "$DAEMON_HOME" --via "$RELAY_URL" 2>/dev/null

section "Start daemon in follow mode (background)"
FOLLOW_OUT=$(mktemp /tmp/cf-follow-XXXX)
register_cleanup "$FOLLOW_OUT"
cf read "$CF_ID" --cf-home "$DAEMON_HOME" --follow --tag live 2>/dev/null >"$FOLLOW_OUT" &
FOLLOW_PID=$!

# Give the follow loop time to start
sleep 2

section "Server sends a message while daemon is polling"
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag live "Live message 1" 2>/dev/null
echo "Sent live message 1"

# Wait for poll cycle to pick it up (relay round-trip + poll interval)
sleep 10

section "Server sends another message"
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag live "Live message 2" 2>/dev/null
echo "Sent live message 2"

sleep 10

section "Stop follow mode and check output"
kill "$FOLLOW_PID" 2>/dev/null || true
wait "$FOLLOW_PID" 2>/dev/null || true

FOLLOW_CONTENT=$(cat "$FOLLOW_OUT")
echo "Follow output:"
echo "$FOLLOW_CONTENT"

assert_contains "Follow mode captured message 1" "$FOLLOW_CONTENT" "Live message 1"
assert_contains "Follow mode captured message 2" "$FOLLOW_CONTENT" "Live message 2"

section "Cleanup"
cf leave "$CF_ID" --cf-home "$SERVER_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$DAEMON_HOME" 2>/dev/null || true

summary

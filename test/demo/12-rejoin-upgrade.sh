#!/usr/bin/env bash
# REQUIRES_PROD: hosted relay (mcp.getcampfire.dev) required; cf read on relay does not deliver messages from other members in CI. Use 09-local-relay.sh for a CI-equivalent test.
# 12-rejoin-upgrade.sh — Leave and re-join a relay campfire.
# Messages sent while away are still readable from relay.
# New JoinedAt. Covers the binary upgrade path.
source "$(dirname "$0")/lib.sh"
require_hosted_relay

trap cleanup EXIT

section "Setup"
SERVER_HOME=$(new_identity server); register_cleanup "$SERVER_HOME"
DAEMON_HOME=$(new_identity daemon); register_cleanup "$DAEMON_HOME"

section "Server creates relay campfire"
CF_ID=$(create_campfire --cf-home "$SERVER_HOME" --via "$RELAY_URL")
echo "Campfire: $CF_ID"

section "Daemon joins, reads initial message"
cf join "$CF_ID" --cf-home "$DAEMON_HOME" --via "$RELAY_URL" 2>/dev/null
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag phase1 "Before leave" 2>/dev/null
READ1=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all --tag phase1 2>/dev/null)
assert_contains "Daemon reads before leave" "$READ1" "Before leave"

section "Daemon leaves"
cf leave "$CF_ID" --cf-home "$DAEMON_HOME" 2>/dev/null
echo "Daemon left"

section "Server sends while daemon is away"
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag phase2 "While daemon was away" 2>/dev/null
echo "Sent message while daemon away"

section "Daemon re-joins (simulating binary upgrade)"
cf join "$CF_ID" --cf-home "$DAEMON_HOME" --via "$RELAY_URL" 2>/dev/null
echo "Daemon re-joined"

section "Daemon reads messages from relay (including while-away)"
READ_ALL=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all 2>/dev/null)
assert_contains "Daemon sees pre-leave message" "$READ_ALL" "Before leave"
assert_contains "Daemon sees while-away message" "$READ_ALL" "While daemon was away"

section "New messages still work after rejoin"
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag phase3 "After rejoin" 2>/dev/null
READ_NEW=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all --tag phase3 2>/dev/null)
assert_contains "Post-rejoin message works" "$READ_NEW" "After rejoin"

section "Cleanup"
cf leave "$CF_ID" --cf-home "$SERVER_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$DAEMON_HOME" 2>/dev/null || true

summary

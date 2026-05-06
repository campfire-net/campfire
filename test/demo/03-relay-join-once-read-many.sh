#!/usr/bin/env bash
# REQUIRES_FIX: relay-redeploy-pending — passes only after mcp.getcampfire.dev is redeployed with the UseNumber() fix (campfireagent-c39 / PR #544). Remove this marker after deploy.
# 03-relay-join-once-read-many.sh — Daemon joins once at startup.
# Server sends 3 messages over time. Daemon reads each with cursor
# advancement. No re-join needed.
source "$(dirname "$0")/lib.sh"
require_hosted_relay

trap cleanup EXIT

section "Setup"
SERVER_HOME=$(new_identity server); register_cleanup "$SERVER_HOME"
DAEMON_HOME=$(new_identity daemon); register_cleanup "$DAEMON_HOME"

section "Server creates relay campfire"
CF_ID=$(create_campfire --cf-home "$SERVER_HOME" --via "$RELAY_URL")
echo "Campfire: $CF_ID"

section "Daemon joins once"
cf join "$CF_ID" --cf-home "$DAEMON_HOME" --via "$RELAY_URL" 2>/dev/null
echo "Daemon joined"

section "Server sends message 1"
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag batch "Message ONE" 2>/dev/null

section "Daemon reads (should see message 1)"
READ1=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all --tag batch 2>/dev/null)
assert_contains "Daemon sees message 1" "$READ1" "Message ONE"

section "Server sends messages 2 and 3"
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag batch "Message TWO" 2>/dev/null
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag batch "Message THREE" 2>/dev/null

section "Daemon reads again (should see all 3 without re-joining)"
READ_ALL=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all --tag batch 2>/dev/null)
assert_contains "Daemon sees message 1" "$READ_ALL" "Message ONE"
assert_contains "Daemon sees message 2" "$READ_ALL" "Message TWO"
assert_contains "Daemon sees message 3" "$READ_ALL" "Message THREE"

section "Daemon reads without --all (cursor-based, new messages only)"
# First read advances cursor. Second read should only show new.
cf read "$CF_ID" --cf-home "$DAEMON_HOME" --tag batch >/dev/null 2>&1 || true
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag batch "Message FOUR" 2>/dev/null
sleep 5  # allow relay propagation + cold-start latency
READ_NEW=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --tag batch 2>/dev/null)
assert_contains "Cursor read sees message 4" "$READ_NEW" "Message FOUR"
assert_not_contains "Cursor read does not re-show message 1" "$READ_NEW" "Message ONE"

section "Cleanup"
cf leave "$CF_ID" --cf-home "$SERVER_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$DAEMON_HOME" 2>/dev/null || true

summary

#!/usr/bin/env bash
# 06-relay-three-agents.sh — Server + two daemons on same relay campfire.
# Server sends. Both daemons read. Both respond. Server reads both.
source "$(dirname "$0")/lib.sh"
require_hosted_relay
# REQUIRES_FIX: cross-relay-read-broken — even after rc.2 deploy with aztable JSON precision fix (c39/PR #544), daemon cannot read server messages via hosted relay. Local repro: server sends, daemon joins+reads → empty result. Suspect: another precision/serialization loss in the message path beyond aztable (HTTP transport JSON, message store sync, or daemon-side handling). See campfireagent-89c follow-up item.

trap cleanup EXIT

section "Setup: server + two daemons"
SERVER_HOME=$(new_identity server); register_cleanup "$SERVER_HOME"
DAEMON1_HOME=$(new_identity daemon1); register_cleanup "$DAEMON1_HOME"
DAEMON2_HOME=$(new_identity daemon2); register_cleanup "$DAEMON2_HOME"

section "Server creates relay campfire"
CF_ID=$(create_campfire --cf-home "$SERVER_HOME" --via "$RELAY_URL")
echo "Campfire: $CF_ID"

section "Both daemons join (no endpoints)"
cf join "$CF_ID" --cf-home "$DAEMON1_HOME" --via "$RELAY_URL" 2>/dev/null
echo "Daemon 1 joined"
cf join "$CF_ID" --cf-home "$DAEMON2_HOME" --via "$RELAY_URL" 2>/dev/null
echo "Daemon 2 joined"

section "Server broadcasts a task"
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag broadcast "Task for all daemons" 2>/dev/null

section "Both daemons read the broadcast"
READ1=$(cf read "$CF_ID" --cf-home "$DAEMON1_HOME" --json --all --tag broadcast 2>/dev/null)
READ2=$(cf read "$CF_ID" --cf-home "$DAEMON2_HOME" --json --all --tag broadcast 2>/dev/null)
assert_contains "Daemon 1 sees broadcast" "$READ1" "Task for all daemons"
assert_contains "Daemon 2 sees broadcast" "$READ2" "Task for all daemons"

section "Both daemons respond"
cf send "$CF_ID" --cf-home "$DAEMON1_HOME" --tag response --tag daemon1 "D1 done" 2>/dev/null
cf send "$CF_ID" --cf-home "$DAEMON2_HOME" --tag response --tag daemon2 "D2 done" 2>/dev/null

section "Server reads both responses"
RESP=$(cf read "$CF_ID" --cf-home "$SERVER_HOME" --json --all --tag response 2>/dev/null)
assert_contains "Server sees daemon 1 response" "$RESP" "D1 done"
assert_contains "Server sees daemon 2 response" "$RESP" "D2 done"

section "Cross-visibility: daemons see each other"
ALL1=$(cf read "$CF_ID" --cf-home "$DAEMON1_HOME" --json --all 2>/dev/null)
assert_contains "Daemon 1 sees daemon 2 response" "$ALL1" "D2 done"

section "Cleanup"
cf leave "$CF_ID" --cf-home "$SERVER_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$DAEMON1_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$DAEMON2_HOME" 2>/dev/null || true

summary

#!/usr/bin/env bash
# 02-relay-request-response.sh — Bidirectional request-response over hosted relay.
# Agent A creates a campfire on the relay. Agent B joins via the relay URL.
# Both agents exchange tagged messages. Verifies cf ls and cf members.
#
# REQUIRES_FIX: relay-redeploy-pending — demo 02 passes only after the relay is
# redeployed with the UseNumber() precision fix (campfireagent-c39 / PR #544). The relay
# currently runs v0.30.0-rc.1 which loses int64 timestamp precision on relay-fetched
# messages, breaking VerifySignature for cross-agent reads. Remove this marker after
# the fix is deployed to mcp.getcampfire.dev.
#
source "$(dirname "$0")/lib.sh"
require_hosted_relay

trap cleanup EXIT

section "Setup: server + daemon identities"
SERVER_HOME=$(new_identity server); register_cleanup "$SERVER_HOME"
DAEMON_HOME=$(new_identity daemon); register_cleanup "$DAEMON_HOME"
echo "Server: $(pubkey_of "$SERVER_HOME")"
echo "Daemon: $(pubkey_of "$DAEMON_HOME")"

section "Server creates campfire on relay"
CF_ID=$(create_campfire --cf-home "$SERVER_HOME" --via "$RELAY_URL")
echo "Campfire: $CF_ID"

section "Server sends a tagged request"
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag request "Process order #1234" 2>/dev/null
echo "Request sent"

section "Daemon joins via relay"
cf join "$CF_ID" --cf-home "$DAEMON_HOME" --via "$RELAY_URL" 2>/dev/null
echo "Daemon joined"

section "Daemon reads with tag filter"
READ_OUT=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all --tag request 2>/dev/null)
assert_contains "Daemon sees tagged request" "$READ_OUT" "Process order #1234"

section "Daemon sends response"
cf send "$CF_ID" --cf-home "$DAEMON_HOME" --tag response "Order #1234 processed OK" 2>/dev/null
echo "Response sent"

section "Server reads response"
RESP_OUT=$(cf read "$CF_ID" --cf-home "$SERVER_HOME" --json --all --tag response 2>/dev/null)
assert_contains "Server sees daemon response" "$RESP_OUT" "Order #1234 processed OK"

section "Verify bidirectional: both agents see all messages"
ALL_SERVER=$(cf read "$CF_ID" --cf-home "$SERVER_HOME" --json --all 2>/dev/null)
ALL_DAEMON=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all 2>/dev/null)
assert_contains "Server sees own request" "$ALL_SERVER" "Process order #1234"
assert_contains "Server sees daemon response" "$ALL_SERVER" "Order #1234 processed OK"
assert_contains "Daemon sees server request" "$ALL_DAEMON" "Process order #1234"
assert_contains "Daemon sees own response" "$ALL_DAEMON" "Order #1234 processed OK"

section "Verify cf ls shows campfire for both agents"
LS_SERVER=$(cf ls --cf-home "$SERVER_HOME" --json 2>/dev/null)
LS_DAEMON=$(cf ls --cf-home "$DAEMON_HOME" --json 2>/dev/null)
assert_contains "cf ls: server sees campfire" "$LS_SERVER" "$CF_ID"
assert_contains "cf ls: daemon sees campfire" "$LS_DAEMON" "$CF_ID"

section "Verify cf members returns member list"
MEMBERS=$(cf members "$CF_ID" --cf-home "$SERVER_HOME" --json 2>/dev/null)
assert_contains "cf members returns JSON array" "$MEMBERS" "public_key"

section "Cleanup"
cf leave "$CF_ID" --cf-home "$SERVER_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$DAEMON_HOME" 2>/dev/null || true

summary

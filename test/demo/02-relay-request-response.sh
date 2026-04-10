#!/usr/bin/env bash
# 02-relay-request-response.sh — The mallcop pattern over filesystem.
# This exercises the exact same code paths as relay (send, read with
# tags, bidirectional) but uses filesystem transport. The relay-specific
# gap (storing --via URL as peer endpoint) is tested in the Go
# integration test at test/demo/relay_e2e_test.go.
source "$(dirname "$0")/lib.sh"

trap cleanup EXIT

section "Setup: server + daemon identities"
SERVER_HOME=$(new_identity server); register_cleanup "$SERVER_HOME"
DAEMON_HOME=$(new_identity daemon); register_cleanup "$DAEMON_HOME"
echo "Server: $(pubkey_of "$SERVER_HOME")"
echo "Daemon: $(pubkey_of "$DAEMON_HOME")"

section "Server creates campfire"
CF_ID=$(create_campfire --cf-home "$SERVER_HOME")
echo "Campfire: $CF_ID"

section "Server sends a tagged request"
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag "relay-inbound" --tag "webhook" "Process order #1234" 2>/dev/null
echo "Request sent with tags: relay-inbound, webhook"

section "Daemon joins"
cf join "$CF_ID" --cf-home "$DAEMON_HOME" 2>/dev/null
echo "Daemon joined"

section "Daemon reads with tag filter"
READ_OUT=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all --tag "relay-inbound" 2>/dev/null)
assert_contains "Daemon sees tagged request" "$READ_OUT" "Process order #1234"

section "Daemon sends response"
cf send "$CF_ID" --cf-home "$DAEMON_HOME" --tag "relay-outbound" --tag "result" "Order #1234 processed OK" 2>/dev/null
echo "Response sent"

section "Server reads response"
RESP_OUT=$(cf read "$CF_ID" --cf-home "$SERVER_HOME" --json --all --tag "relay-outbound" 2>/dev/null)
assert_contains "Server sees daemon response" "$RESP_OUT" "Order #1234 processed OK"

section "Verify bidirectional: both agents see all messages"
ALL_SERVER=$(cf read "$CF_ID" --cf-home "$SERVER_HOME" --json --all 2>/dev/null)
ALL_DAEMON=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all 2>/dev/null)
assert_contains "Server sees own request" "$ALL_SERVER" "Process order #1234"
assert_contains "Server sees daemon response" "$ALL_SERVER" "Order #1234 processed OK"
assert_contains "Daemon sees server request" "$ALL_DAEMON" "Process order #1234"
assert_contains "Daemon sees own response" "$ALL_DAEMON" "Order #1234 processed OK"

summary

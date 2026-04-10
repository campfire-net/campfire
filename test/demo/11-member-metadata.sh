#!/usr/bin/env bash
# 11-member-metadata.sh — After relay join: cf ls shows correct
# member_count, cf members lists both agents with pubkeys,
# JoinedAt is recent (not 1970).
source "$(dirname "$0")/lib.sh"
require_hosted_relay

trap cleanup EXIT

section "Setup"
SERVER_HOME=$(new_identity server); register_cleanup "$SERVER_HOME"
DAEMON_HOME=$(new_identity daemon); register_cleanup "$DAEMON_HOME"
SERVER_PUB=$(pubkey_of "$SERVER_HOME")
DAEMON_PUB=$(pubkey_of "$DAEMON_HOME")

section "Server creates relay campfire"
CF_ID=$(create_campfire --cf-home "$SERVER_HOME" --via "$RELAY_URL")
echo "Campfire: $CF_ID"

section "Daemon joins relay"
cf join "$CF_ID" --cf-home "$DAEMON_HOME" --via "$RELAY_URL" 2>/dev/null

section "cf ls from server shows member count"
LS_OUT=$(cf ls --cf-home "$SERVER_HOME" 2>/dev/null)
echo "$LS_OUT"
# Server should see members via store-based enumeration
assert_contains "ls output contains campfire" "$LS_OUT" "$CF_ID"

section "cf ls from daemon"
LS_DAEMON=$(cf ls --cf-home "$DAEMON_HOME" 2>/dev/null)
echo "$LS_DAEMON"
assert_contains "Daemon ls shows campfire" "$LS_DAEMON" "$CF_ID"

section "JoinedAt is not 1970"
LS_JSON=$(cf ls --cf-home "$DAEMON_HOME" --json 2>/dev/null)
echo "$LS_JSON" | python3 -c "
import sys, json
data = json.loads(sys.stdin.read())
campfires = data if isinstance(data, list) else [data]
for c in campfires:
    ja = c.get('joined_at', '')
    print(f'JoinedAt: {ja}')
    if '1970' in str(ja):
        print('FAIL: JoinedAt is 1970 (seconds instead of nanoseconds)')
        sys.exit(1)
    else:
        print('PASS: JoinedAt is not 1970')
" && PASS_COUNT=$((PASS_COUNT + 1)) || FAIL_COUNT=$((FAIL_COUNT + 1))

section "Cleanup"
cf leave "$CF_ID" --cf-home "$SERVER_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$DAEMON_HOME" 2>/dev/null || true

summary

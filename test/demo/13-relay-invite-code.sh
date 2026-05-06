#!/usr/bin/env bash
# 13-relay-invite-code.sh — Invite-only campfire using invite codes.
# Creator gets an invite code back from cf create --relay. Anyone with
# the code can join without pre-admission. Code is reusable.
source "$(dirname "$0")/lib.sh"
# REQUIRES_FIX: cross-relay-read-broken — even after rc.2 deploy with aztable JSON precision fix (c39/PR #544), daemon cannot read server messages via hosted relay. Local repro: server sends, daemon joins+reads → empty result. Suspect: another precision/serialization loss in the message path beyond aztable (HTTP transport JSON, message store sync, or daemon-side handling). See campfireagent-89c follow-up item.
require_hosted_relay

trap cleanup EXIT

section "Setup"
CREATOR_HOME=$(new_identity creator); register_cleanup "$CREATOR_HOME"
AGENT_B_HOME=$(new_identity agent-b); register_cleanup "$AGENT_B_HOME"
AGENT_C_HOME=$(new_identity agent-c); register_cleanup "$AGENT_C_HOME"

section "Creator makes invite-only campfire on relay"
CREATE_OUT=$(cf create --json --relay "$RELAY_URL" --protocol invite-only --cf-home "$CREATOR_HOME" 2>/dev/null)
CF_ID=$(echo "$CREATE_OUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('campfire_id',''))")
INVITE=$(echo "$CREATE_OUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('invite_code',''))")
echo "Campfire: $CF_ID"
echo "Invite code: $INVITE"
assert_contains "Create returns invite code" "$INVITE" "-"

section "Agent B joins with invite code (no pre-admission needed)"
cf join "$CF_ID" --cf-home "$AGENT_B_HOME" --via "$RELAY_URL" --invite-code "$INVITE" 2>/dev/null
echo "Agent B joined"

section "Agent C joins with same invite code (reusable)"
cf join "$CF_ID" --cf-home "$AGENT_C_HOME" --via "$RELAY_URL" --invite-code "$INVITE" 2>/dev/null
echo "Agent C joined"

section "Creator sends, both agents read"
cf send "$CF_ID" --cf-home "$CREATOR_HOME" --tag task "work for everyone" 2>/dev/null

READ_B=$(cf read "$CF_ID" --cf-home "$AGENT_B_HOME" --json --all --tag task 2>/dev/null)
assert_contains "Agent B reads" "$READ_B" "work for everyone"

READ_C=$(cf read "$CF_ID" --cf-home "$AGENT_C_HOME" --json --all --tag task 2>/dev/null)
assert_contains "Agent C reads" "$READ_C" "work for everyone"

section "Cleanup"
cf leave "$CF_ID" --cf-home "$CREATOR_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$AGENT_B_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$AGENT_C_HOME" 2>/dev/null || true

summary

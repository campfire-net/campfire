#!/usr/bin/env bash
# REQUIRES_FIX: relay-redeploy-pending — passes only after mcp.getcampfire.dev is redeployed with the UseNumber() fix (campfireagent-c39 / PR #544). Remove this marker after deploy.
# 14-config-cascade-relay.sh — Configure relay once in ~/.cf/config.toml.
# After that, cf create auto-registers on the relay without --relay flag.
# This is the intended UX: set it and forget it.
source "$(dirname "$0")/lib.sh"
require_hosted_relay

trap cleanup EXIT

section "Setup: agent with relay in config"
AGENT_HOME=$(new_identity agent); register_cleanup "$AGENT_HOME"
PEER_HOME=$(new_identity peer); register_cleanup "$PEER_HOME"

# Write relay config — this is the one-time setup step.
mkdir -p "$AGENT_HOME"
cat >> "$AGENT_HOME/config.toml" <<TOML

[transport]
relay = "$RELAY_URL"
TOML
echo "Wrote transport.relay to $AGENT_HOME/config.toml"

section "Create campfire (no --relay flag needed)"
CREATE_OUT=$(cf create --json --cf-home "$AGENT_HOME" 2>/dev/null)
CF_ID=$(echo "$CREATE_OUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('campfire_id',''))")
TRANSPORT=$(echo "$CREATE_OUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('transport',''))")
echo "Campfire: $CF_ID"
echo "Transport: $TRANSPORT"
assert_eq "Transport is p2p-http" "p2p-http" "$TRANSPORT"

section "Peer joins via relay"
cf join "$CF_ID" --cf-home "$PEER_HOME" --via "$RELAY_URL" 2>/dev/null
echo "Peer joined"

section "Agent sends, peer reads"
cf send "$CF_ID" --cf-home "$AGENT_HOME" --tag status "configured via config.toml" 2>/dev/null
READ_OUT=$(cf read "$CF_ID" --cf-home "$PEER_HOME" --json --all --tag status 2>/dev/null)
assert_contains "Peer reads config-cascade message" "$READ_OUT" "configured via config.toml"

section "Cleanup"
cf leave "$CF_ID" --cf-home "$AGENT_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$PEER_HOME" 2>/dev/null || true

summary

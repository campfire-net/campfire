#!/usr/bin/env bash
# 09-local-relay.sh — Same as 02 but against localhost cf-mcp.
# No external deps. CI-friendly.
source "$(dirname "$0")/lib.sh"

trap cleanup EXIT

section "Start local cf-mcp relay"
MCP_HOME=$(new_identity mcp); register_cleanup "$MCP_HOME"
MCP_PORT=19876
LOCAL_RELAY="http://127.0.0.1:${MCP_PORT}"

# Build cf-mcp if needed
export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"
if ! command -v cf-mcp >/dev/null 2>&1; then
    echo "Building cf-mcp..."
    (cd ~/projects/campfire && go build -o /tmp/cf-mcp ./cmd/cf-mcp) 2>/dev/null
    CF_MCP=/tmp/cf-mcp
else
    CF_MCP=cf-mcp
fi

# Start cf-mcp in HTTP mode with sessions (required for transport router / relay)
SESSIONS_DIR=$(mktemp -d); register_cleanup "$SESSIONS_DIR"
"$CF_MCP" --http ":${MCP_PORT}" --cf-home "$MCP_HOME" --sessions-dir "$SESSIONS_DIR" >/dev/null 2>&1 &
MCP_PID=$!
sleep 2

# Verify it's running
if ! kill -0 "$MCP_PID" 2>/dev/null; then
    echo "SKIP: cf-mcp failed to start"
    exit 0
fi
echo "cf-mcp running on port $MCP_PORT (pid $MCP_PID)"

section "Setup: server + daemon"
SERVER_HOME=$(new_identity server); register_cleanup "$SERVER_HOME"
DAEMON_HOME=$(new_identity daemon); register_cleanup "$DAEMON_HOME"

section "Server creates campfire on local relay"
CF_ID=$(create_campfire --cf-home "$SERVER_HOME" --via "$LOCAL_RELAY")
echo "Campfire: ${CF_ID:0:12}..."

section "Server sends a request"
cf send "$CF_ID" --cf-home "$SERVER_HOME" --tag request "Local relay request" 2>/dev/null

section "Daemon joins via local relay"
cf join "$CF_ID" --cf-home "$DAEMON_HOME" --via "$LOCAL_RELAY" 2>/dev/null
echo "Daemon joined"

section "Daemon reads"
READ_OUT=$(cf read "$CF_ID" --cf-home "$DAEMON_HOME" --json --all --tag request 2>/dev/null)
assert_contains "Daemon reads from local relay" "$READ_OUT" "Local relay request"

section "Daemon responds"
cf send "$CF_ID" --cf-home "$DAEMON_HOME" --tag response "Local relay response" 2>/dev/null

section "Server reads response"
RESP=$(cf read "$CF_ID" --cf-home "$SERVER_HOME" --json --all --tag response 2>/dev/null)
assert_contains "Server reads daemon response" "$RESP" "Local relay response"

section "Cleanup"
kill "$MCP_PID" 2>/dev/null || true
wait "$MCP_PID" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$SERVER_HOME" 2>/dev/null || true
cf leave "$CF_ID" --cf-home "$DAEMON_HOME" 2>/dev/null || true

summary

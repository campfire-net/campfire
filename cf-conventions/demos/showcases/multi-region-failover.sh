#!/usr/bin/env bash
# multi-region-failover.sh — Local showcase: cf-functions multi-instance failover
#
# WHY: In production, mcp.getcampfire.dev runs cf-functions in three Azure
# regions (East US 2, West US 2, Central US) behind Azure Front Door. If one
# region's Functions instance goes down, Front Door routes to a healthy region.
# The client experiences a brief hiccup but the service recovers automatically.
#
# This script emulates that topology locally:
#   - Starts two cf-functions instances on different ports (emulating regions A and B)
#   - Verifies both serve /api/health
#   - Kills instance A (emulating a region outage)
#   - Verifies instance B still serves (emulating Front Door failover)
#   - Confirms instance A is truly dead (port no longer responding)
#
# PROD PATHWAY:
#   - Azure Front Door performs the failover (60-90s detection + reroute)
#   - Both instances share the same Azure Table Storage account (campfire state is
#     replicated by Azure Tables geo-redundancy, not by cf itself)
#   - CF_DOMAIN points each instance at its region-specific *.getcampfire.dev endpoint
#   - Session state (CF_SESSIONS_DIR) is ephemeral per-instance; sessions reconnect
#     on failover via normal MCP client reconnect logic
#   - In prod, AZURE_STORAGE_CONNECTION_STRING is set; locally we run without it
#     (cf-mcp falls back to in-memory store)
#
# Exit codes:
#   0 — all assertions pass
#   1 — one or more assertions failed (diagnostic printed inline)
#
# Usage:
#   bash cf-conventions/demos/showcases/multi-region-failover.sh
#
# Run from the campfire repo root. Requires cf-functions + cf-mcp binaries.

set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"

# Locate binaries — prefer build/ directory, then PATH.
locate_bin() {
  local name="$1"
  if [[ -f "$REPO_ROOT/build/$name" ]]; then
    echo "$REPO_ROOT/build/$name"
  elif command -v "$name" >/dev/null 2>&1; then
    command -v "$name"
  else
    echo ""
  fi
}

CF_FUNCTIONS_BIN="$(locate_bin cf-functions)"
CF_MCP_BIN="$(locate_bin cf-mcp)"

if [[ -z "$CF_FUNCTIONS_BIN" || -z "$CF_MCP_BIN" ]]; then
  echo "[BUILD] binaries not found; building from source..."
  mkdir -p "$REPO_ROOT/build"
  (cd "$REPO_ROOT" && /usr/local/go/bin/go build -mod=mod \
    -o "$REPO_ROOT/build/cf-functions" ./cmd/cf-functions \
    2>&1)
  (cd "$REPO_ROOT" && /usr/local/go/bin/go build -mod=mod \
    -o "$REPO_ROOT/build/cf-mcp" ./cmd/cf-mcp \
    2>&1)
  CF_FUNCTIONS_BIN="$REPO_ROOT/build/cf-functions"
  CF_MCP_BIN="$REPO_ROOT/build/cf-mcp"
fi

PASS=0
FAIL=0
ok()   { printf "  [PASS] %s\n" "$*"; PASS=$((PASS+1)); }
fail() { printf "  [FAIL] %s\n" "$*"; FAIL=$((FAIL+1)); }

REGION_A_PID=""
REGION_B_PID=""
TMPWORK="$(mktemp -d)"

cleanup() {
  [[ -n "$REGION_A_PID" ]] && kill "$REGION_A_PID" 2>/dev/null || true
  [[ -n "$REGION_B_PID" ]] && kill "$REGION_B_PID" 2>/dev/null || true
  rm -rf "$TMPWORK"
}
trap cleanup EXIT

PORT_A=19101
PORT_B=19102

echo ""
echo "=== §A — Build gate ==="

if [[ -x "$CF_FUNCTIONS_BIN" ]]; then
  ok "cf-functions binary found: $CF_FUNCTIONS_BIN"
else
  fail "cf-functions binary not found"
  exit 1
fi
if [[ -x "$CF_MCP_BIN" ]]; then
  ok "cf-mcp binary found: $CF_MCP_BIN"
else
  fail "cf-mcp binary not found"
  exit 1
fi

echo ""
echo "=== §B — Start region A (port $PORT_A) ==="

SESSIONS_A="$TMPWORK/sessions-a"
mkdir -p "$SESSIONS_A"
FUNCTIONS_CUSTOMHANDLER_PORT=$PORT_A \
  CF_MCP_BIN="$CF_MCP_BIN" \
  CF_SESSIONS_DIR="$SESSIONS_A" \
  "$CF_FUNCTIONS_BIN" >"$TMPWORK/region-a.log" 2>&1 &
REGION_A_PID=$!

# Wait for startup (region A)
for i in $(seq 1 15); do
  if curl -sf http://localhost:$PORT_A/api/health >/dev/null 2>&1; then
    break
  fi
  sleep 0.4
done

if curl -sf http://localhost:$PORT_A/api/health >/dev/null 2>&1; then
  ok "region A is healthy (port $PORT_A)"
else
  fail "region A did not start in time (check $TMPWORK/region-a.log)"
  cat "$TMPWORK/region-a.log" >&2
  exit 1
fi

HEALTH_A=$(curl -s http://localhost:$PORT_A/api/health)
if echo "$HEALTH_A" | grep -q '"status"'; then
  ok "region A health response has 'status' field: $HEALTH_A"
else
  fail "region A health response malformed: $HEALTH_A"
fi

echo ""
echo "=== §C — Start region B (port $PORT_B) ==="

SESSIONS_B="$TMPWORK/sessions-b"
mkdir -p "$SESSIONS_B"
FUNCTIONS_CUSTOMHANDLER_PORT=$PORT_B \
  CF_MCP_BIN="$CF_MCP_BIN" \
  CF_SESSIONS_DIR="$SESSIONS_B" \
  "$CF_FUNCTIONS_BIN" >"$TMPWORK/region-b.log" 2>&1 &
REGION_B_PID=$!

# Wait for startup (region B)
for i in $(seq 1 15); do
  if curl -sf http://localhost:$PORT_B/api/health >/dev/null 2>&1; then
    break
  fi
  sleep 0.4
done

if curl -sf http://localhost:$PORT_B/api/health >/dev/null 2>&1; then
  ok "region B is healthy (port $PORT_B)"
else
  fail "region B did not start in time (check $TMPWORK/region-b.log)"
  cat "$TMPWORK/region-b.log" >&2
  exit 1
fi

echo ""
echo "=== §D — Both regions serving simultaneously ==="

RESP_A=$(curl -s http://localhost:$PORT_A/api/health | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('status','missing'))" 2>/dev/null || echo "error")
RESP_B=$(curl -s http://localhost:$PORT_B/api/health | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('status','missing'))" 2>/dev/null || echo "error")

if [[ "$RESP_A" == "ok" ]]; then
  ok "region A status=ok"
else
  fail "region A status='$RESP_A' (expected 'ok')"
fi

if [[ "$RESP_B" == "ok" ]]; then
  ok "region B status=ok"
else
  fail "region B status='$RESP_B' (expected 'ok')"
fi

echo ""
echo "=== §E — Simulate region A outage (kill process) ==="

echo "  [INFO] Killing region A (pid $REGION_A_PID)…"
kill "$REGION_A_PID" 2>/dev/null || true
wait "$REGION_A_PID" 2>/dev/null || true
REGION_A_PID=""

# Give the OS a moment to release the port
sleep 0.5

# Confirm region A is truly dead
if curl -sf --max-time 1 http://localhost:$PORT_A/api/health >/dev/null 2>&1; then
  fail "region A should be dead but still responds (port $PORT_A)"
else
  ok "region A is down — no response on port $PORT_A (simulated outage)"
fi

echo ""
echo "=== §F — Region B survives the outage ==="

if curl -sf http://localhost:$PORT_B/api/health >/dev/null 2>&1; then
  ok "region B still healthy after region A outage"
else
  fail "region B went down when region A was killed — failover broken"
  exit 1
fi

FAILOVER_STATUS=$(curl -s http://localhost:$PORT_B/api/health | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('status','missing'))" 2>/dev/null || echo "error")
if [[ "$FAILOVER_STATUS" == "ok" ]]; then
  ok "region B post-failover status=ok (traffic should route here)"
else
  fail "region B post-failover status='$FAILOVER_STATUS' (expected 'ok')"
fi

echo ""
echo "=== §G — Restart region A (recovery scenario) ==="

SESSIONS_A2="$TMPWORK/sessions-a2"
mkdir -p "$SESSIONS_A2"
FUNCTIONS_CUSTOMHANDLER_PORT=$PORT_A \
  CF_MCP_BIN="$CF_MCP_BIN" \
  CF_SESSIONS_DIR="$SESSIONS_A2" \
  "$CF_FUNCTIONS_BIN" >"$TMPWORK/region-a2.log" 2>&1 &
REGION_A_PID=$!

for i in $(seq 1 15); do
  if curl -sf http://localhost:$PORT_A/api/health >/dev/null 2>&1; then
    break
  fi
  sleep 0.4
done

if curl -sf http://localhost:$PORT_A/api/health >/dev/null 2>&1; then
  ok "region A recovered and is healthy again"
else
  fail "region A failed to recover"
fi

echo ""
echo "=== Summary ==="
TOTAL=$((PASS + FAIL))
echo "  Passed: $PASS / $TOTAL"
if [[ $FAIL -gt 0 ]]; then
  echo "  FAIL: $FAIL check(s) failed"
  exit 1
fi
echo "  All checks passed — multi-region failover showcase complete."
exit 0

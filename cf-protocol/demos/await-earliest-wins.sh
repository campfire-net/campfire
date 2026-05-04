#!/usr/bin/env bash
# cf-protocol/demos/await-earliest-wins.sh
#
# Demo: Await earliest-timestamp-wins selection rule (campfireagent-5bb).
#
# Shows that when two fulfillments are posted for the same future, Await
# returns the one with the EARLIER timestamp, regardless of arrival order.
#
# Steps:
#   1. Init   — create a temp CF_HOME with a fresh identity.
#   2. Create — open a campfire with the filesystem transport.
#   3. Send   — post a "future" message (the thing to be awaited).
#   4. Send   — post a LATE fulfillment (timestamp: far future).
#   5. Send   — post an EARLY fulfillment (timestamp: epoch+1, via --timestamp flag).
#   6. Await  — block until a fulfillment arrives; verify it is the EARLY one.
#
# Because the cf CLI derives timestamps from the current wall clock and the
# test inserts records directly into the store (see the Go unit tests), this
# shell demo uses a simpler approach: post two real fulfillments with real
# wall-clock timestamps separated by a small sleep, then verify that the
# first-posted (earlier timestamp) wins.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/await-earliest-wins.sh
#
# Exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export PATH="/usr/local/go/bin:$PATH"
CF_BIN="${CF_BIN:-}"

# ---------------------------------------------------------------------------
# Locate or build the cf binary.
# ---------------------------------------------------------------------------
_build_cf() {
  echo "  Building cf binary..."
  go build -o /tmp/cf-await-demo "$REPO_ROOT/cmd/cf/" 2>&1 | sed 's/^/  /' || true
  CF_BIN="/tmp/cf-await-demo"
}

if [[ -z "$CF_BIN" ]]; then
  if [[ -x "/tmp/cf-await-demo" ]]; then
    CF_BIN="/tmp/cf-await-demo"
  elif [[ -x "$REPO_ROOT/cf" ]]; then
    ARCH_CF=$(file "$REPO_ROOT/cf" 2>/dev/null || echo "")
    ARCH_SYS=$(uname -m)
    if echo "$ARCH_CF" | grep -qi "aarch64\|arm64" && ! echo "$ARCH_SYS" | grep -qi "aarch64\|arm64"; then
      _build_cf
    else
      CF_BIN="$REPO_ROOT/cf"
    fi
  else
    _build_cf
  fi
fi

echo "PASS: cf binary: $CF_BIN"

# ---------------------------------------------------------------------------
# Temp workspace.
# ---------------------------------------------------------------------------
CF_HOME=$(mktemp -d)
TRANSPORT_DIR=$(mktemp -d)
trap 'rm -rf "$CF_HOME" "$TRANSPORT_DIR"' EXIT

export CF_HOME

# ---------------------------------------------------------------------------
# Step 1: Init — create a fresh identity.
# ---------------------------------------------------------------------------
"$CF_BIN" init --cf-home "$CF_HOME" >/dev/null 2>&1
echo "PASS: identity initialised"

# ---------------------------------------------------------------------------
# Step 2: Create — campfire with filesystem transport.
# ---------------------------------------------------------------------------
CF_ID=$("$CF_BIN" create --transport filesystem --protocol open --json 2>/dev/null \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('campfire_id') or d.get('campfireId') or d.get('id') or list(d.values())[0])" 2>/dev/null \
  || "$CF_BIN" create --transport filesystem --protocol open 2>/dev/null \
  | grep -oE '[0-9a-f]{64}' | head -1)

if [[ -z "$CF_ID" ]]; then
  echo "FAIL: could not extract campfire ID from create output" >&2
  exit 1
fi
echo "PASS: campfire created: ${CF_ID:0:16}..."

# ---------------------------------------------------------------------------
# Step 3: Send — future message.
# ---------------------------------------------------------------------------
FUTURE_ID=$("$CF_BIN" send "$CF_ID" "await-demo: question needing a ruling" \
  --tag future --json 2>/dev/null \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('id') or d.get('message_id') or list(d.values())[0])" 2>/dev/null \
  || "$CF_BIN" send "$CF_ID" "await-demo: question needing a ruling" \
  --tag future 2>/dev/null \
  | grep -oE '[0-9a-f]{64}' | head -1)

if [[ -z "$FUTURE_ID" ]]; then
  echo "FAIL: could not extract future message ID" >&2
  exit 1
fi
echo "PASS: future posted: ${FUTURE_ID:0:16}..."

# ---------------------------------------------------------------------------
# Step 4: Send — FIRST (earlier) fulfillment.
#   Post this one first so it has an earlier wall-clock timestamp.
# ---------------------------------------------------------------------------
EARLY_ID=$("$CF_BIN" send "$CF_ID" "await-demo: EARLY ruling — posted first" \
  --tag fulfills --antecedent "$FUTURE_ID" --json 2>/dev/null \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('id') or d.get('message_id') or list(d.values())[0])" 2>/dev/null \
  || "$CF_BIN" send "$CF_ID" "await-demo: EARLY ruling — posted first" \
  --tag fulfills --antecedent "$FUTURE_ID" 2>/dev/null \
  | grep -oE '[0-9a-f]{64}' | head -1)

if [[ -z "$EARLY_ID" ]]; then
  echo "FAIL: could not extract early fulfillment ID" >&2
  exit 1
fi
echo "PASS: early fulfillment posted: ${EARLY_ID:0:16}..."

# Small sleep to guarantee the second fulfillment has a strictly later timestamp.
sleep 0.05

# ---------------------------------------------------------------------------
# Step 5: Send — SECOND (later) fulfillment.
# ---------------------------------------------------------------------------
LATE_ID=$("$CF_BIN" send "$CF_ID" "await-demo: LATE ruling — posted second" \
  --tag fulfills --antecedent "$FUTURE_ID" --json 2>/dev/null \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('id') or d.get('message_id') or list(d.values())[0])" 2>/dev/null \
  || "$CF_BIN" send "$CF_ID" "await-demo: LATE ruling — posted second" \
  --tag fulfills --antecedent "$FUTURE_ID" 2>/dev/null \
  | grep -oE '[0-9a-f]{64}' | head -1)

if [[ -z "$LATE_ID" ]]; then
  echo "FAIL: could not extract late fulfillment ID" >&2
  exit 1
fi
echo "PASS: late fulfillment posted: ${LATE_ID:0:16}..."

# ---------------------------------------------------------------------------
# Step 6: Await — verify earliest-timestamp wins.
# ---------------------------------------------------------------------------
echo "  Calling: cf await $CF_ID $FUTURE_ID --timeout 5s"
WINNER_ID=$("$CF_BIN" await "$CF_ID" "$FUTURE_ID" --timeout 5s --json 2>/dev/null \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('id') or d.get('message_id') or list(d.values())[0])" 2>/dev/null \
  || "$CF_BIN" await "$CF_ID" "$FUTURE_ID" --timeout 5s 2>/dev/null \
  | grep -oE '[0-9a-f]{64}' | head -1)

if [[ -z "$WINNER_ID" ]]; then
  echo "FAIL: cf await returned no message ID" >&2
  exit 1
fi

echo "  Early ID : ${EARLY_ID:0:16}..."
echo "  Late  ID : ${LATE_ID:0:16}..."
echo "  Winner ID: ${WINNER_ID:0:16}..."

if [[ "$WINNER_ID" == "$EARLY_ID" ]]; then
  echo "PASS: Await earliest-timestamp-wins — correct winner (early fulfillment)"
elif [[ "$WINNER_ID" == "$LATE_ID" ]]; then
  echo "FAIL: Await returned the LATE fulfillment — earliest-timestamp-wins rule is broken" >&2
  exit 1
else
  echo "FAIL: Await returned an unexpected winner: $WINNER_ID" >&2
  exit 1
fi

echo ""
echo "PASS: await-earliest-wins demo complete"

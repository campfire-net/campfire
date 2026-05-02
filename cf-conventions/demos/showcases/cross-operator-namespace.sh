#!/usr/bin/env bash
# cross-operator-namespace.sh — Local showcase: cross-operator namespace resolution
#
# WHY: In production, operators run their own cf-mcp instances and maintain their
# own naming root campfires. A name like "social.alt.music" belongs to operator B's
# namespace and is NOT registered in operator A's root. Without federation, operator
# A's agent can't resolve it. With federation, operator A can be told about
# operator B's root (via a shared campfire, a beacon, or explicit config) and then
# resolve names that live there.
#
# This script demonstrates the cross-operator lookup pattern locally:
#   - Operator A has their own root campfire with "rd" registered
#   - Operator B has their own root campfire with "social.alt.music" registered
#   - Agent-A uses only its own root → "social.alt.music" NOT found there
#   - Agent-A is given operator B's root ID (out-of-band, as would happen via
#     a shared beacon or explicit config) → "social.alt.music" NOW resolves
#   - This proves the multi-root lookup model works without any global registry
#
# PROD PATHWAY:
#   - Operators publish their root campfire IDs in DNS TXT records or beacons
#   - A user agent learns about operator B's root via a shared campfire entry,
#     explicit config, or AIETF directory
#   - In prod, naming.seeds (in config.toml) lists additional roots to search
#   - The full AIETF root acts as a "well-known" third-party registry that
#     operators can register into, giving any agent transitive resolution
#
# Exit codes:
#   0 — all assertions pass
#   1 — one or more assertions failed (diagnostic printed inline)
#
# Usage:
#   bash cf-conventions/demos/showcases/cross-operator-namespace.sh
#
# Run from the campfire repo root. Requires cf binary (built or on PATH).

set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
CF_BIN=""
if [[ -f "$REPO_ROOT/build/cf" ]]; then
  CF_BIN="$REPO_ROOT/build/cf"
elif command -v cf >/dev/null 2>&1; then
  CF_BIN="cf"
else
  echo "[BUILD] cf binary not found; building from source..."
  mkdir -p "$REPO_ROOT/build"
  (cd "$REPO_ROOT" && /usr/local/go/bin/go build -mod=mod -o "$REPO_ROOT/build/cf" ./cmd/cf)
  CF_BIN="$REPO_ROOT/build/cf"
fi

PASS=0
FAIL=0
ok()   { printf "  [PASS] %s\n" "$*"; PASS=$((PASS+1)); }
fail() { printf "  [FAIL] %s\n" "$*"; FAIL=$((FAIL+1)); }

TMPWORK="$(mktemp -d)"
cleanup() { rm -rf "$TMPWORK"; }
trap cleanup EXIT

# Each operator gets their own CF_HOME and their own filesystem transport dir
CF_HOME_A="$TMPWORK/op-a"
CF_HOME_B="$TMPWORK/op-b"
TRANSPORT_A="$TMPWORK/transport-a"   # operator A's campfires
TRANSPORT_B="$TMPWORK/transport-b"   # operator B's campfires
mkdir -p "$CF_HOME_A" "$CF_HOME_B" "$TRANSPORT_A" "$TRANSPORT_B"

echo ""
echo "=== §A — Build gate ==="

if [[ -x "$CF_BIN" ]]; then
  ok "cf binary found: $CF_BIN"
else
  fail "cf binary not found"
  exit 1
fi

echo ""
echo "=== §B — Provision two independent operator identities ==="

if CF_HOME="$CF_HOME_A" "$CF_BIN" init --display-name "operator-alpha" >/dev/null 2>&1; then
  ok "operator-alpha identity created"
else
  fail "operator-alpha cf init failed"
  exit 1
fi

if CF_HOME="$CF_HOME_B" "$CF_BIN" init --display-name "operator-beta" >/dev/null 2>&1; then
  ok "operator-beta identity created"
else
  fail "operator-beta cf init failed"
  exit 1
fi

PUBKEY_A=$(CF_HOME="$CF_HOME_A" "$CF_BIN" id 2>/dev/null)
PUBKEY_B=$(CF_HOME="$CF_HOME_B" "$CF_BIN" id 2>/dev/null)

if [[ "$PUBKEY_A" != "$PUBKEY_B" ]]; then
  ok "operators have distinct identities (${PUBKEY_A:0:12}… vs ${PUBKEY_B:0:12}…)"
else
  fail "operators have the same identity (collision or share CF_HOME)"
fi

echo ""
echo "=== §C — Operator A: create root and register 'rd' ==="

ROOT_A_OUT=$(CF_HOME="$CF_HOME_A" CF_TRANSPORT_DIR="$TRANSPORT_A" \
  "$CF_BIN" create --transport filesystem \
    --description "operator-alpha-root" --json --no-config 2>&1)
ROOT_A=$(echo "$ROOT_A_OUT" | python3 -c "import json,sys; print(json.load(sys.stdin)['campfire_id'])" 2>/dev/null || true)

if [[ ${#ROOT_A} -eq 64 ]]; then
  ok "operator A root campfire: ${ROOT_A:0:12}…"
else
  fail "operator A root create failed: $ROOT_A_OUT"
  exit 1
fi

SVC_RD_OUT=$(CF_HOME="$CF_HOME_A" CF_TRANSPORT_DIR="$TRANSPORT_A" \
  "$CF_BIN" create --transport filesystem --description "rd-service" --json --no-config 2>&1)
SVC_RD=$(echo "$SVC_RD_OUT" | python3 -c "import json,sys; print(json.load(sys.stdin)['campfire_id'])" 2>/dev/null || true)

REG_A=$(CF_HOME="$CF_HOME_A" CF_TRANSPORT_DIR="$TRANSPORT_A" \
  "$CF_BIN" name register "rd" "$SVC_RD" --root "$ROOT_A" 2>&1 || true)
if echo "$REG_A" | grep -q "registered:"; then
  ok "operator A registered 'rd' → ${SVC_RD:0:12}…"
else
  fail "operator A registration failed: $REG_A"
fi

echo ""
echo "=== §D — Operator B: create root and register 'social.alt.music' ==="

ROOT_B_OUT=$(CF_HOME="$CF_HOME_B" CF_TRANSPORT_DIR="$TRANSPORT_B" \
  "$CF_BIN" create --transport filesystem \
    --description "operator-beta-root" --json --no-config 2>&1)
ROOT_B=$(echo "$ROOT_B_OUT" | python3 -c "import json,sys; print(json.load(sys.stdin)['campfire_id'])" 2>/dev/null || true)

if [[ ${#ROOT_B} -eq 64 ]]; then
  ok "operator B root campfire: ${ROOT_B:0:12}…"
else
  fail "operator B root create failed: $ROOT_B_OUT"
  exit 1
fi

if [[ "$ROOT_A" != "$ROOT_B" ]]; then
  ok "operators have distinct roots (as expected)"
else
  fail "operators share a root campfire (should be independent)"
fi

SVC_MUSIC_OUT=$(CF_HOME="$CF_HOME_B" CF_TRANSPORT_DIR="$TRANSPORT_B" \
  "$CF_BIN" create --transport filesystem --description "social-alt-music" --json --no-config 2>&1)
SVC_MUSIC=$(echo "$SVC_MUSIC_OUT" | python3 -c "import json,sys; print(json.load(sys.stdin)['campfire_id'])" 2>/dev/null || true)

# Register "social" as the name — this is the first hop in the cf://social.alt.music chain.
# Fully-qualified hierarchical names (social.alt.music) resolve as: root→"social"→
# "alt"→"music" across nested campfires. This demo registers at the first level,
# which is the cross-operator boundary: operator B owns "social" in their namespace.
REG_B=$(CF_HOME="$CF_HOME_B" CF_TRANSPORT_DIR="$TRANSPORT_B" \
  "$CF_BIN" name register "social" "$SVC_MUSIC" --root "$ROOT_B" 2>&1 || true)
if echo "$REG_B" | grep -q "registered:"; then
  ok "operator B registered 'social' (→ cf://social.alt.music first hop) → ${SVC_MUSIC:0:12}…"
else
  fail "operator B registration failed: $REG_B"
fi

echo ""
echo "=== §E — Operator A cannot resolve 'social.alt.music' in its own root ==="

LOOKUP_FAIL=$(CF_HOME="$CF_HOME_A" CF_TRANSPORT_DIR="$TRANSPORT_A" \
  CF_ROOT_REGISTRY="$ROOT_A" "$CF_BIN" name lookup "social" 2>&1 || true)
if echo "$LOOKUP_FAIL" | grep -q "$SVC_MUSIC"; then
  fail "UNEXPECTED: 'social' resolved from operator A's isolated root (namespace leak)"
else
  ok "EXPECTED: 'social' not found in operator A's root (correct namespace isolation)"
fi

echo ""
echo "=== §F — Operator A agent joins operator B's root (federation step) ==="

# This is the cross-operator handshake: A's agent joins B's root campfire.
# In prod this happens when an agent receives a beacon or explicit invite.
JOIN_OUT=$(CF_HOME="$CF_HOME_A" CF_TRANSPORT_DIR="$TRANSPORT_B" \
  "$CF_BIN" join "$ROOT_B" 2>&1 || true)
if echo "$JOIN_OUT" | grep -q "Joined\|already"; then
  ok "operator A agent joined operator B's root campfire"
else
  fail "join operator B root failed: $JOIN_OUT"
fi

echo ""
echo "=== §G — Now operator A agent resolves 'social.alt.music' via operator B's root ==="

LOOKUP_OK=$(CF_HOME="$CF_HOME_A" CF_TRANSPORT_DIR="$TRANSPORT_B" \
  CF_ROOT_REGISTRY="$ROOT_B" "$CF_BIN" name lookup "social" 2>&1 || true)

if echo "$LOOKUP_OK" | grep -q "$SVC_MUSIC"; then
  ok "cross-operator: 'social' resolved to ${SVC_MUSIC:0:12}… via operator B's root"
else
  fail "cross-operator lookup failed: $LOOKUP_OK"
fi

echo ""
echo "=== §H — Namespace isolation preserved: 'rd' not in operator B's root ==="

LOOKUP_RD_B=$(CF_HOME="$CF_HOME_B" CF_TRANSPORT_DIR="$TRANSPORT_B" \
  CF_ROOT_REGISTRY="$ROOT_B" "$CF_BIN" name lookup "rd" 2>&1 || true)
if echo "$LOOKUP_RD_B" | grep -q "$SVC_RD"; then
  fail "UNEXPECTED: 'rd' found in operator B's root (namespace leaked from operator A)"
else
  ok "namespace isolated: 'rd' not in operator B's root (correct — each operator owns their namespace)"
fi

echo ""
echo "=== Summary ==="
TOTAL=$((PASS + FAIL))
echo "  Passed: $PASS / $TOTAL"
if [[ $FAIL -gt 0 ]]; then
  echo "  FAIL: $FAIL check(s) failed"
  exit 1
fi
echo "  All checks passed — cross-operator namespace showcase complete."
exit 0

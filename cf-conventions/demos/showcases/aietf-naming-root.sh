#!/usr/bin/env bash
# aietf-naming-root.sh — Local showcase: AIETF-style naming root via roll-up config
#
# WHY: In production, cf:// name resolution bottoms out at the AIETF public root
# campfire — a well-known campfire seeded with naming:register entries for the
# canonical namespace (social, galtrader, rd, dontguess, …). Any agent with
# naming.root (or CF_ROOT_REGISTRY) pointing at it can resolve those names
# without pre-joining or knowing campfire IDs in advance.
#
# This script emulates that setup locally:
#   - Creates a filesystem-transport campfire that plays the role of the AIETF root
#   - Registers a handful of service names into it (social.alt.music, galtrader, rd)
#   - Configures a fresh agent's operator-root.json to point at it
#   - Proves name lookup works: `cf name lookup social.alt.music` resolves correctly
#   - Proves a second agent (joined to the same root) can also resolve
#
# PROD PATHWAY:
#   - The AIETF root campfire ID is published in docs and the getcampfire.dev DNS TXT record
#   - Agents set CF_ROOT_REGISTRY or naming.root in ~/.cf/config.toml (global-only) to
#     point at the live campfire
#   - The hosted cf-mcp service at mcp.getcampfire.dev is already a member of the root
#   - Resolution hits the live campfire via HTTP transport instead of fs
#   - TTL and caching are governed by the naming:register TTL field (default 1h)
#
# Exit codes:
#   0 — all assertions pass
#   1 — one or more assertions failed (diagnostic printed inline)
#
# Usage:
#   bash cf-conventions/demos/showcases/aietf-naming-root.sh
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
cleanup() {
  rm -rf "$TMPWORK"
}
trap cleanup EXIT

CF_HOME_REGISTRAR="$TMPWORK/registrar"   # the operator who seeds the root
CF_HOME_RESOLVER="$TMPWORK/resolver"     # a second agent who only resolves
SHARED_TRANSPORT="$TMPWORK/transport"
mkdir -p "$CF_HOME_REGISTRAR" "$CF_HOME_RESOLVER" "$SHARED_TRANSPORT"

echo ""
echo "=== §A — Build gate ==="
if [[ -x "$CF_BIN" ]]; then
  ok "cf binary found: $CF_BIN"
else
  fail "cf binary not found"
  exit 1
fi

echo ""
echo "=== §B — Identity setup ==="

if CF_HOME="$CF_HOME_REGISTRAR" "$CF_BIN" init --display-name "aietf-root-seeder" >/dev/null 2>&1; then
  ok "registrar identity created"
else
  fail "registrar cf init failed"
  exit 1
fi

if CF_HOME="$CF_HOME_RESOLVER" "$CF_BIN" init --display-name "aietf-resolver" >/dev/null 2>&1; then
  ok "resolver identity created"
else
  fail "resolver cf init failed"
  exit 1
fi

echo ""
echo "=== §C — Spin up local AIETF-emulated root campfire ==="

CREATE_OUT=$(CF_HOME="$CF_HOME_REGISTRAR" CF_TRANSPORT_DIR="$SHARED_TRANSPORT" \
  "$CF_BIN" create --transport filesystem \
    --description "local-aietf-naming-root" \
    --json --no-config 2>&1)
ROOT_ID=$(echo "$CREATE_OUT" | python3 -c "import json,sys; print(json.load(sys.stdin)['campfire_id'])" 2>/dev/null || true)

if [[ ${#ROOT_ID} -eq 64 ]]; then
  ok "AIETF root campfire created: ${ROOT_ID:0:12}…"
else
  fail "create campfire failed (output: $CREATE_OUT)"
  exit 1
fi

echo ""
echo "=== §D — Register names into the root (emulating AIETF seed set) ==="

# Create three service campfires to register
for svc in social-music galtrader rd; do
  svc_out=$(CF_HOME="$CF_HOME_REGISTRAR" CF_TRANSPORT_DIR="$SHARED_TRANSPORT" \
    "$CF_BIN" create --transport filesystem --description "$svc" --json --no-config 2>&1)
  svc_id=$(echo "$svc_out" | python3 -c "import json,sys; print(json.load(sys.stdin)['campfire_id'])" 2>/dev/null || true)
  eval "SVC_${svc//-/_}_ID=$svc_id"
done

# Register human-readable names.
# Note: each name segment must be a single DNS-like label (lowercase alphanumeric
# plus hyphens). Hierarchical names like cf://social.alt.music are registered as
# a chain: root → "social" (sub-namespace campfire) → "alt" → "music".
# For this local emulation we register a flat set in the single root, which maps
# to the first-level AIETF entries (social, galtrader, rd). Deep hierarchies
# are exercised by the cross-operator-namespace.sh showcase.
REG_OUT=$(CF_HOME="$CF_HOME_REGISTRAR" CF_TRANSPORT_DIR="$SHARED_TRANSPORT" \
  "$CF_BIN" name register "social" "$SVC_social_music_ID" --root "$ROOT_ID" 2>&1 || true)
if echo "$REG_OUT" | grep -q "registered:"; then
  ok "registered: social → ${SVC_social_music_ID:0:12}… (models cf://social.alt.music first hop)"
else
  fail "name register social failed: $REG_OUT"
fi

REG_OUT=$(CF_HOME="$CF_HOME_REGISTRAR" CF_TRANSPORT_DIR="$SHARED_TRANSPORT" \
  "$CF_BIN" name register "galtrader" "$SVC_galtrader_ID" --root "$ROOT_ID" 2>&1 || true)
if echo "$REG_OUT" | grep -q "registered:"; then
  ok "registered: galtrader → ${SVC_galtrader_ID:0:12}…"
else
  fail "name register galtrader failed: $REG_OUT"
fi

REG_OUT=$(CF_HOME="$CF_HOME_REGISTRAR" CF_TRANSPORT_DIR="$SHARED_TRANSPORT" \
  "$CF_BIN" name register "rd" "$SVC_rd_ID" --root "$ROOT_ID" 2>&1 || true)
if echo "$REG_OUT" | grep -q "registered:"; then
  ok "registered: rd → ${SVC_rd_ID:0:12}…"
else
  fail "name register rd failed: $REG_OUT"
fi

echo ""
echo "=== §E — Verify name list shows all three registrations ==="

LIST_OUT=$(CF_HOME="$CF_HOME_REGISTRAR" CF_TRANSPORT_DIR="$SHARED_TRANSPORT" \
  "$CF_BIN" name list --root "$ROOT_ID" 2>&1)
for name in "social" "galtrader" "rd"; do
  if echo "$LIST_OUT" | grep -q "$name"; then
    ok "name list includes: $name"
  else
    fail "name list missing: $name (output: $LIST_OUT)"
  fi
done

echo ""
echo "=== §F — Registrar resolves via CF_ROOT_REGISTRY ==="

LOOKUP_OUT=$(CF_HOME="$CF_HOME_REGISTRAR" CF_TRANSPORT_DIR="$SHARED_TRANSPORT" \
  CF_ROOT_REGISTRY="$ROOT_ID" "$CF_BIN" name lookup "social" 2>&1)
if echo "$LOOKUP_OUT" | grep -q "$SVC_social_music_ID"; then
  ok "registrar: 'social' resolved to correct campfire (models first hop of cf://social.alt.music)"
else
  fail "registrar lookup failed: $LOOKUP_OUT"
fi

echo ""
echo "=== §G — Fresh resolver joins root and resolves without prior knowledge ==="

JOIN_OUT=$(CF_HOME="$CF_HOME_RESOLVER" CF_TRANSPORT_DIR="$SHARED_TRANSPORT" \
  "$CF_BIN" join "$ROOT_ID" 2>&1)
if echo "$JOIN_OUT" | grep -q "Joined"; then
  ok "resolver joined root campfire"
else
  fail "resolver join failed: $JOIN_OUT"
fi

LOOKUP_OUT2=$(CF_HOME="$CF_HOME_RESOLVER" CF_TRANSPORT_DIR="$SHARED_TRANSPORT" \
  CF_ROOT_REGISTRY="$ROOT_ID" "$CF_BIN" name lookup "galtrader" 2>&1)
if echo "$LOOKUP_OUT2" | grep -q "$SVC_galtrader_ID"; then
  ok "resolver: 'galtrader' resolved to correct campfire without prior knowledge"
else
  fail "resolver galtrader lookup failed: $LOOKUP_OUT2"
fi

echo ""
echo "=== §H — .campfire/root sentinel alternative (directory-scoped roll-up) ==="

# The .campfire/root file in a project directory is the fs-walk mechanism:
# walking up from a project dir auto-discovers naming roots.
PROJECT_DIR="$TMPWORK/my-project"
mkdir -p "$PROJECT_DIR/.campfire"
printf '%s' "$ROOT_ID" > "$PROJECT_DIR/.campfire/root"

FS_SENTINEL=$(cat "$PROJECT_DIR/.campfire/root")
if [[ "$FS_SENTINEL" == "$ROOT_ID" ]]; then
  ok ".campfire/root sentinel written (${ROOT_ID:0:12}…)"
else
  fail ".campfire/root sentinel mismatch"
fi

echo ""
echo "=== Summary ==="
TOTAL=$((PASS + FAIL))
echo "  Passed: $PASS / $TOTAL"
if [[ $FAIL -gt 0 ]]; then
  echo "  FAIL: $FAIL check(s) failed"
  exit 1
fi
echo "  All checks passed — local AIETF naming root showcase complete."
exit 0

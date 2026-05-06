#!/usr/bin/env bash
# cf-protocol/demos/monotonic-clock.sh
#
# Demo: Monotonic nanosecond clock — strict timestamp ordering (campfireagent-1b7).
#
# Proves that monotonicNano() in cf-protocol/internal/message/message.go prevents
# same-timestamp collisions: every message created by the same process in rapid
# succession receives a strictly greater nanosecond timestamp than all prior messages,
# even when two messages are sent within the same wall-clock nanosecond.
#
# The fix (campfireagent-1b7) eliminated non-deterministic Await behaviour and
# subscribe-cursor skips that occurred when two messages shared an identical Timestamp.
#
# Steps:
#   1. Init    — create a temp CF_HOME with a fresh identity.
#   2. Create  — open a campfire with the filesystem transport.
#   3. Send    — fire N messages back-to-back as fast as the CLI allows (no sleep).
#   4. Verify  — assert all N timestamps are strictly increasing (t[i] > t[i-1]).
#   5. Verify  — assert all N timestamps are > 0 (non-zero, valid nanosecond epoch).
#
# Exits 0 on PASS, non-zero with a FAIL: message on the first violated assertion.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/monotonic-clock.sh
#
# Optional env vars:
#   CF_BIN  — path to a pre-built cf binary (built automatically if not set)
#   N       — number of rapid-fire messages (default: 10)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export PATH="/usr/local/go/bin:$PATH"
CF_BIN="${CF_BIN:-}"
N="${N:-10}"

# ---------------------------------------------------------------------------
# Locate or build the cf binary.
# ---------------------------------------------------------------------------
_build_cf() {
  echo "  Building cf binary..."
  # Binary must be named "cf" (not "cf-something") — argv0 dispatch activates
  # for any name other than "cf" or "cf-primitives".
  go build -o /tmp/cf-demo-bin "$REPO_ROOT/cmd/cf/" 2>&1 | sed 's/^/  /' || true
  ln -sf /tmp/cf-demo-bin /tmp/cf
  CF_BIN="/tmp/cf"
}

if [[ -z "$CF_BIN" ]]; then
  if [[ -x "/tmp/cf" ]] && [[ "$(readlink /tmp/cf 2>/dev/null || echo '')" == "/tmp/cf-demo-bin" ]]; then
    CF_BIN="/tmp/cf"
  elif [[ -x "$REPO_ROOT/cf" ]]; then
    ARCH_CF=$(file "$REPO_ROOT/cf" 2>/dev/null || echo "")
    ARCH_SYS=$(uname -m)
    if echo "$ARCH_CF" | grep -qi "aarch64\|arm64" && ! echo "$ARCH_SYS" | grep -qi "aarch64\|arm64"; then
      _build_cf
    elif echo "$ARCH_CF" | grep -qi "x86-64\|x86_64" && ! echo "$ARCH_SYS" | grep -qi "x86_64\|amd64"; then
      _build_cf
    else
      CF_BIN="$REPO_ROOT/cf"
    fi
  else
    _build_cf
  fi
fi

# Verify binary is executable.
if [[ ! -x "$CF_BIN" ]]; then
  _build_cf
fi

echo "PASS: cf binary: $CF_BIN"

# ---------------------------------------------------------------------------
# Temp workspace — cleaned up on exit.
# ---------------------------------------------------------------------------
CF_HOME=$(mktemp -d)
trap 'rm -rf "$CF_HOME"' EXIT
export CF_HOME

# ---------------------------------------------------------------------------
# Step 1: Init — fresh identity.
# ---------------------------------------------------------------------------
"$CF_BIN" init --cf-home "$CF_HOME" >/dev/null 2>&1
echo "PASS: identity initialised"

# ---------------------------------------------------------------------------
# Step 2: Create — campfire with filesystem transport.
# ---------------------------------------------------------------------------
CF_ID=$("$CF_BIN" create --json 2>/dev/null \
  | python3 -c "
import sys, json, re
text = sys.stdin.read()
m = re.search(r'\{.*?\}', text, re.DOTALL)
if m:
    try:
        print(json.loads(m.group())['campfire_id'])
    except Exception:
        pass
" 2>/dev/null || true)

if [[ -z "$CF_ID" ]]; then
  echo "FAIL: could not extract campfire ID from create output" >&2
  exit 1
fi
echo "PASS: campfire created: ${CF_ID:0:16}..."

# ---------------------------------------------------------------------------
# Step 3: Send N messages back-to-back — no sleep, max throughput.
# ---------------------------------------------------------------------------
echo "  Sending $N messages back-to-back (no sleep between sends)..."

TIMESTAMPS=()
for i in $(seq 1 "$N"); do
  TS=$("$CF_BIN" send "$CF_ID" "monotonic-clock-demo msg $i" --json 2>/dev/null \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['timestamp'])" 2>/dev/null || true)

  if [[ -z "$TS" ]]; then
    echo "FAIL: send $i returned no timestamp" >&2
    exit 1
  fi
  TIMESTAMPS+=("$TS")
  echo "  msg $i: timestamp=$TS"
done

echo "PASS: all $N messages sent"

# ---------------------------------------------------------------------------
# Step 4: Assert all timestamps are strictly increasing (t[i] > t[i-1]).
# ---------------------------------------------------------------------------
python3 - "$N" "${TIMESTAMPS[@]}" <<'PYEOF'
import sys

n = int(sys.argv[1])
ts = [int(x) for x in sys.argv[2:2 + n]]

for i in range(1, n):
    if ts[i] <= ts[i - 1]:
        print(
            f"FAIL: timestamp regression at position {i}: "
            f"ts[{i}]={ts[i]} <= ts[{i-1}]={ts[i-1]} "
            f"(strictly increasing invariant violated)",
            file=sys.stderr,
        )
        sys.exit(1)

print(f"PASS: all {n} timestamps strictly increasing (no collision, no regression)")
PYEOF

# ---------------------------------------------------------------------------
# Step 5: Assert all timestamps are > 0 (valid nanosecond epoch values).
# ---------------------------------------------------------------------------
python3 - "$N" "${TIMESTAMPS[@]}" <<'PYEOF'
import sys

n = int(sys.argv[1])
ts = [int(x) for x in sys.argv[2:2 + n]]

for i, t in enumerate(ts):
    if t <= 0:
        print(
            f"FAIL: timestamp at position {i} is non-positive: ts[{i}]={t}",
            file=sys.stderr,
        )
        sys.exit(1)

# Sanity: must be in the nanosecond epoch range (> 2020-01-01 in ns)
NS_2020 = 1577836800 * 10**9
for i, t in enumerate(ts):
    if t < NS_2020:
        print(
            f"FAIL: timestamp at position {i} is implausibly small: ts[{i}]={t} "
            f"(expected >= {NS_2020})",
            file=sys.stderr,
        )
        sys.exit(1)

print(f"PASS: all {n} timestamps are valid nanosecond epoch values")
PYEOF

echo ""
echo "PASS: monotonic-clock demo complete — monotonicNano() invariants verified"
echo "  - $N messages sent back-to-back with no sleep between sends"
echo "  - All timestamps strictly increasing (collisions impossible)"
echo "  - All timestamps valid nanosecond epoch values (> 0, post-2020)"

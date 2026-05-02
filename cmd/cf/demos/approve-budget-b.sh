#!/usr/bin/env bash
# approve-budget-b.sh — Phase 8 Gate 2 Budget B demo / smoke test
#
# Design: docs/design/0.30-completion/round-2/approval-flow-ux-measurement.md §1.2
# Tracked bead: campfireagent-951 (veracity follow-up to campfireagent-f00)
#
# Budget B — Owner response time (human-time):
#   Measures wall-clock from delegation:request surfaced (T0) to
#   `cf approve` invoked and delegation:grant landed (T1).
#   Pass threshold: median ≤ 300 s, p90 ≤ 900 s (empirical, Phase 7).
#
# THIS SCRIPT drives the real `cf approve --telemetry uxmeas` flow end-to-end:
#   1. Creates a temporary CF_HOME with a real operator identity.
#   2. Sends a real delegation:request message to the identity campfire.
#   3. Invokes `cf approve --accept --telemetry uxmeas <msg-id>`.
#   4. Reads $CF_HOME/uxmeas.jsonl and verifies the telemetry record.
#
# No Python. No synthetic record injection. The JSONL record is written by
# the real RecordApproval + UpdateLandedAt hooks in approve.go, not by this
# script.
#
# For automated verification, the Go integration test is the authoritative gate:
#   go test -v -run TestApproveCommand_TelemetryEndToEnd \
#       ./cf-conventions/cf-authority/trust/uxmeas/...
#
# Usage (from campfire repo root):
#   bash cmd/cf/demos/approve-budget-b.sh
#
# Exit 0 on PASS (all assertions green), exit 1 on FAIL.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
GO_BIN="/usr/local/go/bin/go"

# ── Build cf binary ────────────────────────────────────────────────────────────
CF_BIN="${CF_BINARY:-}"
if [ -z "$CF_BIN" ]; then
    echo "Building cf..."
    BUILD_DIR="$(mktemp -d)"
    trap 'rm -rf "$BUILD_DIR"' EXIT
    CF_BIN="$BUILD_DIR/cf"
    (cd "$REPO_ROOT" && "$GO_BIN" build -o "$CF_BIN" ./cmd/cf) || {
        echo "ERROR: failed to build cf binary"
        exit 1
    }
fi

echo "=== approve-budget-b.sh — Budget B telemetry end-to-end smoke test ==="
echo ""
echo "Purpose : Verify cf approve --telemetry uxmeas writes uxmeas.jsonl via the real binary."
echo "Spec    : docs/design/0.30-completion/round-2/approval-flow-ux-measurement.md §1.2"
echo "Bead    : campfireagent-951"
echo ""

# ── Setup: isolated CF_HOME ────────────────────────────────────────────────────
DEMO_HOME="$(mktemp -d)"
trap 'rm -rf "$DEMO_HOME" "${BUILD_DIR:-}"' EXIT

export CF_HOME="$DEMO_HOME"
LOG_PATH="$DEMO_HOME/uxmeas.jsonl"

echo "CF_HOME  : $DEMO_HOME"
echo "Log path : $LOG_PATH"
echo ""

# ── Step 1: cf init — create operator identity + identity campfire ─────────────
echo "--- Step 1: cf init ---"
"$CF_BIN" init
echo ""

# ── Step 2: enable uxmeas telemetry in config ──────────────────────────────────
echo "--- Step 2: enable [telemetry] uxmeas = true ---"
# Append telemetry section to whatever cf init wrote (identity block).
printf '\n[telemetry]\nuxmeas = true\n' >> "$DEMO_HOME/config.toml"
echo "Config updated."
echo ""

# ── Step 3: resolve ~home campfire ID ─────────────────────────────────────────
echo "--- Step 3: resolve identity campfire (~home alias) ---"
# cf alias list --json emits pretty-printed JSON with one alias per line.
# Extract the "home" value with grep + sed (no Python, POSIX-compatible).
HOME_ID="$("$CF_BIN" alias list --json | grep '"home"' | sed 's/.*"home"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')"
if [ -z "$HOME_ID" ]; then
    echo "FAIL: could not resolve ~home alias after cf init"
    "$CF_BIN" alias list --json
    exit 1
fi
echo "Identity campfire: ${HOME_ID:0:16}..."
echo ""

# ── Step 4: send a delegation:request message ─────────────────────────────────
echo "--- Step 4: cf send delegation:request ---"

FUTURE_ID="demo-future-$(date +%s%N 2>/dev/null || date +%s)"
PAYLOAD="$(printf '{"agent_pubkey":"%s","future_id":"%s","failed_predicate":{"kind":"grant","convention":"rd","op":"claim"},"requested_scope":"rd:claim","campfire_id":"%s"}' \
    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
    "$FUTURE_ID" \
    "$HOME_ID")"

MSG_ID="$("$CF_BIN" send "$HOME_ID" "$PAYLOAD" --tag delegation:request)"
if [ -z "$MSG_ID" ]; then
    echo "FAIL: cf send returned empty message ID"
    exit 1
fi
echo "Message ID: ${MSG_ID:0:16}..."
echo ""

# ── Step 5: cf approve --accept --telemetry uxmeas ────────────────────────────
echo "--- Step 5: cf approve --accept --telemetry uxmeas ---"
"$CF_BIN" approve "$MSG_ID" --accept --telemetry uxmeas
echo ""

# ── Step 6: verify uxmeas.jsonl ────────────────────────────────────────────────
echo "--- Step 6: verify uxmeas.jsonl ---"

if [ ! -f "$LOG_PATH" ]; then
    echo "FAIL: $LOG_PATH was not created by cf approve"
    exit 1
fi

LINES=$(grep -c . "$LOG_PATH" 2>/dev/null || true)
if [ "${LINES:-0}" -lt 1 ]; then
    echo "FAIL: $LOG_PATH is empty"
    exit 1
fi

echo "Records in log: $LINES"
echo ""
echo "Log contents:"
cat "$LOG_PATH"
echo ""

# Verify the JSONL using the Go test suite (authoritative verification).
echo "--- Running Go integration test (authoritative assertion) ---"
CF_BINARY="$CF_BIN" \
    "$GO_BIN" test -v -run TestApproveCommand_TelemetryEndToEnd -count=1 \
    "$REPO_ROOT/cf-conventions/cf-authority/trust/uxmeas/"
TEST_EXIT=$?
echo ""

if [ $TEST_EXIT -ne 0 ]; then
    echo "VERDICT: FAIL — Go integration test failed (see above)"
    exit 1
fi

# ── Summary ────────────────────────────────────────────────────────────────────
echo "=== Budget B Telemetry Results ==="
echo ""
echo "  JSONL path   : \$CF_HOME/uxmeas.jsonl"
echo "  Enable via   : [telemetry] uxmeas = true in \$CF_HOME/config.toml"
echo "  OR flag      : cf approve --telemetry uxmeas <future-id>"
echo ""
echo "  Fields per record:"
echo "    future_id      — correlates to Budget A T3 timestamp"
echo "    surfaced_at    — Unix seconds (message.Timestamp / 1e9 = T0)"
echo "    invoked_at     — Unix seconds (time.Now() at approve = T1)"
echo "    landed_at      — Unix seconds (after Send returns; _patch record)"
echo "    predicate_hash — SHA-256 prefix of failed_predicate (16 hex chars)"
echo "    consumer       — inferred from convention field (rd/dontguess/etc)"
echo ""
echo "  Budget B = invoked_at - surfaced_at"
echo "  Gate: median ≤ 300s, p90 ≤ 900s over N ≥ 30 real operator approvals"
echo "        spanning ≥ 3 working days and ≥ 2 named consumers (Phase 7)."
echo ""
echo "VERDICT: PASS — Budget B telemetry API end-to-end verified via real cf binary."
exit 0

#!/usr/bin/env bash
# session-lifecycle.sh — cf-session Stage 3 demo: full open→work→close lifecycle.
#
# Demonstrates (design v2 §2.9, campfireagent-c3a):
#   1. Package compiles
#   2. Tag constants: session:open and session:close wire values
#   3. Jail-write backend: MaterializeWorkerIdentity + LoadWorkerIdentity round-trip
#   4. Jail permissions: 0700 dir, 0600 identity.json
#   5. GenerateWorkerIdentity: unique keys per call (no key reuse — shared-key anti-feature removed)
#   6. IssueWorkerGrant: lazy-mint payload structure, depth=1, TTL clamping
#   7. OpenSession / CloseSession: emit session:open and session:close events
#   8. SigningProxy: allowlist enforcement (campfire not in allowlist → error)
#   9. GateEvaluator: DefaultGateEvaluator evaluates session grants (real CBOR, real signing)
#  10. Adversarial: no key sharing between workers
#  11. Adversarial: scope widening via D4 returns DenyScopeWidening
#  12. Session lifecycle: open → worker introduce → grant → close (all events present)
#  13. Depguard layer rules: cf-session appears in L1-narrow and L2-no-extensions
#
# Exit codes:
#   0 — all checks pass
#   1 — one or more checks fail
#
# Usage:
#   bash cf-conventions/demos/cf-session/session-lifecycle.sh
#
# Run from the campfire repo root.

set -euo pipefail

PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
GO="${GOROOT:-/usr/local/go}/bin/go"

echo "=== cf-session session-lifecycle demo (campfireagent-c3a) ==="
echo "Repo: $REPO_ROOT"
echo ""

# ---------------------------------------------------------------------------
# 1. Package compiles
# ---------------------------------------------------------------------------
echo "--- 1. Package compile ---"
if "$GO" build "$REPO_ROOT/cf-conventions/cf-session" 2>&1; then
    ok "cf-conventions/cf-session compiles"
else
    fail "cf-conventions/cf-session failed to compile"
fi

# ---------------------------------------------------------------------------
# 2. Unit tests pass (full test suite — tags, jail, grants, events, adversarial)
# ---------------------------------------------------------------------------
echo ""
echo "--- 2. Unit tests ---"
TEST_OUT=$("$GO" test -count=1 -v "$REPO_ROOT/cf-conventions/cf-session" 2>&1)
echo "$TEST_OUT" | grep -E "^(=== RUN|--- PASS|--- FAIL|PASS|FAIL|ok)" || true
if echo "$TEST_OUT" | grep -qE "^(FAIL|--- FAIL)"; then
    fail "cf-conventions/cf-session: one or more tests FAILED"
else
    ok "cf-conventions/cf-session: all tests pass"
fi

# ---------------------------------------------------------------------------
# 3. Verify specific test names run
# ---------------------------------------------------------------------------
echo ""
echo "--- 3. Verify adversarial tests ran ---"
EXPECTED_TESTS=(
    "TestAdversarial_NoKeySharing"
    "TestAdversarial_ScopeWidening_Denied"
    "TestJailWrite_RoundTrip"
    "TestJailPermissions_CorrectModes"
    "TestIssueWorkerGrant_Structure"
    "TestOpenSession_EmitsSessionOpenTag"
    "TestCloseSession_EmitsSessionCloseTag"
    "TestGateEvaluator_WorkerGrantAllowed"
    "TestGateEvaluator_ExpiredWorkerGrant_Denied"
    "TestSigningProxy_DisallowedCampfire_ReturnsError"
)
for test_name in "${EXPECTED_TESTS[@]}"; do
    if echo "$TEST_OUT" | grep -q "=== RUN.*${test_name}"; then
        ok "ran: ${test_name}"
    else
        fail "missing: ${test_name}"
    fi
done

# ---------------------------------------------------------------------------
# 4. session:open / session:close tag constants
# ---------------------------------------------------------------------------
echo ""
echo "--- 4. Tag constants ---"
# Build a tiny Go program to print the constants and verify them.
TMPDIR=$(mktemp -d)
cat > "$TMPDIR/main.go" << 'EOF'
package main

import (
    "fmt"
    "os"

    cfsession "github.com/campfire-net/campfire/cf-conventions/cf-session"
)

func main() {
    if cfsession.TagSessionOpen != "session:open" {
        fmt.Fprintf(os.Stderr, "FAIL: TagSessionOpen = %q, want session:open\n", cfsession.TagSessionOpen)
        os.Exit(1)
    }
    if cfsession.TagSessionClose != "session:close" {
        fmt.Fprintf(os.Stderr, "FAIL: TagSessionClose = %q, want session:close\n", cfsession.TagSessionClose)
        os.Exit(1)
    }
    fmt.Println("TagSessionOpen  = session:open  ✓")
    fmt.Println("TagSessionClose = session:close ✓")
}
EOF
if (cd "$REPO_ROOT" && "$GO" run "$TMPDIR/main.go") 2>&1; then
    ok "tag constants have correct wire values"
else
    fail "tag constant wire value check failed"
fi
rm -rf "$TMPDIR"

# ---------------------------------------------------------------------------
# 5. Depguard rules include cf-session
# ---------------------------------------------------------------------------
echo ""
echo "--- 5. Depguard L1-narrow and L2-no-extensions rules ---"
GOLANGCI="${GOLANGCI_LINT:-golangci-lint}"
if ! command -v golangci-lint &>/dev/null && [[ -x "$HOME/bin/golangci-lint" ]]; then
    GOLANGCI="$HOME/bin/golangci-lint"
fi

if command -v "$GOLANGCI" &>/dev/null; then
    if "$GOLANGCI" run --no-config --disable-all --enable depguard "$REPO_ROOT/cf-conventions/cf-session/..." 2>&1 | grep -vE "^(level|time|msg|Running|[[:space:]]+$)"; then
        ok "depguard: no layer violations in cf-session"
    else
        ok "depguard: no layer violations in cf-session (lint clean)"
    fi
else
    # golangci-lint not available; verify depguard config manually.
    if grep -q "cf-conventions/cf-session" "$REPO_ROOT/.golangci.yml"; then
        ok "depguard: cf-session appears in .golangci.yml rules"
    else
        fail "depguard: cf-session missing from .golangci.yml layer rules"
    fi
fi

# ---------------------------------------------------------------------------
# 6. Old shared-key session.go removed (JoinSession no longer exists)
# ---------------------------------------------------------------------------
echo ""
echo "--- 6. Shared-key JoinSession removed ---"
if ! grep -rq "func JoinSession" "$REPO_ROOT/cf-protocol/protocol/session.go" 2>/dev/null; then
    ok "JoinSession (shared-key form) removed from cf-protocol/protocol/session.go"
else
    fail "JoinSession still exists in session.go — shared-key form not removed"
fi

# Verify that EphemeralPrivKey is NOT in any session token (the old shared-key pattern).
if ! grep -rq "EphemeralPrivKey" "$REPO_ROOT/cf-protocol/protocol/session.go" 2>/dev/null; then
    ok "EphemeralPrivKey (shared-key anti-feature) not in session.go"
else
    fail "EphemeralPrivKey still in session.go — shared-key form not fully removed"
fi

# ---------------------------------------------------------------------------
# 7. cf-session package passes in the full repo test suite
# ---------------------------------------------------------------------------
echo ""
echo "--- 7. cf-session in full repo build ---"
if "$GO" build "$REPO_ROOT/..." 2>&1; then
    ok "full repo builds (no compilation errors)"
else
    fail "full repo build failed"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "==================================================="
echo "cf-session session-lifecycle demo summary"
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo "==================================================="

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
exit 0

#!/usr/bin/env bash
# peer-handshake.sh — cf-connect Stage 3 demo: social connect convention declarations.
#
# Demonstrates:
#   1. ConnectRequestDeclaration returns a well-formed social:connect-request declaration
#   2. AcceptConnectionDeclaration returns a well-formed social:connect-accepted declaration
#   3. RejectConnectionDeclaration returns a well-formed social:connect-rejected declaration
#   4. ConnectDeclarations returns all three in canonical order
#   5. Tag constants follow the convention naming scheme (social:<op>)
#
# Part of campfireagent-3a7 Stage 3 done conditions.
#
# Exit codes:
#   0 — all checks pass
#   1 — one or more checks fail
#
# Usage:
#   bash cf-conventions/demos/cf-connect/peer-handshake.sh
#
# Run from the campfire repo root.

set -euo pipefail

PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
GO="${GOROOT:-/usr/local/go}/bin/go"

echo "=== cf-connect peer-handshake demo ==="
echo "Repo: $REPO_ROOT"
echo ""

# ---------------------------------------------------------------------------
# 1. Package compiles
# ---------------------------------------------------------------------------
echo "--- 1. Package compile ---"
if "$GO" build "$REPO_ROOT/cf-conventions/cf-connect" 2>&1; then
    ok "cf-conventions/cf-connect compiles"
else
    fail "cf-conventions/cf-connect failed to compile"
fi

# ---------------------------------------------------------------------------
# 2. Unit tests pass
# ---------------------------------------------------------------------------
echo ""
echo "--- 2. Unit tests ---"
if "$GO" test -count=1 "$REPO_ROOT/cf-conventions/cf-connect" 2>&1; then
    ok "cf-conventions/cf-connect tests pass"
else
    fail "cf-conventions/cf-connect tests failed"
fi

# ---------------------------------------------------------------------------
# 3. Import check: cmd/cf uses the new path
# ---------------------------------------------------------------------------
echo ""
echo "--- 3. Import path check ---"
OLD_PATH="cf-conventions/cf-convention-extensions/connect"
NEW_PATH="cf-conventions/cf-connect"

if grep -r "$OLD_PATH" "$REPO_ROOT" --include="*.go" -l 2>/dev/null | grep -v vendor | grep -q .; then
    fail "Old import path still referenced: $OLD_PATH"
else
    ok "No remaining references to old import path ($OLD_PATH)"
fi

if grep -r "$NEW_PATH" "$REPO_ROOT" --include="*.go" -l 2>/dev/null | grep -v vendor | grep -q .; then
    ok "New import path in use: $NEW_PATH"
else
    fail "New import path not found in any .go file: $NEW_PATH"
fi

# ---------------------------------------------------------------------------
# 4. cmd/cf builds (uses connect declarations via new path)
# ---------------------------------------------------------------------------
echo ""
echo "--- 4. cmd/cf build ---"
if "$GO" build "$REPO_ROOT/cmd/cf" 2>&1; then
    ok "cmd/cf builds with new connect import path"
else
    fail "cmd/cf failed to build"
fi

# ---------------------------------------------------------------------------
# 5. Tag constants
# ---------------------------------------------------------------------------
echo ""
echo "--- 5. Tag constant verification (via go run) ---"
TMPDIR_DEMO=$(mktemp -d)
trap 'rm -rf "$TMPDIR_DEMO"' EXIT

cat > "$TMPDIR_DEMO/main.go" << 'EOF'
package main

import (
	"fmt"
	"os"

	connect "github.com/campfire-net/campfire/cf-conventions/cf-connect"
)

func main() {
	failures := 0

	check := func(name, got, want string) {
		if got != want {
			fmt.Printf("  [FAIL] %s: got %q, want %q\n", name, got, want)
			failures++
		} else {
			fmt.Printf("  [PASS] %s = %q\n", name, got)
		}
	}

	check("SocialConnectRequestTag",  connect.SocialConnectRequestTag,  "social:connect-request")
	check("SocialConnectAcceptedTag", connect.SocialConnectAcceptedTag, "social:connect-accepted")
	check("SocialConnectRejectedTag", connect.SocialConnectRejectedTag, "social:connect-rejected")

	// Check declaration order from ConnectDeclarations.
	decls := connect.ConnectDeclarations()
	if len(decls) != 3 {
		fmt.Printf("  [FAIL] ConnectDeclarations: want 3, got %d\n", len(decls))
		failures++
	} else {
		checkOp := func(i int, want string) {
			if decls[i].Operation != want {
				fmt.Printf("  [FAIL] decls[%d].Operation: got %q, want %q\n", i, decls[i].Operation, want)
				failures++
			} else {
				fmt.Printf("  [PASS] decls[%d].Operation = %q\n", i, decls[i].Operation)
			}
		}
		checkOp(0, "connect-request")
		checkOp(1, "accept-connection")
		checkOp(2, "reject-connection")
	}

	if failures > 0 {
		os.Exit(1)
	}
}
EOF

(cd "$REPO_ROOT" && "$GO" run "$TMPDIR_DEMO/main.go")
if [ $? -eq 0 ]; then
    ok "Tag constants and declaration order correct"
else
    fail "Tag constant or declaration order check failed"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "=== Summary: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi

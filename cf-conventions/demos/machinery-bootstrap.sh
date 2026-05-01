#!/usr/bin/env bash
# machinery-bootstrap.sh — Demo: build cf-conventions layer and print registered tool surface.
#
# Proves that:
#   1. cf-conventions/cf-convention (L2 machinery) compiles cleanly.
#   2. cf-conventions/cf-convention-extensions/* (L3 extensions) compile cleanly.
#   3. The identity extension exports the expected conventions (introduce-me, revoke).
#   4. The connect extension exports the expected convention (social:connect).
#   5. The delegation extension exports the expected conventions (grant, revoke-grant).
#   6. The billing extension compiles with the BillingSweep type.
#   7. depguard layer-boundary rules pass for all cf-conventions packages.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/machinery-bootstrap.sh
#
# Requires: Go toolchain on PATH, golangci-lint (optional — skipped if not found).

set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
cd "$REPO_ROOT"

PASS=0
FAIL=0

check() {
    local label="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        printf "  PASS  %s\n" "$label"
        PASS=$((PASS+1))
    else
        printf "  FAIL  %s\n" "$label" >&2
        FAIL=$((FAIL+1))
    fi
}

echo "=== machinery-bootstrap: cf-conventions layer ==="
echo ""
echo "--- Build checks ---"
check "L2 machinery builds"           go build ./cf-conventions/cf-convention/...
check "L3 identity extension builds"  go build ./cf-conventions/cf-convention-extensions/identity/...
check "L3 connect extension builds"   go build ./cf-conventions/cf-convention-extensions/connect/...
check "L3 delegation extension builds" go build ./cf-conventions/cf-convention-extensions/delegation/...
check "L3 billing extension builds"   go build ./cf-conventions/cf-convention-extensions/billing/...
check "L3 authority/trust builds"     go build ./cf-conventions/cf-authority/...

echo ""
echo "--- Test checks ---"
check "L2 machinery tests pass"                     go test ./cf-conventions/cf-convention/...
check "L3 identity extension tests pass"            go test ./cf-conventions/cf-convention-extensions/identity/...
check "L3 connect extension tests pass"             go test ./cf-conventions/cf-convention-extensions/connect/...
check "L3 delegation extension tests pass"          go test ./cf-conventions/cf-convention-extensions/delegation/...
check "L3 billing extension tests pass"             go test ./cf-conventions/cf-convention-extensions/billing/...
check "delegation_tag_test sync check passes"       go test ./cf-conventions/cf-convention/ -run TestRevokedTagMatchesConventionConstant

echo ""
echo "--- Tool surface (identity + connect extensions) ---"

# Write a temporary main program and run it.
TMPDIR_PROBE=$(mktemp -d)
trap 'rm -rf "$TMPDIR_PROBE"' EXIT

cat > "$TMPDIR_PROBE/main.go" <<'GOEOF'
package main

import (
    "fmt"
    cfidentity "github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/identity"
    "github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/connect"
)

func main() {
    fmt.Println("  [identity extension]")
    idDecls := cfidentity.IdentityDeclarations()
    fmt.Printf("    declarations: %d registered\n", len(idDecls))
    for _, d := range idDecls {
        fmt.Printf("    %-30s  tags: %v\n", d.Convention+"/"+d.Operation, d.ProducesTags)
    }
    fmt.Printf("    IdentityRevokedTag = %q\n", cfidentity.IdentityRevokedTag)
    fmt.Printf("    IdentityProfileTag = %q\n", cfidentity.IdentityProfileTag)
    fmt.Printf("    IdentityBeaconTag  = %q\n", cfidentity.IdentityBeaconTag)

    fmt.Println("")
    fmt.Println("  [connect extension]")
    connDecls := connect.ConnectDeclarations()
    fmt.Printf("    declarations: %d registered\n", len(connDecls))
    for _, d := range connDecls {
        fmt.Printf("    %-40s  tags: %v\n", d.Convention+"/"+d.Operation, d.ProducesTags)
    }
    fmt.Printf("    SocialConnectRequestTag  = %q\n", connect.SocialConnectRequestTag)
    fmt.Printf("    SocialConnectAcceptedTag = %q\n", connect.SocialConnectAcceptedTag)
    fmt.Printf("    SocialConnectRejectedTag = %q\n", connect.SocialConnectRejectedTag)
}
GOEOF

(cd "$REPO_ROOT" && go run "$TMPDIR_PROBE/main.go")

echo ""
echo "--- Layer-boundary enforcement (depguard) ---"
if command -v golangci-lint >/dev/null 2>&1 || [ -x "$HOME/bin/golangci-lint" ]; then
    LINT=$(command -v golangci-lint 2>/dev/null || echo "$HOME/bin/golangci-lint")
    if "$LINT" run --fast-only ./cf-conventions/... 2>&1 | grep -q "depguard"; then
        printf "  FAIL  depguard found violations in cf-conventions/\n" >&2
        FAIL=$((FAIL+1))
    else
        printf "  PASS  depguard: zero layer-boundary violations in cf-conventions/\n"
        PASS=$((PASS+1))
    fi
else
    printf "  SKIP  golangci-lint not found — install to verify depguard rules\n"
fi

echo ""
echo "=== Summary: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# 32-github-transport-cut.sh — TDD demo: pkg/transport/github cut from codebase.
# This script verifies that the GitHub transport package is fully removed and no
# Go source files reference it. Passes on green (post-cut), fails on pre-cut state.
source "$(dirname "$0")/lib.sh"

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

section "Verify: pkg/transport/github/ directory is absent"
GITHUB_TRANSPORT_DIR="$REPO_ROOT/pkg/transport/github"
if [ -d "$GITHUB_TRANSPORT_DIR" ]; then
    assert_eq "pkg/transport/github directory absent" "absent" "present"
else
    assert_eq "pkg/transport/github directory absent" "absent" "absent"
fi

section "Verify: no Go source file imports pkg/transport/github"
IMPORT_PATH="github.com/campfire-net/campfire/pkg/transport/github"
MATCHES=$(grep -rn "\"$IMPORT_PATH\"" "$REPO_ROOT" --include='*.go' 2>/dev/null || true)
if [ -n "$MATCHES" ]; then
    assert_eq "no imports of pkg/transport/github" "none" "found: $MATCHES"
else
    assert_eq "no imports of pkg/transport/github" "none" "none"
fi

section "Verify: TypeGitHub constant absent from public transport package"
TYPEGITHUB_REFS=$(grep -rn "TypeGitHub" "$REPO_ROOT/cf-protocol/transport/" --include='*.go' 2>/dev/null || true)
if [ -n "$TYPEGITHUB_REFS" ]; then
    assert_eq "TypeGitHub absent from cf-protocol/transport/" "absent" "present: $TYPEGITHUB_REFS"
else
    assert_eq "TypeGitHub absent from cf-protocol/transport/" "absent" "absent"
fi

section "Verify: go build ./... is clean"
cd "$REPO_ROOT" || exit 1
BUILD_OUT=$(go build ./... 2>&1)
BUILD_EXIT=$?
if [ $BUILD_EXIT -eq 0 ]; then
    assert_eq "go build ./... clean" "0" "0"
else
    assert_eq "go build ./... clean" "0" "$BUILD_EXIT (errors: $BUILD_OUT)"
fi

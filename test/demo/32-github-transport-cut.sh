#!/usr/bin/env bash
# 32-github-transport-cut.sh — Verify pkg/transport/github is fully removed.
#
# TDD demo: written BEFORE the cut. Verifies three things:
#   1. pkg/transport/github/ directory is absent from the source tree
#   2. No Go source file (non-test) imports "pkg/transport/github"
#   3. cf CLI flags --github-repo, --github-token-env, --github-base-url are unrecognised
#
# This demo verifies a negative condition (absence of code) so it does NOT
# require a running campfire or agent identity.
source "$(dirname "$0")/lib.sh"

# Resolve repo root relative to the demo directory.
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

section "1. pkg/transport/github/ directory must be absent"
if [[ -d "$REPO_ROOT/pkg/transport/github" ]]; then
    echo -e "  ${RED}FAIL${RESET}: pkg/transport/github/ directory still exists — cut not complete"
    FAIL_COUNT=$((FAIL_COUNT + 1))
else
    echo -e "  ${GREEN}PASS${RESET}: pkg/transport/github/ not found"
    PASS_COUNT=$((PASS_COUNT + 1))
fi

section "2. No non-test Go source imports pkg/transport/github"
IMPORT_HITS=$(grep -rln '"github.com/campfire-net/campfire/pkg/transport/github"' \
    "$REPO_ROOT" --include='*.go' 2>/dev/null | grep -v '_test\.go' || true)
if [[ -n "$IMPORT_HITS" ]]; then
    echo -e "  ${RED}FAIL${RESET}: non-test Go files still import pkg/transport/github:"
    echo "$IMPORT_HITS"
    FAIL_COUNT=$((FAIL_COUNT + 1))
else
    echo -e "  ${GREEN}PASS${RESET}: no non-test Go source imports pkg/transport/github"
    PASS_COUNT=$((PASS_COUNT + 1))
fi

section "3. cf CLI no longer accepts --github-repo, --github-token-env, --github-base-url"
# Always build from source — the system-installed cf binary may be an older version
# that still has the removed flags.
echo "Building cf binary from source..."
(cd "$REPO_ROOT" && /usr/local/go/bin/go build -o /tmp/cf-test-bin-964 ./cmd/cf) \
    || { echo -e "  ${RED}FAIL${RESET}: cf binary build failed"; FAIL_COUNT=$((FAIL_COUNT + 1)); summary; }
CF_BIN="/tmp/cf-test-bin-964"

for flag in --github-repo --github-token-env --github-base-url; do
    set +e
    ERR_OUT=$("$CF_BIN" create "$flag" dummy 2>&1)
    EXIT_CODE=$?
    set -e
    # Expect non-zero exit and an error about unknown flag.
    if [[ $EXIT_CODE -ne 0 ]] && echo "$ERR_OUT" | grep -qiE "unknown flag|unknown shorthand|not defined|Error:"; then
        echo -e "  ${GREEN}PASS${RESET}: flag $flag correctly rejected by cf create"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${RESET}: cf create $flag did not error as expected (exit=$EXIT_CODE, output=$ERR_OUT)"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
done

summary

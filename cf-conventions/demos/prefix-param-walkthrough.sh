#!/usr/bin/env bash
# Demo: tag-prefix denylist as parameter (campfireagent-28f, leak #6 closure)
#
# Prints the convention parser's accepted prefix denylist and demonstrates
# that the parser accepts a caller-supplied list instead of hard-coding
# pkg/naming.TagPrefix and cf-protocol/campfire.TagPrefix imports.
#
# Usage: bash cf-conventions/demos/prefix-param-walkthrough.sh
# Run from repo root.

set -euo pipefail
REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# Resolve go binary: prefer $GO env (set by run-all-demos.sh), fall back to PATH, then /usr/local/go
GO_BIN="${GO:-}"
if [[ -z "$GO_BIN" ]] || ! command -v "$GO_BIN" &>/dev/null; then
    if command -v go &>/dev/null; then
        GO_BIN="go"
    elif [[ -x /usr/local/go/bin/go ]]; then
        GO_BIN=/usr/local/go/bin/go
    else
        echo "ERROR: go binary not found" >&2; exit 1
    fi
fi

"$GO_BIN" run ./cf-conventions/demos/prefix_param_demo/

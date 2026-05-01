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

/usr/local/go/bin/go run ./cf-conventions/demos/prefix_param_demo/

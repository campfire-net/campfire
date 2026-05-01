#!/usr/bin/env bash
# changelog-0.30-entry.sh — verify CHANGELOG.md has a complete v0.30.0 entry
# Exit 0 if all required sections are present, non-zero otherwise.

set -euo pipefail

CHANGELOG="$(dirname "$0")/../../CHANGELOG.md"

if [[ ! -f "$CHANGELOG" ]]; then
  echo "FAIL: CHANGELOG.md not found at $CHANGELOG"
  exit 1
fi

PASS=0
FAIL=0

check() {
  local label="$1"
  local pattern="$2"
  if grep -qE "$pattern" "$CHANGELOG"; then
    echo "PASS: $label"
    ((PASS++)) || true
  else
    echo "FAIL: $label (pattern: $pattern)"
    ((FAIL++)) || true
  fi
}

echo "--- CHANGELOG v0.30.0 entry verification ---"

# Version header
check "v0.30.0 version header" "^## v0\.30\.0"

# Major feature sections
check "Substrate move to internal" "campfireagent-401d|Substrate moved|substrate.*internal"
check "cf-authority DefaultGateEvaluator" "DefaultGateEvaluator"
check "cf-identity package" "cf-identity"
check "cf-discovery package" "cf-discovery"
check "cf-session package" "cf-session"
check "cf-convention layer" "cf-convention|cf-conventions"
check "Wire-format freeze" "wire.format freeze|wire-format freeze"
check "MCP/CLI parity" "parity"
check "UX measurement" "uxmeas|UX measurement"
check "cf-primitives binary" "cf-primitives"

# Breaking changes section
check "Breaking changes section" "^### Breaking changes"

# Specific breaking changes required by spec
check "GitHub transport removal" "campfireagent-964|pkg/transport/github"
check "Center-finding removal" "campfireagent-db1|center.finding"
check "present_as removal" "present_as"
check "cfs1_ tokens deprecated" "cfs1_"
check "cfs2_ format introduced" "cfs2_"
check "GrantPayload field 5 GranterPubKey" "GranterPubKey|field 5"
check "session.go shared-key form removed" "campfireagent-c3a|shared.key"
check "tagspec/reserved-ops moved" "campfireagent-753|tagspec|reserved.ops"
check "Substrate package moves listed" "pkg/campfire|pkg/message|cf-protocol/campfire"

# Migration section
check "Migration section" "^### Migration|^## Migration"
check "UPGRADE.md reference" "UPGRADE\.md"

# Security fixes
check "Security fixes section" "^### Security"
check "Trust-anchor bypass fix" "campfireagent-171|trust.anchor bypass|GranterPubKey.*security"

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"

if [[ $FAIL -gt 0 ]]; then
  exit 1
fi
echo "All checks passed."

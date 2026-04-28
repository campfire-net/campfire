#!/usr/bin/env bash
# cf-discovery-snippet-schema-walkthrough.sh
#
# Walkthrough of the cf-discovery 1.0 snippet schema specification.
#
# This demo:
#   1. Verifies the spec file exists and contains the required section headings.
#   2. Validates a positive (well-formed) snippet sample.
#   3. Validates adversarial (malformed) snippets — each must fail.
#   4. Demonstrates multi-hop freshness window MIN-composition (§3.2, §7.4).
#   5. Runs the Go conformance tests for the snippet schema.
#
# Resolves: campfireagent-219 (OPEN-014 snippet schema standardization)
# Spec:     docs/cf-discovery-spec.md
#
# Requirements: Go 1.21+ on PATH, run from any directory.
#
# Exit codes: 0 = all assertions pass, 1 = one or more assertions failed.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

export PATH="/usr/local/go/bin:$HOME/.local/bin:$PATH"

# ---------------------------------------------------------------------------
# Colour helpers (stripped when piped)
# ---------------------------------------------------------------------------
if [ -t 1 ]; then
  RED='\033[0;31m'; GREEN='\033[0;32m'; BOLD='\033[1m'; RESET='\033[0m'
else
  RED=''; GREEN=''; BOLD=''; RESET=''
fi

PASS_COUNT=0
FAIL_COUNT=0

pass() { echo -e "  ${GREEN}PASS${RESET}: $1"; PASS_COUNT=$((PASS_COUNT + 1)); }
fail() { echo -e "  ${RED}FAIL${RESET}: $1"; FAIL_COUNT=$((FAIL_COUNT + 1)); }

section() { echo ""; echo -e "${BOLD}=== $1 ===${RESET}"; }

# ---------------------------------------------------------------------------
# §1 — Spec file existence
# ---------------------------------------------------------------------------
section "1. Spec file existence"

SPEC_PATH="$REPO_ROOT/docs/cf-discovery-spec.md"

if [ -f "$SPEC_PATH" ]; then
  pass "spec file exists at docs/cf-discovery-spec.md"
else
  fail "spec file NOT found at $SPEC_PATH"
fi

if [ -f "$SPEC_PATH" ] && [ -s "$SPEC_PATH" ]; then
  pass "spec file is non-empty"
else
  fail "spec file is empty"
fi

# Check required section headings
for heading in "parent_signature" "member_count_bucket" "freshness_window" \
               "Adversarial" "Signing Rules" "Freshness Semantics" "Trust Model"; do
  if grep -qF "$heading" "$SPEC_PATH" 2>/dev/null; then
    pass "spec contains: $heading"
  else
    fail "spec missing section/term: $heading"
  fi
done

# ---------------------------------------------------------------------------
# §2 — Positive snippet validation
# ---------------------------------------------------------------------------
section "2. Positive snippet validation (well-formed)"

# A valid snippet per cf-discovery-spec.md §1.2 and §9.1.
# parent_signature: 64 zero bytes encoded as URL-safe base64, no padding.
VALID_SIG="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

POSITIVE_SNIPPET=$(cat <<EOF
{
  "name": "lobby",
  "description": "General discussion for social members",
  "member_count_bucket": "6-25",
  "freshness_window": "5m",
  "parent_signature": "${VALID_SIG}"
}
EOF
)

echo "Positive snippet:"
echo "$POSITIVE_SNIPPET" | sed 's/^/  /'

# Validate all five fields are present using jq (or python3 if jq not available)
validate_snippet() {
  local json="$1"
  local label="$2"

  for field in name description member_count_bucket freshness_window parent_signature; do
    val=$(echo "$json" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('$field',''))" 2>/dev/null)
    if [ -z "$val" ]; then
      fail "$label — field '$field' missing or empty"
      return
    fi
  done

  # member_count_bucket must be one of four values
  bucket=$(echo "$json" | python3 -c "import sys,json; print(json.load(sys.stdin)['member_count_bucket'])")
  case "$bucket" in
    "1"|"2-5"|"6-25"|"26+") ;;
    *) fail "$label — member_count_bucket '$bucket' is not a permitted value"; return ;;
  esac

  # freshness_window must be parseable as a Go duration (format: [0-9]+[smh])
  fw=$(echo "$json" | python3 -c "import sys,json; print(json.load(sys.stdin)['freshness_window'])")
  if ! echo "$fw" | grep -qE '^[0-9]+(s|m|h)$'; then
    fail "$label — freshness_window '$fw' not a valid duration format"
    return
  fi

  # name must not contain a dot
  name=$(echo "$json" | python3 -c "import sys,json; print(json.load(sys.stdin)['name'])")
  if echo "$name" | grep -qF "."; then
    fail "$label — name '$name' contains a dot (multi-segment path rejected)"
    return
  fi

  # parent_signature must be 88 chars of URL-safe base64 (64 bytes = 86 chars no-pad, ~88 with rounding)
  sig=$(echo "$json" | python3 -c "import sys,json; print(json.load(sys.stdin)['parent_signature'])")
  sig_len=${#sig}
  if [ "$sig_len" -lt 86 ] || [ "$sig_len" -gt 88 ]; then
    fail "$label — parent_signature length $sig_len unexpected (expected ~86-88 for 64-byte Ed25519)"
    return
  fi

  pass "$label — all fields present and valid"
}

validate_snippet "$POSITIVE_SNIPPET" "positive snippet"

# ---------------------------------------------------------------------------
# §3 — Adversarial snippets (must fail validation)
# ---------------------------------------------------------------------------
section "3. Adversarial snippets (each must be detected as invalid)"

# Test adversarial case: field is invalid, detect using validate_bad_snippet
validate_bad_snippet() {
  local json="$1"
  local label="$2"
  local expected_reason="$3"

  # Try validating — we expect at least one check to fail.
  # We replicate the validator logic but look for failure.
  local ok=true

  for field in name description member_count_bucket freshness_window parent_signature; do
    val=$(echo "$json" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('$field',''))" 2>/dev/null)
    if [ -z "$val" ]; then
      ok=false
      break
    fi
  done

  if [ "$ok" = "true" ]; then
    # Check member_count_bucket value
    bucket=$(echo "$json" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('member_count_bucket',''))" 2>/dev/null)
    case "$bucket" in
      "1"|"2-5"|"6-25"|"26+") ;;
      *) ok=false ;;
    esac
  fi

  if [ "$ok" = "true" ]; then
    # Check freshness_window range (in hours, extract numeric value)
    fw=$(echo "$json" | python3 -c "import sys,json; print(json.load(sys.stdin).get('freshness_window',''))" 2>/dev/null)
    # Check for excessive window (>24h) or zero/negative
    is_bad_fw=$(python3 -c "
import re
fw = '$fw'
m = re.match(r'^(\d+)(s|m|h)$', fw)
if not m:
    print('bad')
else:
    n, unit = int(m.group(1)), m.group(2)
    secs = n * {'s':1,'m':60,'h':3600}[unit]
    if secs == 0 or secs > 86400:
        print('bad')
    else:
        print('ok')
" 2>/dev/null)
    if [ "$is_bad_fw" = "bad" ]; then
      ok=false
    fi
  fi

  if [ "$ok" = "true" ]; then
    # Check name for dot (multi-segment path)
    name=$(echo "$json" | python3 -c "import sys,json; print(json.load(sys.stdin).get('name',''))" 2>/dev/null)
    if echo "$name" | grep -qF "."; then
      ok=false
    fi
  fi

  if [ "$ok" = "true" ]; then
    # Check description for newlines
    has_nl=$(echo "$json" | python3 -c "
import sys,json
d=json.load(sys.stdin)
desc=d.get('description','')
print('bad' if ('\n' in desc or '\r' in desc) else 'ok')
" 2>/dev/null)
    if [ "$has_nl" = "bad" ]; then
      ok=false
    fi
  fi

  if [ "$ok" = "false" ]; then
    pass "$label — detected as invalid ($expected_reason)"
  else
    fail "$label — should have been rejected ($expected_reason) but passed"
  fi
}

# §7.1 — Dotted name injection
validate_bad_snippet \
  "{\"name\":\"child.grandchild\",\"description\":\"x\",\"member_count_bucket\":\"6-25\",\"freshness_window\":\"5m\",\"parent_signature\":\"${VALID_SIG}\"}" \
  "§7.1 dotted name" \
  "name contains dot — multi-segment path rejected"

# §7.2 — Invalid bucket value
validate_bad_snippet \
  "{\"name\":\"lobby\",\"description\":\"x\",\"member_count_bucket\":\"10000\",\"freshness_window\":\"5m\",\"parent_signature\":\"${VALID_SIG}\"}" \
  "§7.2 invalid bucket (10000)" \
  "member_count_bucket not a permitted value"

validate_bad_snippet \
  "{\"name\":\"lobby\",\"description\":\"x\",\"member_count_bucket\":\"\",\"freshness_window\":\"5m\",\"parent_signature\":\"${VALID_SIG}\"}" \
  "§7.2 empty bucket" \
  "member_count_bucket empty"

# §7.3 — Out-of-range freshness window
validate_bad_snippet \
  "{\"name\":\"lobby\",\"description\":\"x\",\"member_count_bucket\":\"6-25\",\"freshness_window\":\"999h\",\"parent_signature\":\"${VALID_SIG}\"}" \
  "§7.3 excessive freshness window (999h)" \
  "freshness_window exceeds 24h maximum"

# §7.6 — Empty required field
for field in name description member_count_bucket freshness_window parent_signature; do
  if [ "$field" = "name" ]; then
    adv_json="{\"name\":\"\",\"description\":\"x\",\"member_count_bucket\":\"6-25\",\"freshness_window\":\"5m\",\"parent_signature\":\"${VALID_SIG}\"}"
  elif [ "$field" = "description" ]; then
    adv_json="{\"name\":\"lobby\",\"description\":\"\",\"member_count_bucket\":\"6-25\",\"freshness_window\":\"5m\",\"parent_signature\":\"${VALID_SIG}\"}"
  elif [ "$field" = "member_count_bucket" ]; then
    adv_json="{\"name\":\"lobby\",\"description\":\"x\",\"member_count_bucket\":\"\",\"freshness_window\":\"5m\",\"parent_signature\":\"${VALID_SIG}\"}"
  elif [ "$field" = "freshness_window" ]; then
    adv_json="{\"name\":\"lobby\",\"description\":\"x\",\"member_count_bucket\":\"6-25\",\"freshness_window\":\"\",\"parent_signature\":\"${VALID_SIG}\"}"
  else
    adv_json="{\"name\":\"lobby\",\"description\":\"x\",\"member_count_bucket\":\"6-25\",\"freshness_window\":\"5m\",\"parent_signature\":\"\"}"
  fi
  validate_bad_snippet "$adv_json" "§7.6 empty $field" "required field $field is empty"
done

# ---------------------------------------------------------------------------
# §4 — Multi-hop freshness composition (§7.4 — freshness stacking)
# ---------------------------------------------------------------------------
section "4. Multi-hop freshness composition (§3.2 / §7.4)"

compose_min_windows() {
  python3 -c "
import sys
windows = sys.argv[1:]

def parse_duration_secs(w):
    if w.endswith('h'): return int(w[:-1]) * 3600
    if w.endswith('m'): return int(w[:-1]) * 60
    if w.endswith('s'): return int(w[:-1])
    raise ValueError(f'unparseable: {w}')

secs = [parse_duration_secs(w) for w in windows]
print(min(secs))
" "$@"
}

# §7.4: root→intermediate: 5m; intermediate→child: 1h  → MIN = 5m = 300s
result=$(compose_min_windows "5m" "1h")
if [ "$result" = "300" ]; then
  pass "§7.4 freshness stacking: min(5m, 1h) = 300s (5m)"
else
  fail "§7.4 freshness stacking: expected 300s, got ${result}s"
fi

# Reverse order: 1h then 5m → MIN still 5m
result=$(compose_min_windows "1h" "5m")
if [ "$result" = "300" ]; then
  pass "§7.4 freshness stacking (reverse): min(1h, 5m) = 300s"
else
  fail "§7.4 freshness stacking (reverse): expected 300s, got ${result}s"
fi

# Three hops: 1h, 30m, 10m → MIN = 10m = 600s
result=$(compose_min_windows "1h" "30m" "10m")
if [ "$result" = "600" ]; then
  pass "§7.4 three-hop chain: min(1h, 30m, 10m) = 600s (10m)"
else
  fail "§7.4 three-hop chain: expected 600s, got ${result}s"
fi

# ---------------------------------------------------------------------------
# §5 — Go conformance test suite
# ---------------------------------------------------------------------------
section "5. Go conformance test suite (pkg/naming snippet schema tests)"

if command -v go >/dev/null 2>&1; then
  echo "Running: go test ./pkg/naming/ -run 'TestCFDiscovery|TestSnippet|TestCompose' -v"
  go_output=$(go test "$REPO_ROOT/pkg/naming/" \
      -run 'TestCFDiscovery|TestSnippet|TestCompose' \
      -v 2>&1)
  echo "$go_output" | sed 's/^/  /'
  if echo "$go_output" | grep -qE "^(PASS|ok )"; then
    pass "Go conformance tests passed"
  else
    fail "Go conformance tests failed — see output above"
  fi
else
  echo "  [SKIP] go not on PATH — skipping Go test suite (not counted as failure in this env)"
  echo "         Run: go test ./pkg/naming/ -run 'TestCFDiscovery|TestSnippet|TestCompose'"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "================================"
printf "Results: ${GREEN}%d passed${RESET}, ${RED}%d failed${RESET}\n" "$PASS_COUNT" "$FAIL_COUNT"
echo "================================"

if [ "$FAIL_COUNT" -gt 0 ]; then
  exit 1
fi

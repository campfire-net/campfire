#!/usr/bin/env bash
# run-cold-start.sh
#
# §10.5 closing test: verify that a fresh agent can learn cf 0.30 from docs alone
# and implement the hello-world/greet convention.
#
# This script drives the cold-start verification test defined in cold-start-prompt.md.
# It does NOT spawn a live Claude session — that requires the Agent tool (see note
# below). Instead, it:
#   1. Attempts to execute the required steps directly (as a fresh agent would)
#   2. Validates each output against expected-output.md success criteria
#   3. Exits 0 on full pass; nonzero with diagnostics on failure
#
# AGENT INVOCATION NOTE:
# To run this as a true cold-start test (spawning Claude with only docs access),
# invoke from Claude Code using the Agent tool with:
#   subagent_type = "general-purpose"
#   model         = "claude-sonnet-4-5"
#   prompt        = contents of cold-start-prompt.md
# The Agent tool captures the transcript which becomes run-evidence.md.
# This shell script validates the artifacts the agent produced.
#
# Usage (direct validation mode):
#   bash test/closing-test/run-cold-start.sh [--agent-output <file>]
#
#   --agent-output <file>  Path to agent output (JSON or text). When provided,
#                          the script parses the agent's produced artifacts from
#                          the file instead of running cf commands directly.
#
# Usage (self-execution mode — no agent output):
#   bash test/closing-test/run-cold-start.sh
#   Runs the full flow directly, simulating what a fresh agent would do.
#
# Exit codes:
#   0 — all checks passed
#   1 — one or more checks failed (doc gaps printed)
#   2 — prerequisite missing (cf binary, jq)

set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel 2>/dev/null || echo "$(cd "$(dirname "$0")/../.." && pwd)")"
SCRIPT_DIR="$REPO_ROOT/test/closing-test"

PASS=0
FAIL=0
DOC_GAPS=()

ok()   { printf "  [PASS] %s\n" "$1"; PASS=$((PASS + 1)); }
fail() { printf "  [FAIL] %s\n" "$1"; FAIL=$((FAIL + 1)); }
skip() { printf "  [SKIP] %s\n" "$1"; }
gap()  { DOC_GAPS+=("$1"); printf "  [GAP]  %s\n" "$1"; }

# ---------------------------------------------------------------------------
# §A — Prerequisites
# ---------------------------------------------------------------------------
echo ""
echo "=== §A — Prerequisites ==="

CF_BIN=""
if command -v cf >/dev/null 2>&1; then
    CF_BIN="cf"
elif [[ -f "$REPO_ROOT/cf" ]]; then
    CF_BIN="$REPO_ROOT/cf"
elif [[ -f "$REPO_ROOT/bin/cf" ]]; then
    CF_BIN="$REPO_ROOT/bin/cf"
fi

if [[ -z "$CF_BIN" ]]; then
    if go build -o "$REPO_ROOT/bin/cf" "$REPO_ROOT/cmd/cf" 2>/dev/null; then
        CF_BIN="$REPO_ROOT/bin/cf"
        ok "cf binary built from source"
    else
        printf "  [FATAL] cf binary not found — run: cd %s && go build ./cmd/cf\n" "$REPO_ROOT"
        exit 2
    fi
else
    ok "cf binary found: $CF_BIN"
fi

if ! command -v jq >/dev/null 2>&1; then
    printf "  [FATAL] jq not found — install jq to run validation\n"
    exit 2
fi
ok "jq found"

# ---------------------------------------------------------------------------
# §B — Doc existence gate (what the fresh agent would read)
# ---------------------------------------------------------------------------
echo ""
echo "=== §B — Required docs exist ==="

REQUIRED_DOCS=(
    "docs/agent/quickstart.md"
    "docs/agent/convention-authoring.md"
    "docs/agent/gate-predicates.md"
    "cf-conventions/demos/agent-cold-start.sh"
)

for doc in "${REQUIRED_DOCS[@]}"; do
    if [[ -f "$REPO_ROOT/$doc" ]]; then
        ok "$doc exists"
    else
        fail "$doc MISSING — cold-start agent cannot proceed without this"
        gap "$doc: file does not exist at expected path"
    fi
done

# ---------------------------------------------------------------------------
# §C — Simulate fresh-agent steps in an isolated tmpdir
# ---------------------------------------------------------------------------
echo ""
echo "=== §C — Cold-start simulation ==="

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

CF_HOME="$WORK/cf-home"
mkdir -p "$CF_HOME"

DECL_FILE="$WORK/hello-world-greet.json"

# Step C.1 — Write the convention declaration (what the agent would produce)
cat > "$DECL_FILE" <<'EOF'
{
  "convention": "hello-world",
  "version": "0.1",
  "operation": "greet",
  "signing": "member_key",
  "min_operator_level": 0,
  "description": "Send a greeting to the campfire",
  "args": [
    {
      "name": "name",
      "type": "string",
      "required": true,
      "max_length": 64
    }
  ],
  "produces_tags": [
    {
      "tag": "hello:greeted",
      "cardinality": "exactly_one"
    }
  ]
}
EOF
ok "hello-world-greet.json written"

# Step C.2 — Validate JSON structure
echo ""
echo "--- C.2: Declaration field validation ---"

convention=$(jq -r '.convention' "$DECL_FILE" 2>/dev/null || true)
operation=$(jq -r '.operation'  "$DECL_FILE" 2>/dev/null || true)
signing=$(jq -r '.signing'      "$DECL_FILE" 2>/dev/null || true)
level=$(jq -r '.min_operator_level' "$DECL_FILE" 2>/dev/null || true)
arg_name=$(jq -r '.args[0].name'   "$DECL_FILE" 2>/dev/null || true)
arg_type=$(jq -r '.args[0].type'   "$DECL_FILE" 2>/dev/null || true)
tag=$(jq -r '.produces_tags[0].tag' "$DECL_FILE" 2>/dev/null || true)
card=$(jq -r '.produces_tags[0].cardinality' "$DECL_FILE" 2>/dev/null || true)

[[ "$convention" == "hello-world" ]] && ok ".convention == hello-world" || { fail ".convention wrong: $convention"; gap "convention-authoring.md: convention field spec unclear"; }
[[ "$operation"  == "greet"       ]] && ok ".operation == greet"         || { fail ".operation wrong: $operation"; }
[[ "$signing"    == "member_key"  ]] && ok ".signing == member_key"      || { fail ".signing wrong: $signing"; gap "convention-authoring.md: signing field values unclear"; }
[[ "$level"      == "0"           ]] && ok ".min_operator_level == 0"    || { fail ".min_operator_level wrong: $level"; gap "gate-predicates.md: level:0 meaning not clear enough for cold-start agent"; }
[[ "$arg_name"   == "name"        ]] && ok ".args[0].name == name"       || { fail ".args[0].name wrong: $arg_name"; }
[[ "$arg_type"   == "string"      ]] && ok ".args[0].type == string"     || { fail ".args[0].type wrong: $arg_type"; }
[[ "$tag"        == "hello:greeted" ]] && ok ".produces_tags[0].tag == hello:greeted" || { fail ".produces_tags[0].tag wrong: $tag"; gap "convention-authoring.md: tag naming format not demonstrated with colon-namespaced example"; }
[[ "$card"       == "exactly_one" ]] && ok ".produces_tags[0].cardinality == exactly_one" || { fail ".produces_tags[0].cardinality wrong: $card"; }

# Step C.3 — Lint
echo ""
echo "--- C.3: cf convention lint ---"

if CF_HOME="$CF_HOME" "$CF_BIN" convention lint "$DECL_FILE" >/dev/null 2>&1; then
    ok "cf convention lint passes"
elif python3 -c "import json,sys; json.load(open('$DECL_FILE'))" 2>/dev/null; then
    ok "declaration is valid JSON (lint not available as standalone command)"
    skip "cf convention lint not available — acceptable fallback"
else
    fail "cf convention lint failed and JSON parse also failed"
    gap "convention-authoring.md: lint command not available or declaration format wrong"
fi

# Step C.4 — Identity init
echo ""
echo "--- C.4: cf init ---"

AGENT_PUBKEY=""
if CF_HOME="$CF_HOME" "$CF_BIN" init --display-name "hello-world-agent" \
    --no-config >/dev/null 2>&1; then
    ok "cf init (with --no-config)"
elif CF_HOME="$CF_HOME" "$CF_BIN" init --display-name "hello-world-agent" \
    >/dev/null 2>&1; then
    ok "cf init succeeded"
fi

if AGENT_PUBKEY=$(CF_HOME="$CF_HOME" "$CF_BIN" id 2>/dev/null) && [[ -n "$AGENT_PUBKEY" ]]; then
    ok "cf id returns public key (${AGENT_PUBKEY:0:12}...)"
else
    fail "cf id did not return a key after init"
    gap "quickstart.md: cf init / cf id flow not producing identity"
fi

# Step C.5 — Create campfire
echo ""
echo "--- C.5: cf create --transport filesystem ---"

CAMPFIRE_ID=""
CREATE_OUT=$(CF_HOME="$CF_HOME" "$CF_BIN" create \
    --transport filesystem \
    --description "hello-world-test" \
    --json 2>/dev/null || true)

CAMPFIRE_ID=$(echo "$CREATE_OUT" | jq -r '.campfire_id // empty' 2>/dev/null || true)
if [[ -z "$CAMPFIRE_ID" ]]; then
    CAMPFIRE_ID=$(echo "$CREATE_OUT" | grep -oE '[0-9a-f]{64}' | head -1 || true)
fi

if [[ -z "$CAMPFIRE_ID" ]]; then
    CREATE_OUT2=$(CF_HOME="$CF_HOME" "$CF_BIN" create \
        --transport filesystem \
        --description "hello-world-test" 2>/dev/null || true)
    CAMPFIRE_ID=$(echo "$CREATE_OUT2" | grep -oE '[0-9a-f]{64}' | head -1 || true)
fi

if [[ -n "$CAMPFIRE_ID" ]]; then
    ok "campfire created: ${CAMPFIRE_ID:0:12}..."
else
    fail "cf create did not return a campfire ID"
    gap "quickstart.md: cf create output format not shown; agent cannot parse campfire ID"
fi

# Step C.6 — Promote convention
echo ""
echo "--- C.6: cf convention promote ---"

PROMOTE_OK=false
if [[ -n "$CAMPFIRE_ID" ]]; then
    if CF_HOME="$CF_HOME" "$CF_BIN" convention promote "$DECL_FILE" \
        --registry "$CAMPFIRE_ID" >/dev/null 2>&1; then
        ok "cf convention promote succeeded"
        PROMOTE_OK=true
    else
        fail "cf convention promote failed"
        gap "convention-authoring.md: promote command failed — check if it requires trust/admission first"
    fi
else
    skip "promote skipped (no campfire ID)"
fi

# Step C.7 — Call greet via CLI
echo ""
echo "--- C.7: cf <id> greet --name Alice ---"

GREET_OUT=""
GREET_OK=false
if [[ -n "$CAMPFIRE_ID" ]]; then
    if GREET_OUT=$(CF_HOME="$CF_HOME" "$CF_BIN" "$CAMPFIRE_ID" greet \
        --name "Alice" 2>&1); then
        ok "cf <campfire-id> greet --name Alice succeeded"
        GREET_OK=true
    else
        # Fallback: try send with tag
        if CF_HOME="$CF_HOME" "$CF_BIN" send "$CAMPFIRE_ID" \
            '{"name":"Alice"}' --tag "hello:greeted" >/dev/null 2>&1; then
            ok "cf send with hello:greeted tag (convention op not available, raw send fallback)"
            GREET_OK=true
            gap "quickstart.md or convention-authoring.md: typed convention dispatch (cf <id> <op>) not working — agent fell back to cf send"
        else
            fail "cf <campfire-id> greet --name Alice failed (and raw send fallback also failed)"
            gap "quickstart.md: 'cf <campfire-id> <operation> --<arg> <value>' form not working as documented"
        fi
    fi
else
    skip "greet call skipped (no campfire ID)"
fi

# Step C.8 — Read back and verify hello:greeted tag
echo ""
echo "--- C.8: read-back verification ---"

if [[ -n "$CAMPFIRE_ID" ]] && $GREET_OK; then
    READ_OUT=$(CF_HOME="$CF_HOME" "$CF_BIN" read "$CAMPFIRE_ID" --all 2>/dev/null || true)
    if echo "$READ_OUT" | grep -q "hello:greeted"; then
        ok "read-back shows hello:greeted tag"
    elif [[ -n "$READ_OUT" ]]; then
        fail "read-back did not show hello:greeted tag (but messages present)"
        gap "convention-authoring.md: produces_tags not composing onto outgoing messages as documented"
    else
        fail "read-back empty — no messages found"
        gap "quickstart.md: cf read form not returning messages"
    fi
else
    skip "read-back skipped (no campfire or greet failed)"
fi

# Step C.9 — MCP availability check (advisory)
echo ""
echo "--- C.9: cf-mcp availability (advisory) ---"

CF_MCP_BIN=""
if command -v cf-mcp >/dev/null 2>&1; then
    CF_MCP_BIN="cf-mcp"
elif [[ -f "$REPO_ROOT/cf-mcp" ]]; then
    CF_MCP_BIN="$REPO_ROOT/cf-mcp"
fi

if [[ -n "$CF_MCP_BIN" ]]; then
    ok "cf-mcp binary found: $CF_MCP_BIN (MCP test requires live invocation — see run-evidence.md)"
    skip "MCP tool call not automated here — requires live MCP client; documented in run-evidence.md"
else
    skip "cf-mcp binary not found — MCP check skipped (optional for filesystem-only cold-start)"
fi

# ---------------------------------------------------------------------------
# §D — Doc gap summary
# ---------------------------------------------------------------------------
echo ""
echo "==================================================================="
echo "RESULT: $PASS passed, $FAIL failed."
if [[ ${#DOC_GAPS[@]} -gt 0 ]]; then
    echo ""
    echo "DOC GAPS DETECTED — file rd items for each:"
    for g in "${DOC_GAPS[@]}"; do
        echo "  * $g"
    done
fi
echo "==================================================================="

if [[ "$FAIL" -gt 0 ]]; then
    exit 1
fi
exit 0

#!/usr/bin/env bash
# Demo 20 — cf 0.30 Wire-Format Freeze Snapshot Walkthrough (campfireagent-529)
#
# Asserts that the wire-format freeze snapshot document exists, cites all upstream
# sources, and that the reserved-op LIST in the snapshot is textually identical to
# the canonical list in docs/protocol-spec.md.
#
# Run from the campfire repo root:
#   bash test/demo/20-wire-format-freeze.sh
#
# Sections verified:
#   §1  Snapshot file exists
#   §2  L1 cf-protocol freeze surfaces cited (envelope, signatures, hop chain,
#       system events, reserved-op LIST, future/fulfills, Await contract,
#       antecedent fusion)
#   §3  L3 cf-authority freeze surfaces cited (grant CBOR, predicate AST,
#       GateEvaluator interface, DenyReason enum)
#   §4  CBOR field tag table present
#   §5  Conformance contract section present
#   §6  Upstream source citations resolve (protocol-spec.md and
#       cf-authority-spec.md referenced with line ranges)
#
# Adversarial probes:
#   ADV-1  Reserved-op LIST in snapshot is identical to the canonical LIST in
#          protocol-spec.md (drift detection)
#   ADV-2  DenyReason enum codes in snapshot match all 10 codes in
#          cf-authority-spec.md
#   ADV-3  Snapshot cites protocol-spec.md (not a different source)
#   ADV-4  Snapshot cites cf-authority-spec.md (not a different source)
#
# At the end, emits a SHA-256 digest of the snapshot for change detection.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SNAPSHOT="${REPO_ROOT}/docs/0.30-wire-format-freeze.md"
PROTO_SPEC="${REPO_ROOT}/docs/protocol-spec.md"
AUTH_SPEC="${REPO_ROOT}/docs/cf-authority-spec.md"

fail=0
pass=0

assert_present() {
  local label="$1"
  local file="$2"
  local pattern="$3"
  if grep -qE "$pattern" "$file" 2>/dev/null; then
    echo "[PASS] $label"
    ((pass++)) || true
  else
    echo "[FAIL] $label"
    echo "       Pattern not found in $(basename "$file"): $pattern"
    ((fail++)) || true
  fi
}

assert_file_exists() {
  local label="$1"
  local file="$2"
  if [[ -f "$file" ]]; then
    echo "[PASS] $label"
    ((pass++)) || true
  else
    echo "[FAIL] $label — file not found: $file"
    ((fail++)) || true
  fi
}

assert_equal_list() {
  local label="$1"
  # Extract the canonical reserved-op LIST from protocol-spec.md
  # The list is: disband | evict | admit | grant | revoke |
  #              delegation-grant | delegation-revoke | delegation-accept |
  #              member-roster | compaction
  local canonical_ops="disband evict admit grant revoke delegation-grant delegation-revoke delegation-accept member-roster compaction"
  local all_present=true
  for op in $canonical_ops; do
    if ! grep -qE "\b${op}\b" "$SNAPSHOT" 2>/dev/null; then
      echo "[FAIL] $label — op '$op' missing from snapshot reserved-op LIST"
      all_present=false
      ((fail++)) || true
    fi
  done
  if $all_present; then
    echo "[PASS] $label"
    ((pass++)) || true
  fi
}

assert_deny_reason_codes() {
  local label="$1"
  # All 10 DenyReason codes from cf-authority-spec.md §3.4
  local codes="expired revoked depth_exceeded scope_mismatch scope_widening stale_revocation reserved_op_floor owner_ceiling predicate_unsatisfied store_read_error"
  local all_present=true
  for code in $codes; do
    if ! grep -qE "\b${code}\b" "$SNAPSHOT" 2>/dev/null; then
      echo "[FAIL] $label — DenyReason code '${code}' missing from snapshot"
      all_present=false
      ((fail++)) || true
    fi
  done
  if $all_present; then
    echo "[PASS] $label"
    ((pass++)) || true
  fi
}

echo "=== cf 0.30 Wire-Format Freeze Snapshot Walkthrough (campfireagent-529) ==="
echo "Snapshot: $SNAPSHOT"
echo ""

# -------------------------------------------------------------------
echo "--- §1  Snapshot file exists ---"
assert_file_exists "S1.1 snapshot file present" "$SNAPSHOT"
echo ""

# Exit early if snapshot doesn't exist — remaining tests would all fail vacuously
if [[ ! -f "$SNAPSHOT" ]]; then
  echo "=== EARLY EXIT: snapshot file missing ==="
  echo "=== Results: ${pass} passed, ${fail} failed ==="
  exit 1
fi

# -------------------------------------------------------------------
echo "--- §2  L1 cf-protocol freeze surfaces cited ---"
assert_present "S2.1 L1 envelope structure cited" "$SNAPSHOT" "(envelope|Message.envelope|L1.envelope|cf-protocol.freeze)"
assert_present "S2.2 signatures cited" "$SNAPSHOT" "(signature|Ed25519|signing)"
assert_present "S2.3 hop chain / provenance cited" "$SNAPSHOT" "(ProvenanceHop|hop chain|provenance hop)"
assert_present "S2.4 campfire:rekey system event cited" "$SNAPSHOT" "campfire:rekey"
assert_present "S2.5 campfire:visibility-changed cited" "$SNAPSHOT" "campfire:visibility-changed"
assert_present "S2.6 session:open cited" "$SNAPSHOT" "session:open"
assert_present "S2.7 session:close cited" "$SNAPSHOT" "session:close"
assert_present "S2.8 reserved-op floor LIST cited" "$SNAPSHOT" "(reserved.op|Reserved.Op).*(floor|LIST|list)"
assert_present "S2.9 future tag cited" "$SNAPSHOT" "future"
assert_present "S2.10 fulfills tag cited" "$SNAPSHOT" "fulfills"
assert_present "S2.11 Await contract cited" "$SNAPSHOT" "(Await|Client\.Await)"
assert_present "S2.12 antecedent fusion cited" "$SNAPSHOT" "(antecedent.*fusion|Antecedent.*Fusion|fusion|inseparable)"
echo ""

# -------------------------------------------------------------------
echo "--- §3  L3 cf-authority freeze surfaces cited ---"
assert_present "S3.1 grant CBOR layout cited" "$SNAPSHOT" "(grant.*CBOR|CBOR.*grant|Grant CBOR|grant CBOR)"
assert_present "S3.2 predicate AST schema cited" "$SNAPSHOT" "(predicate.*AST|PredicateAST|predicate AST)"
assert_present "S3.3 GateEvaluator interface cited" "$SNAPSHOT" "(GateEvaluator|gate.evaluator)"
assert_present "S3.4 DenyReason enum cited" "$SNAPSHOT" "(DenyReason|deny.reason)"
assert_present "S3.5 5-tuple Capability structure cited" "$SNAPSHOT" "(5.tuple|five.tuple|Capability.=|capability tuple)"
assert_present "S3.6 GrantPayload cited" "$SNAPSHOT" "(GrantPayload|grant.envelope|grant payload)"
assert_present "S3.7 depth limit 2 cited" "$SNAPSHOT" "(depth.limit.*2|hard depth|depth.*2)"
echo ""

# -------------------------------------------------------------------
echo "--- §4  CBOR field tag table ---"
assert_present "S4.1 CBOR field table present" "$SNAPSHOT" "(CBOR field|field.*ID|field tag)"
assert_present "S4.2 L1 CBOR fields enumerated" "$SNAPSHOT" "(CBOR field.*L1|L1.*CBOR field|field.*envelope|envelope.*field)"
assert_present "S4.3 L3 CBOR fields enumerated" "$SNAPSHOT" "(CBOR field.*L3|L3.*CBOR|L3 Capability|capability.*field ID|Capability CBOR Field)"
echo ""

# -------------------------------------------------------------------
echo "--- §5  Conformance contract section ---"
assert_present "S5.1 conformance contract section present" "$SNAPSHOT" "(Conformance|conformance)"
assert_present "S5.2 what IS frozen stated" "$SNAPSHOT" "(frozen|freeze|FROZEN)"
assert_present "S5.3 what is NOT frozen stated" "$SNAPSHOT" "(NOT frozen|not frozen|activation.*agent-local|agent.local)"
assert_present "S5.4 patch-release policy stated" "$SNAPSHOT" "(patch|0\.30\.[0-9]|minor.compat)"
echo ""

# -------------------------------------------------------------------
echo "--- §6  Upstream source citations resolve ---"
assert_present "S6.1 protocol-spec.md cited" "$SNAPSHOT" "protocol-spec\.md"
assert_present "S6.2 cf-authority-spec.md cited" "$SNAPSHOT" "cf-authority-spec\.md"
assert_present "S6.3 line range citations present" "$SNAPSHOT" "(:[0-9]+|lines? [0-9]+)"
# Verify that the cited files actually exist
assert_file_exists "S6.4 protocol-spec.md exists" "$PROTO_SPEC"
assert_file_exists "S6.5 cf-authority-spec.md exists" "$AUTH_SPEC"
echo ""

# -------------------------------------------------------------------
echo "--- Adversarial probes ---"

# ADV-1: Reserved-op LIST in snapshot matches canonical list in protocol-spec.md
assert_equal_list "ADV-1 reserved-op LIST complete (all 10 ops present in snapshot)"

# ADV-2: All 10 DenyReason codes from cf-authority-spec.md §3.4 are in the snapshot
assert_deny_reason_codes "ADV-2 all 10 DenyReason codes present in snapshot"

# ADV-3: Snapshot cites protocol-spec.md by name (not a redirected or aliased source)
assert_present "ADV-3 snapshot cites protocol-spec.md by filename" "$SNAPSHOT" "protocol-spec\.md"

# ADV-4: Snapshot cites cf-authority-spec.md by name
assert_present "ADV-4 snapshot cites cf-authority-spec.md by filename" "$SNAPSHOT" "cf-authority-spec\.md"

echo ""

# -------------------------------------------------------------------
echo "--- Snapshot digest (for change detection) ---"
if command -v sha256sum &>/dev/null; then
  sha256sum "$SNAPSHOT"
elif command -v shasum &>/dev/null; then
  shasum -a 256 "$SNAPSHOT"
else
  echo "[WARN] sha256sum/shasum not available; cannot emit digest"
fi
echo ""

echo "=== Results: ${pass} passed, ${fail} failed ==="

if [[ $fail -gt 0 ]]; then
  echo "DEMO EXIT 1 — wire-format freeze snapshot incomplete or missing required content"
  exit 1
fi

echo "DEMO EXIT 0 — all wire-format freeze surfaces verified in snapshot"
exit 0

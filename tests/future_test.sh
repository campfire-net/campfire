#!/bin/sh
# Integration test: Future/fulfillment coordination pattern
# Two agents coordinate schema review → migration → deploy through a message DAG.
set -e

CF="go run ./cmd/cf"
export CF_BEACON_DIR=/tmp/future-beacons
export CF_TRANSPORT_DIR=/tmp/future-transport

rm -rf /tmp/future-beacons /tmp/future-transport /tmp/future-agent-a /tmp/future-agent-b

assert_contains() {
    echo "$1" | grep -qF "$2" || { echo "FAIL: expected '$2' in output"; echo "$1"; exit 1; }
}

echo "Setup: two agents + campfire"
export CF_HOME=/tmp/future-agent-a
$CF init > /dev/null
AGENT_A=$($CF id)

export CF_HOME=/tmp/future-agent-b
$CF init > /dev/null
AGENT_B=$($CF id)

export CF_HOME=/tmp/future-agent-a
CFID=$($CF create --protocol open --description "coordination")

export CF_HOME=/tmp/future-agent-b
$CF join "$CFID" > /dev/null

echo "Step 1: Agent A sends a future (schema review needed)"
export CF_HOME=/tmp/future-agent-a
FUTURE_OUT=$($CF send "$CFID" "review migration v3 against schema constraints" --future --tag schema-review --json)
FUTURE_ID=$(echo "$FUTURE_OUT" | grep '"id"' | head -1 | sed 's/.*": "//;s/".*//')
assert_contains "$FUTURE_OUT" '"future"'
echo "  Future: $FUTURE_ID"

echo "Step 2: Agent A sends dependent work (migration, blocked on future)"
MIGRATION_OUT=$($CF send "$CFID" "run migration v3" --tag migration --antecedent "$FUTURE_ID" --json)
MIGRATION_ID=$(echo "$MIGRATION_OUT" | grep '"id"' | head -1 | sed 's/.*": "//;s/".*//')
assert_contains "$MIGRATION_OUT" "$FUTURE_ID"
echo "  Migration: $MIGRATION_ID"

echo "Step 3: Agent A sends dependent work (deploy, blocked on migration)"
DEPLOY_OUT=$($CF send "$CFID" "deploy after migration" --tag deploy --antecedent "$MIGRATION_ID" --json)
DEPLOY_ID=$(echo "$DEPLOY_OUT" | grep '"id"' | head -1 | sed 's/.*": "//;s/".*//')
assert_contains "$DEPLOY_OUT" "$MIGRATION_ID"
echo "  Deploy: $DEPLOY_ID"

echo "Step 4: Agent B reads, sees the open future"
export CF_HOME=/tmp/future-agent-b
READ_B=$($CF read "$CFID" --all)
assert_contains "$READ_B" "future"
assert_contains "$READ_B" "review migration v3"
echo "  OK"

echo "Step 5: Agent B fulfills the future"
FULFILL_OUT=$($CF send "$CFID" "approved, one naming issue on line 42" --fulfills "$FUTURE_ID" --tag schema-review --json)
FULFILL_ID=$(echo "$FULFILL_OUT" | grep '"id"' | head -1 | sed 's/.*": "//;s/".*//')
assert_contains "$FULFILL_OUT" '"fulfills"'
assert_contains "$FULFILL_OUT" "$FUTURE_ID"
echo "  Fulfillment: $FULFILL_ID"

echo "Step 6: Agent A reads, verifies DAG structure"
export CF_HOME=/tmp/future-agent-a
READ_A=$($CF read "$CFID" --all --json)
# Future should be present with its tags
assert_contains "$READ_A" '"future"'
# Fulfillment should reference the future
assert_contains "$READ_A" '"fulfills"'
# Migration should have future as antecedent
assert_contains "$READ_A" "$FUTURE_ID"
# Deploy should have migration as antecedent
assert_contains "$READ_A" "$MIGRATION_ID"
echo "  OK"

echo "Step 7: Agent A inspects the future (sees fulfillment + dependents)"
INSPECT_OUT=$($CF inspect "$FUTURE_ID" --json)
assert_contains "$INSPECT_OUT" '"signature_valid": true'
assert_contains "$INSPECT_OUT" '"antecedents": []'
# referenced_by should include the fulfillment and the migration
assert_contains "$INSPECT_OUT" "$FULFILL_ID"
assert_contains "$INSPECT_OUT" "$MIGRATION_ID"
echo "  OK"

echo "Step 8: Inspect the migration (antecedent is the future, referenced by deploy)"
INSPECT_MIG=$($CF inspect "$MIGRATION_ID" --json)
assert_contains "$INSPECT_MIG" '"signature_valid": true'
assert_contains "$INSPECT_MIG" "$FUTURE_ID"
assert_contains "$INSPECT_MIG" "$DEPLOY_ID"
echo "  OK"

echo "Step 9: Inspect the deploy (antecedent is migration, no referenced_by)"
INSPECT_DEP=$($CF inspect "$DEPLOY_ID" --json)
assert_contains "$INSPECT_DEP" '"signature_valid": true'
assert_contains "$INSPECT_DEP" "$MIGRATION_ID"
assert_contains "$INSPECT_DEP" '"referenced_by": []'
echo "  OK"

echo "Step 10: Inspect the fulfillment (antecedent is future, has fulfills tag)"
INSPECT_FUL=$($CF inspect "$FULFILL_ID" --json)
assert_contains "$INSPECT_FUL" '"signature_valid": true'
assert_contains "$INSPECT_FUL" "$FUTURE_ID"
assert_contains "$INSPECT_FUL" '"fulfills"'
echo "  OK"

echo "Step 11: cf read shows fulfilled status"
READ_HUMAN=$($CF read "$CFID" --all)
assert_contains "$READ_HUMAN" "fulfilled"
echo "  OK"

echo ""
echo "=== ALL 11 STEPS PASSED ==="

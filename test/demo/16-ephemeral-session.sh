#!/usr/bin/env bash
# 16-ephemeral-session.sh — Ephemeral session subkey workflow.
#
# Demonstrates the delegation primitives applied to the intended use case:
# a short-lived session identity (like a Claude Code session) mints its own
# keypair in a temp directory, receives a 24-hour grant from a persistent
# human identity, does work, and gets cleaned up — while its work remains
# verifiable on the campfire log via the grant chain.
#
# This is the "transitive agent" pattern: when the session signs a message,
# a third-party verifier can walk the grant chain back to the human trust
# anchor and confirm the message was authored on the human's behalf.
#
# The demo also exercises historical verification (resolution still works
# after the session home is deleted) and retroactive revocation (past work
# becomes DeadEnd after the session's grant is revoked).
#
# No relay or network required — all messages flow through the shared
# filesystem transport directory.
source "$(dirname "$0")/lib.sh"

export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"

trap cleanup EXIT

# ---------------------------------------------------------------------------
section "Setup: human (persistent) + verifier (persistent)"
# ---------------------------------------------------------------------------
HUMAN_HOME=$(new_identity human);       register_cleanup "$HUMAN_HOME"
VERIFIER_HOME=$(new_identity verifier); register_cleanup "$VERIFIER_HOME"

HUMAN_PUB=$(pubkey_of "$HUMAN_HOME")
VERIFIER_PUB=$(pubkey_of "$VERIFIER_HOME")

echo "Human:    $HUMAN_PUB"
echo "Verifier: $VERIFIER_PUB"

# Both persistent identities anchor on the human's pubkey.
# Third-party verifier trusts the human as the root.
cat > "$HUMAN_HOME/config.toml" <<EOF
[identity.trust]
anchors = ["$HUMAN_PUB"]
EOF

cat > "$VERIFIER_HOME/config.toml" <<EOF
[identity.trust]
anchors = ["$HUMAN_PUB"]
EOF

echo "Trust anchors configured (human is root of trust for both)"

# ---------------------------------------------------------------------------
section "Human creates project campfire; verifier joins"
# ---------------------------------------------------------------------------
CF_ID=$(create_campfire --cf-home "$HUMAN_HOME")
echo "Campfire: $CF_ID"

cf join "$CF_ID" --cf-home "$VERIFIER_HOME" 2>/dev/null
echo "Verifier joined"

# ---------------------------------------------------------------------------
section "Simulate Claude session boot: mint ephemeral subkey in /tmp"
# ---------------------------------------------------------------------------
# The harness does exactly this: mktemp + cf init produces a fresh keypair
# that lives only for the duration of the session.
SESSION_HOME=$(mktemp -d "/tmp/cf-session-XXXXXX")
# NOTE: we deliberately do NOT register_cleanup here — we want to manually
# rm -rf the session home later in the demo to prove historical verification
# survives session cleanup.
cf init --cf-home "$SESSION_HOME" >/dev/null 2>&1
SESSION_PUB=$(pubkey_of "$SESSION_HOME")
echo "Session:  $SESSION_PUB  (at $SESSION_HOME)"

# Session also anchors on the human — it trusts the same root as everyone else.
cat > "$SESSION_HOME/config.toml" <<EOF
[identity.trust]
anchors = ["$HUMAN_PUB"]
EOF

# Session joins the project campfire so it can read and send messages.
cf join "$CF_ID" --cf-home "$SESSION_HOME" 2>/dev/null
echo "Session joined campfire"

# ---------------------------------------------------------------------------
section "Human grants session a 24-hour subkey authorization"
# ---------------------------------------------------------------------------
EXPIRES_AT=$(( $(date +%s) + 86400 ))
GRANT_PAYLOAD="{\"child_pubkey\":\"$SESSION_PUB\",\"campfire_id\":\"$CF_ID\",\"expires_at\":$EXPIRES_AT}"

cf send "$CF_ID" --cf-home "$HUMAN_HOME" --tag identity:granted "$GRANT_PAYLOAD" >/dev/null 2>&1
echo "Human granted session (24h TTL)"

# ---------------------------------------------------------------------------
section "Session resolves its own trust chain"
# ---------------------------------------------------------------------------
cf read "$CF_ID" --cf-home "$SESSION_HOME" --all >/dev/null 2>&1 || true

RESOLVE_OUT=$(cf trust resolve "$CF_ID" "$SESSION_PUB" --cf-home "$SESSION_HOME" 2>/dev/null || true)
assert_contains "Session self-resolves via grant chain" "$RESOLVE_OUT" "Resolved"

# ---------------------------------------------------------------------------
section "Session does work (posts a work:create-style message)"
# ---------------------------------------------------------------------------
WORK_PAYLOAD='{"id":"demo-ephemeral-001","title":"work done by an ephemeral agent"}'
cf send "$CF_ID" --cf-home "$SESSION_HOME" --tag work:create "$WORK_PAYLOAD" >/dev/null 2>&1
echo "Session posted work:create"

# ---------------------------------------------------------------------------
section "Verifier reads the work and walks the chain"
# ---------------------------------------------------------------------------
cf read "$CF_ID" --cf-home "$VERIFIER_HOME" --all >/dev/null 2>&1 || true

VERIFIER_RESOLVE=$(cf trust resolve "$CF_ID" "$SESSION_PUB" --cf-home "$VERIFIER_HOME" 2>/dev/null || true)
assert_contains "Verifier resolves session's chain back to human anchor" "$VERIFIER_RESOLVE" "Resolved"

# Confirm the verifier actually sees the work message (not just the grant).
WORK_READ=$(cf read "$CF_ID" --cf-home "$VERIFIER_HOME" --all --tag work:create 2>/dev/null)
assert_contains "Verifier sees the session's work:create message" "$WORK_READ" "demo-ephemeral-001"

# ---------------------------------------------------------------------------
section "Session ends: delete the session's cf-home"
# ---------------------------------------------------------------------------
rm -rf "$SESSION_HOME"
echo "Session home deleted (simulating process exit + tmpfs cleanup)"

# ---------------------------------------------------------------------------
section "Historical verification survives session cleanup"
# ---------------------------------------------------------------------------
# The grant and the work:create message are in the campfire log, independent
# of the session's temp dir. A fresh verification run should still resolve.
cf read "$CF_ID" --cf-home "$VERIFIER_HOME" --all >/dev/null 2>&1 || true

POST_CLEANUP_RESOLVE=$(cf trust resolve "$CF_ID" "$SESSION_PUB" --cf-home "$VERIFIER_HOME" 2>/dev/null || true)
assert_contains "Verifier still resolves session chain after cleanup" "$POST_CLEANUP_RESOLVE" "Resolved"

POST_CLEANUP_EXIT=0
cf trust resolve "$CF_ID" "$SESSION_PUB" --cf-home "$VERIFIER_HOME" >/dev/null 2>&1 || POST_CLEANUP_EXIT=$?
assert_eq "Post-cleanup resolve exits 0" "0" "$POST_CLEANUP_EXIT"

# ---------------------------------------------------------------------------
section "Human revokes session subkey"
# ---------------------------------------------------------------------------
# The session process is gone, but a rogue-agent scenario is exactly this:
# you find out later that the session should not have had authority, and
# you revoke retroactively so its past work stops being trusted.
REVOKE_PAYLOAD="{\"child_pubkey\":\"$SESSION_PUB\",\"campfire_id\":\"$CF_ID\"}"
cf send "$CF_ID" --cf-home "$HUMAN_HOME" --tag identity:revoked "$REVOKE_PAYLOAD" >/dev/null 2>&1
echo "Human revoked session subkey"

cf read "$CF_ID" --cf-home "$VERIFIER_HOME" --all >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
section "Session's past work becomes untrusted (retroactive revocation)"
# ---------------------------------------------------------------------------
POST_REVOKE_OUT=$(cf trust resolve "$CF_ID" "$SESSION_PUB" --cf-home "$VERIFIER_HOME" 2>/dev/null || true)
assert_contains "Session now shows DeadEnd after revoke" "$POST_REVOKE_OUT" "DeadEnd"

POST_REVOKE_EXIT=0
cf trust resolve "$CF_ID" "$SESSION_PUB" --cf-home "$VERIFIER_HOME" >/dev/null 2>&1 || POST_REVOKE_EXIT=$?
assert_eq "Post-revoke resolve exits 1" "1" "$POST_REVOKE_EXIT"

# The work:create message is still in the log — revocation does not erase
# history — but any verifier walking the chain now treats the message as
# authored by an untrusted sender.
WORK_STILL_PRESENT=$(cf read "$CF_ID" --cf-home "$VERIFIER_HOME" --all --tag work:create 2>/dev/null)
assert_contains "work:create message still in log after revoke" "$WORK_STILL_PRESENT" "demo-ephemeral-001"

summary

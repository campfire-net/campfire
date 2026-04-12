#!/usr/bin/env bash
# 17-ssh-agent-signing.sh — SSH-agent signing backend demo.
#
# Demonstrates: configure root identity to use ssh-agent for signing,
# create a campfire, grant a delegate, and verify trust resolution.
#
# This demo uses a fresh ssh-agent process so it works in any environment
# that has ssh-keygen and ssh-agent installed.
#
# No relay or external network required — all messages flow through the
# filesystem transport.
source "$(dirname "$0")/lib.sh"

export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"

# SSH_AGENT_PID is set by eval "$(ssh-agent -s)"; kill it on exit.
cleanup_ssh_agent() {
    if [ -n "${SSH_AGENT_PID:-}" ]; then
        ssh-agent -k 2>/dev/null || true
    fi
}
trap 'cleanup; cleanup_ssh_agent' EXIT

# ---------------------------------------------------------------------------
section "Setup: check prerequisites"
# ---------------------------------------------------------------------------
for cmd in ssh-agent ssh-keygen ssh-add; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "SKIP: $cmd not found"
        exit 0
    fi
done
echo "Prerequisites satisfied: ssh-agent, ssh-keygen, ssh-add"

# ---------------------------------------------------------------------------
section "Setup: start a fresh ssh-agent and generate an ed25519 key"
# ---------------------------------------------------------------------------
AGENT_TMPDIR=$(mktemp -d "/tmp/cf-demo-ssh-agent-XXXX")
register_cleanup "$AGENT_TMPDIR"

KEY_FILE="$AGENT_TMPDIR/id_ed25519"
ssh-keygen -t ed25519 -f "$KEY_FILE" -N "" -C "campfire-demo-17" -q

# Start a fresh agent (isolated from any existing SSH_AUTH_SOCK).
eval "$(ssh-agent -s)" > /dev/null

ssh-add "$KEY_FILE" 2>/dev/null
echo "Key added to ssh-agent (PID: ${SSH_AGENT_PID})"

# Extract the SHA256 fingerprint.
FINGERPRINT=$(ssh-keygen -l -E sha256 -f "${KEY_FILE}.pub" 2>/dev/null | awk '{print $2}')
echo "Fingerprint: $FINGERPRINT"

if [ -z "$FINGERPRINT" ]; then
    echo "FAIL: could not extract fingerprint"
    exit 1
fi

# ---------------------------------------------------------------------------
section "Setup: create root and delegate identities"
# ---------------------------------------------------------------------------
ROOT_HOME=$(mktemp -d "/tmp/cf-demo-root-XXXX")
register_cleanup "$ROOT_HOME"
DELEGATE_HOME=$(new_identity delegate)
register_cleanup "$DELEGATE_HOME"

# Initialize root identity (file-based keypair; agent holds the private signing key).
cf init --cf-home "$ROOT_HOME" >/dev/null 2>&1

ROOT_PUB=$(pubkey_of "$ROOT_HOME")
DELEGATE_PUB=$(pubkey_of "$DELEGATE_HOME")

echo "Root:     $ROOT_PUB"
echo "Delegate: $DELEGATE_PUB"

# ---------------------------------------------------------------------------
section "Setup: configure root to use ssh-agent backend"
# ---------------------------------------------------------------------------
# The [identity] section activates the ssh-agent signing backend.
# In production the operator uses 'ssh-add' to load their key and sets
# backend/fingerprint in ~/.cf/config.toml.
cat > "$ROOT_HOME/config.toml" <<EOF
[identity]
backend = "ssh-agent"
fingerprint = "$FINGERPRINT"

[identity.trust]
anchors = ["$ROOT_PUB"]
EOF

cat > "$DELEGATE_HOME/config.toml" <<EOF
[identity.trust]
anchors = ["$ROOT_PUB"]
EOF

echo "Config written: backend = ssh-agent, fingerprint = $FINGERPRINT"

# Verify cf id still works with the ssh-agent backend config.
ROOT_ID_OUT=$(cf id --cf-home "$ROOT_HOME" 2>&1 || true)
assert_contains "cf id accepts ssh-agent backend config" "$ROOT_ID_OUT" "$ROOT_PUB"

# ---------------------------------------------------------------------------
section "Root creates campfire and delegate joins"
# ---------------------------------------------------------------------------
CF_ID=$(create_campfire --cf-home "$ROOT_HOME" --no-config)
echo "Campfire: $CF_ID"

cf join "$CF_ID" --cf-home "$DELEGATE_HOME" 2>/dev/null
echo "Delegate joined"

# ---------------------------------------------------------------------------
section "Root grants delegate (signing goes through ssh-agent backend)"
# ---------------------------------------------------------------------------
EXPIRES_AT=$(( $(date +%s) + 7 * 86400 ))
GRANT_PAYLOAD="{\"child_pubkey\":\"$DELEGATE_PUB\",\"campfire_id\":\"$CF_ID\",\"expires_at\":$EXPIRES_AT}"

cf send "$CF_ID" --cf-home "$ROOT_HOME" --tag identity:granted "$GRANT_PAYLOAD" >/dev/null 2>&1
echo "Root granted delegate"

# ---------------------------------------------------------------------------
section "Verify delegate is trusted"
# ---------------------------------------------------------------------------
cf read "$CF_ID" --cf-home "$DELEGATE_HOME" --all >/dev/null 2>&1 || true

RESOLVE_OUT=$(cf trust resolve "$CF_ID" "$DELEGATE_PUB" --cf-home "$DELEGATE_HOME" 2>/dev/null || true)
assert_contains "Delegate resolves after grant" "$RESOLVE_OUT" "Resolved"

RESOLVE_EXIT=0
cf trust resolve "$CF_ID" "$DELEGATE_PUB" --cf-home "$DELEGATE_HOME" >/dev/null 2>&1 || RESOLVE_EXIT=$?
assert_eq "cf trust resolve exits 0 for delegate" "0" "$RESOLVE_EXIT"

# ---------------------------------------------------------------------------
section "JSON output mode"
# ---------------------------------------------------------------------------
JSON_OUT=$(cf trust resolve "$CF_ID" "$DELEGATE_PUB" --cf-home "$DELEGATE_HOME" --json 2>/dev/null || true)
assert_contains "JSON output contains status" "$JSON_OUT" '"status"'
assert_contains "JSON output shows Resolved" "$JSON_OUT" '"Resolved"'

summary

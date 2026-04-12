package delegation_test

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention/delegation"
)

// TestPostRevoke_RoundTrip calls PostRevoke, reads back the campfire, and verifies
// the message signature and payload fields.
func TestPostRevoke_RoundTrip(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	childPub := env.pubkey(1)

	msg, err := delegation.PostRevoke(ctx, env.clients[0], env.campfireIDRaw, childPub)
	if err != nil {
		t.Fatalf("PostRevoke: %v", err)
	}
	if msg == nil {
		t.Fatal("PostRevoke returned nil message")
	}
	if msg.ID == "" {
		t.Error("posted message has empty ID")
	}
	if msg.Timestamp == 0 {
		t.Error("posted message has zero Timestamp")
	}
	if !msg.VerifySignature() {
		t.Error("posted revoke message signature does not verify")
	}
	// Sender must be the parent (revoker) identity's public key.
	parentPub := env.pubkey(0)
	if !ed25519.PublicKey(msg.Sender).Equal(parentPub) {
		t.Errorf("posted message Sender does not match parent pubkey")
	}
	// Tag must be identity:revoked.
	var hasRevokedTag bool
	for _, tag := range msg.Tags {
		if tag == delegation.RevokedTag {
			hasRevokedTag = true
		}
	}
	if !hasRevokedTag {
		t.Errorf("expected tag %q, got %v", delegation.RevokedTag, msg.Tags)
	}
	// Payload child_pubkey must match.
	childHex := hex.EncodeToString(childPub)
	if !strings.Contains(string(msg.Payload), childHex) {
		t.Errorf("payload does not contain expected child_pubkey %s: %s", childHex, msg.Payload)
	}
}

// TestPostRevoke_NilClient asserts that a nil client is rejected before any send.
func TestPostRevoke_NilClient(t *testing.T) {
	env := newTrustTestEnv(t, 1)
	ctx := context.Background()

	_, err := delegation.PostRevoke(ctx, nil, env.campfireIDRaw, env.pubkey(0))
	if err == nil {
		t.Fatal("expected error for nil client, got nil")
	}
	if !strings.Contains(err.Error(), "client is nil") {
		t.Errorf("expected 'client is nil' error, got: %v", err)
	}
}

// TestPostRevoke_InvalidChildKey asserts that a 31-byte (non-standard) key is rejected.
func TestPostRevoke_InvalidChildKey(t *testing.T) {
	env := newTrustTestEnv(t, 1)
	ctx := context.Background()

	shortKey := make(ed25519.PublicKey, 31)
	_, err := delegation.PostRevoke(ctx, env.clients[0], env.campfireIDRaw, shortKey)
	if err == nil {
		t.Fatal("expected error for 31-byte key, got nil")
	}
	if !strings.Contains(err.Error(), "child pubkey must be") {
		t.Errorf("expected size validation error, got: %v", err)
	}
}

// TestPostRevoke_EmptyCampfireID asserts that an empty campfire ID is rejected.
func TestPostRevoke_EmptyCampfireID(t *testing.T) {
	env := newTrustTestEnv(t, 1)
	ctx := context.Background()

	_, err := delegation.PostRevoke(ctx, env.clients[0], []byte{}, env.pubkey(0))
	if err == nil {
		t.Fatal("expected error for empty campfire ID, got nil")
	}
	if !strings.Contains(err.Error(), "campfire ID is empty") {
		t.Errorf("expected 'campfire ID is empty' error, got: %v", err)
	}
}

// TestPostRevoke_EndToEnd issues a grant via PostGrant, verifies Resolve returns
// Resolved, then posts a revocation via PostRevoke and verifies Resolve returns DeadEnd.
// This is the full PostGrant→Resolve(Resolved)→PostRevoke→syncAll→Resolve(DeadEnd) cycle.
func TestPostRevoke_EndToEnd(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	parentPub := env.pubkey(0)
	childPub := env.pubkey(1)
	anchors := env.anchors(0)

	// Step 1: PostGrant — child should resolve to parent anchor.
	if _, err := delegation.PostGrant(ctx, env.clients[0], env.campfireIDRaw, childPub, 24*time.Hour); err != nil {
		t.Fatalf("PostGrant: %v", err)
	}
	env.syncAll(t)

	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, childPub, anchors)
	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved after PostGrant, got %T: %+v", out, out)
	}
	if !r.Anchor.Equal(parentPub) {
		t.Errorf("expected anchor == parent")
	}

	// Step 2: PostRevoke — child should no longer resolve.
	if _, err := delegation.PostRevoke(ctx, env.clients[0], env.campfireIDRaw, childPub); err != nil {
		t.Fatalf("PostRevoke: %v", err)
	}
	env.syncAll(t)

	out2 := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, childPub, anchors)
	if _, ok := out2.(delegation.DeadEnd); !ok {
		t.Fatalf("expected DeadEnd after PostRevoke, got %T: %+v", out2, out2)
	}
}

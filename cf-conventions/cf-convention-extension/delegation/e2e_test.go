package delegation_test

// E2E lifecycle tests for the identity delegation convention.
//
// These tests exercise the full grant → resolve → revoke → re-resolve lifecycle
// end-to-end: real ed25519 keys, real filesystem campfire, real SQLite store,
// real protocol.Client, real message signing, real Resolve() calls.
//
// They complement the unit tests in trust_test.go (which isolate individual
// behaviours) by proving the entire suite works together as a cohesive system.

import (
	"context"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention-extension/delegation"
)

// TestE2E_FullLifecycle verifies the complete single-delegation lifecycle:
//
//	grant → Resolved
//	revoke → DeadEnd
//	re-grant → Resolved (re-grant works after revoke)
func TestE2E_FullLifecycle(t *testing.T) {
	// Setup: root (index 0) is the trust anchor, child (index 1) is the delegatee.
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	rootPub := env.pubkey(0)
	childPub := env.pubkey(1)
	anchors := env.anchors(0)

	// Step 1: No grant posted yet → child should be DeadEnd.
	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, childPub, anchors)
	if _, ok := out.(delegation.DeadEnd); !ok {
		t.Fatalf("step 1 (no grant): expected DeadEnd, got %T: %+v", out, out)
	}

	// Step 2: Root posts identity:granted for child.
	env.postGrant(t, 0, childPub, futureExpiry())

	// Step 3: Resolve child → expect Resolved{chain:[grant], anchor:root}.
	out = delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, childPub, anchors)
	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("step 3 (after grant): expected Resolved, got %T: %+v", out, out)
	}
	if len(r.Chain) != 1 {
		t.Errorf("step 3: expected chain length 1, got %d", len(r.Chain))
	}
	if !r.Anchor.Equal(rootPub) {
		t.Errorf("step 3: expected anchor == root")
	}
	if !r.Chain[0].ParentPubkey.Equal(rootPub) {
		t.Errorf("step 3: chain[0].ParentPubkey should be root")
	}
	if !r.Chain[0].ChildPubkey.Equal(childPub) {
		t.Errorf("step 3: chain[0].ChildPubkey should be child")
	}

	// Step 4: Root posts identity:revoked for child.
	env.postRevoke(t, 0, childPub)

	// Step 5: Resolve child → expect DeadEnd (revocation takes effect).
	out = delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, childPub, anchors)
	if _, ok := out.(delegation.DeadEnd); !ok {
		t.Fatalf("step 5 (after revoke): expected DeadEnd, got %T: %+v", out, out)
	}

	// Step 6: Root posts a NEW identity:granted for child (re-grant after revoke).
	// Small pause ensures the re-grant timestamp is strictly after the revoke.
	time.Sleep(2 * time.Millisecond)
	env.postGrant(t, 0, childPub, futureExpiry())

	// Step 7: Resolve child → expect Resolved (re-grant supersedes revoke).
	out = delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, childPub, anchors)
	r, ok = out.(delegation.Resolved)
	if !ok {
		t.Fatalf("step 7 (after re-grant): expected Resolved, got %T: %+v", out, out)
	}
	if len(r.Chain) != 1 {
		t.Errorf("step 7: expected chain length 1, got %d", len(r.Chain))
	}
	if !r.Anchor.Equal(rootPub) {
		t.Errorf("step 7: expected anchor == root after re-grant")
	}
}

// TestE2E_SubtreeCascade verifies that revoking a mid-chain node causes all
// downstream nodes to become DeadEnd — the revocation cascades up the trust tree.
//
// Chain: root (index 0) → mid (index 1) → leaf (index 2).
func TestE2E_SubtreeCascade(t *testing.T) {
	// Setup: 3 identities — root, mid, leaf.
	env := newTrustTestEnv(t, 3)
	ctx := context.Background()

	rootPub := env.pubkey(0)
	midPub := env.pubkey(1)
	leafPub := env.pubkey(2)
	anchors := env.anchors(0) // only root is a trust anchor

	// Step 1: Build the 3-hop chain.
	//   root grants mid, mid grants leaf.
	env.postGrant(t, 0, midPub, futureExpiry())  // root → mid
	env.postGrant(t, 1, leafPub, futureExpiry()) // mid → leaf

	// Step 2: Verify mid resolves to root (chain length 1).
	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, midPub, anchors)
	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("step 2 (mid): expected Resolved, got %T: %+v", out, out)
	}
	if len(r.Chain) != 1 {
		t.Errorf("step 2: mid chain length: want 1, got %d", len(r.Chain))
	}
	if !r.Anchor.Equal(rootPub) {
		t.Errorf("step 2: expected anchor == root for mid")
	}

	// Step 3: Verify leaf resolves via the 2-hop chain: leaf→mid→root.
	out = delegation.Resolve(ctx, env.clients[2], env.campfireIDRaw, leafPub, anchors)
	r, ok = out.(delegation.Resolved)
	if !ok {
		t.Fatalf("step 3 (leaf before revoke): expected Resolved, got %T: %+v", out, out)
	}
	if len(r.Chain) != 2 {
		t.Errorf("step 3: leaf chain length: want 2, got %d", len(r.Chain))
	}
	if !r.Anchor.Equal(rootPub) {
		t.Errorf("step 3: expected anchor == root for leaf")
	}
	// Chain[0] is the leaf-level grant (mid → leaf); Chain[1] is the root-level grant (root → mid).
	if !r.Chain[0].ParentPubkey.Equal(midPub) {
		t.Errorf("step 3: chain[0].ParentPubkey should be mid")
	}
	if !r.Chain[1].ParentPubkey.Equal(rootPub) {
		t.Errorf("step 3: chain[1].ParentPubkey should be root")
	}

	// Step 4: Root revokes mid (revoke the root→mid grant).
	env.postRevoke(t, 0, midPub)

	// Step 5: Resolve mid → DeadEnd (its grant from root is revoked).
	out = delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, midPub, anchors)
	if _, ok := out.(delegation.DeadEnd); !ok {
		t.Fatalf("step 5 (mid after revoke): expected DeadEnd, got %T: %+v", out, out)
	}

	// Step 6: Resolve leaf → DeadEnd (walk fails at mid hop because mid→root is revoked).
	// The leaf→mid grant is still valid, but mid cannot resolve to root.
	out = delegation.Resolve(ctx, env.clients[2], env.campfireIDRaw, leafPub, anchors)
	if _, ok := out.(delegation.DeadEnd); !ok {
		t.Fatalf("step 6 (leaf after mid revoke): expected DeadEnd, got %T: %+v", out, out)
	}
}

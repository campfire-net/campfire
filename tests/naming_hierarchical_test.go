// Package tests — E2E tests for hierarchical naming resolution.
//
// These tests validate the full hierarchical naming flow end-to-end on real
// filesystem transport. No mocks — real protocol.Client, real beacon discovery,
// real auto-join.
//
// Test cases:
//   - TestHierarchicalResolve: root→child→leaf resolution with auto-join
//   - TestHierarchicalResolveInviteOnly: invite-only campfire returns clear error
//   - TestHierarchicalResolveTwoLevels: three-level deep walk with auto-join at each level
package tests

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// TestHierarchicalResolve validates a full hierarchical walk:
// root→child→leaf. Client B starts with only root membership and resolves
// "child.leaf" — auto-join kicks in for the child campfire.
func TestHierarchicalResolve(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	ctx := context.Background()

	// Client A (sysop): creates all campfires and registers names.
	clientA, err := protocol.Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	rootResult, err := clientA.Create(protocol.CreateRequest{
		Description: "root namespace",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	rootID := rootResult.CampfireID

	childResult, err := clientA.Create(protocol.CreateRequest{
		Description: "child namespace",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}
	childID := childResult.CampfireID

	leafResult, err := clientA.Create(protocol.CreateRequest{
		Description: "leaf campfire",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create leaf: %v", err)
	}
	leafID := leafResult.CampfireID

	// Register "child" in root → childID.
	if _, err := naming.Register(ctx, clientA, rootID, "child", childID, nil); err != nil {
		t.Fatalf("Register child in root: %v", err)
	}

	// Register "leaf" in child → leafID.
	if _, err := naming.Register(ctx, clientA, childID, "leaf", leafID, nil); err != nil {
		t.Fatalf("Register leaf in child: %v", err)
	}

	// Client B (resolver): joins root only.
	clientB, err := protocol.Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init B: %v", err)
	}
	t.Cleanup(func() { clientB.Close() })

	rootCampfireDir := filepath.Join(transportDir, rootID)
	if _, err := clientB.Join(protocol.JoinRequest{
		CampfireID: rootID,
		Transport:  &protocol.FilesystemTransport{Dir: rootCampfireDir},
	}); err != nil {
		t.Fatalf("B.Join root: %v", err)
	}

	// Verify B is NOT a member of child yet.
	m, err := clientB.GetMembership(childID)
	if err != nil {
		t.Fatalf("GetMembership child: %v", err)
	}
	if m != nil {
		t.Fatal("B should not be a member of child before resolution")
	}

	// B resolves "child.leaf" via hierarchical walk.
	resolver := naming.NewResolverFromClient(clientB, rootID, naming.ResolverClientOptions{
		BeaconDir: beaconDir,
	})
	result, err := resolver.ResolveURI(ctx, "cf://child.leaf")
	if err != nil {
		t.Fatalf("ResolveURI cf://child.leaf: %v", err)
	}

	// Verify resolved to leaf campfire.
	if result.CampfireID != leafID {
		t.Errorf("resolved CampfireID = %s, want %s", result.CampfireID, leafID)
	}

	// Verify B auto-joined child campfire.
	m, err = clientB.GetMembership(childID)
	if err != nil {
		t.Fatalf("GetMembership child after resolve: %v", err)
	}
	if m == nil {
		t.Error("B should be a member of child after auto-join during resolution")
	}
}

// TestHierarchicalResolveInviteOnly verifies that resolving through an
// invite-only campfire returns a clear error containing "invite-only".
func TestHierarchicalResolveInviteOnly(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	ctx := context.Background()

	// Client A (sysop): creates root (open) and private (invite-only).
	clientA, err := protocol.Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	rootResult, err := clientA.Create(protocol.CreateRequest{
		Description: "root namespace",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	rootID := rootResult.CampfireID

	privateResult, err := clientA.Create(protocol.CreateRequest{
		Description:  "private namespace",
		JoinProtocol: "invite-only",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:    beaconDir,
	})
	if err != nil {
		t.Fatalf("Create private: %v", err)
	}
	privateID := privateResult.CampfireID

	// Register "private" in root → privateID.
	if _, err := naming.Register(ctx, clientA, rootID, "private", privateID, nil); err != nil {
		t.Fatalf("Register private: %v", err)
	}

	// Sysop registers "secret" in private campfire (sysop is already a member).
	secretResult, err := clientA.Create(protocol.CreateRequest{
		Description: "secret leaf",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create secret leaf: %v", err)
	}
	if _, err := naming.Register(ctx, clientA, privateID, "secret", secretResult.CampfireID, nil); err != nil {
		t.Fatalf("Register secret: %v", err)
	}

	// Client B: joins root only.
	clientB, err := protocol.Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init B: %v", err)
	}
	t.Cleanup(func() { clientB.Close() })

	rootCampfireDir := filepath.Join(transportDir, rootID)
	if _, err := clientB.Join(protocol.JoinRequest{
		CampfireID: rootID,
		Transport:  &protocol.FilesystemTransport{Dir: rootCampfireDir},
	}); err != nil {
		t.Fatalf("B.Join root: %v", err)
	}

	// B resolves "private.secret" — should fail with invite-only error.
	resolver := naming.NewResolverFromClient(clientB, rootID, naming.ResolverClientOptions{
		BeaconDir: beaconDir,
	})
	_, err = resolver.ResolveURI(ctx, "cf://private.secret")
	if err == nil {
		t.Fatal("expected error resolving through invite-only campfire, got nil")
	}

	if !errors.Is(err, naming.ErrInviteOnly) && !strings.Contains(err.Error(), "invite-only") {
		t.Errorf("expected invite-only error, got: %v", err)
	}
}

// TestHierarchicalResolveTwoLevels validates a three-level deep walk:
// root → level1 → level2 → target. A fresh client that only joined root
// resolves "level1.level2.target" and auto-joins level1 and level2.
func TestHierarchicalResolveTwoLevels(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	ctx := context.Background()

	// Client A (sysop): creates all campfires.
	clientA, err := protocol.Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	rootResult, err := clientA.Create(protocol.CreateRequest{
		Description: "root namespace",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	rootID := rootResult.CampfireID

	level1Result, err := clientA.Create(protocol.CreateRequest{
		Description: "level1 namespace",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create level1: %v", err)
	}
	level1ID := level1Result.CampfireID

	level2Result, err := clientA.Create(protocol.CreateRequest{
		Description: "level2 namespace",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create level2: %v", err)
	}
	level2ID := level2Result.CampfireID

	targetResult, err := clientA.Create(protocol.CreateRequest{
		Description: "target campfire",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}
	targetID := targetResult.CampfireID

	// Wire the name chain: root/level1 → level1/level2 → level2/target.
	if _, err := naming.Register(ctx, clientA, rootID, "level1", level1ID, nil); err != nil {
		t.Fatalf("Register level1: %v", err)
	}
	if _, err := naming.Register(ctx, clientA, level1ID, "level2", level2ID, nil); err != nil {
		t.Fatalf("Register level2: %v", err)
	}
	if _, err := naming.Register(ctx, clientA, level2ID, "target", targetID, nil); err != nil {
		t.Fatalf("Register target: %v", err)
	}

	// Client B: joins root only.
	clientB, err := protocol.Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init B: %v", err)
	}
	t.Cleanup(func() { clientB.Close() })

	rootCampfireDir := filepath.Join(transportDir, rootID)
	if _, err := clientB.Join(protocol.JoinRequest{
		CampfireID: rootID,
		Transport:  &protocol.FilesystemTransport{Dir: rootCampfireDir},
	}); err != nil {
		t.Fatalf("B.Join root: %v", err)
	}

	// Verify B has no membership in level1 or level2.
	for _, id := range []string{level1ID, level2ID} {
		m, err := clientB.GetMembership(id)
		if err != nil {
			t.Fatalf("GetMembership %s: %v", id[:12], err)
		}
		if m != nil {
			t.Fatalf("B should not be a member of %s before resolution", id[:12])
		}
	}

	// B resolves "level1.level2.target" — three segments, two auto-joins.
	resolver := naming.NewResolverFromClient(clientB, rootID, naming.ResolverClientOptions{
		BeaconDir: beaconDir,
	})
	result, err := resolver.ResolveURI(ctx, "cf://level1.level2.target")
	if err != nil {
		t.Fatalf("ResolveURI cf://level1.level2.target: %v", err)
	}

	// Verify resolved to target campfire.
	if result.CampfireID != targetID {
		t.Errorf("resolved CampfireID = %s, want %s", result.CampfireID, targetID)
	}

	// Verify B auto-joined level1 and level2.
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"level1", level1ID},
		{"level2", level2ID},
	} {
		m, err := clientB.GetMembership(tc.id)
		if err != nil {
			t.Fatalf("GetMembership %s after resolve: %v", tc.name, err)
		}
		if m == nil {
			t.Errorf("B should be a member of %s after auto-join during resolution", tc.name)
		}
	}
}

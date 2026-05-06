package naming_test

// resolve_chain_multi_level_test.go
//
// Integration tests demonstrating ResolveChain multi-level chain walks using
// real protocol.Client instances with filesystem transport.
//
// Chain topologies under test:
//   3-hop: root → org → team → service        (cf://org.team.service)
//   4-hop: root → org → team → squad → service (cf://org.team.squad.service)
//
// Each test asserts:
//   - The correct campfire ID is returned at each segment
//   - Auto-join fires for intermediate campfires the resolver hasn't joined
//   - The final leaf ID matches the registered target
//
// Part of campfireagent-750 (DEMO GAP: ResolveChain multi-level chain walks).

import (
	"context"
	"path/filepath"
	"testing"

	naming "github.com/campfire-net/campfire/cf-conventions/cf-discovery/naming"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// TestResolveChain_MultiLevel_Tracked is the canonical 3-hop resolution test.
//
// Topology: root → org → team → service (3 name hops: cf://org.team.service)
//
// clientB joins only root. It must auto-join org and team during resolution.
//
// Assertions:
//  1. cf://org               resolves to orgID   (hop 1)
//  2. cf://org.team          resolves to teamID  (hop 2)
//  3. cf://org.team.service  resolves to serviceID (hop 3 — the multi-level case)
//  4. clientB auto-joined org and team (was not a member before resolution)
func TestResolveChain_MultiLevel_Tracked(t *testing.T) {
	ctx := context.Background()
	sharedBeaconDir := t.TempDir()

	// Client A: creator of all campfires.
	configDirA := t.TempDir()
	clientA, _, err := protocol.Init(configDirA)
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	// Create campfires with explicit transport base dirs so clientB can
	// compute the campfire subdirectory for its explicit root join.
	rootTransportBase := t.TempDir()
	orgTransportBase := t.TempDir()
	teamTransportBase := t.TempDir()
	serviceTransportBase := t.TempDir()

	rootResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: rootTransportBase},
		BeaconDir: sharedBeaconDir,
	})
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	rootID := rootResult.CampfireID
	rootCampfireDir := filepath.Join(rootTransportBase, rootID)

	orgResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: orgTransportBase},
		BeaconDir: sharedBeaconDir,
	})
	if err != nil {
		t.Fatalf("Create org: %v", err)
	}
	orgID := orgResult.CampfireID

	teamResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: teamTransportBase},
		BeaconDir: sharedBeaconDir,
	})
	if err != nil {
		t.Fatalf("Create team: %v", err)
	}
	teamID := teamResult.CampfireID

	serviceResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: serviceTransportBase},
		BeaconDir: sharedBeaconDir,
	})
	if err != nil {
		t.Fatalf("Create service: %v", err)
	}
	serviceID := serviceResult.CampfireID

	t.Logf("Campfire IDs: root=%s org=%s team=%s service=%s",
		rootID[:12], orgID[:12], teamID[:12], serviceID[:12])

	// Register the 3-level hierarchy.
	// root:  "org"     → orgID
	// org:   "team"    → teamID
	// team:  "service" → serviceID
	if _, err := naming.Register(ctx, clientA, rootID, "org", orgID, nil); err != nil {
		t.Fatalf("Register root/org: %v", err)
	}
	if _, err := naming.Register(ctx, clientA, orgID, "team", teamID, nil); err != nil {
		t.Fatalf("Register org/team: %v", err)
	}
	if _, err := naming.Register(ctx, clientA, teamID, "service", serviceID, nil); err != nil {
		t.Fatalf("Register team/service: %v", err)
	}

	// Client B: knows only root. Will auto-join org and team via beacon scan.
	configDirB := t.TempDir()
	clientB, _, err := protocol.Init(configDirB)
	if err != nil {
		t.Fatalf("Init B: %v", err)
	}
	t.Cleanup(func() { clientB.Close() })

	// B joins root explicitly (it knows the root transport directory).
	if _, err := clientB.Join(protocol.JoinRequest{
		CampfireID: rootID,
		Transport:  &protocol.FilesystemTransport{Dir: rootCampfireDir},
	}); err != nil {
		t.Fatalf("B.Join root: %v", err)
	}

	// Verify B is NOT yet a member of org or team.
	for label, id := range map[string]string{"org": orgID, "team": teamID} {
		m, err := clientB.GetMembership(id)
		if err != nil {
			t.Fatalf("GetMembership %s: %v", label, err)
		}
		if m != nil {
			t.Fatalf("B should not be a member of %s yet", label)
		}
	}

	// Create resolver: B uses root as the starting point, with beacon-based auto-join.
	resolver := naming.NewResolverFromClient(clientB, rootID, naming.ResolverClientOptions{
		BeaconDir: sharedBeaconDir,
	})

	// ── Assertion 1: 1-hop resolution (cf://org → orgID) ─────────────────────
	result1, err := resolver.ResolveURI(ctx, "cf://org")
	if err != nil {
		t.Fatalf("Hop 1 (cf://org): %v", err)
	}
	if result1.CampfireID != orgID {
		t.Errorf("Hop 1: got %s, want %s", result1.CampfireID[:12], orgID[:12])
	}
	t.Logf("PASS  Hop 1: cf://org → %s", orgID[:12])

	// ── Assertion 2: 2-hop resolution (cf://org.team → teamID) ───────────────
	// Clear TOFU pins from the 1-hop to allow re-resolution under the same resolver instance.
	resolver.ClearTOFUPin("org")
	result2, err := resolver.ResolveURI(ctx, "cf://org.team")
	if err != nil {
		t.Fatalf("Hop 2 (cf://org.team): %v", err)
	}
	if result2.CampfireID != teamID {
		t.Errorf("Hop 2: got %s, want %s", result2.CampfireID[:12], teamID[:12])
	}
	t.Logf("PASS  Hop 2: cf://org.team → %s", teamID[:12])

	// ── Assertion 3: 3-hop resolution (cf://org.team.service → serviceID) ────
	// This is the multi-level chain walk: three distinct campfire hops.
	resolver.ClearTOFUPin("org")
	resolver.ClearTOFUPin("org.team")
	result3, err := resolver.ResolveURI(ctx, "cf://org.team.service")
	if err != nil {
		t.Fatalf("Hop 3 (cf://org.team.service): %v", err)
	}
	if result3.CampfireID != serviceID {
		t.Errorf("Hop 3: got %s, want %s", result3.CampfireID[:12], serviceID[:12])
	}
	t.Logf("PASS  Hop 3: cf://org.team.service → %s", serviceID[:12])

	// ── Assertion 4: B auto-joined org and team ───────────────────────────────
	for label, id := range map[string]string{"org": orgID, "team": teamID} {
		m, err := clientB.GetMembership(id)
		if err != nil {
			t.Fatalf("GetMembership %s after resolve: %v", label, err)
		}
		if m == nil {
			t.Errorf("B should be a member of %s after 3-hop resolve", label)
		} else {
			t.Logf("PASS  Auto-join: B is now a member of %s (%s)", label, id[:12])
		}
	}
}

// TestResolveChain_FourLevel tests 4-hop resolution to confirm the chain
// generalises beyond 3 hops: root → org → team → squad → service.
//
// Topology: cf://org.team.squad.service (4 segments, 4 hops)
//
// Assertions:
//   - cf://org.team.squad.service resolves to the leaf campfire ID
//   - All intermediate campfires (org, team, squad) are auto-joined
func TestResolveChain_FourLevel(t *testing.T) {
	ctx := context.Background()
	sharedBeaconDir := t.TempDir()

	configDirA := t.TempDir()
	clientA, _, err := protocol.Init(configDirA)
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	type campfireInfo struct {
		id          string
		campfireDir string
	}

	newCampfire := func(label string) campfireInfo {
		t.Helper()
		base := t.TempDir()
		result, err := clientA.Create(protocol.CreateRequest{
			Transport: protocol.FilesystemTransport{Dir: base},
			BeaconDir: sharedBeaconDir,
		})
		if err != nil {
			t.Fatalf("Create %s: %v", label, err)
		}
		return campfireInfo{
			id:          result.CampfireID,
			campfireDir: filepath.Join(base, result.CampfireID),
		}
	}

	root    := newCampfire("root")
	org     := newCampfire("org")
	team    := newCampfire("team")
	squad   := newCampfire("squad")
	service := newCampfire("service")

	// Register 4-level hierarchy.
	registrations := []struct{ parent, name, target string }{
		{root.id, "org", org.id},
		{org.id, "team", team.id},
		{team.id, "squad", squad.id},
		{squad.id, "service", service.id},
	}
	for _, r := range registrations {
		if _, err := naming.Register(ctx, clientA, r.parent, r.name, r.target, nil); err != nil {
			t.Fatalf("Register %s/%s: %v", r.parent[:12], r.name, err)
		}
	}

	t.Logf("4-level topology: root=%s → org=%s → team=%s → squad=%s → service=%s",
		root.id[:12], org.id[:12], team.id[:12], squad.id[:12], service.id[:12])

	// Client B: joins only root.
	configDirB := t.TempDir()
	clientB, _, err := protocol.Init(configDirB)
	if err != nil {
		t.Fatalf("Init B: %v", err)
	}
	t.Cleanup(func() { clientB.Close() })

	if _, err := clientB.Join(protocol.JoinRequest{
		CampfireID: root.id,
		Transport:  &protocol.FilesystemTransport{Dir: root.campfireDir},
	}); err != nil {
		t.Fatalf("B.Join root: %v", err)
	}

	resolver := naming.NewResolverFromClient(clientB, root.id, naming.ResolverClientOptions{
		BeaconDir: sharedBeaconDir,
	})

	// Resolve the 4-segment URI.
	result, err := resolver.ResolveURI(ctx, "cf://org.team.squad.service")
	if err != nil {
		t.Fatalf("4-level resolve cf://org.team.squad.service: %v", err)
	}
	if result.CampfireID != service.id {
		t.Errorf("4-level: got %s, want %s", result.CampfireID[:12], service.id[:12])
	}
	t.Logf("PASS  4-level resolve: cf://org.team.squad.service → %s", service.id[:12])

	// Verify all intermediate campfires were auto-joined.
	for label, id := range map[string]string{"org": org.id, "team": team.id, "squad": squad.id} {
		m, err := clientB.GetMembership(id)
		if err != nil {
			t.Fatalf("GetMembership %s: %v", label, err)
		}
		if m == nil {
			t.Errorf("B should be a member of %s after 4-hop resolve", label)
		} else {
			t.Logf("PASS  Auto-join %s (%s)", label, id[:12])
		}
	}
}

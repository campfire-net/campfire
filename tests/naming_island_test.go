// Package tests — E2E tests for island naming (single-namespace, no hierarchy).
//
// These tests validate the complete island naming lifecycle on a real filesystem
// transport. No mocks — real protocol.Client, real messages, real resolution.
//
// Test cases:
//   - TestIslandNamingRoundTrip: Init → Create → Register → Resolve → List with field validation
//   - TestIslandNamingSupersession: re-registering a name points it to a new campfire
//   - TestIslandNamingUnregister: unregistering makes a name unresolvable
//   - TestIslandNamingMultipleNames: register 5, unregister 2, verify 3 remain
package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// TestIslandNamingRoundTrip validates the full single-namespace lifecycle:
// create a namespace campfire, register names, resolve them, list them, and
// verify all Registration fields are populated correctly.
func TestIslandNamingRoundTrip(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	ctx := context.Background()

	client, err := protocol.Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	// Create namespace campfire.
	nsResult, err := client.Create(protocol.CreateRequest{
		Description: "island namespace",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create namespace: %v", err)
	}
	nsID := nsResult.CampfireID

	// Create two target campfires.
	searchResult, err := client.Create(protocol.CreateRequest{
		Description: "search service",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create search: %v", err)
	}
	searchCampfireID := searchResult.CampfireID

	feedResult, err := client.Create(protocol.CreateRequest{
		Description: "feed service",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create feed: %v", err)
	}
	feedCampfireID := feedResult.CampfireID

	// Register "search".
	if _, err := naming.Register(ctx, client, nsID, "search", searchCampfireID, nil); err != nil {
		t.Fatalf("Register search: %v", err)
	}

	// Resolve "search" → verify correct campfire ID.
	resolved, err := naming.Resolve(ctx, client, nsID, "search")
	if err != nil {
		t.Fatalf("Resolve search: %v", err)
	}
	if resolved.CampfireID != searchCampfireID {
		t.Errorf("Resolve search: got %s, want %s", resolved.CampfireID, searchCampfireID)
	}

	// Register "feed".
	if _, err := naming.Register(ctx, client, nsID, "feed", feedCampfireID, nil); err != nil {
		t.Fatalf("Register feed: %v", err)
	}

	// List → verify both names present.
	regs, err := naming.List(ctx, client, nsID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(regs) != 2 {
		t.Fatalf("List: got %d registrations, want 2", len(regs))
	}

	// Build lookup map for field validation.
	regMap := make(map[string]naming.Registration)
	for _, r := range regs {
		regMap[r.Name] = r
	}

	for _, name := range []string{"search", "feed"} {
		r, ok := regMap[name]
		if !ok {
			t.Errorf("List: missing registration for %q", name)
			continue
		}
		if r.CampfireID == "" {
			t.Errorf("Registration %q: CampfireID is empty", name)
		}
		if r.MessageID == "" {
			t.Errorf("Registration %q: MessageID is empty", name)
		}
		if r.Timestamp <= 0 {
			t.Errorf("Registration %q: Timestamp = %d, want > 0", name, r.Timestamp)
		}
	}

	// Verify specific campfire IDs.
	if regMap["search"].CampfireID != searchCampfireID {
		t.Errorf("search CampfireID = %s, want %s", regMap["search"].CampfireID, searchCampfireID)
	}
	if regMap["feed"].CampfireID != feedCampfireID {
		t.Errorf("feed CampfireID = %s, want %s", regMap["feed"].CampfireID, feedCampfireID)
	}
}

// TestIslandNamingSupersession verifies that re-registering a name updates
// the resolution target. The latest registration wins.
func TestIslandNamingSupersession(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	ctx := context.Background()

	client, err := protocol.Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	nsResult, err := client.Create(protocol.CreateRequest{
		Description: "supersession namespace",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create namespace: %v", err)
	}
	nsID := nsResult.CampfireID

	// Create two target campfires (A and B).
	campfireA, err := client.Create(protocol.CreateRequest{
		Description: "campfire A",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}

	campfireB, err := client.Create(protocol.CreateRequest{
		Description: "campfire B",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}

	// Register "search" → A.
	if _, err := naming.Register(ctx, client, nsID, "search", campfireA.CampfireID, nil); err != nil {
		t.Fatalf("Register search→A: %v", err)
	}

	// Resolve → should be A.
	resolved, err := naming.Resolve(ctx, client, nsID, "search")
	if err != nil {
		t.Fatalf("Resolve after first register: %v", err)
	}
	if resolved.CampfireID != campfireA.CampfireID {
		t.Errorf("first resolve: got %s, want %s", resolved.CampfireID, campfireA.CampfireID)
	}

	// Register "search" → B (supersession).
	if _, err := naming.Register(ctx, client, nsID, "search", campfireB.CampfireID, nil); err != nil {
		t.Fatalf("Register search→B: %v", err)
	}

	// Resolve → should be B now.
	resolved, err = naming.Resolve(ctx, client, nsID, "search")
	if err != nil {
		t.Fatalf("Resolve after supersession: %v", err)
	}
	if resolved.CampfireID != campfireB.CampfireID {
		t.Errorf("superseded resolve: got %s, want %s", resolved.CampfireID, campfireB.CampfireID)
	}

	// List → should have exactly one "search" entry pointing to B.
	regs, err := naming.List(ctx, client, nsID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(regs) != 1 {
		t.Fatalf("List: got %d registrations, want 1", len(regs))
	}
	if regs[0].Name != "search" {
		t.Errorf("List[0].Name = %q, want %q", regs[0].Name, "search")
	}
	if regs[0].CampfireID != campfireB.CampfireID {
		t.Errorf("List[0].CampfireID = %s, want %s", regs[0].CampfireID, campfireB.CampfireID)
	}
}

// TestIslandNamingUnregister verifies that unregistering a name makes it
// unresolvable and removes it from list results.
func TestIslandNamingUnregister(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	ctx := context.Background()

	client, err := protocol.Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	nsResult, err := client.Create(protocol.CreateRequest{
		Description: "unregister namespace",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create namespace: %v", err)
	}
	nsID := nsResult.CampfireID

	targetResult, err := client.Create(protocol.CreateRequest{
		Description: "target campfire",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	// Register "search".
	if _, err := naming.Register(ctx, client, nsID, "search", targetResult.CampfireID, nil); err != nil {
		t.Fatalf("Register search: %v", err)
	}

	// Resolve → success.
	resolved, err := naming.Resolve(ctx, client, nsID, "search")
	if err != nil {
		t.Fatalf("Resolve before unregister: %v", err)
	}
	if resolved.CampfireID != targetResult.CampfireID {
		t.Errorf("Resolve: got %s, want %s", resolved.CampfireID, targetResult.CampfireID)
	}

	// Unregister "search".
	if err := naming.Unregister(ctx, client, nsID, "search"); err != nil {
		t.Fatalf("Unregister search: %v", err)
	}

	// Resolve → should fail with "not found".
	_, err = naming.Resolve(ctx, client, nsID, "search")
	if err == nil {
		t.Fatal("expected error resolving unregistered name, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}

	// List → should be empty.
	regs, err := naming.List(ctx, client, nsID)
	if err != nil {
		t.Fatalf("List after unregister: %v", err)
	}
	if len(regs) != 0 {
		t.Errorf("List after unregister: got %d registrations, want 0", len(regs))
	}
}

// TestIslandNamingMultipleNames registers 5 names, unregisters 2, and verifies
// only 3 remain in the listing.
func TestIslandNamingMultipleNames(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	ctx := context.Background()

	client, err := protocol.Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	nsResult, err := client.Create(protocol.CreateRequest{
		Description: "multi-name namespace",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("Create namespace: %v", err)
	}
	nsID := nsResult.CampfireID

	// Create 5 target campfires and register names.
	names := []string{"search", "feed", "recommend", "notify", "auth"}
	campfireIDs := make(map[string]string)

	for _, name := range names {
		result, err := client.Create(protocol.CreateRequest{
			Description: name + " service",
			Transport:   protocol.FilesystemTransport{Dir: transportDir},
			BeaconDir:   beaconDir,
		})
		if err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		campfireIDs[name] = result.CampfireID

		if _, err := naming.Register(ctx, client, nsID, name, result.CampfireID, nil); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}

	// List → verify all 5 present.
	regs, err := naming.List(ctx, client, nsID)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(regs) != 5 {
		t.Fatalf("List all: got %d registrations, want 5", len(regs))
	}

	// Verify each name resolves to the correct campfire.
	for _, name := range names {
		resolved, err := naming.Resolve(ctx, client, nsID, name)
		if err != nil {
			t.Errorf("Resolve %s: %v", name, err)
			continue
		}
		if resolved.CampfireID != campfireIDs[name] {
			t.Errorf("Resolve %s: got %s, want %s", name, resolved.CampfireID, campfireIDs[name])
		}
	}

	// Unregister "feed" and "notify".
	for _, name := range []string{"feed", "notify"} {
		if err := naming.Unregister(ctx, client, nsID, name); err != nil {
			t.Fatalf("Unregister %s: %v", name, err)
		}
	}

	// List → verify 3 remaining.
	regs, err = naming.List(ctx, client, nsID)
	if err != nil {
		t.Fatalf("List after unregister: %v", err)
	}
	if len(regs) != 3 {
		t.Fatalf("List after unregister: got %d registrations, want 3", len(regs))
	}

	// Build set of remaining names.
	remaining := make(map[string]bool)
	for _, r := range regs {
		remaining[r.Name] = true
	}

	// Verify the right names survived.
	for _, name := range []string{"search", "recommend", "auth"} {
		if !remaining[name] {
			t.Errorf("expected %q in remaining registrations", name)
		}
	}
	for _, name := range []string{"feed", "notify"} {
		if remaining[name] {
			t.Errorf("unexpected %q in remaining registrations (should be unregistered)", name)
		}
	}

	// Verify unregistered names fail to resolve.
	for _, name := range []string{"feed", "notify"} {
		_, err := naming.Resolve(ctx, client, nsID, name)
		if err == nil {
			t.Errorf("expected error resolving unregistered %q, got nil", name)
		}
	}
}

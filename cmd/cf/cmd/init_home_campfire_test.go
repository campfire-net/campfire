package cmd

// Tests for campfire-agent-lm9: createSelfCampfire coverage gaps —
// beacon publish, membership role/description, message mirroring.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
)

// TestCreateSelfCampfire_BeaconFileExistsAndTagged verifies that a beacon file is
// actually written to the beacon directory and carries the identity:v1 description.
// Uses CF_BEACON_DIR to redirect beacon writes to a temp dir so the test is hermetic.
func TestCreateSelfCampfire_BeaconFileExistsAndTagged(t *testing.T) {
	cfHomeDir := t.TempDir()
	beaconDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_BEACON_DIR", beaconDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	selfCampfireID, _, err := createSelfCampfire(cfHomeDir, agentID, false)
	if err != nil {
		t.Fatalf("createSelfCampfire: %v", err)
	}

	// The beacon file must exist at <beaconDir>/<campfireID>.beacon
	beaconFile := filepath.Join(beaconDir, selfCampfireID+".beacon")
	if _, statErr := os.Stat(beaconFile); statErr != nil {
		t.Fatalf("beacon file not found at %s: %v", beaconFile, statErr)
	}

	// Scan the dir and find our beacon.
	beacons, scanErr := beacon.Scan(beaconDir)
	if scanErr != nil {
		t.Fatalf("scanning beacon dir: %v", scanErr)
	}
	if len(beacons) == 0 {
		t.Fatal("no beacons found after createSelfCampfire")
	}

	var found *beacon.Beacon
	for i := range beacons {
		b := &beacons[i]
		if fmt.Sprintf("%x", b.CampfireID) == selfCampfireID {
			found = b
			break
		}
	}
	if found == nil {
		t.Fatalf("no beacon matching campfire ID %s in beacon dir", selfCampfireID)
	}

	// Description must be identity:v1 (= convention.IdentityBeaconTag).
	if found.Description != convention.IdentityBeaconTag {
		t.Errorf("beacon description = %q, want %q", found.Description, convention.IdentityBeaconTag)
	}

	// Beacon signature must be valid.
	if !found.Verify() {
		t.Error("identity:v1 beacon has an invalid signature")
	}

	// Identity campfires are invite-only — beacon must reflect that.
	if found.JoinProtocol != "invite-only" {
		t.Errorf("beacon join_protocol = %q, want invite-only", found.JoinProtocol)
	}
}

// TestCreateSelfCampfire_MembershipRole verifies that the membership written to the
// store has role=creator, description="identity campfire", join_protocol=invite-only,
// and transport_type=filesystem.
func TestCreateSelfCampfire_MembershipRole(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	selfCampfireID, _, err := createSelfCampfire(cfHomeDir, agentID, false)
	if err != nil {
		t.Fatalf("createSelfCampfire: %v", err)
	}

	s, err := store.Open(store.StorePath(cfHomeDir))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	m, err := s.GetMembership(selfCampfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("membership not found for self-campfire")
	}

	if m.Role != store.PeerRoleCreator {
		t.Errorf("membership role = %q, want %q", m.Role, store.PeerRoleCreator)
	}
	if m.Description != "identity campfire" {
		t.Errorf("membership description = %q, want %q", m.Description, "identity campfire")
	}
	if m.JoinProtocol != "invite-only" {
		t.Errorf("membership join_protocol = %q, want invite-only", m.JoinProtocol)
	}
	if m.TransportType != "filesystem" {
		t.Errorf("membership transport_type = %q, want filesystem", m.TransportType)
	}
}

// TestCreateSelfCampfire_MessagesInStore verifies that genesis messages (identity
// convention declarations + introduce-me) are mirrored into the local store so
// offline readback works without hitting transport.
func TestCreateSelfCampfire_MessagesInStore(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	selfCampfireID, _, err := createSelfCampfire(cfHomeDir, agentID, false)
	if err != nil {
		t.Fatalf("createSelfCampfire: %v", err)
	}

	s, err := store.Open(store.StorePath(cfHomeDir))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	msgs, err := s.ListMessages(selfCampfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	// Expect at least: len(IdentityDeclarations()) genesis messages + 1 introduce-me.
	minExpected := len(convention.IdentityDeclarations()) + 1
	if len(msgs) < minExpected {
		t.Errorf("store has %d messages for self-campfire, want at least %d (declarations + introduce-me)",
			len(msgs), minExpected)
	}
}

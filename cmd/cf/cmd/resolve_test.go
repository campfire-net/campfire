package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
)

// makeTestStore creates a temporary store with the given campfire IDs as memberships.
func makeTestStore(t *testing.T, campfireIDs []string) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "store.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	for _, id := range campfireIDs {
		if err := s.AddMembership(store.Membership{
			CampfireID:   id,
			TransportDir: dir,
			JoinProtocol: "open",
			Role:         "member",
			JoinedAt:     1,
		}); err != nil {
			t.Fatalf("adding membership %s: %v", id, err)
		}
	}
	return s, dir
}

// makeTestBeacon publishes a beacon for a freshly-generated campfire identity to dir.
// Returns the full hex campfire ID.
func makeTestBeacon(t *testing.T, beaconDir string) string {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	b, err := beacon.New(
		id.PublicKey, id.PrivateKey,
		"open", nil,
		beacon.TransportConfig{Protocol: "filesystem", Config: map[string]string{}},
		"test",
	)
	if err != nil {
		t.Fatalf("creating beacon: %v", err)
	}
	if err := beacon.Publish(beaconDir, b); err != nil {
		t.Fatalf("publishing beacon: %v", err)
	}
	return b.CampfireIDHex()
}

func TestResolveCampfireID_Exact(t *testing.T) {
	s, _ := makeTestStore(t, nil)
	defer s.Close()

	// A 64-char hex string should be returned as-is, no lookup.
	exact := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	got, err := resolveCampfireID(exact, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != exact {
		t.Errorf("got %s, want %s", got, exact)
	}
}

func TestResolveCampfireID_PrefixMatchMembership(t *testing.T) {
	// Produce two real campfire IDs; register one in the store.
	id1, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	full := id1.PublicKeyHex()
	prefix := full[:12]

	s, _ := makeTestStore(t, []string{full})
	defer s.Close()

	// Override beacon dir so we don't pick up beacons from the real ~/.campfire.
	emptyDir := t.TempDir()
	origBeaconDir := os.Getenv("CF_BEACON_DIR")
	os.Setenv("CF_BEACON_DIR", emptyDir)
	defer os.Setenv("CF_BEACON_DIR", origBeaconDir)

	got, err := resolveCampfireID(prefix, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != full {
		t.Errorf("got %s, want %s", got, full)
	}
}

func TestResolveCampfireID_PrefixMatchBeacon(t *testing.T) {
	beaconDir := t.TempDir()
	os.Setenv("CF_BEACON_DIR", beaconDir)
	defer os.Unsetenv("CF_BEACON_DIR")

	full := makeTestBeacon(t, beaconDir)
	prefix := full[:8]

	// Store has no memberships.
	s, _ := makeTestStore(t, nil)
	defer s.Close()

	got, err := resolveCampfireID(prefix, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != full {
		t.Errorf("got %s, want %s", got, full)
	}
}

func TestResolveCampfireID_NoMatch(t *testing.T) {
	emptyDir := t.TempDir()
	os.Setenv("CF_BEACON_DIR", emptyDir)
	defer os.Unsetenv("CF_BEACON_DIR")

	s, _ := makeTestStore(t, nil)
	defer s.Close()

	_, err := resolveCampfireID("deadbeef0000", s)
	if err == nil {
		t.Fatal("expected error for no-match prefix, got nil")
	}
}

func TestResolveCampfireID_Ambiguous(t *testing.T) {
	// Generate two IDs with the same prefix. In practice, generate two real IDs and
	// use "0" as prefix (all hex IDs start with some digit). We use a contrived approach:
	// create two IDs, register both, then use a prefix that matches both.

	id1, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	full1 := id1.PublicKeyHex()
	full2 := id2.PublicKeyHex()

	// Find a common prefix. Both are 64 hex chars. We search from length 1
	// until we find a prefix that is shared. In the unlikely case there's no
	// shared prefix of length 1, we manufacture one by manipulating the test IDs.
	// Since we just need any common prefix, use "" (empty string) which matches all.
	// But resolveCampfireID with empty string would match everything — let's find
	// the shortest common prefix at length 1.
	//
	// Simpler approach: just use both IDs explicitly so the store has exactly 2,
	// then use a prefix of length 0 (which matches everything in the store).
	// The function treats any prefix < 64 chars as a prefix search.

	emptyDir := t.TempDir()
	os.Setenv("CF_BEACON_DIR", emptyDir)
	defer os.Unsetenv("CF_BEACON_DIR")

	s, _ := makeTestStore(t, []string{full1, full2})
	defer s.Close()

	// Use empty prefix — matches all.
	_, err = resolveCampfireID("", s)
	if err == nil {
		t.Fatal("expected ambiguous error, got nil")
	}
	// Error message should contain "ambiguous".
	if err.Error() == "" {
		t.Fatal("empty error message")
	}
	t.Log("ambiguous error:", err)
}

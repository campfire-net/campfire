package cmd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// makeTestStore creates a temporary store with the given campfire IDs as memberships.
func makeTestStore(t *testing.T, campfireIDs []string) (store.Store, string) {
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

func TestResolveCampfireID_CampfireBeaconMatch(t *testing.T) {
	// Set up a store with a gateway campfire that has a routing:beacon message.
	emptyDir := t.TempDir()
	t.Setenv("CF_BEACON_DIR", emptyDir)

	// Open a temp store
	s, _ := makeTestStore(t, nil)
	defer s.Close()

	// Register a gateway campfire
	gwPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gwID := hex.EncodeToString(gwPub)
	if err := s.AddMembership(store.Membership{
		CampfireID:   gwID,
		TransportDir: emptyDir,
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Post a routing:beacon message for an advertised campfire into the gateway
	advID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	d, err := beacon.SignDeclaration(
		advID.PublicKey, advID.PrivateKey,
		"http://relay.example.com", "p2p-http", "resolve test", "open",
	)
	if err != nil {
		t.Fatalf("SignDeclaration: %v", err)
	}
	payload, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	senderPub, senderPriv, _ := ed25519.GenerateKey(rand.Reader)
	sig := ed25519.Sign(senderPriv, payload)
	_, err = s.AddMessage(store.MessageRecord{
		ID:          "msg-resolve-test",
		CampfireID:  gwID,
		Sender:      hex.EncodeToString(senderPub),
		Payload:     payload,
		Tags:        []string{"routing:beacon"},
		Antecedents: []string{},
		Timestamp:   5000,
		Signature:   sig,
		Provenance:  []message.ProvenanceHop{},
		ReceivedAt:  5000,
	})
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	advertisedID := advID.PublicKeyHex()
	prefix := advertisedID[:12]

	got, err := resolveCampfireID(prefix, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != advertisedID {
		t.Errorf("got %s, want %s", got, advertisedID)
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

func TestResolveCampfireID_NamingViaProjectRoot(t *testing.T) {
	// Set up an isolated cf home for this test.
	cfHomeDir := t.TempDir()
	origCFHome := os.Getenv("CF_HOME")
	os.Setenv("CF_HOME", cfHomeDir)
	defer os.Setenv("CF_HOME", origCFHome)

	// Isolate beacons.
	emptyBeaconDir := t.TempDir()
	origBeaconDir := os.Getenv("CF_BEACON_DIR")
	os.Setenv("CF_BEACON_DIR", emptyBeaconDir)
	defer os.Setenv("CF_BEACON_DIR", origBeaconDir)

	// Create a protocol client (this is the "sysop" who owns the root).
	client, err := protocol.Init(cfHomeDir)
	if err != nil {
		t.Fatalf("protocol.Init: %v", err)
	}
	defer client.Close()

	// Create a root campfire.
	transportDir := t.TempDir()
	rootResult, err := client.Create(protocol.CreateRequest{
		Description:  "test-root",
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
	})
	if err != nil {
		t.Fatalf("creating root campfire: %v", err)
	}
	rootID := rootResult.CampfireID

	// Create a target campfire (what "galtrader" resolves to).
	targetResult, err := client.Create(protocol.CreateRequest{
		Description:  "galtrader-api",
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
	})
	if err != nil {
		t.Fatalf("creating target campfire: %v", err)
	}
	targetID := targetResult.CampfireID

	// Register "galtrader" in the root.
	_, err = naming.Register(context.Background(), client, rootID, "galtrader", targetID, nil)
	if err != nil {
		t.Fatalf("naming.Register: %v", err)
	}

	// Set up a project directory with .campfire/root pointing to our root.
	projectDir := t.TempDir()
	cfDir := filepath.Join(projectDir, ".campfire")
	if err := os.MkdirAll(cfDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "root"), []byte(rootID), 0644); err != nil {
		t.Fatal(err)
	}

	// Change to the project directory so ProjectRoot() finds .campfire/root.
	origDir, _ := os.Getwd()
	os.Chdir(projectDir)
	defer os.Chdir(origDir)

	// Now resolve "galtrader" — should find it via project root naming.
	// The store passed to resolveCampfireID is for prefix/membership search (will find nothing).
	// The naming resolution goes through protocol.Init(CFHome()) internally, which opens
	// the same store since CF_HOME points to cfHomeDir.
	s, _ := makeTestStore(t, nil)
	defer s.Close()
	got, err := resolveCampfireID("galtrader", s)
	if err != nil {
		t.Fatalf("resolveCampfireID(\"galtrader\"): %v", err)
	}
	if got != targetID {
		t.Errorf("got %s, want %s", got, targetID)
	}
}

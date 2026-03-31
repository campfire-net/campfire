package cmd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// TestIsValidCampfireID verifies the campfire ID validator rejects malformed IDs.
func TestIsValidCampfireID(t *testing.T) {
	// Generate a real valid ID for the positive case.
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	validID := id.PublicKeyHex()

	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid 64-char hex", validID, true},
		{"too short", validID[:32], false},
		{"too long", validID + "00", false},
		{"empty string", "", false},
		{"non-hex chars", strings.Repeat("g", 64), false},
		{"path traversal attempt", "../../../etc/passwd" + strings.Repeat("a", 64-19), false},
		{"null bytes embedded", validID[:32] + "\x00" + validID[33:], false},
		{"uppercase hex", strings.ToUpper(validID), true},
		{"mixed whitespace", validID[:63] + " ", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := isValidCampfireID(tc.input)
			if got != tc.want {
				t.Errorf("isValidCampfireID(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestResolveNameInRoot_RejectsInvalidRootID verifies that resolveNameInRoot
// returns an error immediately when given a malformed root ID, without
// attempting any network or protocol operations. This guards against
// malformed or malicious IDs sourced from untrusted consult agent responses.
func TestResolveNameInRoot_RejectsInvalidRootID(t *testing.T) {
	// Use an isolated CF_HOME so protocol.Init doesn't touch the real identity.
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	malformedIDs := []struct {
		label string
		id    string
	}{
		{"empty", ""},
		{"short", "short"},
		{"63 hex chars", strings.Repeat("a", 63)},
		{"65 hex chars", strings.Repeat("a", 65)},
		{"64 non-hex chars", strings.Repeat("z", 64)},
		{"path traversal", "../../../etc/passwd" + strings.Repeat("a", 45)},
	}

	for _, tc := range malformedIDs {
		tc := tc
		t.Run(tc.label, func(t *testing.T) {
			_, err := resolveNameInRoot(tc.id, "somename")
			if err == nil {
				t.Errorf("resolveNameInRoot(%q, ...) expected error for malformed root ID, got nil", tc.id)
				return
			}
			if !strings.Contains(err.Error(), "invalid root campfire ID") {
				t.Errorf("error %q does not mention 'invalid root campfire ID'", err.Error())
			}
		})
	}
}

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

// TestResolveByName_FSWalkPath verifies that resolveByName uses FSWalkRoots when
// join-policy.json sets consult_campfire to "fs-walk".
func TestResolveByName_FSWalkPath(t *testing.T) {
	// Isolated cf home.
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_BEACON_DIR", t.TempDir())

	// Build the protocol client from cfHomeDir.
	client, err := protocol.Init(cfHomeDir)
	if err != nil {
		t.Fatalf("protocol.Init: %v", err)
	}
	defer client.Close()

	// Create a root campfire and a target campfire.
	transportDir := t.TempDir()
	rootResult, err := client.Create(protocol.CreateRequest{
		Description:  "fswalk-root",
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
	})
	if err != nil {
		t.Fatalf("creating root: %v", err)
	}
	rootID := rootResult.CampfireID

	targetResult, err := client.Create(protocol.CreateRequest{
		Description:  "fswalk-target",
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
	})
	if err != nil {
		t.Fatalf("creating target: %v", err)
	}
	targetID := targetResult.CampfireID

	// Register "mygame" in the root.
	_, err = naming.Register(context.Background(), client, rootID, "mygame", targetID, nil)
	if err != nil {
		t.Fatalf("naming.Register: %v", err)
	}

	// Write join-policy.json pointing to "fs-walk".
	if err := naming.SaveJoinPolicy(cfHomeDir, &naming.JoinPolicy{
		JoinPolicy:      "consult",
		ConsultCampfire: naming.FSWalkSentinel,
		JoinRoot:        rootID,
	}); err != nil {
		t.Fatalf("SaveJoinPolicy: %v", err)
	}

	// Create a project directory with a .campfire/root pointing to our root.
	projectDir := t.TempDir()
	campfireDir := filepath.Join(projectDir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(rootID), 0644); err != nil {
		t.Fatal(err)
	}

	// Change into the project dir so FSWalkRoots finds .campfire/root.
	origDir, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	// resolveByName should find "mygame" via fs-walk path.
	got, err := resolveByName("mygame", nil)
	if err != nil {
		t.Fatalf("resolveByName(\"mygame\"): %v", err)
	}
	if got != targetID {
		t.Errorf("got %s, want %s", got, targetID)
	}
}

// TestResolveByName_FallbackNoPolicy verifies that when no join-policy.json exists,
// resolveByName falls back to the legacy ProjectRoot + CF_ROOT_REGISTRY path.
func TestResolveByName_FallbackNoPolicy(t *testing.T) {
	// Isolated cf home — no join-policy.json.
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_BEACON_DIR", t.TempDir())

	// Build the protocol client.
	client, err := protocol.Init(cfHomeDir)
	if err != nil {
		t.Fatalf("protocol.Init: %v", err)
	}
	defer client.Close()

	// Create a root campfire and a target campfire.
	transportDir := t.TempDir()
	rootResult, err := client.Create(protocol.CreateRequest{
		Description:  "fallback-root",
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
	})
	if err != nil {
		t.Fatalf("creating root: %v", err)
	}
	rootID := rootResult.CampfireID

	targetResult, err := client.Create(protocol.CreateRequest{
		Description:  "fallback-target",
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
	})
	if err != nil {
		t.Fatalf("creating target: %v", err)
	}
	targetID := targetResult.CampfireID

	// Register "myapp" in the root.
	_, err = naming.Register(context.Background(), client, rootID, "myapp", targetID, nil)
	if err != nil {
		t.Fatalf("naming.Register: %v", err)
	}

	// Set CF_ROOT_REGISTRY so the fallback finds our root.
	t.Setenv("CF_ROOT_REGISTRY", rootID)

	// resolveByName with no join policy should fall back to CF_ROOT_REGISTRY.
	got, err := resolveByName("myapp", nil)
	if err != nil {
		t.Fatalf("resolveByName(\"myapp\"): %v", err)
	}
	if got != targetID {
		t.Errorf("got %s, want %s", got, targetID)
	}
}

// TestResolveByName_MalformedPolicy verifies that a malformed join-policy.json
// causes resolveByName to return an error rather than silently fall back to
// legacy behavior — so the operator learns their config is broken.
func TestResolveByName_MalformedPolicy(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_BEACON_DIR", t.TempDir())

	// Write malformed JSON to join-policy.json.
	policyPath := filepath.Join(cfHomeDir, "join-policy.json")
	if err := os.WriteFile(policyPath, []byte(`{bad json`), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := resolveByName("somename", nil)
	if err == nil {
		t.Fatal("expected error on malformed join-policy.json, got nil")
	}
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

// setupAutoJoinEnv creates isolated CF_HOME dirs for a creator and a resolver,
// a shared beacon dir, and wires CF_HOME/CF_BEACON_DIR env vars to the resolver.
// Returns (creatorCFHome, resolverCFHome, beaconDir).
func setupAutoJoinEnv(t *testing.T) (creatorHome, resolverHome, beaconDir string) {
	t.Helper()
	creatorHome = t.TempDir()
	resolverHome = t.TempDir()
	beaconDir = t.TempDir()

	// Wire resolver env vars.
	t.Setenv("CF_HOME", resolverHome)
	t.Setenv("CF_BEACON_DIR", beaconDir)
	// Clear CF_ROOT_REGISTRY so fallback doesn't pick up a stray env.
	t.Setenv("CF_ROOT_REGISTRY", "")

	// Generate and save a resolver identity.
	resolverID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating resolver identity: %v", err)
	}
	if err := resolverID.Save(filepath.Join(resolverHome, "identity.json")); err != nil {
		t.Fatalf("saving resolver identity: %v", err)
	}

	return creatorHome, resolverHome, beaconDir
}

// TestAutoJoin_OpenCampfireJoinedOnResolution verifies that resolving a name
// pointing to an open-protocol campfire the agent has not joined causes the
// agent to auto-join after resolution.
func TestAutoJoin_OpenCampfireJoinedOnResolution(t *testing.T) {
	creatorHome, resolverHome, beaconDir := setupAutoJoinEnv(t)

	// --- Creator: create root and target campfires, register the name. ---
	transportDir := t.TempDir()
	creatorClient, err := protocol.Init(creatorHome)
	if err != nil {
		t.Fatalf("protocol.Init (creator): %v", err)
	}
	defer creatorClient.Close()

	rootResult, err := creatorClient.Create(protocol.CreateRequest{
		Description:  "auto-join-root",
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:    beaconDir,
	})
	if err != nil {
		t.Fatalf("creating root: %v", err)
	}
	rootID := rootResult.CampfireID

	targetResult, err := creatorClient.Create(protocol.CreateRequest{
		Description:  "auto-join-target",
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:    beaconDir,
	})
	if err != nil {
		t.Fatalf("creating target: %v", err)
	}
	targetID := targetResult.CampfireID

	// Register "myservice" → targetID in the root.
	if _, err := naming.Register(context.Background(), creatorClient, rootID, "myservice", targetID, nil); err != nil {
		t.Fatalf("naming.Register: %v", err)
	}

	// --- Resolver: open the resolver store and resolve the name. ---
	resolverStore, err := store.Open(store.StorePath(resolverHome))
	if err != nil {
		t.Fatalf("opening resolver store: %v", err)
	}
	defer resolverStore.Close()

	// Pre-join the resolver to the root so its protocol client can read naming messages.
	// (The auto-join under test is for the *target* campfire, not the root.)
	// TransportDir is the campfire-specific subdir: transportDir/rootID.
	rootTransportDir := filepath.Join(transportDir, rootID)
	if err := resolverStore.AddMembership(store.Membership{
		CampfireID:    rootID,
		TransportDir:  rootTransportDir,
		JoinProtocol:  "open",
		Role:          "member",
		JoinedAt:      1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("pre-joining resolver to root: %v", err)
	}

	// Before resolution: resolver is not a member of the target.
	if m, _ := resolverStore.GetMembership(targetID); m != nil {
		t.Fatal("resolver should not be a member of target before resolution")
	}

	// Set CF_ROOT_REGISTRY so resolveByNameFallback finds the root.
	t.Setenv("CF_ROOT_REGISTRY", rootID)

	got, err := resolveByName("myservice", resolverStore)
	if err != nil {
		t.Fatalf("resolveByName: %v", err)
	}
	if got != targetID {
		t.Errorf("resolved to %s, want %s", got, targetID)
	}

	// After resolution: resolver should now be a member of the target.
	m, err := resolverStore.GetMembership(targetID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Errorf("resolver was not auto-joined to open campfire %s after name resolution", targetID[:12])
	}
}

// TestAutoJoin_InviteOnlyCampfireNotJoined verifies that resolving a name
// pointing to an invite-only campfire does NOT auto-join the resolver, but
// name resolution itself still succeeds.
func TestAutoJoin_InviteOnlyCampfireNotJoined(t *testing.T) {
	creatorHome, resolverHome, beaconDir := setupAutoJoinEnv(t)

	transportDir := t.TempDir()
	creatorClient, err := protocol.Init(creatorHome)
	if err != nil {
		t.Fatalf("protocol.Init (creator): %v", err)
	}
	defer creatorClient.Close()

	rootResult, err := creatorClient.Create(protocol.CreateRequest{
		Description:  "invite-only-root",
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:    beaconDir,
	})
	if err != nil {
		t.Fatalf("creating root: %v", err)
	}
	rootID := rootResult.CampfireID

	// Create an invite-only target.
	targetResult, err := creatorClient.Create(protocol.CreateRequest{
		Description:  "invite-only-target",
		JoinProtocol: "invite-only",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:    beaconDir,
	})
	if err != nil {
		t.Fatalf("creating invite-only target: %v", err)
	}
	targetID := targetResult.CampfireID

	// Register "private" → targetID in the root.
	if _, err := naming.Register(context.Background(), creatorClient, rootID, "private", targetID, nil); err != nil {
		t.Fatalf("naming.Register: %v", err)
	}

	// Resolver store: pre-join root so naming messages are readable.
	// TransportDir is the campfire-specific subdir: transportDir/rootID.
	resolverStore, err := store.Open(store.StorePath(resolverHome))
	if err != nil {
		t.Fatalf("opening resolver store: %v", err)
	}
	defer resolverStore.Close()

	rootTransportDirInvite := filepath.Join(transportDir, rootID)
	if err := resolverStore.AddMembership(store.Membership{
		CampfireID:    rootID,
		TransportDir:  rootTransportDirInvite,
		JoinProtocol:  "open",
		Role:          "member",
		JoinedAt:      1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("pre-joining resolver to root: %v", err)
	}

	t.Setenv("CF_ROOT_REGISTRY", rootID)

	// Resolution should succeed even though auto-join will be silently skipped.
	got, err := resolveByName("private", resolverStore)
	if err != nil {
		t.Fatalf("resolveByName: %v", err)
	}
	if got != targetID {
		t.Errorf("resolved to %s, want %s", got, targetID)
	}

	// Resolver must NOT be a member of the invite-only target.
	m, _ := resolverStore.GetMembership(targetID)
	if m != nil {
		t.Errorf("resolver was unexpectedly auto-joined to invite-only campfire %s", targetID[:12])
	}
}

// TestAutoJoin_AlreadyMemberNoRejoin verifies that resolving a name when the
// resolver is already a member does not cause an error or a duplicate membership.
func TestAutoJoin_AlreadyMemberNoRejoin(t *testing.T) {
	creatorHome, resolverHome, beaconDir := setupAutoJoinEnv(t)

	transportDir := t.TempDir()
	creatorClient, err := protocol.Init(creatorHome)
	if err != nil {
		t.Fatalf("protocol.Init (creator): %v", err)
	}
	defer creatorClient.Close()

	rootResult, err := creatorClient.Create(protocol.CreateRequest{
		Description:  "already-member-root",
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:    beaconDir,
	})
	if err != nil {
		t.Fatalf("creating root: %v", err)
	}
	rootID := rootResult.CampfireID

	targetResult, err := creatorClient.Create(protocol.CreateRequest{
		Description:  "already-member-target",
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:    beaconDir,
	})
	if err != nil {
		t.Fatalf("creating target: %v", err)
	}
	targetID := targetResult.CampfireID

	if _, err := naming.Register(context.Background(), creatorClient, rootID, "existing", targetID, nil); err != nil {
		t.Fatalf("naming.Register: %v", err)
	}

	// Pre-populate resolver store: joined to root and already a member of target.
	// TransportDir is the campfire-specific subdir: transportDir/campfireID.
	resolverStore, err := store.Open(store.StorePath(resolverHome))
	if err != nil {
		t.Fatalf("opening resolver store: %v", err)
	}
	defer resolverStore.Close()

	if err := resolverStore.AddMembership(store.Membership{
		CampfireID:    rootID,
		TransportDir:  filepath.Join(transportDir, rootID),
		JoinProtocol:  "open",
		Role:          "member",
		JoinedAt:      1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("pre-joining resolver to root: %v", err)
	}
	if err := resolverStore.AddMembership(store.Membership{
		CampfireID:    targetID,
		TransportDir:  filepath.Join(transportDir, targetID),
		JoinProtocol:  "open",
		Role:          "member",
		JoinedAt:      2,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("pre-adding target membership: %v", err)
	}

	t.Setenv("CF_ROOT_REGISTRY", rootID)

	// Resolve — should succeed without error and without blowing up on duplicate join.
	got, err := resolveByName("existing", resolverStore)
	if err != nil {
		t.Fatalf("resolveByName: %v", err)
	}
	if got != targetID {
		t.Errorf("resolved to %s, want %s", got, targetID)
	}

	// Membership should still be present.
	m, err := resolverStore.GetMembership(targetID)
	if err != nil || m == nil {
		t.Errorf("membership lost after re-resolution of already-member campfire")
	}
}

// TestConsultTimeout verifies that consultTimeout reads CF_CONSULT_TIMEOUT and
// falls back to 10s when the variable is absent or malformed.
func TestConsultTimeout(t *testing.T) {
	cases := []struct {
		name    string
		envVal  string
		want    time.Duration
	}{
		{"default when unset", "", 10 * time.Second},
		{"valid duration 30s", "30s", 30 * time.Second},
		{"valid duration 2m", "2m", 2 * time.Minute},
		{"valid duration 500ms", "500ms", 500 * time.Millisecond},
		{"invalid string falls back", "notaduration", 10 * time.Second},
		{"zero value falls back", "0s", 10 * time.Second},
		{"negative value falls back", "-5s", 10 * time.Second},
		{"empty string falls back", "", 10 * time.Second},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.envVal == "" {
				t.Setenv("CF_CONSULT_TIMEOUT", "")
			} else {
				t.Setenv("CF_CONSULT_TIMEOUT", tc.envVal)
			}
			got := consultTimeout()
			if got != tc.want {
				t.Errorf("consultTimeout() = %v, want %v (CF_CONSULT_TIMEOUT=%q)", got, tc.want, tc.envVal)
			}
		})
	}
}

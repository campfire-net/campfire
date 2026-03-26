package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

func writeTestBeacon(t *testing.T, dir string) (pubHex string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := beacon.New(pub, priv, "open", nil, beacon.TransportConfig{Protocol: "filesystem"}, "test campfire")
	if err != nil {
		t.Fatal(err)
	}
	if err := beacon.Publish(dir, b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(pub)
}

func captureDiscover(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	rootCmd.SetArgs([]string{"discover"})
	_ = rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading pipe: %v", err)
	}
	return string(out)
}

// TestDiscoverFullID verifies that the full campfire ID appears on a second line.
func TestDiscoverFullID(t *testing.T) {
	beaconDir := t.TempDir()
	fullID := writeTestBeacon(t, beaconDir)

	t.Setenv("CF_BEACON_DIR", beaconDir)

	out := captureDiscover(t)

	if !strings.Contains(out, "id: "+fullID) {
		t.Errorf("expected full ID line 'id: %s' in output, got:\n%s", fullID, out)
	}
	// Short ID (first 12 chars) should appear on first line
	if !strings.Contains(out, fullID[:12]) {
		t.Errorf("expected short ID %q in output, got:\n%s", fullID[:12], out)
	}
}

// TestDiscoverProjectBeacons verifies that project-local beacons are shown first
// under a "Project beacons:" heading, and global beacons under "Global beacons:".
func TestDiscoverProjectBeacons(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	projectBeaconDir := filepath.Join(projectDir, ".campfire", "beacons")

	globalID := writeTestBeacon(t, globalDir)
	projectID := writeTestBeacon(t, projectBeaconDir)

	t.Setenv("CF_BEACON_DIR", globalDir)

	// Write a .campfire/root file so ProjectDir() finds a project root
	rootFile := filepath.Join(projectDir, ".campfire", "root")
	fakeID := strings.Repeat("a", 64)
	if err := os.WriteFile(rootFile, []byte(fakeID), 0644); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

	out := captureDiscover(t)

	if !strings.Contains(out, "Project beacons:") {
		t.Errorf("expected 'Project beacons:' heading in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Global beacons:") {
		t.Errorf("expected 'Global beacons:' heading in output, got:\n%s", out)
	}
	// Project beacon should appear before global beacon
	projIdx := strings.Index(out, projectID[:12])
	globalIdx := strings.Index(out, globalID[:12])
	if projIdx == -1 {
		t.Errorf("project beacon ID %q not found in output:\n%s", projectID[:12], out)
	}
	if globalIdx == -1 {
		t.Errorf("global beacon ID %q not found in output:\n%s", globalID[:12], out)
	}
	if projIdx != -1 && globalIdx != -1 && projIdx > globalIdx {
		t.Errorf("project beacon should appear before global beacon, but positions: project=%d global=%d", projIdx, globalIdx)
	}
}

// postBeaconToStore adds a routing:beacon message to the store for a gateway campfire,
// advertising advPub/advPriv at the given endpoint.
func postBeaconToStore(t *testing.T, s store.Store, gatewayCampfireID string, advPub ed25519.PublicKey, advPriv ed25519.PrivateKey, msgID string) string {
	t.Helper()
	d, err := beacon.SignDeclaration(advPub, advPriv, "http://relay.example.com", "p2p-http", "in-band test campfire", "open")
	if err != nil {
		t.Fatalf("SignDeclaration: %v", err)
	}
	payload, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	senderPub, senderPriv, _ := ed25519.GenerateKey(rand.Reader)
	sig := ed25519.Sign(senderPriv, payload)
	rec := store.MessageRecord{
		ID:          msgID,
		CampfireID:  gatewayCampfireID,
		Sender:      fmt.Sprintf("%x", senderPub),
		Payload:     payload,
		Tags:        []string{"routing:beacon"},
		Antecedents: []string{},
		Timestamp:   5000,
		Signature:   sig,
		Provenance:  []message.ProvenanceHop{},
		ReceivedAt:  5000,
	}
	if _, err := s.AddMessage(rec); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	return d.CampfireID
}

// TestDiscoverCampfireBeacons verifies that beacons posted as routing:beacon
// messages to campfire memberships appear under a "Campfire beacons" heading.
func TestDiscoverCampfireBeacons(t *testing.T) {
	// Set up a temporary CF_HOME with a store containing a gateway campfire
	// and a routing:beacon message in it.
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	emptyBeaconDir := t.TempDir()
	t.Setenv("CF_BEACON_DIR", emptyBeaconDir)

	s, err := store.Open(store.StorePath(cfHomeDir))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	// Register a gateway campfire.
	gwPub, _, _ := ed25519.GenerateKey(rand.Reader)
	gwID := fmt.Sprintf("%x", gwPub)
	if err := s.AddMembership(store.Membership{
		CampfireID:   gwID,
		TransportDir: cfHomeDir,
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Post a beacon for an advertised campfire.
	advPub, advPriv, _ := ed25519.GenerateKey(rand.Reader)
	advertisedID := postBeaconToStore(t, s, gwID, advPub, advPriv, "msg-discover-campfire")
	s.Close()

	out := captureDiscover(t)

	if !strings.Contains(out, "Campfire beacons") {
		t.Errorf("expected 'Campfire beacons' heading in output, got:\n%s", out)
	}
	if !strings.Contains(out, advertisedID[:12]) {
		t.Errorf("expected advertised campfire ID %q in output, got:\n%s", advertisedID[:12], out)
	}
}

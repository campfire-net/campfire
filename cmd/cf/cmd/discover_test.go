package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/beacon"
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

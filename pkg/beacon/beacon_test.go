package beacon

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"
)

func testKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error: %v", err)
	}
	return pub, priv
}

func TestNewAndVerify(t *testing.T) {
	pub, priv := testKeypair(t)
	b, err := New(pub, priv, "open", []string{"status-update"}, TransportConfig{
		Protocol: "filesystem",
		Config:   map[string]string{"dir": "/tmp/campfire/test"},
	}, "test campfire")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if !b.Verify() {
		t.Error("beacon should verify")
	}
	if b.CampfireIDHex() == "" {
		t.Error("campfire ID hex should not be empty")
	}
}

func TestVerifyTampered(t *testing.T) {
	pub, priv := testKeypair(t)
	b, _ := New(pub, priv, "open", nil, TransportConfig{
		Protocol: "filesystem",
		Config:   map[string]string{"dir": "/tmp"},
	}, "test")

	// Tamper with description
	b.Description = "tampered"
	if b.Verify() {
		t.Error("tampered beacon should not verify")
	}
}

func TestPublishScanRemove(t *testing.T) {
	dir := t.TempDir()
	beaconDir := filepath.Join(dir, "beacons")

	pub, priv := testKeypair(t)
	b, _ := New(pub, priv, "open", []string{}, TransportConfig{
		Protocol: "filesystem",
		Config:   map[string]string{"dir": "/tmp/campfire/test"},
	}, "test campfire")

	// Publish
	if err := Publish(beaconDir, b); err != nil {
		t.Fatalf("Publish() error: %v", err)
	}

	// Scan
	beacons, err := Scan(beaconDir)
	if err != nil {
		t.Fatalf("Scan() error: %v", err)
	}
	if len(beacons) != 1 {
		t.Fatalf("got %d beacons, want 1", len(beacons))
	}
	if !beacons[0].Verify() {
		t.Error("scanned beacon should verify")
	}
	if beacons[0].Description != "test campfire" {
		t.Errorf("description = %s, want 'test campfire'", beacons[0].Description)
	}

	// Remove
	if err := Remove(beaconDir, pub); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	beacons, _ = Scan(beaconDir)
	if len(beacons) != 0 {
		t.Errorf("got %d beacons after remove, want 0", len(beacons))
	}
}

func TestScanEmptyDir(t *testing.T) {
	beacons, err := Scan("/nonexistent/path")
	if err != nil {
		t.Fatalf("Scan() should not error on nonexistent dir: %v", err)
	}
	if beacons != nil {
		t.Error("should return nil for nonexistent dir")
	}
}

func TestNilReceptionRequirements(t *testing.T) {
	pub, priv := testKeypair(t)
	b, _ := New(pub, priv, "open", nil, TransportConfig{
		Protocol: "filesystem",
		Config:   map[string]string{},
	}, "test")

	if b.ReceptionRequirements == nil {
		t.Error("reception_requirements should not be nil")
	}
}

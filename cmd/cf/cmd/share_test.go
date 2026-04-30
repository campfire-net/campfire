package cmd

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/beacon"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
)

// setupShareTestBeacon creates a test campfire identity, builds a signed beacon,
// publishes it to beaconDir, and returns the campfire ID hex.
func setupShareTestBeacon(t *testing.T, beaconDir string) string {
	t.Helper()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	b, err := beacon.New(
		cfID.PublicKey,
		cfID.PrivateKey,
		"open",
		[]string{},
		beacon.TransportConfig{
			Protocol: "filesystem",
			Config:   map[string]string{"dir": "/tmp/test-campfire"},
		},
		"test campfire",
	)
	if err != nil {
		t.Fatalf("creating beacon: %v", err)
	}

	if err := beacon.Publish(beaconDir, b); err != nil {
		t.Fatalf("publishing beacon: %v", err)
	}

	return cfID.PublicKeyHex()
}

// TestShareOutputsBeaconString verifies that cf share <campfire> outputs a
// beacon:BASE64 string for the given campfire.
func TestShareOutputsBeaconString(t *testing.T) {
	beaconDir := t.TempDir()
	t.Setenv("CF_BEACON_DIR", beaconDir)

	campfireID := setupShareTestBeacon(t, beaconDir)

	var shareErr error
	out := captureStdout(t, func() {
		shareErr = runShare(campfireID, beaconDir)
	})
	if shareErr != nil {
		t.Fatalf("runShare: %v", shareErr)
	}

	out = strings.TrimSpace(out)
	if !strings.HasPrefix(out, "beacon:") {
		t.Errorf("output %q does not start with beacon:", out)
	}

	// Verify the base64 part decodes to a valid Beacon with the correct campfire ID.
	encoded := strings.TrimPrefix(out, "beacon:")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	var b beacon.Beacon
	if err := cfencoding.Unmarshal(data, &b); err != nil {
		t.Fatalf("unmarshal beacon: %v", err)
	}
	if b.CampfireIDHex() != campfireID {
		t.Errorf("campfire ID: want %s, got %s", campfireID, b.CampfireIDHex())
	}
}

// TestShareBeaconRoundtrip verifies that the beacon string from cf share can
// be parsed back to the same campfire ID (inverse of cf join beacon:...).
func TestShareBeaconRoundtrip(t *testing.T) {
	beaconDir := t.TempDir()
	t.Setenv("CF_BEACON_DIR", beaconDir)

	campfireID := setupShareTestBeacon(t, beaconDir)

	var shareErr error
	out := captureStdout(t, func() {
		shareErr = runShare(campfireID, beaconDir)
	})
	if shareErr != nil {
		t.Fatalf("runShare: %v", shareErr)
	}

	beaconStr := strings.TrimSpace(out)
	parsed, err := parseBeaconString(beaconStr)
	if err != nil {
		t.Fatalf("parseBeaconString: %v", err)
	}
	if parsed.CampfireIDHex() != campfireID {
		t.Errorf("roundtrip campfire ID: want %s, got %s", campfireID, parsed.CampfireIDHex())
	}
}

// TestShareUnknownCampfireReturnsError verifies that cf share on an unknown
// campfire returns an error.
func TestShareUnknownCampfireReturnsError(t *testing.T) {
	beaconDir := t.TempDir()
	t.Setenv("CF_BEACON_DIR", beaconDir)

	// Use a valid-looking campfire ID that has no beacon.
	unknownID := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	err := runShare(unknownID, beaconDir)
	if err == nil {
		t.Error("expected error for unknown campfire, got nil")
	}
}

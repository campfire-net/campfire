package beacon

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestDefaultBeaconDir_NoDirectories verifies that DefaultBeaconDir returns
// ~/.cf/beacons when neither ~/.cf nor ~/.campfire exist.
func TestDefaultBeaconDir_NoDirectories(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_BEACON_DIR", "")
	t.Setenv("CF_HOME", "") // ensure CF_HOME does not influence this case

	want := filepath.Join(fakeHome, ".cf", "beacons")
	got := DefaultBeaconDir()
	if got != want {
		t.Errorf("DefaultBeaconDir() = %q, want %q", got, want)
	}
}

// TestDefaultBeaconDir_OnlyCampfireExists verifies that DefaultBeaconDir returns
// ~/.campfire/beacons when ~/.campfire exists but ~/.cf does not.
func TestDefaultBeaconDir_OnlyCampfireExists(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_BEACON_DIR", "")
	t.Setenv("CF_HOME", "") // ensure CF_HOME does not influence this case

	campfireDir := filepath.Join(fakeHome, ".campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		t.Fatalf("creating .campfire dir: %v", err)
	}

	want := filepath.Join(campfireDir, "beacons")
	got := DefaultBeaconDir()
	if got != want {
		t.Errorf("DefaultBeaconDir() = %q, want %q", got, want)
	}
}

// TestDefaultBeaconDir_CfExists verifies that DefaultBeaconDir returns
// ~/.cf/beacons when ~/.cf exists (regardless of ~/.campfire).
func TestDefaultBeaconDir_CfExists(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_BEACON_DIR", "")
	t.Setenv("CF_HOME", "") // ensure CF_HOME does not influence this case

	cfDir := filepath.Join(fakeHome, ".cf")
	if err := os.MkdirAll(cfDir, 0700); err != nil {
		t.Fatalf("creating .cf dir: %v", err)
	}

	want := filepath.Join(cfDir, "beacons")
	got := DefaultBeaconDir()
	if got != want {
		t.Errorf("DefaultBeaconDir() = %q, want %q", got, want)
	}
}

// TestDefaultBeaconDir_EnvOverride verifies that CF_BEACON_DIR takes precedence
// over filesystem discovery.
func TestDefaultBeaconDir_EnvOverride(t *testing.T) {
	fakeHome := t.TempDir()
	customDir := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_BEACON_DIR", customDir)
	t.Setenv("CF_HOME", "") // CF_BEACON_DIR wins even over CF_HOME

	got := DefaultBeaconDir()
	if got != customDir {
		t.Errorf("DefaultBeaconDir() = %q, want %q (CF_BEACON_DIR env)", got, customDir)
	}
}

// TestDefaultBeaconDir_CFHomeOverride verifies that CF_HOME takes precedence
// over user home directory discovery when CF_BEACON_DIR is not set.
// This is the key invariant for isolated --cf-home invocations: when the CLI
// propagates --cf-home to CF_HOME (via PersistentPreRun), DefaultBeaconDir
// returns $CF_HOME/beacons so that beacon scanning is limited to the isolated
// identity's directory rather than the developer's ~/.cf/beacons (which may
// contain hundreds of thousands of files and cause 30+ second scans).
func TestDefaultBeaconDir_CFHomeOverride(t *testing.T) {
	fakeHome := t.TempDir()
	cfHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_BEACON_DIR", "")
	t.Setenv("CF_HOME", cfHome)

	want := filepath.Join(cfHome, "beacons")
	got := DefaultBeaconDir()
	if got != want {
		t.Errorf("DefaultBeaconDir() = %q, want %q (CF_HOME env should override home discovery)", got, want)
	}
}

// TestDefaultBeaconDir_CFHomeDoesNotShadowCFBeaconDir verifies that CF_BEACON_DIR
// takes precedence over CF_HOME when both are set.
func TestDefaultBeaconDir_CFHomeDoesNotShadowCFBeaconDir(t *testing.T) {
	fakeHome := t.TempDir()
	cfHome := t.TempDir()
	customDir := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_HOME", cfHome)
	t.Setenv("CF_BEACON_DIR", customDir)

	got := DefaultBeaconDir()
	if got != customDir {
		t.Errorf("DefaultBeaconDir() = %q, want %q (CF_BEACON_DIR must outrank CF_HOME)", got, customDir)
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

// TestNotAfterDefault verifies that New() sets not_after to ~30 days (OPEN-024).
func TestNotAfterDefault(t *testing.T) {
	pub, priv := testKeypair(t)
	b, err := New(pub, priv, "open", nil, TransportConfig{
		Protocol: "filesystem",
		Config:   map[string]string{"dir": "/tmp"},
	}, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b.NotAfter == 0 {
		t.Error("NotAfter should be set to ~30 days by default")
	}
	notAfterTime := b.NotAfterTime()
	expectedMin := time.Now().Add(29 * 24 * time.Hour)
	expectedMax := time.Now().Add(31 * 24 * time.Hour)
	if notAfterTime.Before(expectedMin) || notAfterTime.After(expectedMax) {
		t.Errorf("NotAfterTime %v not in [29d, 31d] range", notAfterTime)
	}
}

// TestIsExpired verifies advisory expiry logic.
func TestIsExpired(t *testing.T) {
	pub, priv := testKeypair(t)
	tc := TransportConfig{Protocol: "filesystem", Config: map[string]string{"dir": "/tmp"}}
	b, _ := New(pub, priv, "open", nil, tc, "test")

	if b.IsExpired(time.Now()) {
		t.Error("fresh beacon should not be expired")
	}
	// Check with a far-future time
	if !b.IsExpired(time.Now().Add(35 * 24 * time.Hour)) {
		t.Error("beacon should be expired 35 days after creation")
	}
}

// TestIsExpired_ZeroNotAfter verifies a zero not_after never expires.
func TestIsExpired_ZeroNotAfter(t *testing.T) {
	b := &Beacon{NotAfter: 0}
	if b.IsExpired(time.Now().Add(100 * 365 * 24 * time.Hour)) {
		t.Error("beacon with NotAfter=0 should never be expired")
	}
}

package discovery_test

// beacon_test.go — Tests for Beacon + BeaconDeclaration in cf-discovery.
// Covers not_after field (OPEN-024), signature verification, declaration sign/verify.

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	discovery "github.com/campfire-net/campfire/cf-conventions/cf-discovery"
)

func generateCampfireKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	return pub, priv
}

func TestBeaconNew_HasNotAfterDefault(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	tc := discovery.TransportConfig{Protocol: "filesystem", Config: map[string]string{"dir": "/tmp/test"}}
	b, err := discovery.New(pub, priv, "open", nil, tc, "test campfire")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b.NotAfter == 0 {
		t.Error("NotAfter should be set to default (~30d), got 0")
	}
	notAfterTime := time.Unix(b.NotAfter, 0)
	expectedMin := time.Now().Add(29 * 24 * time.Hour)
	expectedMax := time.Now().Add(31 * 24 * time.Hour)
	if notAfterTime.Before(expectedMin) || notAfterTime.After(expectedMax) {
		t.Errorf("NotAfter %v not in expected [29d, 31d] range from now", notAfterTime)
	}
}

func TestBeaconNewWithExpiry_ZeroTime(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	tc := discovery.TransportConfig{Protocol: "filesystem", Config: map[string]string{"dir": "/tmp/test"}}
	b, err := discovery.NewWithExpiry(pub, priv, "open", nil, tc, "no expiry", time.Time{})
	if err != nil {
		t.Fatalf("NewWithExpiry: %v", err)
	}
	if b.NotAfter != 0 {
		t.Errorf("NotAfter should be 0 for zero time, got %d", b.NotAfter)
	}
}

func TestBeacon_Verify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	tc := discovery.TransportConfig{Protocol: "filesystem", Config: map[string]string{"dir": "/tmp/test"}}
	b, err := discovery.New(pub, priv, "open", nil, tc, "desc")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !b.Verify() {
		t.Error("expected Verify() to return true for a valid beacon")
	}
}

func TestBeacon_IsExpired(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	tc := discovery.TransportConfig{Protocol: "filesystem", Config: map[string]string{"dir": "/tmp/test"}}

	// Create a beacon that expires in 1 second
	b, err := discovery.NewWithExpiry(pub, priv, "open", nil, tc, "short-lived",
		time.Now().Add(time.Second))
	if err != nil {
		t.Fatalf("NewWithExpiry: %v", err)
	}
	if b.IsExpired(time.Now()) {
		t.Error("beacon should not be expired immediately after creation")
	}
	if !b.IsExpired(time.Now().Add(2 * time.Second)) {
		t.Error("beacon should be expired 2s after creation with 1s expiry")
	}
}

func TestBeacon_IsExpired_ZeroNotAfter(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	tc := discovery.TransportConfig{Protocol: "filesystem", Config: map[string]string{"dir": "/tmp/test"}}
	b, err := discovery.NewWithExpiry(pub, priv, "open", nil, tc, "no expiry", time.Time{})
	if err != nil {
		t.Fatalf("NewWithExpiry: %v", err)
	}
	// A beacon with not_after=0 should never be expired
	if b.IsExpired(time.Now().Add(100 * 365 * 24 * time.Hour)) {
		t.Error("beacon with not_after=0 should never be expired")
	}
}

func TestBeaconDeclaration_SignVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	decl, err := discovery.SignDeclaration(pub, priv, "http://example.com", "http", "test", "open")
	if err != nil {
		t.Fatalf("SignDeclaration: %v", err)
	}
	if decl.NotAfter == 0 {
		t.Error("SignDeclaration should set not_after by default")
	}
	if !discovery.VerifyDeclaration(*decl) {
		t.Error("VerifyDeclaration should return true for a fresh declaration")
	}
}

func TestBeaconDeclaration_IsDeclarationExpired(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	// Sign with expiry in the past
	decl, err := discovery.SignDeclarationWithExpiry(pub, priv, "http://example.com", "http", "test", "open",
		time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("SignDeclarationWithExpiry: %v", err)
	}
	if !discovery.IsDeclarationExpired(*decl, time.Now()) {
		t.Error("declaration should be expired after not_after passed")
	}
}

func TestBeaconDeclaration_DefaultLifetime30Days(t *testing.T) {
	if discovery.DefaultBeaconNotAfterDuration != 30*24*time.Hour {
		t.Errorf("DefaultBeaconNotAfterDuration = %v, want 30*24h", discovery.DefaultBeaconNotAfterDuration)
	}
}

func TestDeclarationToBeacon_PreservesNotAfter(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	expiry := time.Now().Add(7 * 24 * time.Hour).Truncate(time.Second)
	decl, err := discovery.SignDeclarationWithExpiry(pub, priv, "http://example.com", "http", "test", "open", expiry)
	if err != nil {
		t.Fatalf("SignDeclarationWithExpiry: %v", err)
	}
	b, err := discovery.DeclarationToBeacon(*decl)
	if err != nil {
		t.Fatalf("DeclarationToBeacon: %v", err)
	}
	if b.NotAfter != decl.NotAfter {
		t.Errorf("NotAfter not preserved: got %d, want %d", b.NotAfter, decl.NotAfter)
	}
}

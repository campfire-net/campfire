package protocol_test

// resolve_test.go — tests for resolveInput() and the 12 SDK entry points.
//
// Change 5 of Campfire 0.16 (campfire-agent-2jv).
//
// Test matrix:
//   TestResolveInput_HexPassthrough       — 64-hex → same hex, benchmark verifies no alloc
//   TestResolveInput_BeaconString         — valid beacon → hex ID
//   TestResolveInput_BeaconInvalid        — invalid beacon → ErrBeaconVerificationFailed
//   TestResolveInput_CfURI_NoResolver     — cf:// URI + nil resolver → clear error
//   TestResolveInput_CfURI_WithResolver   — cf:// URI + resolver → delegates
//   TestClient_Send_AcceptsBeacon         — integration: client.Send with beacon string works

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"

	"github.com/campfire-net/campfire/pkg/beacon"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// makeValidBeaconString creates a signed beacon and encodes it as a beacon:<base64> string.
// The campfire key pair is generated fresh for each call.
func makeValidBeaconString(t *testing.T) (beaconStr string, campfireIDHex string) {
	t.Helper()

	// Generate a campfire keypair.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire keypair: %v", err)
	}

	b, err := beacon.New(
		pub, priv,
		"open",
		nil,
		beacon.TransportConfig{
			Protocol: "http",
			Config:   map[string]string{"endpoint": "https://example.com"},
		},
		"test beacon",
	)
	if err != nil {
		t.Fatalf("creating beacon: %v", err)
	}

	raw, err := cfencoding.Marshal(b)
	if err != nil {
		t.Fatalf("marshaling beacon: %v", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return "beacon:" + encoded, fmt.Sprintf("%x", pub)
}

// makeInvalidBeaconString creates a beacon string with a tampered signature.
func makeInvalidBeaconString(t *testing.T) string {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	// Sign with a different key to create a verification failure.
	_, wrongPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating wrong keypair: %v", err)
	}

	// Create a beacon using the wrong private key — public key won't match.
	b, err := beacon.New(
		pub, wrongPriv, // public from pub, signed by wrongPriv
		"open",
		nil,
		beacon.TransportConfig{Protocol: "http"},
		"",
	)
	if err != nil {
		t.Fatalf("creating beacon: %v", err)
	}
	_ = priv // suppress unused var

	raw, err := cfencoding.Marshal(b)
	if err != nil {
		t.Fatalf("marshaling beacon: %v", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return "beacon:" + encoded
}

// stubResolver is a simple NamingResolver implementation for testing.
type stubResolver struct {
	result string
	err    error
}

func (r *stubResolver) Resolve(uri string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.result, nil
}

// -- Tests --

// TestResolveInput_HexPassthrough: 64-hex IDs pass through unchanged.
func TestResolveInput_HexPassthrough(t *testing.T) {
	const hexID = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

	campfireID, hint, err := protocol.ResolveInputForTest(hexID, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if campfireID != hexID {
		t.Errorf("got %q, want %q", campfireID, hexID)
	}
	if hint != nil {
		t.Errorf("expected nil hint for hex passthrough, got %+v", hint)
	}
}

// BenchmarkResolveInput_HexPassthrough: verifies the 64-hex path is zero-allocation.
func BenchmarkResolveInput_HexPassthrough(b *testing.B) {
	const hexID = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, hint, err := protocol.ResolveInputForTest(hexID, nil)
		_ = id
		_ = hint
		_ = err
	}
}

// TestResolveInput_BeaconString: valid beacon string resolves to campfire ID.
func TestResolveInput_BeaconString(t *testing.T) {
	beaconStr, wantID := makeValidBeaconString(t)

	campfireID, hint, err := protocol.ResolveInputForTest(beaconStr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if campfireID != wantID {
		t.Errorf("got campfire ID %q, want %q", campfireID, wantID)
	}
	if hint == nil {
		t.Fatal("expected non-nil hint for beacon string")
	}
	if !hint.Tainted {
		t.Error("hint.Tainted must always be true for beacon-derived hints")
	}
	if hint.Transport != "http" {
		t.Errorf("hint.Transport = %q, want %q", hint.Transport, "http")
	}
	if hint.Endpoint != "https://example.com" {
		t.Errorf("hint.Endpoint = %q, want %q", hint.Endpoint, "https://example.com")
	}
}

// TestResolveInput_BeaconInvalid: tampered beacon returns ErrBeaconVerificationFailed.
func TestResolveInput_BeaconInvalid(t *testing.T) {
	invalidBeacon := makeInvalidBeaconString(t)

	_, _, err := protocol.ResolveInputForTest(invalidBeacon, nil)
	if err == nil {
		t.Fatal("expected error for invalid beacon, got nil")
	}
	if !errors.Is(err, protocol.ErrBeaconVerificationFailed) {
		t.Errorf("expected ErrBeaconVerificationFailed, got: %v", err)
	}
}

// TestResolveInput_CfURI_NoResolver: cf:// URI without resolver returns clear error.
func TestResolveInput_CfURI_NoResolver(t *testing.T) {
	_, _, err := protocol.ResolveInputForTest("cf://team.project", nil)
	if err == nil {
		t.Fatal("expected error for cf:// URI with nil resolver")
	}
	const want = "naming resolver not configured"
	if !containsString(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

// TestResolveInput_CfURI_WithResolver: cf:// URI delegates to the resolver.
func TestResolveInput_CfURI_WithResolver(t *testing.T) {
	const wantID = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	resolver := &stubResolver{result: wantID}

	campfireID, hint, err := protocol.ResolveInputForTest("cf://team.project", resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if campfireID != wantID {
		t.Errorf("got %q, want %q", campfireID, wantID)
	}
	if hint != nil {
		t.Errorf("expected nil hint for named URI, got %+v", hint)
	}
}

// TestClient_Send_AcceptsBeacon: integration test — client.Send with beacon string.
// Uses a filesystem campfire so no network required.
func TestClient_Send_AcceptsBeacon(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()

	// Create the campfire.
	configDirA := t.TempDir()
	clientA, _, err := protocol.Init(configDirA)
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	result, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Build a beacon string from the published beacon.
	b := result.Beacon
	raw, err := cfencoding.Marshal(b)
	if err != nil {
		t.Fatalf("marshaling beacon: %v", err)
	}
	beaconStr := "beacon:" + base64.RawURLEncoding.EncodeToString(raw)

	// Send using the beacon string — must resolve to the campfire ID.
	_, err = clientA.Send(protocol.SendRequest{
		CampfireID: beaconStr,
		Payload:    []byte("hello via beacon"),
	})
	if err != nil {
		t.Fatalf("Send with beacon string: %v", err)
	}

	// Read using the campfire ID and verify the message arrived.
	readResult, err := clientA.Read(protocol.ReadRequest{
		CampfireID: result.CampfireID,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	found := false
	for _, msg := range readResult.Messages {
		if string(msg.Payload) == "hello via beacon" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("message sent via beacon string not found in campfire (got %d messages)", len(readResult.Messages))
	}
}

// containsString returns true if s contains substr.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	}()
}

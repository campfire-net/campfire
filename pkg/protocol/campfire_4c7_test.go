package protocol_test

// Regression tests for campfire-4c7 (13b): isAlreadyLinked must verify both
// signatures on RecenterClaim messages before returning true (campfire-8dd).

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/protocol"
)

// TestIsAlreadyLinked_RejectsForgedClaim verifies that isAlreadyLinked returns
// false when the center campfire contains a delegation-cert message with the
// correct NewKeyHex but invalid signatures.
//
// Exploit scenario (campfire-8dd): an attacker who can write to the filesystem
// transport injects a delegation-cert message whose NewKeyHex matches the target
// identity but whose CenterSig and/or NewKeySig are garbage. Before the fix,
// isAlreadyLinked returned true on NewKeyHex match alone — the authorize hook
// would be silently suppressed, preventing the legitimate recentering flow.
func TestIsAlreadyLinked_RejectsForgedClaim(t *testing.T) {
	parentDir := t.TempDir()
	cfHome := filepath.Join(parentDir, "child")
	if err := os.MkdirAll(cfHome, 0755); err != nil {
		t.Fatalf("creating cfHome: %v", err)
	}

	// Create a center campfire. The client identity owns both cfHome and the
	// center — it will be a member automatically.
	centerID := setupCenterForRecentering(t, cfHome)

	// Bootstrap without walk-up to get our public key.
	bootstrap, _, err := protocol.Init(cfHome, protocol.WithNoWalkUp())
	if err != nil {
		t.Fatalf("bootstrap Init: %v", err)
	}
	myKeyHex := bootstrap.PublicKeyHex()
	bootstrap.Close()

	// Inject a delegation-cert message with valid NewKeyHex but forged signatures.
	// Both CenterSig and NewKeySig are random bytes — neither verifies.
	_, forgedCenterSig, _ := ed25519.GenerateKey(rand.Reader)
	_, forgedNewKeySig, _ := ed25519.GenerateKey(rand.Reader)

	forgedClaim := protocol.RecenterClaim{
		NewKeyHex: myKeyHex,
		CenterID:  centerID,
		CenterSig: []byte(forgedCenterSig), // random key bytes used as forged sig
		NewKeySig: []byte(forgedNewKeySig),
	}
	claimJSON, err := json.Marshal(forgedClaim)
	if err != nil {
		t.Fatalf("marshaling forged claim: %v", err)
	}

	// Use a bootstrap client (no walk-up) to inject the message.
	injector, _, err := protocol.Init(cfHome, protocol.WithNoWalkUp())
	if err != nil {
		t.Fatalf("injector Init: %v", err)
	}
	if _, err := injector.Send(protocol.SendRequest{
		CampfireID: centerID,
		Payload:    claimJSON,
		Tags:       []string{"delegation-cert"},
	}); err != nil {
		t.Fatalf("injecting forged claim: %v", err)
	}
	injector.Close()

	// Now Init with walk-up. The authorize hook must be called — the forged
	// claim must not satisfy isAlreadyLinked.
	authorizeCalled := 0
	client, _, err := protocol.Init(cfHome,
		protocol.WithWalkUp(),
		protocol.WithAuthorizeFunc(func(desc string) (bool, error) {
			authorizeCalled++
			return false, nil // deny to avoid posting a real claim
		}),
	)
	if err != nil {
		t.Fatalf("Init with walk-up: %v", err)
	}
	client.Close()

	if authorizeCalled == 0 {
		t.Error("authorize hook was NOT called — isAlreadyLinked accepted forged claim (campfire-8dd regression)")
	}
}

// TestIsAlreadyLinked_AcceptsValidClaim verifies that a properly signed
// delegation-cert (posted via the normal recentering flow) causes isAlreadyLinked
// to return true, suppressing the authorize hook on subsequent Init calls.
//
// This is the complementary positive-path test — the signature fix must not
// break the happy path.
func TestIsAlreadyLinked_AcceptsValidClaim(t *testing.T) {
	parentDir := t.TempDir()
	cfHome := filepath.Join(parentDir, "child")
	if err := os.MkdirAll(cfHome, 0755); err != nil {
		t.Fatalf("creating cfHome: %v", err)
	}

	_ = setupCenterForRecentering(t, cfHome)

	// Phase 1: Init with authorize approval — posts a properly signed claim.
	c1, _, err := protocol.Init(cfHome, protocol.WithWalkUp(),
		protocol.WithAuthorizeFunc(func(desc string) (bool, error) {
			return true, nil
		}),
	)
	if err != nil {
		t.Fatalf("phase 1 Init: %v", err)
	}
	c1.Close()

	// Delete the local claimed state file to force isAlreadyLinked re-check.
	claimedPath := filepath.Join(cfHome, "recenter-claimed.json")
	if err := os.Remove(claimedPath); err != nil {
		t.Fatalf("removing claimed file: %v", err)
	}

	// Phase 2: Init with walk-up again. isAlreadyLinked should find the
	// properly signed claim and return true — authorize must NOT be called.
	authorizeCalled := 0
	c2, _, err := protocol.Init(cfHome, protocol.WithWalkUp(),
		protocol.WithAuthorizeFunc(func(desc string) (bool, error) {
			authorizeCalled++
			return true, nil
		}),
	)
	if err != nil {
		t.Fatalf("phase 2 Init: %v", err)
	}
	c2.Close()

	if authorizeCalled != 0 {
		t.Errorf("authorize hook called %d time(s) — isAlreadyLinked did not recognise valid signed claim", authorizeCalled)
	}
}

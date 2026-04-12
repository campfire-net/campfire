package convention_test

// composite_resolver_empty_test.go — Test for CompositeResolver with zero resolvers
// (campfire-f22 item a6e).

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
)

// TestCompositeResolver_EmptyResolvers verifies that NewCompositeResolver called
// with zero resolvers does not panic and returns a zero-valued IdentityInfo.
//
// The CompositeResolver.Resolve loop is a no-op when c.resolvers is empty;
// it returns the zero IdentityInfo (MachineKey nil, flags false).
func TestCompositeResolver_EmptyResolvers(t *testing.T) {
	composite := convention.NewCompositeResolver()

	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	// Must not panic.
	info, err := composite.Resolve(context.Background(), pub)
	if err != nil {
		t.Fatalf("Resolve with zero resolvers: unexpected error: %v", err)
	}

	// All fields should be zero/nil since no resolver ran.
	if info.MachineKey != nil {
		t.Errorf("MachineKey: expected nil, got %v", info.MachineKey)
	}
	if info.IdentityVerified {
		t.Error("IdentityVerified: expected false, got true")
	}
	if info.TrustResolved {
		t.Error("TrustResolved: expected false, got true")
	}
	if info.Identity != "" {
		t.Errorf("Identity: expected empty string, got %q", info.Identity)
	}
}

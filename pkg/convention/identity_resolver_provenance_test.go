package convention_test

// Tests for CompositeResolver provenance tracking (campfire-779 / ruling b).
//
// These tests verify that IdentityInfo.Provenance is populated correctly:
// first-setter-wins per bool field, resolver order determines provenance.

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
)

// fakeResolver is a minimal IdentityResolver for provenance unit tests.
// It returns a fixed IdentityInfo and has a stable Name.
type fakeResolver struct {
	name             string
	identityVerified bool
	trustResolved    bool
}

func (f *fakeResolver) Name() string { return f.name }

func (f *fakeResolver) Resolve(_ context.Context, senderPubkey ed25519.PublicKey) (convention.IdentityInfo, error) {
	return convention.IdentityInfo{
		MachineKey:       senderPubkey,
		IdentityVerified: f.identityVerified,
		TrustResolved:    f.trustResolved,
	}, nil
}

// senderKey generates a test pubkey. Not crypto-quality but sufficient for
// unit tests that only care about field values, not key material.
func senderKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	return pub
}

// TestCompositeResolver_ProvenanceFirstSetter_TrustResolved verifies that when
// two resolvers both set TrustResolved=true, the first one wins in provenance.
func TestCompositeResolver_ProvenanceFirstSetter_TrustResolved(t *testing.T) {
	pub := senderKey(t)

	first := &fakeResolver{name: "first", trustResolved: true}
	second := &fakeResolver{name: "second", trustResolved: true}

	composite := convention.NewCompositeResolver(first, second)
	info, err := composite.Resolve(context.Background(), pub)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !info.TrustResolved {
		t.Error("expected TrustResolved=true")
	}
	if got := info.Provenance["trust_resolved"]; got != "first" {
		t.Errorf("Provenance[trust_resolved] = %q; want %q", got, "first")
	}
}

// TestCompositeResolver_ProvenanceFirstSetter_IdentityVerified verifies that
// when two resolvers both set IdentityVerified=true, the first one wins.
func TestCompositeResolver_ProvenanceFirstSetter_IdentityVerified(t *testing.T) {
	pub := senderKey(t)

	first := &fakeResolver{name: "alpha", identityVerified: true}
	second := &fakeResolver{name: "beta", identityVerified: true}

	composite := convention.NewCompositeResolver(first, second)
	info, err := composite.Resolve(context.Background(), pub)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !info.IdentityVerified {
		t.Error("expected IdentityVerified=true")
	}
	if got := info.Provenance["identity_verified"]; got != "alpha" {
		t.Errorf("Provenance[identity_verified] = %q; want %q", got, "alpha")
	}
}

// TestCompositeResolver_ProvenanceReverseOrder proves that resolver order matters:
// reversing the order changes which resolver is recorded in provenance.
func TestCompositeResolver_ProvenanceReverseOrder(t *testing.T) {
	pub := senderKey(t)

	a := &fakeResolver{name: "a", trustResolved: true}
	b := &fakeResolver{name: "b", trustResolved: true}

	// a first → provenance = "a"
	forwardComposite := convention.NewCompositeResolver(a, b)
	infoForward, err := forwardComposite.Resolve(context.Background(), pub)
	if err != nil {
		t.Fatalf("forward Resolve: %v", err)
	}
	if got := infoForward.Provenance["trust_resolved"]; got != "a" {
		t.Errorf("forward: Provenance[trust_resolved] = %q; want %q", got, "a")
	}

	// b first → provenance = "b"
	reverseComposite := convention.NewCompositeResolver(b, a)
	infoReverse, err := reverseComposite.Resolve(context.Background(), pub)
	if err != nil {
		t.Fatalf("reverse Resolve: %v", err)
	}
	if got := infoReverse.Provenance["trust_resolved"]; got != "b" {
		t.Errorf("reverse: Provenance[trust_resolved] = %q; want %q", got, "b")
	}
}

// TestCompositeResolver_ProvenanceEmptyWhenFalse verifies that a bool field set
// to false by all resolvers has no entry in Provenance.
func TestCompositeResolver_ProvenanceEmptyWhenFalse(t *testing.T) {
	pub := senderKey(t)

	// Neither resolver sets any bool field.
	r1 := &fakeResolver{name: "r1"}
	r2 := &fakeResolver{name: "r2"}

	composite := convention.NewCompositeResolver(r1, r2)
	info, err := composite.Resolve(context.Background(), pub)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if info.IdentityVerified {
		t.Error("expected IdentityVerified=false")
	}
	if info.TrustResolved {
		t.Error("expected TrustResolved=false")
	}
	if _, ok := info.Provenance["identity_verified"]; ok {
		t.Errorf("expected no provenance key for identity_verified when false, got %q", info.Provenance["identity_verified"])
	}
	if _, ok := info.Provenance["trust_resolved"]; ok {
		t.Errorf("expected no provenance key for trust_resolved when false, got %q", info.Provenance["trust_resolved"])
	}
}

// TestCompositeResolver_MixedFields verifies that when one resolver sets
// IdentityVerified and another sets TrustResolved, both are tracked in provenance
// under their respective resolver names.
func TestCompositeResolver_MixedFields(t *testing.T) {
	pub := senderKey(t)

	// fake-a sets IdentityVerified; fake-b sets TrustResolved.
	fakeA := &fakeResolver{name: "fake-a", identityVerified: true}
	fakeB := &fakeResolver{name: "fake-b", trustResolved: true}

	composite := convention.NewCompositeResolver(fakeA, fakeB)
	info, err := composite.Resolve(context.Background(), pub)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !info.IdentityVerified {
		t.Error("expected IdentityVerified=true from fake-a")
	}
	if !info.TrustResolved {
		t.Error("expected TrustResolved=true from fake-b")
	}
	if got := info.Provenance["identity_verified"]; got != "fake-a" {
		t.Errorf("Provenance[identity_verified] = %q; want %q", got, "fake-a")
	}
	if got := info.Provenance["trust_resolved"]; got != "fake-b" {
		t.Errorf("Provenance[trust_resolved] = %q; want %q", got, "fake-b")
	}
}

// TestResolver_NameStable verifies that Name() returns stable, expected values
// for the built-in resolver types used as the named anchors in provenance.
func TestResolver_NameStable(t *testing.T) {
	cache := convention.NewCacheIdentityResolver(nil) // nil cache — Name() doesn't need cache
	if got := cache.Name(); got != "cache" {
		t.Errorf("CacheIdentityResolver.Name() = %q; want %q", got, "cache")
	}

	grant := convention.NewGrantChainResolver(nil, "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90", nil)
	if got := grant.Name(); got != "grant-chain" {
		t.Errorf("GrantChainResolver.Name() = %q; want %q", got, "grant-chain")
	}
}

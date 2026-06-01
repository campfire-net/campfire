package provenance_test

import (
	"context"
	"testing"

	authprov "github.com/campfire-net/campfire/cf-conventions/cf-authority/provenance"
	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/pkg/provenance"
)

// staticLevelSource is a fake LevelSource returning a fixed level for any key.
type staticLevelSource struct{ level provenance.Level }

func (s staticLevelSource) Level(string) provenance.Level { return s.level }

// Compile-time assertion that the production checker satisfies the L2 contract.
var _ convention.ProvenanceCheckerV2 = (*authprov.Checker)(nil)

func TestChecker_MapsLevels(t *testing.T) {
	cases := []struct {
		in   provenance.Level
		want convention.ProvenanceLevel
	}{
		{provenance.LevelAnonymous, convention.ProvenanceLevelAnonymous},
		{provenance.LevelClaimed, convention.ProvenanceLevelClaimed},
		{provenance.LevelContactable, convention.ProvenanceLevelContactable},
		{provenance.LevelPresent, convention.ProvenanceLevelPresent},
		{provenance.Level(-5), convention.ProvenanceLevelAnonymous}, // clamp low
		{provenance.Level(99), convention.ProvenanceLevelPresent},   // clamp high
	}
	for _, c := range cases {
		checker := authprov.NewChecker(staticLevelSource{level: c.in})
		got := checker.CheckProvenance(context.Background(), convention.ProvenanceRequest{SenderKey: "k"})
		if got.Level != c.want {
			t.Errorf("level %d → %d, want %d", c.in, got.Level, c.want)
		}
	}
}

func TestChecker_NilSourceFailsClosed(t *testing.T) {
	// nil source and nil receiver both resolve to Anonymous (fail closed), the
	// opposite of the allow-all stub which always returns Present.
	if got := authprov.NewChecker(nil).CheckProvenance(context.Background(), convention.ProvenanceRequest{SenderKey: "k"}); got.Level != convention.ProvenanceLevelAnonymous {
		t.Errorf("nil source level = %d, want Anonymous(0)", got.Level)
	}
	var nilChecker *authprov.Checker
	if got := nilChecker.CheckProvenance(context.Background(), convention.ProvenanceRequest{SenderKey: "k"}); got.Level != convention.ProvenanceLevelAnonymous {
		t.Errorf("nil receiver level = %d, want Anonymous(0)", got.Level)
	}
}

// TestChecker_RealStore is the ground-source test: it wires the checker to a real
// pkg/provenance.Store (no mock) and verifies end-to-end level resolution.
func TestChecker_RealStore(t *testing.T) {
	store := provenance.NewStore(provenance.StoreConfig{AllowSelfAttestation: true})
	const key = "aabbccdd"

	checker := authprov.NewChecker(store)

	// Unknown key → Anonymous.
	if got := checker.CheckProvenance(context.Background(), convention.ProvenanceRequest{SenderKey: key}); got.Level != convention.ProvenanceLevelAnonymous {
		t.Errorf("unknown key level = %d, want Anonymous(0)", got.Level)
	}

	// Self-claimed key → Claimed (1). Proves the real Store satisfies LevelSource
	// and a non-trivial level flows through the checker.
	store.SetSelfClaimed(key)
	if got := store.Level(key); got != provenance.LevelClaimed {
		t.Fatalf("precondition: store.Level after SetSelfClaimed = %d, want Claimed(1)", got)
	}
	if got := checker.CheckProvenance(context.Background(), convention.ProvenanceRequest{SenderKey: key}); got.Level != convention.ProvenanceLevelClaimed {
		t.Errorf("self-claimed key level = %d, want Claimed(1)", got.Level)
	}
}

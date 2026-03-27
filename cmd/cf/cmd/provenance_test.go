package cmd

import (
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/provenance"
)

// TestLevelDescription verifies all four levels return non-empty descriptions.
func TestLevelDescription(t *testing.T) {
	levels := []provenance.Level{
		provenance.LevelAnonymous,
		provenance.LevelClaimed,
		provenance.LevelContactable,
		provenance.LevelPresent,
	}
	for _, l := range levels {
		desc := levelDescription(l)
		if desc == "" {
			t.Errorf("levelDescription(%d) returned empty string", l)
		}
	}
}

// TestLevelDescription_Unknown verifies out-of-range level returns "unknown".
func TestLevelDescription_Unknown(t *testing.T) {
	desc := levelDescription(provenance.Level(99))
	if desc != "unknown" {
		t.Errorf("expected \"unknown\" for level 99, got %q", desc)
	}
}

func TestFormatAge_Minutes(t *testing.T) {
	got := formatAge(30 * time.Minute)
	if got != "30m" {
		t.Errorf("expected %q, got %q", "30m", got)
	}
}

func TestFormatAge_Hours(t *testing.T) {
	got := formatAge(2 * time.Hour)
	if got != "2h" {
		t.Errorf("expected %q, got %q", "2h", got)
	}
}

func TestFormatAge_HoursAndMinutes(t *testing.T) {
	got := formatAge(2*time.Hour + 30*time.Minute)
	if got != "2h30m" {
		t.Errorf("expected %q, got %q", "2h30m", got)
	}
}

func TestFormatAge_Days(t *testing.T) {
	got := formatAge(3 * 24 * time.Hour)
	if got != "3d" {
		t.Errorf("expected %q, got %q", "3d", got)
	}
}

func TestProvenanceShowHuman_NoAttestations(t *testing.T) {
	s := loadProvenanceStore()
	level := s.Level("test-key-aaa")
	err := provenanceShowHuman("alice", "test-key-aaa", level, nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestProvenanceShowHuman_WithAttestation(t *testing.T) {
	store := provenance.NewStore(provenance.StoreConfig{
		FreshnessWindow:      7 * 24 * time.Hour,
		MaxTransitivityDepth: 1,
		TrustedVerifierKeys:  map[string]int{"verifier-key-bbb": 0},
	})

	a := &provenance.Attestation{
		ID:              "attestation-001",
		TargetKey:       "target-key-aaa",
		VerifierKey:     "verifier-key-bbb",
		Nonce:           "nonce-abc",
		ContactMethod:   "cf://my-campfire",
		ProofType:       provenance.ProofCaptcha,
		ProofProvenance: "captcha-sig",
		VerifiedAt:      time.Now().Add(-1 * time.Hour),
		CoSigned:        true,
	}

	if err := store.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	level := store.Level("target-key-aaa")
	attestations := store.Attestations("target-key-aaa")

	err := provenanceShowHuman("alice", "target-key-aaa", level, attestations)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

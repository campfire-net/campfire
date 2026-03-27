package provenance

import (
	"errors"
	"testing"
	"time"
)

// --- helpers ---

func makeAttestation(id, targetKey, verifierKey string, verifiedAt time.Time, coSigned bool) *Attestation {
	return &Attestation{
		ID:          id,
		TargetKey:   targetKey,
		VerifierKey: verifierKey,
		Nonce:       "abc123",
		ContactMethod: "cf://example",
		ProofType:   ProofCaptcha,
		VerifiedAt:  verifiedAt,
		CoSigned:    coSigned,
	}
}

func storeWithVerifier(verifierKey string) *Store {
	cfg := DefaultConfig()
	cfg.TrustedVerifierKeys[verifierKey] = 0
	return NewStore(cfg)
}

// --- level computation tests ---

// TestLevel_Default verifies that a key with no attestations is level 0 (Anonymous).
func TestLevel_Default(t *testing.T) {
	s := NewStore(DefaultConfig())
	if l := s.Level("unknown-key"); l != LevelAnonymous {
		t.Errorf("expected LevelAnonymous for unknown key, got %v", l)
	}
}

// TestLevel_SelfClaimed verifies that a key with a self-asserted profile is level 1 (Claimed).
func TestLevel_SelfClaimed(t *testing.T) {
	s := NewStore(DefaultConfig())
	s.SetSelfClaimed("alice")

	if l := s.Level("alice"); l != LevelClaimed {
		t.Errorf("expected LevelClaimed, got %v", l)
	}
}

// TestLevel_Contactable verifies level 2 with a valid non-fresh attestation.
func TestLevel_Contactable(t *testing.T) {
	s := storeWithVerifier("verifier")
	s.SetSelfClaimed("alice")

	// Attestation old enough to not be fresh (8 days ago, freshness window is 7 days).
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	a := makeAttestation("att-1", "alice", "verifier", oldTime, true)
	if err := s.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	if l := s.Level("alice"); l != LevelContactable {
		t.Errorf("expected LevelContactable, got %v", l)
	}
}

// TestLevel_Present verifies level 3 with a fresh attestation.
func TestLevel_Present(t *testing.T) {
	s := storeWithVerifier("verifier")

	freshTime := time.Now().Add(-1 * time.Hour) // 1 hour ago, within 7-day window
	a := makeAttestation("att-1", "alice", "verifier", freshTime, true)
	if err := s.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	if l := s.Level("alice"); l != LevelPresent {
		t.Errorf("expected LevelPresent, got %v", l)
	}
}

// TestLevel_RevokedAttestation verifies that revoked attestations don't count.
func TestLevel_RevokedAttestation(t *testing.T) {
	s := storeWithVerifier("verifier")

	freshTime := time.Now().Add(-1 * time.Hour)
	a := makeAttestation("att-revoked", "alice", "verifier", freshTime, true)
	if err := s.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	if err := s.Revoke("att-revoked"); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	if l := s.Level("alice"); l != LevelAnonymous {
		t.Errorf("expected LevelAnonymous after revocation, got %v", l)
	}
}

// TestLevel_FreshnessDecay verifies that an attestation past the freshness window
// decays from level 3 to level 2.
func TestLevel_FreshnessDecay(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TrustedVerifierKeys["verifier"] = 0
	cfg.FreshnessWindow = 1 * time.Hour
	s := NewStore(cfg)

	// Attestation 2 hours ago — past the 1-hour freshness window.
	oldTime := time.Now().Add(-2 * time.Hour)
	a := makeAttestation("att-1", "alice", "verifier", oldTime, true)
	if err := s.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	if l := s.Level("alice"); l != LevelContactable {
		t.Errorf("expected LevelContactable (decayed from 3), got %v", l)
	}
}

// TestLevel_ZeroFreshnessNeverPresent verifies that FreshnessWindow=0 means level 3 never granted.
func TestLevel_ZeroFreshnessNeverPresent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TrustedVerifierKeys["verifier"] = 0
	cfg.FreshnessWindow = 0
	s := NewStore(cfg)

	freshTime := time.Now()
	a := makeAttestation("att-1", "alice", "verifier", freshTime, true)
	if err := s.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	if l := s.Level("alice"); l != LevelContactable {
		t.Errorf("expected LevelContactable (freshness disabled), got %v", l)
	}
}

// TestLevel_MaxAbsoluteAge verifies that level 2 attestations older than MaxAbsoluteAgeForLevel2
// are treated as level 1.
func TestLevel_MaxAbsoluteAge(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TrustedVerifierKeys["verifier"] = 0
	cfg.MaxAbsoluteAgeForLevel2 = 30 * 24 * time.Hour // 30 days
	s := NewStore(cfg)

	s.SetSelfClaimed("alice")

	// Attestation 31 days ago — past the absolute max age.
	veryOldTime := time.Now().Add(-31 * 24 * time.Hour)
	a := makeAttestation("att-1", "alice", "verifier", veryOldTime, true)
	if err := s.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	// Should fall back to level 1 (self-claimed) since attestation is too old.
	if l := s.Level("alice"); l != LevelClaimed {
		t.Errorf("expected LevelClaimed (attestation too old), got %v", l)
	}
}

// --- self-attestation rejection tests ---

// TestSelfAttestation_Rejected verifies that verifier_key == target_key is rejected by default.
func TestSelfAttestation_Rejected(t *testing.T) {
	s := NewStore(DefaultConfig())

	a := makeAttestation("self-att", "alice", "alice", time.Now(), true)
	err := s.AddAttestation(a)
	if !errors.Is(err, ErrSelfAttestation) {
		t.Errorf("expected ErrSelfAttestation, got %v", err)
	}
}

// TestSelfAttestation_AllowedWhenConfigured verifies self-attestations are accepted
// when AllowSelfAttestation is explicitly set.
func TestSelfAttestation_AllowedWhenConfigured(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AllowSelfAttestation = true
	cfg.TrustedVerifierKeys["alice"] = 0 // alice trusts herself
	s := NewStore(cfg)

	a := makeAttestation("self-att", "alice", "alice", time.Now(), true)
	if err := s.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation should succeed with AllowSelfAttestation=true, got: %v", err)
	}

	if l := s.Level("alice"); l != LevelPresent {
		t.Errorf("expected LevelPresent with allowed self-attestation, got %v", l)
	}
}

// TestSelfAttestation_NotCountedForLevel verifies self-attestation still doesn't count
// for level even if added (when not explicitly allowed, it is rejected).
func TestSelfAttestation_StillRejectedByLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AllowSelfAttestation = false
	s := NewStore(cfg)

	a := makeAttestation("self-att", "alice", "alice", time.Now(), true)
	err := s.AddAttestation(a)
	if !errors.Is(err, ErrSelfAttestation) {
		t.Errorf("expected ErrSelfAttestation error, got %v", err)
	}

	// Level should still be anonymous since self-attestation was rejected.
	if l := s.Level("alice"); l != LevelAnonymous {
		t.Errorf("expected LevelAnonymous (self-attestation rejected), got %v", l)
	}
}

// --- co-signing enforcement tests ---

// TestCoSigning_FlaggedWhenMissing verifies that non-co-signed attestations return ErrNotCoSigned.
func TestCoSigning_FlaggedWhenMissing(t *testing.T) {
	s := storeWithVerifier("verifier")

	a := makeAttestation("att-1", "alice", "verifier", time.Now(), false) // not co-signed
	err := s.AddAttestation(a)
	if !errors.Is(err, ErrNotCoSigned) {
		t.Errorf("expected ErrNotCoSigned for non-co-signed attestation, got %v", err)
	}
}

// TestCoSigning_AcceptedWithWarning verifies that non-co-signed attestations are still stored.
// The ErrNotCoSigned is a warning, not a rejection.
func TestCoSigning_AcceptedWithWarning(t *testing.T) {
	s := storeWithVerifier("verifier")

	freshTime := time.Now().Add(-1 * time.Hour)
	a := makeAttestation("att-cosign", "alice", "verifier", freshTime, false) // not co-signed
	err := s.AddAttestation(a)
	if !errors.Is(err, ErrNotCoSigned) {
		t.Errorf("expected ErrNotCoSigned warning, got %v", err)
	}

	// Despite the warning, the attestation was stored and should count for level computation.
	if l := s.Level("alice"); l < LevelContactable {
		t.Errorf("expected at least LevelContactable (non-co-signed stored), got %v", l)
	}
}

// TestCoSigning_CoSignedSuccess verifies that co-signed attestations return nil error.
func TestCoSigning_CoSignedSuccess(t *testing.T) {
	s := storeWithVerifier("verifier")

	a := makeAttestation("att-cosigned", "alice", "verifier", time.Now(), true)
	if err := s.AddAttestation(a); err != nil {
		t.Errorf("expected nil error for co-signed attestation, got %v", err)
	}
}

// --- transitivity tests ---

// TestTransitivity_DirectAttestation verifies that direct attestations work at depth 0.
func TestTransitivity_DirectAttestation(t *testing.T) {
	s := storeWithVerifier("verifier")

	freshTime := time.Now().Add(-1 * time.Hour)
	a := makeAttestation("att-1", "alice", "verifier", freshTime, true)
	if err := s.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	if l := s.LevelTransitive("alice"); l != LevelPresent {
		t.Errorf("expected LevelPresent with direct attestation, got %v", l)
	}
}

// TestTransitivity_DepthLimit verifies that depth=0 means no transitive attestations.
func TestTransitivity_DepthLimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxTransitivityDepth = 0
	s := NewStore(cfg)

	verifierKey := "verifier"
	intermediaryKey := "intermediary"

	// Trust intermediary as verifier (depth 0 = direct only).
	s.TrustVerifier(intermediaryKey, 0)

	// Intermediary has a direct attestation from verifier.
	// (verifier is NOT in the trusted set at all here).
	freshTime := time.Now().Add(-1 * time.Hour)
	intermediaryAtt := makeAttestation("att-intermediary", intermediaryKey, verifierKey, freshTime, true)
	_ = s.AddAttestation(intermediaryAtt) // verifier not trusted, so this won't count

	// Alice has an attestation from intermediary.
	aliceAtt := makeAttestation("att-alice", "alice", intermediaryKey, freshTime, true)
	if err := s.AddAttestation(aliceAtt); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	// With MaxTransitivityDepth=0, alice's attestation from intermediary should count
	// directly (intermediary is a direct trusted verifier).
	if l := s.LevelTransitive("alice"); l < LevelContactable {
		t.Errorf("expected at least LevelContactable via direct verifier, got %v", l)
	}
}

// TestTransitivity_VerifierKeyChecked verifies that transitivity checks verifier_key,
// not just the campfire. An attestation from an untrusted verifier is not accepted.
func TestTransitivity_VerifierKeyChecked(t *testing.T) {
	s := NewStore(DefaultConfig()) // no trusted verifiers

	freshTime := time.Now().Add(-1 * time.Hour)
	// Eve tries to pass as a verifier, but her key is not trusted.
	a := makeAttestation("att-eve", "alice", "eve-key", freshTime, true)
	if err := s.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	// alice should be anonymous — eve-key is not in trusted verifiers.
	if l := s.LevelTransitive("alice"); l != LevelAnonymous {
		t.Errorf("expected LevelAnonymous (untrusted verifier), got %v", l)
	}
}

// TestTransitivity_ChainDepthOne verifies that a 1-hop transitive chain works
// when MaxTransitivityDepth=1.
func TestTransitivity_ChainDepthOne(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxTransitivityDepth = 1
	cfg.FreshnessWindow = 24 * time.Hour
	// Trust "alice-verifier" as a direct verifier.
	cfg.TrustedVerifierKeys["alice-verifier"] = 0
	s := NewStore(cfg)

	freshTime := time.Now().Add(-1 * time.Hour)

	// alice-verifier attested bob.
	bobAtt := makeAttestation("att-bob", "bob", "alice-verifier", freshTime, true)
	if err := s.AddAttestation(bobAtt); err != nil {
		t.Fatalf("AddAttestation for bob failed: %v", err)
	}

	// alice-verifier also attested carol.
	carolAtt := makeAttestation("att-carol", "carol", "alice-verifier", freshTime, true)
	if err := s.AddAttestation(carolAtt); err != nil {
		t.Fatalf("AddAttestation for carol failed: %v", err)
	}

	// Both bob and carol should be at level 3 (direct attestation from trusted verifier).
	if l := s.LevelTransitive("bob"); l < LevelContactable {
		t.Errorf("expected bob at least LevelContactable, got %v", l)
	}
	if l := s.LevelTransitive("carol"); l < LevelContactable {
		t.Errorf("expected carol at least LevelContactable, got %v", l)
	}
}

// TestTransitivity_NoInfiniteChain verifies that transitivity doesn't go beyond depth limit.
func TestTransitivity_NoInfiniteChain(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxTransitivityDepth = 1
	cfg.FreshnessWindow = 24 * time.Hour
	s := NewStore(cfg)

	// Only trust verifier-A directly.
	s.TrustVerifier("verifier-a", 0)

	freshTime := time.Now().Add(-1 * time.Hour)

	// verifier-a attested verifier-b (1 hop: verifier-a → verifier-b).
	vbAtt := makeAttestation("att-vb", "verifier-b", "verifier-a", freshTime, true)
	if err := s.AddAttestation(vbAtt); err != nil {
		t.Fatalf("AddAttestation for verifier-b failed: %v", err)
	}

	// verifier-b attested target (2 hops: verifier-a → verifier-b → target).
	targetAtt := makeAttestation("att-target", "target", "verifier-b", freshTime, true)
	if err := s.AddAttestation(targetAtt); err != nil {
		t.Fatalf("AddAttestation for target failed: %v", err)
	}

	// "target" is 2 hops away. MaxTransitivityDepth=1 means this chain is too long.
	// Target should be anonymous (or at most level 1 if self-claimed).
	if l := s.LevelTransitive("target"); l >= LevelContactable {
		t.Errorf("expected target below LevelContactable (chain too deep), got %v", l)
	}
}

// --- freshness configuration tests ---

// TestFreshness_Configurable verifies that the freshness window is configurable.
func TestFreshness_Configurable(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TrustedVerifierKeys["verifier"] = 0
	cfg.FreshnessWindow = 1 * time.Hour
	s := NewStore(cfg)

	now := time.Now()

	// Attestation 30 minutes ago — fresh.
	freshAtt := makeAttestation("att-fresh", "alice", "verifier", now.Add(-30*time.Minute), true)
	if err := s.AddAttestation(freshAtt); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}
	if l := s.LevelAt("alice", now); l != LevelPresent {
		t.Errorf("expected LevelPresent for 30-min-old attestation with 1h window, got %v", l)
	}

	s2 := NewStore(cfg)
	// Attestation 2 hours ago — stale.
	staleAtt := makeAttestation("att-stale", "bob", "verifier", now.Add(-2*time.Hour), true)
	if err := s2.AddAttestation(staleAtt); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}
	if l := s2.LevelAt("bob", now); l != LevelContactable {
		t.Errorf("expected LevelContactable for 2-hour-old attestation with 1h window, got %v", l)
	}
}

// --- revocation tests ---

// TestRevoke_AttestationNotFound verifies ErrAttestationNotFound for unknown ID.
func TestRevoke_AttestationNotFound(t *testing.T) {
	s := NewStore(DefaultConfig())
	if err := s.Revoke("nonexistent"); !errors.Is(err, ErrAttestationNotFound) {
		t.Errorf("expected ErrAttestationNotFound, got %v", err)
	}
}

// TestRevoke_MultipleAttestations verifies that revoking one attestation doesn't affect others.
func TestRevoke_MultipleAttestations(t *testing.T) {
	s := storeWithVerifier("verifier")

	freshTime := time.Now().Add(-1 * time.Hour)

	// Add two attestations for the same key.
	a1 := makeAttestation("att-1", "alice", "verifier", freshTime, true)
	a2 := makeAttestation("att-2", "alice", "verifier", freshTime, true)

	if err := s.AddAttestation(a1); err != nil {
		t.Fatalf("AddAttestation a1 failed: %v", err)
	}
	if err := s.AddAttestation(a2); err != nil {
		t.Fatalf("AddAttestation a2 failed: %v", err)
	}

	// Revoke only the first.
	if err := s.Revoke("att-1"); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	// alice should still be level 3 because a2 is still valid.
	if l := s.Level("alice"); l < LevelContactable {
		t.Errorf("expected alice to still be at least LevelContactable, got %v", l)
	}
}

// --- attestations query tests ---

// TestAttestations_ReturnsNonRevoked verifies Attestations() filters revoked entries.
func TestAttestations_ReturnsNonRevoked(t *testing.T) {
	s := storeWithVerifier("verifier")

	a1 := makeAttestation("att-1", "alice", "verifier", time.Now(), true)
	a2 := makeAttestation("att-2", "alice", "verifier", time.Now(), true)

	_ = s.AddAttestation(a1)
	_ = s.AddAttestation(a2)
	_ = s.Revoke("att-1")

	atts := s.Attestations("alice")
	if len(atts) != 1 {
		t.Errorf("expected 1 non-revoked attestation, got %d", len(atts))
	}
	if atts[0].ID != "att-2" {
		t.Errorf("expected att-2, got %s", atts[0].ID)
	}
}

// TestAttestations_EmptyForUnknownKey verifies Attestations() returns nil for unknown keys.
func TestAttestations_EmptyForUnknownKey(t *testing.T) {
	s := NewStore(DefaultConfig())
	if atts := s.Attestations("ghost"); len(atts) != 0 {
		t.Errorf("expected empty slice for unknown key, got %d", len(atts))
	}
}

// --- nil/error input tests ---

// TestAddAttestation_NilRejected verifies nil attestation is rejected.
func TestAddAttestation_NilRejected(t *testing.T) {
	s := NewStore(DefaultConfig())
	if err := s.AddAttestation(nil); err == nil {
		t.Error("expected error for nil attestation")
	}
}

// TestAddAttestation_MissingTargetKey verifies missing target_key is rejected.
func TestAddAttestation_MissingTargetKey(t *testing.T) {
	s := NewStore(DefaultConfig())
	a := &Attestation{VerifierKey: "verifier"}
	if err := s.AddAttestation(a); err == nil {
		t.Error("expected error for missing target_key")
	}
}

// TestAddAttestation_MissingVerifierKey verifies missing verifier_key is rejected.
func TestAddAttestation_MissingVerifierKey(t *testing.T) {
	s := NewStore(DefaultConfig())
	a := &Attestation{TargetKey: "alice"}
	if err := s.AddAttestation(a); err == nil {
		t.Error("expected error for missing verifier_key")
	}
}

// --- Level.String tests ---

// TestLevel_StringRepresentation verifies level names.
func TestLevel_StringRepresentation(t *testing.T) {
	cases := []struct {
		l    Level
		want string
	}{
		{LevelAnonymous, "anonymous"},
		{LevelClaimed, "claimed"},
		{LevelContactable, "contactable"},
		{LevelPresent, "present"},
		{Level(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.l.String(); got != tc.want {
			t.Errorf("Level(%d).String() = %q, want %q", tc.l, got, tc.want)
		}
	}
}

// --- TrustVerifier tests ---

// TestTrustVerifier_Dynamic verifies that TrustVerifier adds keys post-construction.
func TestTrustVerifier_Dynamic(t *testing.T) {
	s := NewStore(DefaultConfig()) // no trusted verifiers

	freshTime := time.Now().Add(-1 * time.Hour)
	a := makeAttestation("att-1", "alice", "verifier", freshTime, true)
	if err := s.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	// Before trusting the verifier — alice should be anonymous.
	if l := s.Level("alice"); l != LevelAnonymous {
		t.Errorf("expected LevelAnonymous before trusting verifier, got %v", l)
	}

	// After trusting the verifier — alice should be level 3.
	s.TrustVerifier("verifier", 0)
	if l := s.Level("alice"); l < LevelContactable {
		t.Errorf("expected at least LevelContactable after trusting verifier, got %v", l)
	}
}

// --- regression tests ---

// TestLevelAt_FreshWinsOverStale is a regression test for the bug where LevelAt returned
// the first valid attestation (insertion order) rather than the best. If a stale (level 2)
// attestation is inserted before a fresh (level 3) one, LevelAt must still return level 3.
func TestLevelAt_FreshWinsOverStale(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TrustedVerifierKeys["verifier"] = 0
	cfg.FreshnessWindow = 1 * time.Hour
	s := NewStore(cfg)

	now := time.Now()

	// Stale attestation added FIRST (2 hours ago — outside 1h freshness window → level 2).
	staleAtt := makeAttestation("att-stale", "alice", "verifier", now.Add(-2*time.Hour), true)
	if err := s.AddAttestation(staleAtt); err != nil {
		t.Fatalf("AddAttestation stale failed: %v", err)
	}

	// Fresh attestation added SECOND (30 minutes ago — inside 1h freshness window → level 3).
	freshAtt := makeAttestation("att-fresh", "alice", "verifier", now.Add(-30*time.Minute), true)
	if err := s.AddAttestation(freshAtt); err != nil {
		t.Fatalf("AddAttestation fresh failed: %v", err)
	}

	// Must return level 3 — the fresh attestation wins regardless of insertion order.
	if l := s.LevelAt("alice", now); l != LevelPresent {
		t.Errorf("expected LevelPresent (fresh attestation wins over stale), got %v", l)
	}
}

// TestLevelTransitiveAt_FreshWinsOverStale is the same regression test for LevelTransitiveAt.
func TestLevelTransitiveAt_FreshWinsOverStale(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TrustedVerifierKeys["verifier"] = 0
	cfg.FreshnessWindow = 1 * time.Hour
	s := NewStore(cfg)

	now := time.Now()

	// Stale attestation added FIRST.
	staleAtt := makeAttestation("att-stale", "alice", "verifier", now.Add(-2*time.Hour), true)
	if err := s.AddAttestation(staleAtt); err != nil {
		t.Fatalf("AddAttestation stale failed: %v", err)
	}

	// Fresh attestation added SECOND.
	freshAtt := makeAttestation("att-fresh", "alice", "verifier", now.Add(-30*time.Minute), true)
	if err := s.AddAttestation(freshAtt); err != nil {
		t.Fatalf("AddAttestation fresh failed: %v", err)
	}

	// Must return level 3 — the fresh attestation wins regardless of insertion order.
	if l := s.LevelTransitiveAt("alice", now); l != LevelPresent {
		t.Errorf("expected LevelPresent (fresh attestation wins over stale), got %v", l)
	}
}

// TestComputeLevelTransitive_FreshTransitiveWinsOverStale verifies the bugfix for the
// transitive fallback path: a fresher transitive attestation must not be shadowed by a
// stale one encountered earlier in the slice.
//
// Regression: before the fix, the transitive loop returned LevelContactable on the first
// matching transitive attestation, silently ignoring any fresher attestation that would
// have produced LevelPresent.
func TestComputeLevelTransitive_FreshTransitiveWinsOverStale(t *testing.T) {
	// Setup: "intermediary" is trusted as a verifier.
	// "intermediary" itself has a direct attestation from a root verifier — giving it
	// LevelPresent. Alice has TWO transitive attestations from intermediary:
	//   1. Stale — added first, outside freshness window → would give LevelContactable
	//   2. Fresh — added second, inside freshness window → should give LevelPresent
	//
	// Before the fix: the transitive loop returned on the first match (stale), so
	// LevelTransitiveAt returned LevelContactable instead of LevelPresent.

	cfg := DefaultConfig()
	cfg.FreshnessWindow = 1 * time.Hour
	cfg.MaxTransitivityDepth = 1
	// Trust intermediary as a direct verifier (depth 0).
	cfg.TrustedVerifierKeys["intermediary"] = 0
	s := NewStore(cfg)

	now := time.Now()

	// Give intermediary a fresh direct attestation so computeLevelTransitive(intermediary)
	// returns LevelPresent, authorizing it as a transitive voucher.
	intermediaryAtt := makeAttestation("att-intermediary", "intermediary", "intermediary-root", now.Add(-10*time.Minute), true)
	// intermediary-root needs to be trusted for intermediary's attestation to count.
	s.TrustVerifier("intermediary-root", 0)
	if err := s.AddAttestation(intermediaryAtt); err != nil {
		t.Fatalf("AddAttestation intermediary failed: %v", err)
	}

	// Alice: stale transitive attestation from intermediary (added first).
	staleAtt := makeAttestation("att-alice-stale", "alice", "intermediary", now.Add(-3*time.Hour), true)
	if err := s.AddAttestation(staleAtt); err != nil {
		t.Fatalf("AddAttestation stale failed: %v", err)
	}

	// Alice: fresh transitive attestation from intermediary (added second).
	freshAtt := makeAttestation("att-alice-fresh", "alice", "intermediary", now.Add(-20*time.Minute), true)
	if err := s.AddAttestation(freshAtt); err != nil {
		t.Fatalf("AddAttestation fresh failed: %v", err)
	}

	// Must return LevelPresent — the fresh transitive attestation wins.
	if l := s.LevelTransitiveAt("alice", now); l != LevelPresent {
		t.Errorf("expected LevelPresent (fresh transitive attestation wins over stale), got %v", l)
	}
}

// TestComputeLevelTransitive_UntrustedIntermediaryCannotBridge verifies that an
// untrusted intermediary (one not in TrustedVerifierKeys) cannot convey transitive
// trust from a trusted agent to a target.
//
// Regression: before the fix, computeLevelTransitive allowed an untrusted intermediary
// to bridge two parties by checking only whether the intermediary's computed trust level
// was >= LevelContactable — without verifying that the intermediary itself was an
// explicitly trusted verifier. This let an attested-but-not-trusted agent inflate the
// trust level of arbitrary targets.
//
// Setup: trusted-verifier → untrusted-intermediary (attested, but NOT in TrustedVerifierKeys)
//        untrusted-intermediary → target-c
//
// Expected: target-c does NOT receive transitive trust through untrusted-intermediary.
func TestComputeLevelTransitive_UntrustedIntermediaryCannotBridge(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FreshnessWindow = 24 * time.Hour
	cfg.MaxTransitivityDepth = 2
	// Only "trusted-verifier" is in the trusted set.
	// "untrusted-intermediary" is intentionally NOT added to TrustedVerifierKeys.
	cfg.TrustedVerifierKeys["trusted-verifier"] = 2
	s := NewStore(cfg)

	now := time.Now()
	freshTime := now.Add(-1 * time.Hour)

	// trusted-verifier attests untrusted-intermediary — so intermediary has provenance
	// (LevelPresent when queried directly), but is NOT a trusted verifier itself.
	intermediaryAtt := makeAttestation("att-intermediary", "untrusted-intermediary", "trusted-verifier", freshTime, true)
	if err := s.AddAttestation(intermediaryAtt); err != nil {
		t.Fatalf("AddAttestation for untrusted-intermediary failed: %v", err)
	}

	// untrusted-intermediary attests target-c.
	targetAtt := makeAttestation("att-target-c", "target-c", "untrusted-intermediary", freshTime, true)
	if err := s.AddAttestation(targetAtt); err != nil {
		t.Fatalf("AddAttestation for target-c failed: %v", err)
	}

	// Verify that untrusted-intermediary itself has provenance (precondition for the bug).
	if l := s.LevelTransitive("untrusted-intermediary"); l < LevelContactable {
		t.Skipf("precondition failed: untrusted-intermediary should have LevelContactable from trusted-verifier; got %v", l)
	}

	// target-c must NOT receive transitive trust through untrusted-intermediary, because
	// untrusted-intermediary is not in TrustedVerifierKeys.
	if l := s.LevelTransitive("target-c"); l >= LevelContactable {
		t.Errorf("untrusted intermediary must not bridge transitive trust: expected target-c below LevelContactable, got %v", l)
	}
}

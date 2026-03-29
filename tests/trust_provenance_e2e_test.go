// Package tests — Trust v0.2 + Operator Provenance E2E integration tests.
//
// These tests exercise the full integrated stack across package boundaries:
// pkg/provenance (attestation store, challenge/response, file persistence),
// pkg/convention (executor, min_operator_level gate),
// pkg/trust (policy engine, envelope building, EvaluateCampfire).
//
// Tests do NOT rely on the cf binary — they call the public APIs directly.
// This is intentional: the scenarios here require deterministic time control
// and fine-grained state inspection that CLI invocations cannot provide cleanly.
package tests

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/provenance"
	"github.com/campfire-net/campfire/pkg/trust"
)

// ---- shared fixtures ----

// Fake 64-char hex keys used throughout these tests.
const (
	e2eOperatorKey  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	e2eVerifierKey  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	e2eIntermKey    = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	e2eCampfireID   = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	e2eCampfireKey2 = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

// e2eNoopTransport records sent messages; satisfies convention.ExecutorBackend.
type e2eNoopTransport struct {
	sent [][]string // captured tag slices per send
}

func (t *e2eNoopTransport) SendMessage(_ context.Context, _ string, _ []byte, tags []string, _ []string) (string, error) {
	t.sent = append(t.sent, tags)
	return "msg-e2e-" + strings.Join(tags, "-"), nil
}

func (t *e2eNoopTransport) SendCampfireKeySigned(_ context.Context, _ string, _ []byte, tags []string, _ []string) (string, error) {
	t.sent = append(t.sent, tags)
	return "msg-ck-" + strings.Join(tags, "-"), nil
}

func (t *e2eNoopTransport) ReadMessages(_ context.Context, _ string, _ []string) ([]convention.MessageRecord, error) {
	return nil, nil
}

func (t *e2eNoopTransport) SendFutureAndAwait(_ context.Context, _ string, _ []byte, _ []string, _ time.Duration) ([]byte, error) {
	return nil, nil
}

// e2eProvenanceChecker bridges provenance.Store to convention.ProvenanceChecker.
type e2eProvenanceChecker struct {
	store *provenance.Store
}

func (c *e2eProvenanceChecker) Level(key string) int {
	return int(c.store.Level(key))
}

// e2eFileProvenanceChecker bridges provenance.FileStore to convention.ProvenanceChecker.
type e2eFileProvenanceChecker struct {
	store *provenance.FileStore
}

func (c *e2eFileProvenanceChecker) Level(key string) int {
	return int(c.store.Level(key))
}

// buildGatedDecl builds a convention.Declaration with min_operator_level set to minLevel.
func buildGatedDecl(t *testing.T, minLevel int) *convention.Declaration {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"convention":         "peering",
		"version":            "0.3",
		"operation":          "core-peer-establish",
		"description":        "Establish a core peering link",
		"min_operator_level": minLevel,
		"produces_tags": []any{
			map[string]any{"tag": "peering:core", "cardinality": "exactly_one"},
		},
		"args": []any{
			map[string]any{
				"name":       "peer_key",
				"type":       "string",
				"required":   true,
				"max_length": 64,
			},
		},
		"antecedents": "none",
		"signing":     "member_key",
	})
	if err != nil {
		t.Fatalf("marshal gated decl: %v", err)
	}
	tags := []string{convention.ConventionOperationTag}
	decl, _, parseErr := convention.Parse(tags, payload, e2eOperatorKey, e2eCampfireKey2)
	if parseErr != nil {
		t.Fatalf("parse gated decl: %v", parseErr)
	}
	return decl
}

// ---- Scenario 1: Happy path ----

// TestTrustProvenanceE2E_HappyPath verifies the full happy path:
//
//  1. Create in-memory store with a trusted verifier.
//  2. Set operator self-claimed (level 1).
//  3. Issue challenge → validate response → create attestation (level 2 or 3).
//  4. Check that operator level is now ≥ 2.
//  5. Execute a gated operation (min_operator_level: 2) — must succeed.
//  6. Verify the resulting envelope contains operator_provenance.
func TestTrustProvenanceE2E_HappyPath(t *testing.T) {
	cfg := provenance.DefaultConfig()
	cfg.TrustedVerifierKeys = map[string]int{e2eVerifierKey: 0}
	cfg.FreshnessWindow = 7 * 24 * time.Hour

	store := provenance.NewStore(cfg)

	// Step 1: self-claim level 1.
	store.SetSelfClaimed(e2eOperatorKey)
	if store.Level(e2eOperatorKey) != provenance.LevelClaimed {
		t.Fatalf("expected LevelClaimed after SetSelfClaimed, got %v", store.Level(e2eOperatorKey))
	}

	// Step 2: issue challenge → validate → create attestation.
	challenger := provenance.NewChallenger()
	now := time.Now()
	ch, err := challenger.IssueChallenge("challenge-001", e2eVerifierKey, e2eOperatorKey, e2eCampfireID, now)
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}

	resp := &provenance.ChallengeResponse{
		AntecedentID:  ch.ID,
		ResponderKey:  e2eOperatorKey,
		MessageSender: e2eOperatorKey,
		TargetKey:     ch.TargetKey,
		Nonce:         ch.Nonce,
		ContactMethod: "email:operator@example.com",
		ProofType:     provenance.ProofCaptcha,
		ProofToken:    "captcha-solution-abc123",
		RespondedAt:   now.Add(10 * time.Second),
	}
	validated, err := challenger.ValidateResponse(resp, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("ValidateResponse: %v", err)
	}

	attest, err := provenance.CreateAttestation(store, "attest-001", validated, resp, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("CreateAttestation: %v", err)
	}
	if attest == nil {
		t.Fatal("CreateAttestation returned nil attestation")
	}
	if !attest.CoSigned {
		t.Error("expected CoSigned=true on attestation created from challenge/response")
	}

	// Step 3: verify level is now ≥ 2 (fresh attestation within window → level 3 = Present).
	level := store.Level(e2eOperatorKey)
	if level < provenance.LevelContactable {
		t.Errorf("expected level ≥ 2 after attestation, got %v (%d)", level, level)
	}
	t.Logf("operator level after attestation: %v (%d)", level, level)

	// Step 4: execute gated operation (min_operator_level: 2).
	decl := buildGatedDecl(t, 2)
	transport := &e2eNoopTransport{}
	checker := &e2eProvenanceChecker{store: store}
	exec := convention.NewExecutorForTest(transport, e2eOperatorKey).WithProvenance(checker)

	if err := exec.Execute(context.Background(), decl, e2eCampfireID, map[string]any{
		"peer_key": strings.Repeat("a", 64),
	}); err != nil {
		t.Fatalf("Execute gated op (should succeed): %v", err)
	}
	if len(transport.sent) != 1 {
		t.Errorf("expected 1 message sent, got %d", len(transport.sent))
	}

	// Step 5: verify envelope contains operator_provenance.
	levelInt := int(level)
	env := trust.BuildEnvelope(e2eCampfireID, trust.TrustAdopted, map[string]string{"result": "ok"},
		trust.WithOperatorProvenance(levelInt))
	if env.RuntimeComputed.OperatorProvenance == nil {
		t.Fatal("envelope missing operator_provenance")
	}
	if *env.RuntimeComputed.OperatorProvenance != levelInt {
		t.Errorf("envelope operator_provenance = %d, want %d", *env.RuntimeComputed.OperatorProvenance, levelInt)
	}
	t.Logf("envelope.runtime_computed.operator_provenance = %d", *env.RuntimeComputed.OperatorProvenance)
}

// ---- Scenario 2: Rejection path ----

// TestTrustProvenanceE2E_RejectionPath verifies that an operator with no attestation
// is rejected from a gated operation (min_operator_level: 2).
func TestTrustProvenanceE2E_RejectionPath(t *testing.T) {
	cfg := provenance.DefaultConfig()
	store := provenance.NewStore(cfg)
	// No attestation, no self-claim — operator is anonymous (level 0).

	level := store.Level(e2eOperatorKey)
	if level != provenance.LevelAnonymous {
		t.Errorf("expected LevelAnonymous, got %v", level)
	}

	decl := buildGatedDecl(t, 2)
	transport := &e2eNoopTransport{}
	checker := &e2eProvenanceChecker{store: store}
	exec := convention.NewExecutorForTest(transport, e2eOperatorKey).WithProvenance(checker)

	err := exec.Execute(context.Background(), decl, e2eCampfireID, map[string]any{
		"peer_key": strings.Repeat("a", 64),
	})
	if err == nil {
		t.Fatal("expected rejection for anonymous operator, got nil error")
	}
	if !strings.Contains(err.Error(), "operator provenance level") {
		t.Errorf("expected provenance level error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "requires level 2") {
		t.Errorf("expected 'requires level 2' in error, got: %v", err)
	}
	if len(transport.sent) != 0 {
		t.Errorf("expected no messages sent on rejection, got %d", len(transport.sent))
	}
	t.Logf("rejection error (correct): %v", err)
}

// ---- Scenario 3: Persistence round-trip ----

// TestTrustProvenanceE2E_PersistenceRoundTrip verifies that attestations survive
// a "restart" (closing and reopening the FileStore from disk).
func TestTrustProvenanceE2E_PersistenceRoundTrip(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "attestations.json")

	cfg := provenance.DefaultConfig()
	cfg.TrustedVerifierKeys = map[string]int{e2eVerifierKey: 0}
	cfg.FreshnessWindow = 7 * 24 * time.Hour

	// --- Phase 1: create attestation and write to disk. ---
	fs1, err := provenance.NewFileStore(storePath, cfg)
	if err != nil {
		t.Fatalf("NewFileStore (phase 1): %v", err)
	}

	now := time.Now()

	// Self-claim.
	if err := fs1.SetSelfClaimed(e2eOperatorKey); err != nil {
		t.Fatalf("SetSelfClaimed: %v", err)
	}

	// Issue and validate a challenge.
	challenger := provenance.NewChallenger()
	ch, err := challenger.IssueChallenge("challenge-persist-001", e2eVerifierKey, e2eOperatorKey, e2eCampfireID, now)
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	resp := &provenance.ChallengeResponse{
		AntecedentID:  ch.ID,
		ResponderKey:  e2eOperatorKey,
		MessageSender: e2eOperatorKey,
		TargetKey:     ch.TargetKey,
		Nonce:         ch.Nonce,
		ContactMethod: "email:persist@example.com",
		ProofType:     provenance.ProofTOTP,
		ProofToken:    "123456",
		RespondedAt:   now.Add(5 * time.Second),
	}
	validated, err := challenger.ValidateResponse(resp, now.Add(5*time.Second))
	if err != nil {
		t.Fatalf("ValidateResponse: %v", err)
	}
	if _, err := provenance.CreateAttestation(fs1, "attest-persist-001", validated, resp, now.Add(5*time.Second)); err != nil {
		t.Fatalf("CreateAttestation: %v", err)
	}

	level1 := fs1.Level(e2eOperatorKey)
	if level1 < provenance.LevelContactable {
		t.Fatalf("phase 1: expected level ≥ 2, got %v", level1)
	}
	t.Logf("phase 1 level: %v", level1)

	// --- Phase 2: "restart" — open a new FileStore from the same path. ---
	fs2, err := provenance.NewFileStore(storePath, cfg)
	if err != nil {
		t.Fatalf("NewFileStore (phase 2): %v", err)
	}

	level2 := fs2.Level(e2eOperatorKey)
	if level2 < provenance.LevelContactable {
		t.Errorf("phase 2 (after reload): expected level ≥ 2, got %v", level2)
	} else {
		t.Logf("phase 2 level (reloaded): %v — persistence verified", level2)
	}

	// Verify attestations are present in the reloaded store.
	attests := fs2.Attestations(e2eOperatorKey)
	if len(attests) == 0 {
		t.Error("phase 2: no attestations found after reload")
	} else {
		t.Logf("phase 2: %d attestation(s) loaded from disk", len(attests))
	}

	// Level computation should still work on the reloaded store.
	checker := &e2eFileProvenanceChecker{store: fs2}
	decl := buildGatedDecl(t, 2)
	transport := &e2eNoopTransport{}
	exec := convention.NewExecutorForTest(transport, e2eOperatorKey).WithProvenance(checker)

	if err := exec.Execute(context.Background(), decl, e2eCampfireID, map[string]any{
		"peer_key": strings.Repeat("b", 64),
	}); err != nil {
		t.Errorf("gated op on reloaded store: %v", err)
	}
}

// ---- Scenario 4: Trust evaluation on join with divergent conventions ----

// TestTrustProvenanceE2E_TrustDivergentOnJoin verifies that joining a campfire
// with mismatched convention fingerprints produces TrustDivergent (not spurious
// TrustCompatible). This validates the fingerprint fix in pkg/trust/policy.go.
func TestTrustProvenanceE2E_TrustDivergentOnJoin(t *testing.T) {
	// Build a "local" policy engine seeded with one convention version.
	localPayload, err := json.Marshal(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.3",
		"operation":   "post",
		"description": "Publish a social post (local version)",
		"produces_tags": []any{
			map[string]any{"tag": "social:post", "cardinality": "exactly_one"},
		},
		"args":        []any{},
		"antecedents": "none",
		"signing":     "member_key",
	})
	if err != nil {
		t.Fatalf("marshal local decl: %v", err)
	}
	tags := []string{convention.ConventionOperationTag}
	localDecl, _, err := convention.Parse(tags, localPayload, e2eOperatorKey, e2eCampfireKey2)
	if err != nil {
		t.Fatalf("parse local decl: %v", err)
	}

	pe := trust.NewPolicyEngine()
	pe.SeedConventions([]*convention.Declaration{localDecl})
	pe.MarkInitialized()

	// Build a "remote" declaration for the same convention:operation but with
	// a different description — this changes the semantic fingerprint.
	remotePayload, err := json.Marshal(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.3",
		"operation":   "post",
		"description": "Publish a social post (MODIFIED remote version — fingerprint differs)",
		"produces_tags": []any{
			// Change: add a new tag rule that the local version doesn't have.
			map[string]any{"tag": "social:post", "cardinality": "exactly_one"},
			map[string]any{"tag": "topic:*", "cardinality": "zero_to_many"},
		},
		"args":        []any{},
		"antecedents": "none",
		"signing":     "member_key",
	})
	if err != nil {
		t.Fatalf("marshal remote decl: %v", err)
	}
	remoteDecl, _, err := convention.Parse(tags, remotePayload, e2eVerifierKey, e2eCampfireKey2)
	if err != nil {
		t.Fatalf("parse remote decl: %v", err)
	}

	// Evaluate the incoming campfire's conventions.
	status, _ := pe.EvaluateCampfire([]*convention.Declaration{remoteDecl})
	if status != trust.TrustDivergent {
		t.Errorf("expected TrustDivergent for mismatched convention fingerprint, got %v", status)
	}
	t.Logf("EvaluateCampfire result: %v (correct — divergent, not spurious compatible)", status)

	// Also verify the per-declaration report via CompareCampfireDeclarations.
	report := pe.CompareCampfireDeclarations([]*convention.Declaration{remoteDecl})
	if report.OverallStatus != trust.TrustDivergent {
		t.Errorf("CompareCampfireDeclarations: expected TrustDivergent, got %v", report.OverallStatus)
	}
	if len(report.Conventions) != 1 {
		t.Fatalf("expected 1 convention in report, got %d", len(report.Conventions))
	}
	if report.Conventions[0].Status != trust.TrustDivergent {
		t.Errorf("per-convention status: expected TrustDivergent, got %v", report.Conventions[0].Status)
	}
	t.Logf("per-convention status: %v fingerprint_match=%v (local=%s... remote=%s...)",
		report.Conventions[0].Status,
		report.Conventions[0].FingerprintMatch,
		safePrefix(report.Conventions[0].LocalFingerprint, 12),
		safePrefix(report.Conventions[0].RemoteFingerprint, 12),
	)
}

// ---- Scenario 5: Transitive trust E2E ----

// TestTrustProvenanceE2E_TransitiveTrust verifies the transitive trust computation:
//
//  1. Set up a chain: trusted verifier (V) → attestation for operator (O).
//  2. Verify transitive level for O is ≥ 2.
//  3. Introduce an untrusted intermediary (U): attest O via U (not in trusted set).
//  4. Verify that U cannot bridge trust: LevelTransitive for a fresh operator (O2)
//     attested only via U remains at level 0 (anonymous).
func TestTrustProvenanceE2E_TransitiveTrust(t *testing.T) {
	cfg := provenance.DefaultConfig()
	cfg.TrustedVerifierKeys = map[string]int{
		e2eVerifierKey: 1, // depth 1: V is trusted for 1 hop
	}
	cfg.MaxTransitivityDepth = 1
	cfg.FreshnessWindow = 7 * 24 * time.Hour

	store := provenance.NewStore(cfg)

	now := time.Now()

	// Part A: direct attestation from trusted verifier V → operator O.
	// This should produce level 2 or 3.
	attest1 := &provenance.Attestation{
		ID:          "direct-attest-001",
		TargetKey:   e2eOperatorKey,
		VerifierKey: e2eVerifierKey, // trusted verifier
		Nonce:       "nonce-a",
		ProofType:   provenance.ProofCaptcha,
		VerifiedAt:  now,
		CoSigned:    true,
	}
	if err := store.AddAttestation(attest1); err != nil {
		t.Fatalf("AddAttestation (direct): %v", err)
	}

	level := store.LevelTransitiveAt(e2eOperatorKey, now)
	if level < provenance.LevelContactable {
		t.Errorf("expected level ≥ 2 via trusted verifier, got %v", level)
	}
	t.Logf("direct attestation level: %v", level)

	// Part B: untrusted intermediary (U) attests a second operator (O2).
	// U is NOT in the trusted verifier set. O2 should remain at level 0.
	untrustedIntermKey := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffaaaa"
	o2Key := "1111111111111111111111111111111111111111111111111111111111111111"

	attest2 := &provenance.Attestation{
		ID:          "untrusted-attest-001",
		TargetKey:   o2Key,
		VerifierKey: untrustedIntermKey, // NOT in trusted set
		Nonce:       "nonce-b",
		ProofType:   provenance.ProofCaptcha,
		VerifiedAt:  now,
		CoSigned:    true,
	}
	if err := store.AddAttestation(attest2); err != nil {
		t.Fatalf("AddAttestation (untrusted): %v", err)
	}

	levelO2 := store.LevelTransitiveAt(o2Key, now)
	if levelO2 >= provenance.LevelContactable {
		t.Errorf("expected level < 2 for O2 attested via untrusted intermediary, got %v", levelO2)
	}
	t.Logf("O2 level via untrusted intermediary: %v (correctly blocked)", levelO2)

	// Part C: verify that the untrusted intermediary also cannot chain through
	// the trusted verifier. Give U an attestation from V, but since U is not
	// in the trusted set with depth 1, O2 should still be blocked.
	attestVtoU := &provenance.Attestation{
		ID:          "v-to-u-001",
		TargetKey:   untrustedIntermKey,
		VerifierKey: e2eVerifierKey, // V attests U
		Nonce:       "nonce-c",
		ProofType:   provenance.ProofHardware,
		VerifiedAt:  now,
		CoSigned:    true,
	}
	if err := store.AddAttestation(attestVtoU); err != nil {
		t.Fatalf("AddAttestation (V→U): %v", err)
	}

	// O2 is attested by U. U is attested by V. But U is not in TrustedVerifierKeys
	// so it cannot forward trust transitively.
	levelO2After := store.LevelTransitiveAt(o2Key, now)
	if levelO2After >= provenance.LevelContactable {
		t.Errorf("expected O2 blocked even after V→U attestation (U not in trusted set), got %v", levelO2After)
	}
	t.Logf("O2 level after V→U attest (still blocked, correct): %v", levelO2After)

	// Part D: verify that directly trusted chain still works for operator O.
	levelOFinal := store.LevelTransitiveAt(e2eOperatorKey, now)
	if levelOFinal < provenance.LevelContactable {
		t.Errorf("direct trust chain broken: O expected level ≥ 2, got %v", levelOFinal)
	}
	t.Logf("O final level (direct chain intact): %v", levelOFinal)
}

// ---- Scenario 5b: Proof validation — empty/unknown proof_type rejected ----

// TestTrustProvenanceE2E_ProofValidation verifies that attestations with empty
// or unknown proof_type are rejected by both ValidateResponse and CreateAttestation.
func TestTrustProvenanceE2E_ProofValidation(t *testing.T) {
	cfg := provenance.DefaultConfig()
	cfg.TrustedVerifierKeys = map[string]int{e2eVerifierKey: 0}
	store := provenance.NewStore(cfg)
	challenger := provenance.NewChallenger()
	now := time.Now()

	// Sub-test: empty proof_type rejected by ValidateResponse.
	t.Run("EmptyProofTypeRejectedByValidateResponse", func(t *testing.T) {
		ch, err := challenger.IssueChallenge("challenge-proof-empty", e2eVerifierKey, e2eOperatorKey, e2eCampfireID, now)
		if err != nil {
			t.Fatalf("IssueChallenge: %v", err)
		}
		resp := &provenance.ChallengeResponse{
			AntecedentID:  ch.ID,
			ResponderKey:  e2eOperatorKey,
			MessageSender: e2eOperatorKey,
			TargetKey:     ch.TargetKey,
			Nonce:         ch.Nonce,
			ProofType:     "", // empty — should be rejected
			ProofToken:    "any-token",
			RespondedAt:   now.Add(5 * time.Second),
		}
		_, err = challenger.ValidateResponse(resp, now.Add(5*time.Second))
		if err == nil {
			t.Fatal("expected error for empty proof_type, got nil")
		}
		if !errors.Is(err, provenance.ErrEmptyProofType) {
			t.Errorf("expected ErrEmptyProofType, got: %v", err)
		}
	})

	// Sub-test: unknown proof_type rejected by ValidateResponse.
	t.Run("UnknownProofTypeRejectedByValidateResponse", func(t *testing.T) {
		ch, err := challenger.IssueChallenge("challenge-proof-unknown", e2eVerifierKey, e2eOperatorKey, e2eCampfireID, now.Add(time.Second))
		if err != nil {
			t.Fatalf("IssueChallenge: %v", err)
		}
		resp := &provenance.ChallengeResponse{
			AntecedentID:  ch.ID,
			ResponderKey:  e2eOperatorKey,
			MessageSender: e2eOperatorKey,
			TargetKey:     ch.TargetKey,
			Nonce:         ch.Nonce,
			ProofType:     "unknown-type", // unrecognized
			ProofToken:    "any-token",
			RespondedAt:   now.Add(10 * time.Second),
		}
		_, err = challenger.ValidateResponse(resp, now.Add(10*time.Second))
		if err == nil {
			t.Fatal("expected error for unknown proof_type, got nil")
		}
		if !errors.Is(err, provenance.ErrUnknownProofType) {
			t.Errorf("expected ErrUnknownProofType, got: %v", err)
		}
	})

	// Sub-test: empty proof_token rejected by CreateAttestation.
	t.Run("EmptyProofTokenRejectedByCreateAttestation", func(t *testing.T) {
		// Build a manual challenge and response bypassing ValidateResponse.
		ch := &provenance.Challenge{
			ID:               "challenge-no-token",
			InitiatorKey:     e2eVerifierKey,
			TargetKey:        e2eOperatorKey,
			Nonce:            "nonce-xyz",
			CallbackCampfire: e2eCampfireID,
			IssuedAt:         now,
		}
		resp := &provenance.ChallengeResponse{
			AntecedentID:  ch.ID,
			ResponderKey:  e2eOperatorKey,
			MessageSender: e2eOperatorKey,
			TargetKey:     ch.TargetKey,
			Nonce:         ch.Nonce,
			ProofType:     provenance.ProofCaptcha,
			ProofToken:    "", // empty token — should be rejected
			RespondedAt:   now.Add(5 * time.Second),
		}
		_, err := provenance.CreateAttestation(store, "attest-no-token", ch, resp, now.Add(5*time.Second))
		if err == nil {
			t.Fatal("expected error for empty proof_token in CreateAttestation, got nil")
		}
		if !errors.Is(err, provenance.ErrEmptyProofToken) {
			t.Errorf("expected ErrEmptyProofToken, got: %v", err)
		}
	})
}

// ---- Scenario 5c: Co-signed cap ----

// TestTrustProvenanceE2E_CoSignedCap verifies that non-co-signed attestations
// cannot elevate an operator above level 1.
func TestTrustProvenanceE2E_CoSignedCap(t *testing.T) {
	cfg := provenance.DefaultConfig()
	cfg.TrustedVerifierKeys = map[string]int{e2eVerifierKey: 0}
	cfg.FreshnessWindow = 7 * 24 * time.Hour
	store := provenance.NewStore(cfg)

	now := time.Now()

	// Add a non-co-signed attestation.
	attest := &provenance.Attestation{
		ID:          "non-cosigned-attest",
		TargetKey:   e2eOperatorKey,
		VerifierKey: e2eVerifierKey,
		Nonce:       "nonce-ncs",
		ProofType:   provenance.ProofCaptcha,
		VerifiedAt:  now,
		CoSigned:    false, // NOT co-signed
	}
	err := store.AddAttestation(attest)
	// ErrNotCoSigned is a warning, not a fatal error.
	if err != nil && !errors.Is(err, provenance.ErrNotCoSigned) {
		t.Fatalf("AddAttestation: unexpected error: %v", err)
	}

	// Level should be 0 (anonymous) — non-co-signed attestations cannot raise level.
	level := store.LevelAt(e2eOperatorKey, now)
	if level >= provenance.LevelContactable {
		t.Errorf("expected level < 2 (non-co-signed capped), got %v", level)
	}
	t.Logf("level with non-co-signed attestation: %v (correctly capped)", level)

	// Now add self-claim — level should become 1.
	store.SetSelfClaimed(e2eOperatorKey)
	levelAfterClaim := store.LevelAt(e2eOperatorKey, now)
	if levelAfterClaim != provenance.LevelClaimed {
		t.Errorf("expected LevelClaimed after SetSelfClaimed, got %v", levelAfterClaim)
	}

	// Gated op at level 2 should still be rejected.
	decl := buildGatedDecl(t, 2)
	transport := &e2eNoopTransport{}
	checker := &e2eProvenanceChecker{store: store}
	exec := convention.NewExecutorForTest(transport, e2eOperatorKey).WithProvenance(checker)
	if err := exec.Execute(context.Background(), decl, e2eCampfireID, map[string]any{
		"peer_key": strings.Repeat("c", 64),
	}); err == nil {
		t.Error("expected rejection for non-co-signed attestation (level 1), got nil")
	} else {
		t.Logf("correctly rejected: %v", err)
	}
}

// ---- helpers ----

// safePrefix returns the first n chars of s, or all of s if shorter.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

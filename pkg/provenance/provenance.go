// Package provenance implements the Operator Provenance Convention v0.1.
//
// It stores attestations, computes provenance levels (0–3), handles
// transitivity with depth limits, and evaluates freshness.
//
// Refs: Operator Provenance Convention v0.1 §4, §6, §7, §8.2, §10.5.
package provenance

import (
	"errors"
	"sync"
	"time"
)

// Level is an operator provenance level (0–3).
// See Operator Provenance Convention v0.1 §4.
type Level int

const (
	// LevelAnonymous (0): only a valid keypair. Nothing is known about the operator.
	LevelAnonymous Level = 0
	// LevelClaimed (1): self-asserted operator identity. Tainted — not verified.
	LevelClaimed Level = 1
	// LevelContactable (2): verified by a challenge/response exchange. A human responded.
	LevelContactable Level = 2
	// LevelPresent (3): same as level 2 but the attestation is fresh (within freshness window).
	LevelPresent Level = 3
)

// String returns the level name.
func (l Level) String() string {
	switch l {
	case LevelAnonymous:
		return "anonymous"
	case LevelClaimed:
		return "claimed"
	case LevelContactable:
		return "contactable"
	case LevelPresent:
		return "present"
	default:
		return "unknown"
	}
}

// ProofType is the type of human-presence proof in an attestation.
// See Operator Provenance Convention v0.1 §5.3.
type ProofType string

const (
	ProofCaptcha   ProofType = "captcha"
	ProofTOTP      ProofType = "totp"
	ProofHardware  ProofType = "hardware"
	ProofSMS       ProofType = "sms"
	ProofEmailLink ProofType = "email-link"
)

// Attestation records the result of a verification exchange.
// See Operator Provenance Convention v0.1 §6.2.
type Attestation struct {
	// ID is a unique identifier for this attestation (message ID or derived).
	ID string

	// TargetKey is the public key that was verified.
	TargetKey string

	// VerifierKey is the public key of the entity that issued the challenge.
	VerifierKey string

	// Nonce is the challenge nonce (proves this is a response to a specific challenge).
	Nonce string

	// ContactMethod is the contact URI that was tested.
	ContactMethod string

	// ProofType is the type of human-presence proof.
	ProofType ProofType

	// ProofProvenance is the issuer signature or attestation for the proof.
	ProofProvenance string

	// VerifiedAt is when the verification completed.
	VerifiedAt time.Time

	// CoSigned indicates whether both operator and verifier keys signed the attestation.
	// Attestations SHOULD be co-signed. Non-co-signed attestations are flagged.
	// See Operator Provenance Convention v0.1 §6.2.
	CoSigned bool

	// Revoked indicates whether this attestation has been revoked.
	Revoked bool
}

// StoreConfig holds configuration for the attestation store.
type StoreConfig struct {
	// FreshnessWindow is the time window within which an attestation is considered
	// "fresh" for level 3 (Present). Zero means level 3 is never granted.
	FreshnessWindow time.Duration

	// MaxTransitivityDepth is the maximum number of hops for transitive attestation
	// resolution. 0 means no transitive attestations are accepted.
	// Default per spec: 1 (accept attestations from directly trusted verifiers only).
	MaxTransitivityDepth int

	// AllowSelfAttestation, if true, allows attestations where VerifierKey == TargetKey.
	// The default (false) rejects self-attestations per §10.5.
	AllowSelfAttestation bool

	// MaxAbsoluteAgeForLevel2 is the maximum age for a level 2 attestation before
	// it is treated as level 1. Zero means no absolute maximum.
	MaxAbsoluteAgeForLevel2 time.Duration

	// TrustedVerifierKeys is the set of verifier keys trusted for transitive attestations.
	// Key: verifier public key (hex/base64), value: trust depth level for that verifier.
	// Only attestations whose VerifierKey is in this map are accepted transitively.
	TrustedVerifierKeys map[string]int
}

// DefaultConfig returns the default store configuration.
func DefaultConfig() StoreConfig {
	return StoreConfig{
		FreshnessWindow:      7 * 24 * time.Hour, // 7-day default per spec §4.4
		MaxTransitivityDepth: 1,                  // default per spec §7.2
		AllowSelfAttestation: false,              // default per spec §10.5
		TrustedVerifierKeys:  make(map[string]int),
	}
}

// Store is an in-memory attestation store with configurable freshness and transitivity.
type Store struct {
	mu     sync.RWMutex
	config StoreConfig

	// attestations: targetKey → list of attestations for that key.
	attestations map[string][]*Attestation

	// selfClaimed: set of keys that have published a self-asserted profile (level 1).
	selfClaimed map[string]bool
}

// NewStore creates a new in-memory attestation store with the given config.
func NewStore(cfg StoreConfig) *Store {
	if cfg.TrustedVerifierKeys == nil {
		cfg.TrustedVerifierKeys = make(map[string]int)
	}
	return &Store{
		config:       cfg,
		attestations: make(map[string][]*Attestation),
		selfClaimed:  make(map[string]bool),
	}
}

// AddAttestation stores an attestation in the store.
// Returns ErrSelfAttestation if verifier_key == target_key and self-attestation is not allowed.
// Returns ErrNotCoSigned if the attestation is not co-signed (flagged but stored per spec).
func (s *Store) AddAttestation(a *Attestation) error {
	if a == nil {
		return errors.New("provenance: nil attestation")
	}
	if a.TargetKey == "" {
		return errors.New("provenance: attestation missing target_key")
	}
	if a.VerifierKey == "" {
		return errors.New("provenance: attestation missing verifier_key")
	}

	// Self-attestation rejection: §10.5
	if a.VerifierKey == a.TargetKey && !s.config.AllowSelfAttestation {
		return ErrSelfAttestation
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.attestations[a.TargetKey] = append(s.attestations[a.TargetKey], a)

	if !a.CoSigned {
		return ErrNotCoSigned
	}

	return nil
}

// SetSelfClaimed marks a key as having published a self-asserted profile (level 1).
// See Operator Provenance Convention v0.1 §4.2.
func (s *Store) SetSelfClaimed(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selfClaimed[key] = true
}

// Level computes the provenance level for a key.
// The computation is local — based only on attestations in this store.
// See Operator Provenance Convention v0.1 §8.2.
func (s *Store) Level(key string) Level {
	return s.LevelAt(key, time.Now())
}

// LevelAt computes the provenance level for a key at a specific point in time.
// This allows deterministic testing with fixed timestamps.
func (s *Store) LevelAt(key string, now time.Time) Level {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check for valid attestations (level 2+).
	// Iterate ALL attestations and track the maximum level found — do not return
	// on the first match. An older stale attestation must not shadow a fresher one.
	best := LevelAnonymous
	attestations := s.attestations[key]
	for _, a := range attestations {
		if a.Revoked {
			continue
		}
		if a.VerifierKey == a.TargetKey && !s.config.AllowSelfAttestation {
			continue // self-attestation always rejected
		}

		// Must be a direct attestation or from a trusted verifier.
		if !s.isVerifierTrusted(a.VerifierKey, 0) {
			continue
		}

		// Check absolute max age for level 2.
		if s.config.MaxAbsoluteAgeForLevel2 > 0 {
			if now.Sub(a.VerifiedAt) > s.config.MaxAbsoluteAgeForLevel2 {
				continue // too old, treat as level 1
			}
		}

		// Level 3: fresh attestation (within freshness window).
		if s.config.FreshnessWindow > 0 && now.Sub(a.VerifiedAt) <= s.config.FreshnessWindow {
			best = LevelPresent
			continue // already at max — keep scanning but can't go higher
		}

		// Level 2: valid attestation exists.
		if best < LevelContactable {
			best = LevelContactable
		}
	}
	if best >= LevelContactable {
		return best
	}

	// Level 1: self-claimed profile exists.
	if s.selfClaimed[key] {
		return LevelClaimed
	}

	// Level 0: default.
	return LevelAnonymous
}

// LevelTransitive computes the provenance level for a key including transitive attestations.
// It checks attestations whose verifier_key is in the trusted verifier set, up to
// MaxTransitivityDepth hops. See Operator Provenance Convention v0.1 §7.
func (s *Store) LevelTransitive(key string) Level {
	return s.LevelTransitiveAt(key, time.Now())
}

// LevelTransitiveAt computes the transitive provenance level at a specific time.
func (s *Store) LevelTransitiveAt(key string, now time.Time) Level {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.computeLevelTransitive(key, now, 0)
}

// computeLevelTransitive is the internal recursive implementation.
// depth tracks the current transitivity depth to enforce MaxTransitivityDepth.
func (s *Store) computeLevelTransitive(key string, now time.Time, depth int) Level {
	// Iterate ALL attestations and track the maximum level found — do not return
	// on the first match. An older stale attestation must not shadow a fresher one.
	best := LevelAnonymous
	attestations := s.attestations[key]
	for _, a := range attestations {
		if a.Revoked {
			continue
		}
		if a.VerifierKey == a.TargetKey && !s.config.AllowSelfAttestation {
			continue
		}

		// Transitivity checks verifier_key (not campfire trust): §7.1.
		// The verifier key must be trusted at the current depth.
		if !s.isVerifierTrusted(a.VerifierKey, depth) {
			continue
		}

		// Check absolute max age.
		if s.config.MaxAbsoluteAgeForLevel2 > 0 {
			if now.Sub(a.VerifiedAt) > s.config.MaxAbsoluteAgeForLevel2 {
				continue
			}
		}

		// Level 3: fresh attestation.
		if s.config.FreshnessWindow > 0 && now.Sub(a.VerifiedAt) <= s.config.FreshnessWindow {
			best = LevelPresent
			continue // already at max — keep scanning but can't go higher
		}

		// Level 2: valid attestation exists.
		if best < LevelContactable {
			best = LevelContactable
		}
	}
	if best >= LevelContactable {
		return best
	}

	// Try transitive attestations: look at keys that can vouch for target.
	// Scan ALL transitive attestations and track the best level — do not return
	// on the first match. A fresher transitive attestation must not be shadowed
	// by a stale one encountered earlier.
	if depth < s.config.MaxTransitivityDepth {
		for _, a := range attestations {
			if a.Revoked {
				continue
			}
			if a.VerifierKey == a.TargetKey && !s.config.AllowSelfAttestation {
				continue
			}
			// Recurse: check if the verifier itself is provenance-elevated
			// to authorize transitive trust.
			// Depth increments for the next hop.
			transitiveLevel := s.computeLevelTransitive(a.VerifierKey, now, depth+1)
			if transitiveLevel >= LevelContactable {
				// Check absolute max age for this attestation.
				if s.config.MaxAbsoluteAgeForLevel2 > 0 {
					if now.Sub(a.VerifiedAt) > s.config.MaxAbsoluteAgeForLevel2 {
						continue
					}
				}
				// Fresh transitive attestation elevates to level 3; stale to level 2.
				if s.config.FreshnessWindow > 0 && now.Sub(a.VerifiedAt) <= s.config.FreshnessWindow {
					best = LevelPresent
				} else if best < LevelContactable {
					best = LevelContactable
				}
			}
		}
		if best >= LevelContactable {
			return best
		}
	}

	// Fall back to self-claimed.
	if s.selfClaimed[key] {
		return LevelClaimed
	}

	return LevelAnonymous
}

// isVerifierTrusted checks whether a verifier key is in the trusted set at a given depth.
// Depth 0 means direct trust (the key is directly trusted as a verifier).
func (s *Store) isVerifierTrusted(verifierKey string, depth int) bool {
	// At depth 0, check if the verifier is in the trusted set.
	if depth == 0 {
		_, ok := s.config.TrustedVerifierKeys[verifierKey]
		return ok
	}
	// At deeper depths, the verifier must be in the trusted set with a higher trust level.
	trustLevel, ok := s.config.TrustedVerifierKeys[verifierKey]
	return ok && trustLevel >= depth
}

// TrustVerifier adds a key to the trusted verifier set.
// depth is the maximum transitivity depth this verifier authorizes.
// depth=0 means direct attestations from this verifier are accepted.
// depth=1 means this verifier's attestations are accepted 1 hop away, etc.
func (s *Store) TrustVerifier(verifierKey string, depth int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.TrustedVerifierKeys[verifierKey] = depth
}

// Attestations returns all non-revoked attestations for a key.
func (s *Store) Attestations(key string) []*Attestation {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Attestation
	for _, a := range s.attestations[key] {
		if !a.Revoked {
			result = append(result, a)
		}
	}
	return result
}

// Revoke marks an attestation as revoked by its ID.
// Returns ErrAttestationNotFound if no attestation with that ID exists.
func (s *Store) Revoke(attestationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, attestations := range s.attestations {
		for _, a := range attestations {
			if a.ID == attestationID {
				a.Revoked = true
				return nil
			}
		}
	}
	return ErrAttestationNotFound
}

// Sentinel errors for the provenance package.
var (
	// ErrSelfAttestation is returned when an attestation where verifier_key == target_key
	// is rejected because AllowSelfAttestation is false. See §10.5.
	ErrSelfAttestation = errors.New("provenance: self-attestation rejected (verifier_key == target_key)")

	// ErrNotCoSigned is returned when an attestation is stored but flagged as not co-signed.
	// The attestation IS stored but at reduced trust. See §6.2.
	ErrNotCoSigned = errors.New("provenance: attestation not co-signed (flagged — accepted at reduced trust)")

	// ErrAttestationNotFound is returned when a revoke targets an unknown attestation ID.
	ErrAttestationNotFound = errors.New("provenance: attestation not found")
)

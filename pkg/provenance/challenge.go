// Package provenance — challenge/response verification flow.
//
// Implements the operator-challenge / operator-verify exchange defined in
// Operator Provenance Convention v0.1 §5, §12, §13.
package provenance

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// challengeNonceBytes is the size of the challenge nonce in bytes (32 bytes = 256-bit).
const challengeNonceBytes = 32

// challengeTTL is how long a challenge is considered valid before expiry.
// Spec does not mandate a specific TTL; 5 minutes matches the "wait" timeout in §13.
const challengeTTL = 5 * time.Minute

// challengeRateWindow is the sliding window for target-side rate limiting.
const challengeRateWindow = time.Hour

// challengeRateMax is the target-side limit: max 10 challenges/hour from all senders
// combined. See Operator Provenance Convention v0.1 §12.1.
const challengeRateMax = 10

// Challenge is a pending operator-challenge message, as defined in §12.1.
type Challenge struct {
	// ID is the unique message ID of the operator-challenge message.
	// The operator-verify response MUST reference this ID as its antecedent (§12.2).
	ID string

	// InitiatorKey is the public key of the entity that issued the challenge.
	InitiatorKey string

	// TargetKey is the public key of the operator being challenged.
	TargetKey string

	// Nonce is a cryptographically random 32-byte value (hex-encoded).
	Nonce string

	// CallbackCampfire is the campfire ID where the response should be sent.
	CallbackCampfire string

	// IssuedAt is when the challenge was created.
	IssuedAt time.Time
}

// ChallengeResponse is an operator-verify message that answers a challenge, §12.2.
type ChallengeResponse struct {
	// AntecedentID is the message ID of the challenge this response answers.
	// MUST match Challenge.ID. See §12.2.
	AntecedentID string

	// ResponderKey is the public key of the entity sending this response.
	// MUST match Challenge.TargetKey.
	ResponderKey string

	// MessageSender is the cryptographic sender key extracted from the campfire
	// message envelope (hex-encoded Ed25519 public key). Callers MUST populate
	// this from the transport layer before passing to ValidateResponse. It is
	// verified against TargetKey to prevent forged responses from other members.
	MessageSender string

	// TargetKey is echoed from the challenge. Runtime MUST verify match (§12.2).
	TargetKey string

	// Nonce is echoed from the challenge.
	Nonce string

	// ContactMethod is the URI where the challenge was received.
	ContactMethod string

	// ProofType is the type of human-presence proof included.
	ProofType ProofType

	// ProofToken is the proof itself (CAPTCHA solution, TOTP code, etc.).
	ProofToken string

	// ProofProvenance is the issuer signature or attestation for the proof.
	ProofProvenance string

	// RespondedAt is when the response was created.
	RespondedAt time.Time
}

// Challenger manages active challenges and target-side rate limiting.
// Thread-safe.
type Challenger struct {
	mu sync.Mutex

	// active: challengeID → *Challenge
	active map[string]*Challenge

	// targetTimestamps: targetKey → list of challenge receipt times within rate window.
	// Used for target-side rate limiting (§12.1).
	targetTimestamps map[string][]time.Time
}

// NewChallenger creates a new Challenger instance.
func NewChallenger() *Challenger {
	return &Challenger{
		active:           make(map[string]*Challenge),
		targetTimestamps: make(map[string][]time.Time),
	}
}

// PruneExpired evicts all challenges that have exceeded their TTL. It is safe
// to call at any time (e.g., from a background cleanup goroutine or at startup).
// For most callers, lazy eviction via IssueChallenge is sufficient; this method
// is provided for hosts that issue challenges infrequently and want deterministic
// cleanup.
func (c *Challenger) PruneExpired(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneExpiredChallenges(now)
}

// GenerateNonce creates a cryptographically random 32-byte nonce (hex-encoded).
func GenerateNonce() (string, error) {
	b := make([]byte, challengeNonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("provenance: failed to generate nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// IssueChallenge creates and registers a new operator-challenge.
//
// Returns ErrRateLimitExceeded if the target key has already received the maximum
// number of challenges in the current rate window (§12.1: 10 challenges/hour from
// all senders combined).
//
// The caller is responsible for assigning a unique message ID (e.g., a campfire
// message ID) to the returned Challenge before sending it to the target.
func (c *Challenger) IssueChallenge(id, initiatorKey, targetKey, callbackCampfire string, now time.Time) (*Challenge, error) {
	if id == "" {
		return nil, errors.New("provenance: challenge ID must not be empty")
	}
	if initiatorKey == "" {
		return nil, errors.New("provenance: challenge initiator_key must not be empty")
	}
	if targetKey == "" {
		return nil, errors.New("provenance: challenge target_key must not be empty")
	}
	if callbackCampfire == "" {
		return nil, errors.New("provenance: challenge callback_campfire must not be empty")
	}

	nonce, err := GenerateNonce()
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict expired challenges before adding a new one. This keeps the active
	// map bounded: at most one unexpired challenge per target per rate window,
	// and stale entries from offline/unresponsive targets are cleaned up here.
	c.pruneExpiredChallenges(now)

	// Target-side rate limiting: §12.1.
	// Count challenges received by this target within the rate window.
	c.pruneTargetTimestamps(targetKey, now)
	if len(c.targetTimestamps[targetKey]) >= challengeRateMax {
		return nil, ErrRateLimitExceeded
	}

	// Duplicate ID check: if a challenge with this ID is already active, reject rather
	// than silently overwriting it. A collision would let an attacker (or a buggy caller)
	// clobber a pending challenge — invalidating the original operator's nonce and
	// potentially hijacking the verification flow. IDs are caller-supplied (e.g., campfire
	// message IDs) and MUST be globally unique; a collision is always a bug or attack.
	if _, exists := c.active[id]; exists {
		return nil, ErrChallengeIDCollision
	}

	ch := &Challenge{
		ID:               id,
		InitiatorKey:     initiatorKey,
		TargetKey:        targetKey,
		Nonce:            nonce,
		CallbackCampfire: callbackCampfire,
		IssuedAt:         now,
	}

	c.active[id] = ch
	c.targetTimestamps[targetKey] = append(c.targetTimestamps[targetKey], now)

	return ch, nil
}

// pruneExpiredChallenges removes challenges from the active map that are past
// their TTL, then sweeps targetTimestamps for keys that have no remaining
// in-window timestamps. This is lazy eviction: called from IssueChallenge so
// that long-lived Challenger instances don't accumulate unanswered challenges
// without bound. Challenges that were never answered (target offline, etc.) are
// cleaned up here rather than waiting for a ValidateResponse call that may never
// arrive.
//
// Without the targetTimestamps sweep, unique target keys accumulate unboundedly
// in the rate-limit map across the lifetime of the process — a memory leak and
// a DoS vector (an attacker generating unique target keys fills the map without
// bound regardless of TTL expiry).
//
// Must be called with c.mu held.
func (c *Challenger) pruneExpiredChallenges(now time.Time) {
	for id, ch := range c.active {
		if now.Sub(ch.IssuedAt) > challengeTTL {
			delete(c.active, id)
		}
	}
	// Global sweep: remove targetTimestamps entries whose timestamps have all
	// fallen outside the rate window. pruneTargetTimestamps already handles the
	// delete-if-empty logic, so we just need to call it for every key.
	for targetKey := range c.targetTimestamps {
		c.pruneTargetTimestamps(targetKey, now)
	}
}

// pruneTargetTimestamps removes timestamps outside the rate window.
// Must be called with c.mu held.
func (c *Challenger) pruneTargetTimestamps(targetKey string, now time.Time) {
	cutoff := now.Add(-challengeRateWindow)
	ts := c.targetTimestamps[targetKey]
	var valid []time.Time
	for _, t := range ts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) == 0 {
		delete(c.targetTimestamps, targetKey)
	} else {
		c.targetTimestamps[targetKey] = valid
	}
}

// ValidateResponse checks that a ChallengeResponse correctly answers an active challenge.
//
// Checks performed (§12.2):
//  1. The antecedent message ID references an active, non-expired challenge.
//  2. The response target_key matches the challenge target_key.
//  3. The response nonce matches the challenge nonce.
//  4. The responder_key matches the challenge target_key (the operator signs their own response).
//
// Returns the matching Challenge on success. The challenge is removed from the active set
// (each challenge can be answered exactly once).
func (c *Challenger) ValidateResponse(resp *ChallengeResponse, now time.Time) (*Challenge, error) {
	if resp == nil {
		return nil, errors.New("provenance: nil response")
	}
	if resp.AntecedentID == "" {
		return nil, ErrMissingAntecedent
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	ch, ok := c.active[resp.AntecedentID]
	if !ok {
		return nil, ErrChallengeNotFound
	}

	// Expiry check.
	if now.Sub(ch.IssuedAt) > challengeTTL {
		delete(c.active, ch.ID)
		return nil, ErrChallengeExpired
	}

	// target_key echo check: runtime MUST verify match (§12.2).
	if resp.TargetKey != ch.TargetKey {
		return nil, fmt.Errorf("provenance: response target_key %q does not match challenge target_key %q", resp.TargetKey, ch.TargetKey)
	}

	// Nonce echo check.
	if resp.Nonce != ch.Nonce {
		return nil, fmt.Errorf("provenance: response nonce does not match challenge nonce")
	}

	// Responder must be the target operator.
	if resp.ResponderKey != ch.TargetKey {
		return nil, fmt.Errorf("provenance: responder_key %q does not match challenge target_key %q", resp.ResponderKey, ch.TargetKey)
	}

	// Cryptographic sender verification (§12.2): the campfire message envelope
	// sender MUST match the challenge target_key. This prevents any campfire
	// member from forging an operator-verify response on behalf of the target.
	// Callers populate MessageSender from the transport layer before calling
	// ValidateResponse. A missing MessageSender is an error — callers that omit
	// it cannot satisfy this check.
	if resp.MessageSender == "" {
		return nil, fmt.Errorf("provenance: message_sender not set — caller must populate from transport envelope before calling ValidateResponse")
	}
	if resp.MessageSender != ch.TargetKey {
		return nil, fmt.Errorf("provenance: message envelope sender %q does not match challenge target_key %q — forged response rejected", resp.MessageSender, ch.TargetKey)
	}

	// Proof validation (§5.3, §12.2): proof_type and proof_token are required fields.
	// An empty proof_type means there is no declared proof mechanism — the attestation
	// would carry no evidence of human presence and MUST be rejected.
	// An unknown proof_type is also rejected — accepting unrecognized types would allow
	// an attacker to submit arbitrary strings and bypass proof verification.
	// An empty proof_token means the proof itself is absent — regardless of what
	// proof_type claims, there is nothing to verify.
	// Even a non-empty proof_token must have the correct structural format for its
	// declared proof_type — garbage strings are rejected here. See §5.3.
	if resp.ProofType == "" {
		return nil, ErrEmptyProofType
	}
	if !validProofTypes[resp.ProofType] {
		return nil, ErrUnknownProofType
	}
	if resp.ProofToken == "" {
		return nil, ErrEmptyProofToken
	}
	if err := validateProofTokenFormat(resp.ProofType, resp.ProofToken); err != nil {
		return nil, err
	}

	// Consume the challenge (one-time use).
	delete(c.active, ch.ID)

	return ch, nil
}

// CreateAttestation builds an Attestation from a verified challenge/response pair and
// stores it in the given Store. The attestation is co-signed (CoSigned: true) because
// both parties participated in the exchange.
//
// Returns ErrSelfAttestation if initiator_key == target_key and the store does not
// allow self-attestation (§10.5).
//
// The attestationID should be derived from the response message ID in production.
func CreateAttestation(store AttestationStore, attestationID string, ch *Challenge, resp *ChallengeResponse, now time.Time) (*Attestation, error) {
	if store == nil {
		return nil, errors.New("provenance: nil store")
	}
	if attestationID == "" {
		return nil, errors.New("provenance: attestation ID must not be empty")
	}
	if ch == nil {
		return nil, errors.New("provenance: nil challenge")
	}
	if resp == nil {
		return nil, errors.New("provenance: nil response")
	}

	// Proof validation (§5.3, §12.2): defense-in-depth check mirroring the validation
	// in ValidateResponse. CreateAttestation may be called with a manually constructed
	// ChallengeResponse (e.g., in tests or future callers that bypass ValidateResponse),
	// so the proof invariants are enforced here too. An attestation built on an empty or
	// unknown proof_type, empty proof_token, or structurally invalid proof_token is not
	// a valid attestation.
	if resp.ProofType == "" {
		return nil, ErrEmptyProofType
	}
	if !validProofTypes[resp.ProofType] {
		return nil, ErrUnknownProofType
	}
	if resp.ProofToken == "" {
		return nil, ErrEmptyProofToken
	}
	if err := validateProofTokenFormat(resp.ProofType, resp.ProofToken); err != nil {
		return nil, err
	}

	a := &Attestation{
		ID:              attestationID,
		TargetKey:       ch.TargetKey,
		VerifierKey:     ch.InitiatorKey,
		Nonce:           ch.Nonce,
		ContactMethod:   resp.ContactMethod,
		ProofType:       resp.ProofType,
		ProofProvenance: resp.ProofProvenance,
		VerifiedAt:      now,
		CoSigned:        true, // challenge/response implies both parties signed
	}

	if err := store.AddAttestation(a); err != nil {
		return nil, err
	}

	return a, nil
}

// validProofTypes is the set of accepted proof_type values per §5.3.
// An attestation with an unrecognized proof_type is rejected — accepting unknown
// proof types would allow an attacker to smuggle unverifiable "proofs" past the
// validation layer.
var validProofTypes = map[ProofType]bool{
	ProofCaptcha:   true,
	ProofTOTP:      true,
	ProofHardware:  true,
	ProofSMS:       true,
	ProofEmailLink: true,
}

// proofTokenMinLenCaptcha is the minimum token length for CAPTCHA proof tokens.
// CAPTCHA solution tokens are opaque strings from an external service; >=16 chars
// distinguishes them from random short garbage while staying well below any real
// CAPTCHA provider's minimum token length.
const proofTokenMinLenCaptcha = 16

// proofTokenMinLenOpaque is the minimum token length for hardware and email-link
// proof tokens. Hardware attestation blobs and signed email-link tokens carry more
// entropy than CAPTCHA tokens; >=32 chars is a conservative lower bound.
const proofTokenMinLenOpaque = 32

// validateProofTokenFormat checks that proof_token has the correct structural format
// for the given proof_type. It does NOT verify the cryptographic authenticity of the
// token (that requires the issuing service), but it rejects clearly invalid tokens:
// random short strings, wrong character sets, whitespace-contaminated values.
// See Operator Provenance Convention v0.1 §5.3.
//
// Validation rules per proof_type:
//   - ProofTOTP:      exactly 6 or 8 ASCII decimal digits (RFC 6238 §4, §5.2)
//   - ProofSMS:       4-8 ASCII decimal digits (typical OTP code length)
//   - ProofCaptcha:   >=16 printable non-whitespace characters
//   - ProofHardware:  >=32 printable non-whitespace characters (attestation blob)
//   - ProofEmailLink: >=32 printable non-whitespace characters (signed redirect token)
func validateProofTokenFormat(pt ProofType, token string) error {
	switch pt {
	case ProofTOTP:
		// RFC 6238: TOTP codes are exactly 6 or 8 decimal digits.
		if len(token) != 6 && len(token) != 8 {
			return fmt.Errorf("%w: totp proof_token must be exactly 6 or 8 decimal digits, got %d chars", ErrInvalidProofToken, len(token))
		}
		for _, r := range token {
			if r < '0' || r > '9' {
				return fmt.Errorf("%w: totp proof_token must contain only decimal digits", ErrInvalidProofToken)
			}
		}
	case ProofSMS:
		// SMS OTP codes are 4-8 decimal digits.
		if len(token) < 4 || len(token) > 8 {
			return fmt.Errorf("%w: sms proof_token must be 4-8 decimal digits, got %d chars", ErrInvalidProofToken, len(token))
		}
		for _, r := range token {
			if r < '0' || r > '9' {
				return fmt.Errorf("%w: sms proof_token must contain only decimal digits", ErrInvalidProofToken)
			}
		}
	case ProofCaptcha:
		// CAPTCHA solution tokens are opaque strings from the CAPTCHA service.
		if len(token) < proofTokenMinLenCaptcha {
			return fmt.Errorf("%w: captcha proof_token too short (min %d chars, got %d)", ErrInvalidProofToken, proofTokenMinLenCaptcha, len(token))
		}
		for _, r := range token {
			// Reject ASCII control characters and whitespace (0x00-0x20, 0x7F).
			if r <= 0x20 || r == 0x7F {
				return fmt.Errorf("%w: captcha proof_token must not contain whitespace or control characters", ErrInvalidProofToken)
			}
		}
	case ProofHardware:
		// Hardware key attestation blobs are base64-encoded CBOR or similar data.
		if len(token) < proofTokenMinLenOpaque {
			return fmt.Errorf("%w: hardware proof_token too short (min %d chars, got %d)", ErrInvalidProofToken, proofTokenMinLenOpaque, len(token))
		}
		for _, r := range token {
			if r <= 0x20 || r == 0x7F {
				return fmt.Errorf("%w: hardware proof_token must not contain whitespace or control characters", ErrInvalidProofToken)
			}
		}
	case ProofEmailLink:
		// Email-link tokens are signed URL query parameters or opaque redirect tokens.
		if len(token) < proofTokenMinLenOpaque {
			return fmt.Errorf("%w: email-link proof_token too short (min %d chars, got %d)", ErrInvalidProofToken, proofTokenMinLenOpaque, len(token))
		}
		for _, r := range token {
			if r <= 0x20 || r == 0x7F {
				return fmt.Errorf("%w: email-link proof_token must not contain whitespace or control characters", ErrInvalidProofToken)
			}
		}
	}
	return nil
}

// Challenge-response sentinel errors.
var (
	// ErrRateLimitExceeded is returned when a target key has received the maximum
	// number of challenges in the rate window. See §12.1.
	ErrRateLimitExceeded = errors.New("provenance: target-side rate limit exceeded (max 10 challenges/hour)")

	// ErrChallengeNotFound is returned when a response references an unknown or
	// already-consumed challenge ID.
	ErrChallengeNotFound = errors.New("provenance: challenge not found (unknown or already consumed)")

	// ErrChallengeExpired is returned when a response arrives after the challenge TTL.
	ErrChallengeExpired = errors.New("provenance: challenge expired")

	// ErrMissingAntecedent is returned when a response does not include an antecedent
	// message ID. See §12.2.
	ErrMissingAntecedent = errors.New("provenance: response missing antecedent (MUST reference challenge message ID)")

	// ErrEmptyProofType is returned when proof_type is empty. See §5.3, §12.2.
	// An attestation without a proof_type provides no evidence of human presence
	// and MUST be rejected.
	ErrEmptyProofType = errors.New("provenance: proof_type must not be empty")

	// ErrUnknownProofType is returned when proof_type is not a recognized value.
	// See §5.3. Accepting unknown proof types would allow unverifiable claims.
	ErrUnknownProofType = errors.New("provenance: proof_type is not a recognized value (must be one of: captcha, totp, hardware, sms, email-link)")

	// ErrEmptyProofToken is returned when proof_token is empty. See §5.3, §12.2.
	// Without a proof_token there is no actual proof -- the attestation would be
	// meaningless and MUST be rejected.
	ErrEmptyProofToken = errors.New("provenance: proof_token must not be empty")

	// ErrInvalidProofToken is returned when proof_token does not match the structural
	// format required by the declared proof_type. See §5.3.
	// Format rules: TOTP = 6 or 8 decimal digits; SMS = 4-8 decimal digits;
	// captcha/hardware/email-link = >=16/32 printable non-whitespace characters.
	// This check rejects clearly invalid tokens (random short strings, non-digit TOTP
	// codes, whitespace-contaminated tokens) without requiring a call to the issuing
	// service. Cryptographic proof of authenticity is handled at the application layer.
	ErrInvalidProofToken = errors.New("provenance: proof_token format invalid for declared proof_type")

	// ErrChallengeIDCollision is returned when IssueChallenge is called with an ID
	// that is already present in the active challenge map. Challenge IDs are
	// caller-supplied (e.g., campfire message IDs) and MUST be globally unique.
	// A collision would silently overwrite the original operator's pending nonce,
	// so it is rejected as a bug or potential attack.
	ErrChallengeIDCollision = errors.New("provenance: challenge ID already exists in active set -- IDs must be unique")
)

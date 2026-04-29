package trust

import (
	"crypto/sha256"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// cborEncMode is the CBOR encoder mode for deterministic encoding per
// RFC 8949 §4.2.1 (CoreDetEncOptions: definite-length, sorted map keys,
// smallest integer encoding, no NaN/Inf).
//
// Initialized once at package init; safe for concurrent use.
var cborEncMode cbor.EncMode

// cborDecMode is the CBOR decoder mode.
var cborDecMode cbor.DecMode

func init() {
	var err error
	cborEncMode, err = cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(fmt.Sprintf("cf-authority: initializing CBOR encoder: %v", err))
	}
	cborDecMode, err = cbor.DecOptions{}.DecMode()
	if err != nil {
		panic(fmt.Sprintf("cf-authority: initializing CBOR decoder: %v", err))
	}
}

// Capability is a scoped capability tuple as defined in §1.1.
//
// Wire format is a CBOR map with integer keys:
//
//	1: convention  (tstr)
//	2: op_pattern  (tstr)
//	3: where       ([+WhereMatcher]) — encoded as array of maps
//	4: bounds      ({tstr => BoundValue}) — may be empty map
//	5: until       (int64) — nanoseconds since Unix epoch UTC; MANDATORY
//	6: nonce       (bstr) — 16 bytes random
//
// Field 5 (until) is MANDATORY. Grants without until MUST be rejected at parse time (§1.1).
// grant-id = SHA-256(canonical-CBOR(Capability)) per §1.2.
//
// CBOR tag struct — field keys match §1.1 integer keys.
type Capability struct {
	Convention string            `cbor:"1,keyasint"`
	OpPattern  string            `cbor:"2,keyasint"`
	Where      []WhereMatcherCBOR `cbor:"3,keyasint"`
	Bounds     map[string]any    `cbor:"4,keyasint"`
	Until      int64             `cbor:"5,keyasint"` // nanoseconds since epoch; MANDATORY
	Nonce      []byte            `cbor:"6,keyasint"` // 16 bytes
}

// WhereMatcherCBOR is the wire representation of a WhereMatcher (§1.3).
// Three kinds compose as OR.
type WhereMatcherCBOR struct {
	Kind       uint   `cbor:"1,keyasint"`           // 1=CampfireByID, 2=NamePrefix, 3=TagMatcher
	CampfireID []byte `cbor:"2,keyasint,omitempty"` // kind=1: exact campfire hex-id as bytes
	Prefix     string `cbor:"3,keyasint,omitempty"` // kind=2: campfire name prefix
	Tag        string `cbor:"4,keyasint,omitempty"` // kind=3: campfire tag
}

// GrantPayload is the wire payload for a delegation:grant message (§1.2).
//
// Wire format is a CBOR map with integer keys:
//
//	1: parent_grant_id  (bstr | null) — null for owner-root grants
//	2: child_pubkey     (bstr) — Ed25519 public key, 32 bytes raw
//	3: capabilities     ([+Capability])
//	4: depth            (uint) — hop depth (0 = owner-root)
//
// grant-id = SHA-256(deterministic-CBOR(GrantPayload)).
type GrantPayload struct {
	ParentGrantID []byte       `cbor:"1,keyasint"` // null → nil slice
	ChildPubkey   []byte       `cbor:"2,keyasint"` // 32-byte Ed25519 pubkey
	Capabilities  []Capability `cbor:"3,keyasint"`
	Depth         uint         `cbor:"4,keyasint"`
}

// MarshalCapabilityCBOR encodes a Capability to deterministic CBOR per
// RFC 8949 §4.2.1 (cf-authority spec §4.4).
//
// Returns an error if the capability is structurally invalid (zero until,
// invalid nonce length, etc.) or if CBOR encoding fails.
func MarshalCapabilityCBOR(cap Capability) ([]byte, error) {
	if cap.Until == 0 {
		return nil, fmt.Errorf("cf-authority: Capability.Until is mandatory (§1.1); got zero")
	}
	if len(cap.Nonce) != 16 {
		return nil, fmt.Errorf("cf-authority: Capability.Nonce must be 16 bytes (§1.1); got %d", len(cap.Nonce))
	}
	return cborEncMode.Marshal(cap)
}

// UnmarshalCapabilityCBOR decodes a Capability from CBOR bytes.
// Returns an error if the CBOR is malformed or a mandatory field is missing.
func UnmarshalCapabilityCBOR(data []byte) (Capability, error) {
	var cap Capability
	if err := cborDecMode.Unmarshal(data, &cap); err != nil {
		return Capability{}, fmt.Errorf("cf-authority: decoding Capability CBOR: %w", err)
	}
	if cap.Until == 0 {
		return Capability{}, fmt.Errorf("cf-authority: decoded Capability missing mandatory 'until' field (field 5)")
	}
	if cap.Convention == "" {
		return Capability{}, fmt.Errorf("cf-authority: decoded Capability missing mandatory 'convention' field (field 1)")
	}
	return cap, nil
}

// CapabilityGrantID computes the grant-id for a Capability as
// SHA-256(canonical-CBOR(Capability)) per §1.2.
func CapabilityGrantID(cap Capability) ([]byte, error) {
	encoded, err := MarshalCapabilityCBOR(cap)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(encoded)
	return h[:], nil
}

// MarshalGrantPayloadCBOR encodes a GrantPayload to deterministic CBOR per
// RFC 8949 §4.2.1.
func MarshalGrantPayloadCBOR(gp GrantPayload) ([]byte, error) {
	if len(gp.ChildPubkey) != 32 {
		return nil, fmt.Errorf("cf-authority: GrantPayload.ChildPubkey must be 32 bytes; got %d", len(gp.ChildPubkey))
	}
	if len(gp.Capabilities) == 0 {
		return nil, fmt.Errorf("cf-authority: GrantPayload.Capabilities must be non-empty")
	}
	return cborEncMode.Marshal(gp)
}

// UnmarshalGrantPayloadCBOR decodes a GrantPayload from CBOR bytes.
func UnmarshalGrantPayloadCBOR(data []byte) (GrantPayload, error) {
	var gp GrantPayload
	if err := cborDecMode.Unmarshal(data, &gp); err != nil {
		return GrantPayload{}, fmt.Errorf("cf-authority: decoding GrantPayload CBOR: %w", err)
	}
	if len(gp.ChildPubkey) != 32 {
		return GrantPayload{}, fmt.Errorf("cf-authority: decoded GrantPayload.ChildPubkey must be 32 bytes; got %d", len(gp.ChildPubkey))
	}
	if len(gp.Capabilities) == 0 {
		return GrantPayload{}, fmt.Errorf("cf-authority: decoded GrantPayload has no capabilities")
	}
	return gp, nil
}

// GrantPayloadID computes the grant-id for a GrantPayload as
// SHA-256(deterministic-CBOR(GrantPayload)) per §1.2.
// This is the canonical identifier for revocation.
func GrantPayloadID(gp GrantPayload) ([]byte, error) {
	encoded, err := MarshalGrantPayloadCBOR(gp)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(encoded)
	return h[:], nil
}

// UnmarshalCBORRaw decodes CBOR bytes into the target value using the package
// decode mode. Exposed for tests that need to inspect raw CBOR structure
// (e.g. verifying integer field keys per §1.1).
func UnmarshalCBORRaw(data []byte, v any) error {
	return cborDecMode.Unmarshal(data, v)
}

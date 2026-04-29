package trust_test

import (
	"bytes"
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

// ---------------------------------------------------------------------------
// Condition (b): Real CBOR encode/decode roundtrip test against spec field layout
// Uses the real github.com/fxamacker/cbor/v2 library — NO mock encoder.
// ---------------------------------------------------------------------------

// TestCapabilityCBORRoundtrip verifies that a Capability encodes to CBOR and
// decodes back with field-by-field equality (spec §1.1, §4.4).
//
// Canonical t0 = 1767225600000000000 ns (2026-01-01T00:00:00Z per §5.4).
func TestCapabilityCBORRoundtrip(t *testing.T) {
	nonce := make([]byte, 16)
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}

	original := trust.Capability{
		Convention: "ready",
		OpPattern:  "claim",
		Where: []trust.WhereMatcherCBOR{
			{Kind: 2, Prefix: "rd-"},
		},
		Bounds: map[string]any{},
		Until:  1767225600000000000, // canonical t0 per §5.4
		Nonce:  nonce,
	}

	// Encode to CBOR.
	encoded, err := trust.MarshalCapabilityCBOR(original)
	if err != nil {
		t.Fatalf("MarshalCapabilityCBOR: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("encoded CBOR is empty")
	}

	// Decode back.
	decoded, err := trust.UnmarshalCapabilityCBOR(encoded)
	if err != nil {
		t.Fatalf("UnmarshalCapabilityCBOR: %v", err)
	}

	// Field-by-field equality (spec §4.4 — re-encode must produce same bytes).
	if decoded.Convention != original.Convention {
		t.Errorf("Convention: got %q want %q", decoded.Convention, original.Convention)
	}
	if decoded.OpPattern != original.OpPattern {
		t.Errorf("OpPattern: got %q want %q", decoded.OpPattern, original.OpPattern)
	}
	if decoded.Until != original.Until {
		t.Errorf("Until: got %d want %d", decoded.Until, original.Until)
	}
	if !bytes.Equal(decoded.Nonce, original.Nonce) {
		t.Errorf("Nonce: got %x want %x", decoded.Nonce, original.Nonce)
	}
	if len(decoded.Where) != len(original.Where) {
		t.Errorf("Where length: got %d want %d", len(decoded.Where), len(original.Where))
	} else if len(decoded.Where) > 0 {
		if decoded.Where[0].Kind != original.Where[0].Kind {
			t.Errorf("Where[0].Kind: got %d want %d", decoded.Where[0].Kind, original.Where[0].Kind)
		}
		if decoded.Where[0].Prefix != original.Where[0].Prefix {
			t.Errorf("Where[0].Prefix: got %q want %q", decoded.Where[0].Prefix, original.Where[0].Prefix)
		}
	}

	// Re-encode the decoded value; bytes MUST be identical (canonical encoding).
	reEncoded, err := trust.MarshalCapabilityCBOR(decoded)
	if err != nil {
		t.Fatalf("re-encode MarshalCapabilityCBOR: %v", err)
	}
	if !bytes.Equal(encoded, reEncoded) {
		t.Errorf("CBOR re-encode is not byte-identical (canonicalization failure):\n"+
			"  first:  %x\n  second: %x", encoded, reEncoded)
	}
}

// TestCapabilityCBORFieldIDs verifies that integer field keys 1-6 are present
// in the encoded CBOR output (spec §1.1 field layout).
//
// This is a structural check: we decode the raw bytes as a cbor.RawMessage map
// by using a map[int]cbor.RawMessage to verify key presence.
func TestCapabilityCBORFieldIDs(t *testing.T) {
	nonce := make([]byte, 16)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	cap := trust.Capability{
		Convention: "cf-authority",
		OpPattern:  "*",
		Where:      nil,
		Bounds:     map[string]any{},
		Until:      1767225600000000000,
		Nonce:      nonce,
	}

	encoded, err := trust.MarshalCapabilityCBOR(cap)
	if err != nil {
		t.Fatalf("MarshalCapabilityCBOR: %v", err)
	}

	// Decode as map[int]RawMessage to verify integer key presence.
	var raw map[int]any
	if err := trust.UnmarshalCBORRaw(encoded, &raw); err != nil {
		t.Fatalf("raw decode: %v", err)
	}

	// §1.1: fields 1-6 must all be present.
	requiredFields := []int{1, 2, 3, 4, 5, 6}
	for _, fid := range requiredFields {
		if _, ok := raw[fid]; !ok {
			t.Errorf("CBOR field key %d (§1.1) is missing from encoded output", fid)
		}
	}
}

// TestCapabilityCBORDeterminism verifies that encoding the same Capability
// three times produces byte-identical CBOR each time (§4.1 determinism).
func TestCapabilityCBORDeterminism(t *testing.T) {
	nonce := bytes.Repeat([]byte{0xAB}, 16)
	cap := trust.Capability{
		Convention: "swarm-coordination",
		OpPattern:  "claim-item|complete",
		Where:      []trust.WhereMatcherCBOR{{Kind: 1, CampfireID: []byte("abc123")}},
		Bounds:     map[string]any{},
		Until:      1767225600000000000,
		Nonce:      nonce,
	}

	var results [3][]byte
	for i := range results {
		b, err := trust.MarshalCapabilityCBOR(cap)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		results[i] = b
	}
	for i := 1; i < 3; i++ {
		if !bytes.Equal(results[0], results[i]) {
			t.Errorf("run 0 vs run %d: not byte-identical (determinism failure)", i)
		}
	}
}

// TestCapabilityCBORMissingUntilRejected verifies that a Capability with zero
// Until is rejected at marshal time (spec §1.1: "Field 5 (until) is mandatory").
func TestCapabilityCBORMissingUntilRejected(t *testing.T) {
	nonce := make([]byte, 16)
	cap := trust.Capability{
		Convention: "ready",
		OpPattern:  "claim",
		Until:      0, // deliberately missing
		Nonce:      nonce,
	}
	_, err := trust.MarshalCapabilityCBOR(cap)
	if err == nil {
		t.Fatal("expected error for missing 'until' field; got nil")
	}
}

// TestCapabilityCBOREmptyCapabilities verifies that a GrantPayload with an
// empty capabilities array is rejected (adversarial case — spec §1.2 requires
// at least one capability).
func TestCapabilityCBOREmptyCapabilities(t *testing.T) {
	childKey := make([]byte, 32)
	gp := trust.GrantPayload{
		ParentGrantID: nil,
		ChildPubkey:   childKey,
		Capabilities:  nil, // empty
		Depth:         0,
	}
	_, err := trust.MarshalGrantPayloadCBOR(gp)
	if err == nil {
		t.Fatal("expected error for empty capabilities; got nil")
	}
}

// TestGrantPayloadCBORRoundtrip verifies GrantPayload encode/decode roundtrip
// with field-by-field equality (spec §1.2).
func TestGrantPayloadCBORRoundtrip(t *testing.T) {
	nonce := make([]byte, 16)
	for i := range nonce {
		nonce[i] = byte(i + 42)
	}
	childKey := make([]byte, 32)
	for i := range childKey {
		childKey[i] = byte(i)
	}

	cap := trust.Capability{
		Convention: "ready",
		OpPattern:  "claim",
		Where:      []trust.WhereMatcherCBOR{},
		Bounds:     map[string]any{},
		Until:      1767225600000000000,
		Nonce:      nonce,
	}

	original := trust.GrantPayload{
		ParentGrantID: nil,
		ChildPubkey:   childKey,
		Capabilities:  []trust.Capability{cap},
		Depth:         0,
	}

	encoded, err := trust.MarshalGrantPayloadCBOR(original)
	if err != nil {
		t.Fatalf("MarshalGrantPayloadCBOR: %v", err)
	}

	decoded, err := trust.UnmarshalGrantPayloadCBOR(encoded)
	if err != nil {
		t.Fatalf("UnmarshalGrantPayloadCBOR: %v", err)
	}

	if !bytes.Equal(decoded.ChildPubkey, original.ChildPubkey) {
		t.Errorf("ChildPubkey mismatch")
	}
	if decoded.Depth != original.Depth {
		t.Errorf("Depth: got %d want %d", decoded.Depth, original.Depth)
	}
	if len(decoded.Capabilities) != 1 {
		t.Fatalf("Capabilities: got %d want 1", len(decoded.Capabilities))
	}
	if decoded.Capabilities[0].Convention != original.Capabilities[0].Convention {
		t.Errorf("Capability.Convention mismatch")
	}
	if decoded.Capabilities[0].Until != original.Capabilities[0].Until {
		t.Errorf("Capability.Until mismatch")
	}
}

// TestGrantPayloadIDDeterminism verifies that GrantPayloadID is deterministic
// (same GrantPayload → same 32-byte SHA-256 every time).
func TestGrantPayloadIDDeterminism(t *testing.T) {
	nonce := make([]byte, 16)
	childKey := make([]byte, 32)
	for i := range childKey {
		childKey[i] = byte(i)
	}
	cap := trust.Capability{
		Convention: "swarm-coordination",
		OpPattern:  "claim-item",
		Where:      []trust.WhereMatcherCBOR{},
		Bounds:     map[string]any{},
		Until:      1767225600000000001,
		Nonce:      nonce,
	}
	gp := trust.GrantPayload{
		ParentGrantID: nil,
		ChildPubkey:   childKey,
		Capabilities:  []trust.Capability{cap},
		Depth:         1,
	}

	var ids [3][]byte
	for i := range ids {
		id, err := trust.GrantPayloadID(gp)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		ids[i] = id
	}
	for i := 1; i < 3; i++ {
		if !bytes.Equal(ids[0], ids[i]) {
			t.Errorf("run 0 vs run %d: GrantPayloadID not deterministic", i)
		}
	}
	if len(ids[0]) != 32 {
		t.Errorf("GrantPayloadID must be 32 bytes (SHA-256); got %d", len(ids[0]))
	}
}

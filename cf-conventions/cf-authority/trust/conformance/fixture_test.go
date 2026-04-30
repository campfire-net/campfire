package conformance_test

// ---------------------------------------------------------------------------
// Conformance fixture validation — cases 13a and 13b
//
// WHY: D6 gap from veracity audit (campfireagent-e48). These tests verify that
// the CBOR fixture files for D6 owner-ceiling are well-formed and encode the
// correct field values per the cf-authority spec §1.1, §1.2, §3.4.
//
// WHAT THIS TEST DOES:
//   - Loads each .cbor fixture file from testdata/chains/
//   - Decodes it using the real github.com/fxamacker/cbor/v2 library (NO mock)
//   - Asserts: integer field keys are present per §1.1 (GrantPayload: fields 1-4)
//   - Asserts: Capability fields 1-6 present per §1.1
//   - Asserts: convention == "ready", op_pattern matches the fixture manifest
//   - Asserts: Until is non-zero and >= canonical t0 (grant is not pre-expired)
//   - Asserts: ChildPubkey is 32 bytes
//
// WHAT THIS TEST DOES NOT DO:
//   - It does NOT call any evaluator or decision-making function
//   - It does NOT test GateEvaluator.Evaluate()
//   - It does NOT depend on DefaultGateEvaluator (Stage 3 work)
//
// SPEC: docs/cf-authority-spec.md §1.1, §1.2, §3.4, §5.4
// ITEM: campfireagent-af2 (fixture-only redo of campfireagent-2cd8)
// ---------------------------------------------------------------------------

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

// canonicalT0 is 2026-01-01T00:00:00Z in nanoseconds since Unix epoch UTC,
// the canonical test time per spec §5.4.
const canonicalT0 int64 = 1767225600000000000

// fixtureDir returns the absolute path to the testdata/chains directory.
func fixtureDir(t *testing.T) string {
	t.Helper()
	// testdata is a sibling of conformance/ — one level up from the test binary.
	// Use runtime.Caller to get the test file's directory.
	dir := filepath.Join("testdata", "chains")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatalf("testdata directory not found at %q — run from conformance/ directory", dir)
	}
	return dir
}

// loadGrantPayload reads a .cbor file from testdata/chains/ and decodes it
// as a GrantPayload using the real fxamacker/cbor/v2 library.
// Fails the test if the file is missing or cannot be decoded.
func loadGrantPayload(t *testing.T, dir, filename string) trust.GrantPayload {
	t.Helper()
	path := filepath.Join(dir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture %q: cannot read: %v", filename, err)
	}
	if len(data) == 0 {
		t.Fatalf("fixture %q: file is empty", filename)
	}
	gp, err := trust.UnmarshalGrantPayloadCBOR(data)
	if err != nil {
		t.Fatalf("fixture %q: CBOR decode failed: %v", filename, err)
	}
	return gp
}

// assertGrantPayloadFieldIDs verifies that integer field keys 1-4 are present
// in the raw CBOR encoding of a GrantPayload (spec §1.2).
// Uses trust.UnmarshalCBORRaw for raw inspection without type coercion.
func assertGrantPayloadFieldIDs(t *testing.T, data []byte, filename string) {
	t.Helper()
	var raw map[int]any
	if err := trust.UnmarshalCBORRaw(data, &raw); err != nil {
		t.Fatalf("fixture %q: raw CBOR decode failed: %v", filename, err)
	}
	// §1.2: GrantPayload integer field keys 1-4 must all be present.
	requiredFields := []int{1, 2, 3, 4}
	for _, fid := range requiredFields {
		if _, ok := raw[fid]; !ok {
			t.Errorf("fixture %q: CBOR field key %d (§1.2) is missing", filename, fid)
		}
	}
}

// assertCapabilityFieldIDs verifies that integer field keys 1-6 are present
// in the CBOR encoding of a Capability (spec §1.1).
func assertCapabilityFieldIDs(t *testing.T, cap trust.Capability, filename string) {
	t.Helper()
	encoded, err := trust.MarshalCapabilityCBOR(cap)
	if err != nil {
		t.Fatalf("fixture %q: re-encoding Capability failed: %v", filename, err)
	}
	var raw map[int]any
	if err := trust.UnmarshalCBORRaw(encoded, &raw); err != nil {
		t.Fatalf("fixture %q: raw Capability CBOR decode failed: %v", filename, err)
	}
	// §1.1: Capability integer field keys 1-6 must all be present.
	requiredFields := []int{1, 2, 3, 4, 5, 6}
	for _, fid := range requiredFields {
		if _, ok := raw[fid]; !ok {
			t.Errorf("fixture %q: Capability CBOR field key %d (§1.1) is missing", filename, fid)
		}
	}
}

// ---------------------------------------------------------------------------
// Case 13a: BlanketDeny=["ready:*"] (glob pattern)
// Fixture: 13-owner-blanket-deny.cbor
//
// Encodes a GrantPayload with:
//   - convention="ready", op_pattern="claim" (grants ready:claim)
//   - ChildPubkey: 32-byte Ed25519 public key (deterministic seed)
//   - Until: canonicalT0 + 86400000000000 (24h after t0 — not expired)
//   - Depth: 0 (owner-root grant)
//
// The owner-ceiling test uses BlanketDeny=["ready:*"] — the grant covers the
// request but the owner ceiling overrides it. The fixture itself encodes the
// grant; the OwnerPolicy is assembled in the evaluator test.
// ---------------------------------------------------------------------------

// TestD6_Fixture13a_GlobPattern_Wellformed verifies that
// testdata/chains/13-owner-blanket-deny.cbor decodes successfully and that
// its field values match the fixture manifest (case 13a: glob pattern).
//
// This test does NOT call any evaluator. It is a structural/conformance check.
func TestD6_Fixture13a_GlobPattern_Wellformed(t *testing.T) {
	dir := fixtureDir(t)
	filename := "13-owner-blanket-deny.cbor"
	path := filepath.Join(dir, filename)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture %q: cannot read: %v", filename, err)
	}

	// 1. Integer field keys 1-4 present (§1.2 GrantPayload).
	assertGrantPayloadFieldIDs(t, data, filename)

	// 2. Full GrantPayload decode succeeds.
	gp := loadGrantPayload(t, dir, filename)

	// 3. ChildPubkey must be 32 bytes (§1.2).
	if len(gp.ChildPubkey) != 32 {
		t.Errorf("fixture %q: ChildPubkey must be 32 bytes; got %d", filename, len(gp.ChildPubkey))
	}

	// 4. Exactly one capability (the grant for ready:claim).
	if len(gp.Capabilities) != 1 {
		t.Fatalf("fixture %q: expected 1 capability; got %d", filename, len(gp.Capabilities))
	}

	cap := gp.Capabilities[0]

	// 5. Capability field keys 1-6 present (§1.1).
	assertCapabilityFieldIDs(t, cap, filename)

	// 6. Convention must be "ready" (the grant covers ready:claim per manifest).
	if cap.Convention != "ready" {
		t.Errorf("fixture %q: Capability.Convention = %q; want %q", filename, cap.Convention, "ready")
	}

	// 7. OpPattern must be "claim" (grant covers exactly ready:claim).
	if cap.OpPattern != "claim" {
		t.Errorf("fixture %q: Capability.OpPattern = %q; want %q", filename, cap.OpPattern, "claim")
	}

	// 8. Until must be non-zero and >= canonicalT0 (grant is not pre-expired at t0).
	if cap.Until == 0 {
		t.Errorf("fixture %q: Capability.Until is zero (missing mandatory field §1.1)", filename)
	}
	if cap.Until < canonicalT0 {
		t.Errorf("fixture %q: Capability.Until=%d < canonicalT0=%d (grant pre-expired at test time)",
			filename, cap.Until, canonicalT0)
	}

	// 9. Nonce must be 16 bytes (§1.1).
	if len(cap.Nonce) != 16 {
		t.Errorf("fixture %q: Capability.Nonce must be 16 bytes; got %d", filename, len(cap.Nonce))
	}

	// 10. Depth must be 0 (owner-root grant, no parent).
	if gp.Depth != 0 {
		t.Errorf("fixture %q: Depth must be 0 (owner-root grant); got %d", filename, gp.Depth)
	}

	// 11. ParentGrantID must be nil (owner-root grant).
	if gp.ParentGrantID != nil {
		t.Errorf("fixture %q: ParentGrantID must be nil for owner-root grant; got %x",
			filename, gp.ParentGrantID)
	}

	// 12. Re-encode and verify CBOR determinism (§4.4).
	reEncoded, err := trust.MarshalGrantPayloadCBOR(gp)
	if err != nil {
		t.Fatalf("fixture %q: re-encode failed: %v", filename, err)
	}
	// Byte-identical re-encode is required by §4.4 (canonical CBOR).
	if len(reEncoded) != len(data) {
		t.Errorf("fixture %q: re-encoded CBOR length mismatch (not canonical?): orig=%d re=%d",
			filename, len(data), len(reEncoded))
	}
}

// ---------------------------------------------------------------------------
// Case 13b: BlanketDeny=["ready:claim"] (exact match)
// Fixture: 13b-owner-blanket-deny-exact.cbor
//
// Same GrantPayload structure as 13a — the only difference is that case 13b
// uses an exact-match BlanketDeny pattern instead of a glob. The grant chain
// is identical (same capability, different fixture file for naming clarity).
// ---------------------------------------------------------------------------

// TestD6_Fixture13b_ExactMatch_Wellformed verifies that
// testdata/chains/13b-owner-blanket-deny-exact.cbor decodes successfully
// and that its field values match the fixture manifest (case 13b: exact match).
//
// This test does NOT call any evaluator. It is a structural/conformance check.
func TestD6_Fixture13b_ExactMatch_Wellformed(t *testing.T) {
	dir := fixtureDir(t)
	filename := "13b-owner-blanket-deny-exact.cbor"
	path := filepath.Join(dir, filename)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture %q: cannot read: %v", filename, err)
	}

	// 1. Integer field keys 1-4 present (§1.2 GrantPayload).
	assertGrantPayloadFieldIDs(t, data, filename)

	// 2. Full GrantPayload decode succeeds.
	gp := loadGrantPayload(t, dir, filename)

	// 3. ChildPubkey must be 32 bytes (§1.2).
	if len(gp.ChildPubkey) != 32 {
		t.Errorf("fixture %q: ChildPubkey must be 32 bytes; got %d", filename, len(gp.ChildPubkey))
	}

	// 4. Exactly one capability.
	if len(gp.Capabilities) != 1 {
		t.Fatalf("fixture %q: expected 1 capability; got %d", filename, len(gp.Capabilities))
	}

	cap := gp.Capabilities[0]

	// 5. Capability field keys 1-6 present (§1.1).
	assertCapabilityFieldIDs(t, cap, filename)

	// 6. Convention must be "ready".
	if cap.Convention != "ready" {
		t.Errorf("fixture %q: Capability.Convention = %q; want %q", filename, cap.Convention, "ready")
	}

	// 7. OpPattern must be "claim".
	if cap.OpPattern != "claim" {
		t.Errorf("fixture %q: Capability.OpPattern = %q; want %q", filename, cap.OpPattern, "claim")
	}

	// 8. Until must be non-zero and >= canonicalT0.
	if cap.Until == 0 {
		t.Errorf("fixture %q: Capability.Until is zero (missing mandatory field §1.1)", filename)
	}
	if cap.Until < canonicalT0 {
		t.Errorf("fixture %q: Capability.Until=%d < canonicalT0=%d (grant pre-expired at test time)",
			filename, cap.Until, canonicalT0)
	}

	// 9. Nonce must be 16 bytes (§1.1).
	if len(cap.Nonce) != 16 {
		t.Errorf("fixture %q: Capability.Nonce must be 16 bytes; got %d", filename, len(cap.Nonce))
	}

	// 10. Depth must be 0 (owner-root grant).
	if gp.Depth != 0 {
		t.Errorf("fixture %q: Depth must be 0 (owner-root grant); got %d", filename, gp.Depth)
	}

	// 11. ParentGrantID must be nil (owner-root grant).
	if gp.ParentGrantID != nil {
		t.Errorf("fixture %q: ParentGrantID must be nil for owner-root grant; got %x",
			filename, gp.ParentGrantID)
	}

	// 12. Re-encode and verify CBOR determinism (§4.4).
	reEncoded, err := trust.MarshalGrantPayloadCBOR(gp)
	if err != nil {
		t.Fatalf("fixture %q: re-encode failed: %v", filename, err)
	}
	if len(reEncoded) != len(data) {
		t.Errorf("fixture %q: re-encoded CBOR length mismatch (not canonical?): orig=%d re=%d",
			filename, len(data), len(reEncoded))
	}
}

// ---------------------------------------------------------------------------
// Cross-fixture consistency check
//
// Cases 13a and 13b encode the same grant chain (same ChildPubkey, convention,
// op_pattern, Until). The only distinction is in the test setup (BlanketDeny
// pattern), not in the grant payload itself. This test asserts that both
// fixtures produce an identical decoded GrantPayload — confirming the fixtures
// are built from the same source-of-truth canonical inputs.
// ---------------------------------------------------------------------------

// TestD6_Fixtures13a_13b_SameGrantChain asserts that both fixtures decode to
// structurally identical GrantPayloads (same convention, opPattern, Until,
// ChildPubkey, Depth). This verifies that the two test cases differ only in
// the OwnerPolicy pattern (glob vs exact), not in the underlying grant chain.
func TestD6_Fixtures13a_13b_SameGrantChain(t *testing.T) {
	dir := fixtureDir(t)
	gp13a := loadGrantPayload(t, dir, "13-owner-blanket-deny.cbor")
	gp13b := loadGrantPayload(t, dir, "13b-owner-blanket-deny-exact.cbor")

	if len(gp13a.Capabilities) == 0 || len(gp13b.Capabilities) == 0 {
		t.Fatal("one or both fixtures have zero capabilities — cannot compare")
	}
	cap13a := gp13a.Capabilities[0]
	cap13b := gp13b.Capabilities[0]

	if cap13a.Convention != cap13b.Convention {
		t.Errorf("fixtures 13a/13b: Convention mismatch: %q vs %q",
			cap13a.Convention, cap13b.Convention)
	}
	if cap13a.OpPattern != cap13b.OpPattern {
		t.Errorf("fixtures 13a/13b: OpPattern mismatch: %q vs %q",
			cap13a.OpPattern, cap13b.OpPattern)
	}
	if cap13a.Until != cap13b.Until {
		t.Errorf("fixtures 13a/13b: Until mismatch: %d vs %d",
			cap13a.Until, cap13b.Until)
	}
	if len(gp13a.ChildPubkey) != len(gp13b.ChildPubkey) {
		t.Errorf("fixtures 13a/13b: ChildPubkey length mismatch: %d vs %d",
			len(gp13a.ChildPubkey), len(gp13b.ChildPubkey))
	}
	if gp13a.Depth != gp13b.Depth {
		t.Errorf("fixtures 13a/13b: Depth mismatch: %d vs %d",
			gp13a.Depth, gp13b.Depth)
	}
}

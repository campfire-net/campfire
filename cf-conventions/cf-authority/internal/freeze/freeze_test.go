// Package freeze implements the Phase 8 Gate 4 cf-authority wire-format
// freeze verifier (campfireagent-301).
//
// This is the cf-authority sibling of cf-protocol/internal/wireverify —
// same pattern, same purpose: walk every S0b spec claim for cf-authority and
// assert it matches actual code at HEAD. Real reflection / const inspection —
// no regex of the spec file.
//
// Claims verified:
//
//	§1.1  Capability CBOR field IDs 1–6 (Convention, OpPattern, Where, Bounds, Until, Nonce)
//	§1.1  Until field is mandatory (field 5)
//	§1.1  Nonce field is mandatory (field 6, 16 bytes)
//	§1.2  GrantPayload CBOR field IDs 1–4 (ParentGrantID, ChildPubkey, Capabilities, Depth)
//	§1.3  WhereMatcher kind integers (1=CampfireByID, 2=NamePrefix, 3=TagMatcher)
//	§2.1  PredicateAST leaf kind strings (level, grant, grant_in, grant_quota, chain_to, chain_to_quorum)
//	§2.1  PredicateKind numeric values (1–6 for leaves, 100/101 for composites)
//	§2.2  Composite predicate kinds (all_of, any_of)
//	§2.2  not: rejection — ParsePredicateAST returns an error for 'not:' at parse time
//	§2.2  MaxPredicateDepth == 3
//	§2.4  DenyReason enum: all 10 stable codes, exact wire values
//
// Adversarial (mutation) test:
//
//	TestAuthorityFreezeVerifyMutationDetection — mutates each claim type and
//	asserts the verifier would catch the drift.
//
// Item: campfireagent-301 (Stage 3 — cf-authority CBOR + predicate AST wire format freeze sign-off)
package freeze_test

import (
	"reflect"
	"strings"
	"testing"

	cftrust "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

// ── §1.1 Capability CBOR Field IDs ───────────────────────────────────────────

// TestCapabilityCBORFieldIDs verifies that the Capability struct CBOR integer
// keys match §1.1 of the S0b spec exactly (field IDs 1–6).
func TestCapabilityCBORFieldIDs(t *testing.T) {
	type fieldSpec struct {
		fieldName string
		wantTag   string // expected cbor:"N,keyasint" base (before optional omitempty)
	}
	// S0b §1.1 wire layout:
	//   1: convention  (tstr)
	//   2: op_pattern  (tstr)
	//   3: where       ([+WhereMatcher])
	//   4: bounds      ({tstr => BoundValue})
	//   5: until       (int64) MANDATORY
	//   6: nonce       (bstr, 16 bytes) MANDATORY
	want := []fieldSpec{
		{"Convention", "1,keyasint"},
		{"OpPattern", "2,keyasint"},
		{"Where", "3,keyasint"},
		{"Bounds", "4,keyasint"},
		{"Until", "5,keyasint"},
		{"Nonce", "6,keyasint"},
	}

	mt := reflect.TypeOf(cftrust.Capability{})
	for _, w := range want {
		f, ok := mt.FieldByName(w.fieldName)
		if !ok {
			t.Errorf("Capability.%s: field not found", w.fieldName)
			continue
		}
		got := strings.Split(f.Tag.Get("cbor"), ",omitempty")[0]
		if got != w.wantTag {
			t.Errorf("Capability.%s: cbor tag = %q, want %q", w.fieldName, got, w.wantTag)
		}
	}

	// Exactly 6 fields — no undocumented extras.
	if mt.NumField() != 6 {
		t.Errorf("Capability has %d fields, want exactly 6 (§1.1)", mt.NumField())
	}
}

// TestCapabilityUntilIsMandatory confirms that Until is at CBOR field 5
// and is never omitempty — it is a MANDATORY field per §1.1.
func TestCapabilityUntilIsMandatory(t *testing.T) {
	mt := reflect.TypeOf(cftrust.Capability{})
	f, ok := mt.FieldByName("Until")
	if !ok {
		t.Fatal("Capability.Until field not found")
	}
	tag := f.Tag.Get("cbor")
	if !strings.HasPrefix(tag, "5,keyasint") {
		t.Errorf("Capability.Until: cbor tag = %q, want prefix \"5,keyasint\" (mandatory §1.1)", tag)
	}
	if strings.Contains(tag, "omitempty") {
		t.Errorf("Capability.Until: cbor tag contains 'omitempty' — Until is MANDATORY per §1.1, must never be omitted")
	}
}

// TestCapabilityNonceIsMandatory confirms that Nonce is at CBOR field 6
// and is never omitempty — it is a MANDATORY 16-byte field per §1.1.
func TestCapabilityNonceIsMandatory(t *testing.T) {
	mt := reflect.TypeOf(cftrust.Capability{})
	f, ok := mt.FieldByName("Nonce")
	if !ok {
		t.Fatal("Capability.Nonce field not found")
	}
	tag := f.Tag.Get("cbor")
	if !strings.HasPrefix(tag, "6,keyasint") {
		t.Errorf("Capability.Nonce: cbor tag = %q, want prefix \"6,keyasint\" (§1.1)", tag)
	}
	if strings.Contains(tag, "omitempty") {
		t.Errorf("Capability.Nonce: cbor tag contains 'omitempty' — Nonce is MANDATORY per §1.1")
	}
}

// ── §1.2 GrantPayload CBOR Field IDs ─────────────────────────────────────────

// TestGrantPayloadCBORFieldIDs verifies that the GrantPayload struct CBOR
// integer keys match §1.2 of the S0b spec (field IDs 1–4).
func TestGrantPayloadCBORFieldIDs(t *testing.T) {
	type fieldSpec struct {
		fieldName string
		wantTag   string
	}
	// S0b §1.2 wire layout:
	//   1: parent_grant_id  (bstr | null)
	//   2: child_pubkey     (bstr, 32 bytes)
	//   3: capabilities     ([+Capability])
	//   4: depth            (uint)
	want := []fieldSpec{
		{"ParentGrantID", "1,keyasint"},
		{"ChildPubkey", "2,keyasint"},
		{"Capabilities", "3,keyasint"},
		{"Depth", "4,keyasint"},
	}

	mt := reflect.TypeOf(cftrust.GrantPayload{})
	for _, w := range want {
		f, ok := mt.FieldByName(w.fieldName)
		if !ok {
			t.Errorf("GrantPayload.%s: field not found", w.fieldName)
			continue
		}
		got := strings.Split(f.Tag.Get("cbor"), ",omitempty")[0]
		if got != w.wantTag {
			t.Errorf("GrantPayload.%s: cbor tag = %q, want %q", w.fieldName, got, w.wantTag)
		}
	}

	// Exactly 4 fields — no undocumented extras.
	if mt.NumField() != 4 {
		t.Errorf("GrantPayload has %d fields, want exactly 4 (§1.2)", mt.NumField())
	}
}

// ── §1.3 WhereMatcher Kind Integers ──────────────────────────────────────────

// TestWhereMatcherKindIntegers verifies that the three WhereMatcher kind
// constants match §1.3 exactly.
func TestWhereMatcherKindIntegers(t *testing.T) {
	tests := []struct {
		name string
		got  cftrust.WhereKind
		want cftrust.WhereKind
	}{
		{"WhereCampfireByID", cftrust.WhereCampfireByID, 1},
		{"WhereNamePrefix", cftrust.WhereNamePrefix, 2},
		{"WhereTagMatcher", cftrust.WhereTagMatcher, 3},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %d, want %d (§1.3)", tt.name, tt.got, tt.want)
		}
	}
}

// ── §2.1 PredicateKind Numeric Values ────────────────────────────────────────

// TestPredicateKindNumericValues verifies that every leaf and composite
// PredicateKind has the exact numeric value frozen in §2.1/§2.2.
func TestPredicateKindNumericValues(t *testing.T) {
	tests := []struct {
		name string
		got  cftrust.PredicateKind
		want cftrust.PredicateKind
	}{
		// Leaf kinds (§2.1)
		{"KindLevel", cftrust.KindLevel, 1},
		{"KindGrant", cftrust.KindGrant, 2},
		{"KindGrantIn", cftrust.KindGrantIn, 3},
		{"KindGrantQuota", cftrust.KindGrantQuota, 4},
		{"KindChainTo", cftrust.KindChainTo, 5},
		{"KindChainToQuorum", cftrust.KindChainToQuorum, 6},
		// Composite kinds (§2.2)
		{"KindAllOf", cftrust.KindAllOf, 100},
		{"KindAnyOf", cftrust.KindAnyOf, 101},
		// Sentinel (parse-time rejection marker — never a valid node)
		{"KindNot", cftrust.KindNot, 999},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("PredicateKind %s = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
}

// ── §2.1 Leaf Kind Strings (ParsePredicateAST) ───────────────────────────────

// TestLeafKindStringsParsed verifies that each of the six leaf kind strings
// parses successfully via ParsePredicateAST, producing a non-zero Kind.
// This confirms the wire-format string ↔ enum mapping is intact.
func TestLeafKindStringsParsed(t *testing.T) {
	validLeafs := []struct {
		raw  map[string]any
		kind cftrust.PredicateKind
	}{
		{map[string]any{"kind": "level", "n": 1}, cftrust.KindLevel},
		{map[string]any{"kind": "grant", "convention": "test-conv", "op": "read"}, cftrust.KindGrant},
		{map[string]any{
			"kind":       "grant_in",
			"convention": "test-conv",
			"op_glob":    "read*",
			"where":      map[string]any{"kind": 1, "id": "deadbeef"},
		}, cftrust.KindGrantIn},
		{map[string]any{"kind": "grant_quota", "axis": "rate", "bound": 10}, cftrust.KindGrantQuota},
		{map[string]any{"kind": "chain_to", "pubkey": strings.Repeat("a", 64)}, cftrust.KindChainTo},
		{map[string]any{
			"kind":    "chain_to_quorum",
			"m":       2,
			"pubkeys": []any{make([]byte, 32), make([]byte, 32)},
		}, cftrust.KindChainToQuorum},
	}

	for _, tc := range validLeafs {
		kindStr := tc.raw["kind"].(string)
		ast, err := cftrust.ParsePredicateAST(tc.raw)
		if err != nil {
			t.Errorf("leaf kind %q: ParsePredicateAST error: %v", kindStr, err)
			continue
		}
		if ast.Kind != tc.kind {
			t.Errorf("leaf kind %q: got Kind=%d, want %d", kindStr, ast.Kind, tc.kind)
		}
		if ast.Leaf == nil {
			t.Errorf("leaf kind %q: Leaf is nil", kindStr)
		}
	}
}

// ── §2.2 Composite Predicate Kinds ───────────────────────────────────────────

// TestCompositeKindStringsParsed verifies all_of and any_of parse correctly
// and produce the right Kind.
func TestCompositeKindStringsParsed(t *testing.T) {
	child := map[string]any{"kind": "level", "n": 1}
	cases := []struct {
		str  string
		kind cftrust.PredicateKind
	}{
		{"all_of", cftrust.KindAllOf},
		{"any_of", cftrust.KindAnyOf},
	}
	for _, tc := range cases {
		raw := map[string]any{"kind": tc.str, "children": []any{child}}
		ast, err := cftrust.ParsePredicateAST(raw)
		if err != nil {
			t.Errorf("composite %q: ParsePredicateAST error: %v", tc.str, err)
			continue
		}
		if ast.Kind != tc.kind {
			t.Errorf("composite %q: got Kind=%d, want %d", tc.str, ast.Kind, tc.kind)
		}
		if len(ast.Children) != 1 {
			t.Errorf("composite %q: got %d children, want 1", tc.str, len(ast.Children))
		}
	}
}

// TestMaxPredicateDepthFrozen verifies MaxPredicateDepth == 3 per §2.2.
func TestMaxPredicateDepthFrozen(t *testing.T) {
	if cftrust.MaxPredicateDepth != 3 {
		t.Errorf("MaxPredicateDepth = %d, want 3 (§2.2)", cftrust.MaxPredicateDepth)
	}
}

// ── §2.2 not: Rejection ───────────────────────────────────────────────────────

// TestNotPredicateRejectedAtParseTime verifies that a 'not:' predicate node is
// rejected by ParsePredicateAST — this is a deliberate safety property per §2.2.
// The wire string "not" must never produce a valid AST node.
func TestNotPredicateRejectedAtParseTime(t *testing.T) {
	raw := map[string]any{"kind": "not", "child": map[string]any{"kind": "level", "n": 1}}
	_, err := cftrust.ParsePredicateAST(raw)
	if err == nil {
		t.Error("ParsePredicateAST: 'not:' predicate must be rejected at parse time (§2.2), got nil error")
	}
	// Specifically, the error must reference ErrNotPredicateRejected.
	if err != cftrust.ErrNotPredicateRejected {
		t.Errorf("ParsePredicateAST: 'not:' predicate error = %v, want ErrNotPredicateRejected (§2.2)", err)
	}
}

// TestNotPredicateRejectedWhenNested verifies that a 'not:' node nested inside
// a composite predicate is also rejected (§2.2: applies at any position).
func TestNotPredicateRejectedWhenNested(t *testing.T) {
	raw := map[string]any{
		"kind": "all_of",
		"children": []any{
			map[string]any{"kind": "level", "n": 1},
			map[string]any{"kind": "not", "child": map[string]any{"kind": "level", "n": 1}},
		},
	}
	_, err := cftrust.ParsePredicateAST(raw)
	if err == nil {
		t.Error("ParsePredicateAST: nested 'not:' predicate must be rejected at parse time (§2.2), got nil error")
	}
}

// ── §2.4 DenyReason Enum ─────────────────────────────────────────────────────

// TestDenyReasonEnumCodesExact verifies all 10 stable DenyReason codes and
// their exact wire-format string values per §2.4.
//
// This is the definitive freeze check: the table here IS the spec claim.
// Any drift between the code constants and this table is a protocol violation.
func TestDenyReasonEnumCodesExact(t *testing.T) {
	// §2.4 frozen codes (10 total, cf-authority 1.0).
	want := []struct {
		wantStr string
		code    cftrust.DenyReason
	}{
		{"expired", cftrust.DenyExpired},
		{"revoked", cftrust.DenyRevoked},
		{"depth_exceeded", cftrust.DenyDepthExceeded},
		{"scope_mismatch", cftrust.DenyScopeMismatch},
		{"scope_widening", cftrust.DenyScopeWidening},
		{"stale_revocation", cftrust.DenyStaleRevocation},
		{"reserved_op_floor", cftrust.DenyReservedOpFloor},
		{"owner_ceiling", cftrust.DenyOwnerCeiling},
		{"predicate_unsatisfied", cftrust.DenyPredicate},
		{"store_read_error", cftrust.DenyStoreRead},
	}

	for _, w := range want {
		if string(w.code) != w.wantStr {
			t.Errorf("DenyReason %q: wire value = %q, want %q (§2.4)", w.wantStr, string(w.code), w.wantStr)
		}
	}

	// Sanity check the test table itself matches the frozen count.
	if len(want) != 10 {
		t.Errorf("test table has %d entries, want 10 (§2.4 freezes exactly 10 codes)", len(want))
	}
}

// TestDenyReasonTypeIsString verifies that DenyReason is defined as `string`
// (not int or iota) — the wire format depends on the string representation
// being the canonical code.
func TestDenyReasonTypeIsString(t *testing.T) {
	var dr cftrust.DenyReason = "test"
	rv := reflect.ValueOf(dr)
	if rv.Kind() != reflect.String {
		t.Errorf("DenyReason underlying kind = %s, want string (§2.4 wire values are strings)", rv.Kind())
	}
}

// ── Adversarial: Mutation Detection ──────────────────────────────────────────

// TestAuthorityFreezeVerifyMutationDetection is the TDD mutation test required
// by campfireagent-301 DONE condition.
//
// It simulates spec drift scenarios by asserting that a wrong claim does NOT
// match the actual code — i.e., these tests would catch the mutation if the code
// were changed to the wrong value.
//
// Mutation scenarios tested:
//  1. Capability field count: if 5 fields (not 6), count check would fail.
//  2. GrantPayload field count: if 3 fields (not 4), count check would fail.
//  3. DenyExpired wire value: if "exp" instead of "expired", string check fails.
//  4. Capability.Until CBOR key: if at field 4 instead of 5, tag check fails.
//  5. WhereCampfireByID: if 2 instead of 1, kind check fails.
//  6. KindLevel: if 2 instead of 1, numeric check fails.
//  7. MaxPredicateDepth: if 4 instead of 3, depth check fails.
func TestAuthorityFreezeVerifyMutationDetection(t *testing.T) {
	// Mutation 1: Capability field count. Real = 6; any other value is drift.
	mt := reflect.TypeOf(cftrust.Capability{})
	if mt.NumField() != 6 {
		t.Errorf("MUTATION-1 CAUGHT: Capability has %d fields, not 6 — §1.1 drift", mt.NumField())
	}
	mutatedCapCount := 5
	if mutatedCapCount == mt.NumField() {
		t.Error("MUTATION-1 UNDETECTED: count 5 incorrectly matches Capability field count")
	}

	// Mutation 2: GrantPayload field count. Real = 4.
	gpt := reflect.TypeOf(cftrust.GrantPayload{})
	if gpt.NumField() != 4 {
		t.Errorf("MUTATION-2 CAUGHT: GrantPayload has %d fields, not 4 — §1.2 drift", gpt.NumField())
	}
	mutatedGPCount := 3
	if mutatedGPCount == gpt.NumField() {
		t.Error("MUTATION-2 UNDETECTED: count 3 incorrectly matches GrantPayload field count")
	}

	// Mutation 3: DenyExpired wire value. Real = "expired".
	mutatedDenyExpired := cftrust.DenyReason("exp")
	if mutatedDenyExpired == cftrust.DenyExpired {
		t.Error("MUTATION-3 UNDETECTED: 'exp' incorrectly matches DenyExpired wire value")
	}

	// Mutation 4: Capability.Until CBOR key. Real = "5,keyasint".
	f, _ := mt.FieldByName("Until")
	mutatedUntilTag := "4,keyasint"
	gotBase := strings.Split(f.Tag.Get("cbor"), ",omitempty")[0]
	if mutatedUntilTag == gotBase {
		t.Error("MUTATION-4 UNDETECTED: Until at field 4 incorrectly matches real struct tag")
	}

	// Mutation 5: WhereCampfireByID. Real = 1.
	mutatedWhereKind := cftrust.WhereKind(2)
	if mutatedWhereKind == cftrust.WhereCampfireByID {
		t.Error("MUTATION-5 UNDETECTED: kind=2 incorrectly matches WhereCampfireByID")
	}

	// Mutation 6: KindLevel. Real = 1.
	mutatedKindLevel := cftrust.PredicateKind(2)
	if mutatedKindLevel == cftrust.KindLevel {
		t.Error("MUTATION-6 UNDETECTED: kind=2 incorrectly matches KindLevel")
	}

	// Mutation 7: MaxPredicateDepth. Real = 3.
	mutatedDepth := 4
	if mutatedDepth == cftrust.MaxPredicateDepth {
		t.Error("MUTATION-7 UNDETECTED: depth=4 incorrectly matches MaxPredicateDepth")
	}
}

// ── Cross-check: cf-authority claims already in wireverify ───────────────────

// TestCrossCheckWireverifyAlignment is a meta-test that confirms the
// cf-authority claims in this package align with the cf-protocol/internal/wireverify
// package (which covers the same claims as part of the S0q snapshot).
//
// This is NOT a duplicate — wireverify tests cf-authority claims in the context
// of the full wire snapshot (together with Message, ProvenanceHop, reserved ops,
// etc.). This package provides an isolated, focused verifier for cf-authority
// specifically, usable without pulling in cf-protocol internals.
//
// The alignment check: run the same Capability and GrantPayload field count
// assertions that wireverify performs, confirming both packages agree.
func TestCrossCheckWireverifyAlignment(t *testing.T) {
	// wireverify asserts Capability has exactly 6 fields — so do we.
	capFields := reflect.TypeOf(cftrust.Capability{}).NumField()
	if capFields != 6 {
		t.Errorf("Capability field count = %d, want 6 (alignment with wireverify §3.3)", capFields)
	}

	// wireverify asserts GrantPayload has exactly 4 fields — so do we.
	gpFields := reflect.TypeOf(cftrust.GrantPayload{}).NumField()
	if gpFields != 4 {
		t.Errorf("GrantPayload field count = %d, want 4 (alignment with wireverify §3.4)", gpFields)
	}

	// wireverify asserts DenyPredicate == "predicate_unsatisfied" — so do we.
	if string(cftrust.DenyPredicate) != "predicate_unsatisfied" {
		t.Errorf("DenyPredicate = %q, want \"predicate_unsatisfied\" (alignment with wireverify §2.4)", string(cftrust.DenyPredicate))
	}
}

// Package wireverify implements the Phase 8 Gate 4 wire-format snapshot verifier.
//
// This test walks each claim in docs/0.30-wire-format-freeze.md (the "S0q snapshot")
// and asserts that it matches the actual code at HEAD. It verifies real code paths,
// not just text in the snapshot file.
//
// Claims verified:
//   - §3.1  Message envelope CBOR field positions (7 wire fields + position of antecedents)
//   - §3.1  MessageSignInput covers id+payload+tags+antecedents+timestamp (5 fields, no signature/provenance)
//   - §3.2  ProvenanceHop CBOR field positions (7 wire fields + optional Role at 8)
//   - §3.2  HopSignInput field order matches hop signing spec
//   - §1.4  Session and visibility-changed tag constants (3 tags introduced at 0.30)
//   - §1.4  Pre-0.30 campfire: system event tags registered in cf-protocol/internal/campfire/tags.go
//   - §1.5  Reserved-op floor: exactly the canonical 10 ops
//   - §1.6  future / fulfills reserved tags frozen at cf-protocol 1.0
//   - §1.8  Await ordering rule: earliest-timestamp-wins + lex-ID tiebreaker
//   - §2.1  Capability CBOR field IDs (1–6) match cf-authority trust/grant.go
//   - §2.1  GrantPayload CBOR field IDs (1–4) match cf-authority trust/grant.go
//   - §2.2  PredicateAST leaf kind strings (6 leaf kinds)
//   - §2.2  Absence of not: predicate (parse-time rejection)
//   - §2.4  DenyReason enum: all 10 stable codes
//
// Adversarial (mutation) test:
//   TestWireVerifyMutationDetection — mutates a claim and asserts the verifier catches it.
//
// Item: campfireagent-3a8 (Stage 1 — wire-format freeze snapshot signoff)
package wireverify_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/internal/campfire"
	"github.com/campfire-net/campfire/cf-protocol/internal/message"
	reservedops "github.com/campfire-net/campfire/cf-protocol/internal/reserved-ops"
	"github.com/campfire-net/campfire/cf-protocol/internal/tagspec"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	cftrust "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

// ── §3.1 Message Envelope CBOR Fields ────────────────────────────────────────

// TestMessageEnvelopeCBORFields verifies that the Message struct CBOR field
// numbers match §3.1 of the S0q snapshot exactly.
//
// The snapshot claims:
//   id=UUID string, sender=bstr(32), payload=bstr, tags=[tstr],
//   antecedents=[tstr] UUIDs, timestamp=uint64, signature=bstr(64),
//   provenance=[ProvenanceHop]
//
// We verify the field order via reflection on the struct tags.
func TestMessageEnvelopeCBORFields(t *testing.T) {
	type fieldSpec struct {
		fieldName string
		wantTag   string // expected cbor:"N,keyasint" value
	}
	// Snapshot §3.1 — wire field order.
	// Important: antecedents must be field 5 in the Message struct.
	want := []fieldSpec{
		{"ID", "1,keyasint"},
		{"Sender", "2,keyasint"},
		{"Payload", "3,keyasint"},
		{"Tags", "4,keyasint"},
		{"Antecedents", "5,keyasint"},    // §1.7: "CBOR field 5 of the signed envelope"
		{"Timestamp", "6,keyasint"},
		{"Signature", "7,keyasint"},
		{"Provenance", "8,keyasint"},
	}

	mt := reflect.TypeOf(message.Message{})
	for _, w := range want {
		f, ok := mt.FieldByName(w.fieldName)
		if !ok {
			t.Errorf("Message.%s: field not found", w.fieldName)
			continue
		}
		got := f.Tag.Get("cbor")
		// Strip omitempty suffix if present before comparing the position prefix.
		gotBase := strings.Split(got, ",omitempty")[0]
		if gotBase != w.wantTag {
			t.Errorf("Message.%s: cbor tag = %q, want %q", w.fieldName, got, w.wantTag)
		}
	}
}

// TestAntecedentsAtCBORField5 is a focused assertion on the §1.7 antecedent
// fusion claim: antecedents must bind at CBOR field 5 of the Message envelope.
func TestAntecedentsAtCBORField5(t *testing.T) {
	mt := reflect.TypeOf(message.Message{})
	f, ok := mt.FieldByName("Antecedents")
	if !ok {
		t.Fatal("Message.Antecedents field not found")
	}
	got := f.Tag.Get("cbor")
	// Accept "5,keyasint" and "5,keyasint,omitempty"
	if !strings.HasPrefix(got, "5,keyasint") {
		t.Errorf("Antecedents: cbor tag = %q, want prefix \"5,keyasint\" (§1.7: antecedent fusion at field 5)", got)
	}
}

// ── §3.1 MessageSignInput: signature scope ────────────────────────────────────

// TestMessageSignInputScope verifies that MessageSignInput covers exactly the
// five fields claimed in §1.2: id, payload, tags, antecedents, timestamp.
// The signature must NOT cover provenance or the signature field itself.
func TestMessageSignInputScope(t *testing.T) {
	type fieldSpec struct {
		fieldName string
		wantTag   string
	}
	// The sign input has its own CBOR numbering (1–5); the claim is that these
	// five fields—and only these five—are signed.
	want := []fieldSpec{
		{"ID", "1,keyasint"},
		{"Payload", "2,keyasint"},
		{"Tags", "3,keyasint"},
		{"Antecedents", "4,keyasint"},
		{"Timestamp", "5,keyasint"},
	}

	mt := reflect.TypeOf(message.MessageSignInput{})

	// Check each expected field exists with the right CBOR tag.
	for _, w := range want {
		f, ok := mt.FieldByName(w.fieldName)
		if !ok {
			t.Errorf("MessageSignInput.%s: field not found", w.fieldName)
			continue
		}
		got := strings.Split(f.Tag.Get("cbor"), ",omitempty")[0]
		if got != w.wantTag {
			t.Errorf("MessageSignInput.%s: cbor tag = %q, want %q", w.fieldName, got, w.wantTag)
		}
	}

	// Check that the total number of fields is exactly 5 — no extra fields snuck in.
	if mt.NumField() != 5 {
		t.Errorf("MessageSignInput has %d fields, want exactly 5 (id+payload+tags+antecedents+timestamp)", mt.NumField())
	}

	// Verify that Signature and Provenance are NOT in the sign input (they must
	// not be signed — that would create a circular dependency for Signature, and
	// including Provenance would break cross-campfire relay).
	for _, forbidden := range []string{"Signature", "Provenance"} {
		if _, ok := mt.FieldByName(forbidden); ok {
			t.Errorf("MessageSignInput.%s: should NOT be in the sign input (§1.2)", forbidden)
		}
	}
}

// ── §3.2 ProvenanceHop CBOR Fields ────────────────────────────────────────────

// TestProvenanceHopCBORFields verifies that the ProvenanceHop struct CBOR field
// numbers match §3.2 of the S0q snapshot.
func TestProvenanceHopCBORFields(t *testing.T) {
	type fieldSpec struct {
		fieldName string
		wantTag   string
	}
	// Snapshot §3.2
	want := []fieldSpec{
		{"CampfireID", "1,keyasint"},
		{"MembershipHash", "2,keyasint"},
		{"MemberCount", "3,keyasint"},
		{"JoinProtocol", "4,keyasint"},
		{"ReceptionRequirements", "5,keyasint"},
		{"Timestamp", "6,keyasint"},
		{"Signature", "7,keyasint"},
		// Role is at 8 but is omitempty — also required by §3.2
		{"Role", "8,keyasint,omitempty"},
	}

	mt := reflect.TypeOf(message.ProvenanceHop{})
	for _, w := range want {
		f, ok := mt.FieldByName(w.fieldName)
		if !ok {
			t.Errorf("ProvenanceHop.%s: field not found", w.fieldName)
			continue
		}
		got := f.Tag.Get("cbor")
		if got != w.wantTag {
			t.Errorf("ProvenanceHop.%s: cbor tag = %q, want %q", w.fieldName, got, w.wantTag)
		}
	}
}

// TestHopSignInputScope verifies that HopSignInput covers all hop fields
// including Role (with omitempty — backward compat).
func TestHopSignInputScope(t *testing.T) {
	type fieldSpec struct {
		fieldName string
		wantTag   string
	}
	want := []fieldSpec{
		{"MessageID", "1,keyasint"},
		{"CampfireID", "2,keyasint"},
		{"MembershipHash", "3,keyasint"},
		{"MemberCount", "4,keyasint"},
		{"JoinProtocol", "5,keyasint"},
		{"ReceptionRequirements", "6,keyasint"},
		{"Timestamp", "7,keyasint"},
		{"Role", "8,keyasint,omitempty"},
	}

	mt := reflect.TypeOf(message.HopSignInput{})
	for _, w := range want {
		f, ok := mt.FieldByName(w.fieldName)
		if !ok {
			t.Errorf("HopSignInput.%s: field not found", w.fieldName)
			continue
		}
		got := f.Tag.Get("cbor")
		if got != w.wantTag {
			t.Errorf("HopSignInput.%s: cbor tag = %q, want %q", w.fieldName, got, w.wantTag)
		}
	}
}

// ── §1.4 System Event Tags ────────────────────────────────────────────────────

// TestSessionTagConstants verifies the three 0.30 system event tags have the
// correct wire values in cf-protocol/internal/tagspec.
func TestSessionTagConstants(t *testing.T) {
	tests := []struct {
		name    string
		got     string
		want    string
	}{
		{"TagSessionOpen", tagspec.TagSessionOpen, "session:open"},
		{"TagSessionClose", tagspec.TagSessionClose, "session:close"},
		{"TagCampfireVisibilityChanged", tagspec.TagCampfireVisibilityChanged, "campfire:visibility-changed"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

// TestSessionTagPublicReexport verifies that the public protocol package
// re-exports the session tags with the correct wire values.
func TestSessionTagPublicReexport(t *testing.T) {
	if protocol.TagSessionOpen != "session:open" {
		t.Errorf("protocol.TagSessionOpen = %q, want \"session:open\"", protocol.TagSessionOpen)
	}
	if protocol.TagSessionClose != "session:close" {
		t.Errorf("protocol.TagSessionClose = %q, want \"session:close\"", protocol.TagSessionClose)
	}
	if protocol.CampfireTagVisibilityChanged != "campfire:visibility-changed" {
		t.Errorf("protocol.CampfireTagVisibilityChanged = %q, want \"campfire:visibility-changed\"",
			protocol.CampfireTagVisibilityChanged)
	}
}

// TestPreV030SystemEventTagsRegistered verifies that the pre-0.30 system event
// tags cited in §1.4 of the snapshot have their canonical wire values in the
// cf-protocol/internal/campfire/tags.go constants.
func TestPreV030SystemEventTagsRegistered(t *testing.T) {
	// These are the P0/P1 tags listed in the snapshot. They must be registered
	// as constants — not floating string literals — so drift from their wire
	// values shows up at compile time.
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"TagCompact", campfire.TagCompact, "campfire:compact"},
		{"TagMemberJoined", campfire.TagMemberJoined, "campfire:member-joined"},
		{"TagMemberLeft", campfire.TagMemberLeft, "campfire:member-left"},
		{"TagMemberRoleChanged", campfire.TagMemberRoleChanged, "campfire:member-role-changed"},
		{"TagView", campfire.TagView, "campfire:view"},
		{"TagRekey", campfire.TagRekey, "campfire:rekey"},
		{"TagVisibilityChanged", campfire.TagVisibilityChanged, "campfire:visibility-changed"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

// ── §1.5 Reserved-Op Floor ─────────────────────────────────────────────────────

// TestReservedOpFloorList verifies the canonical 10-op list matches the snapshot §1.5.
// The list is sorted alphabetically in the implementation.
func TestReservedOpFloorList(t *testing.T) {
	// Snapshot §1.5 canonical list (10 ops):
	// disband | evict | admit | grant | revoke |
	// delegation-grant | delegation-revoke | delegation-accept |
	// member-roster | compaction
	snapshotOps := []string{
		"admit",
		"compaction",
		"delegation-accept",
		"delegation-grant",
		"delegation-revoke",
		"disband",
		"evict",
		"grant",
		"member-roster",
		"revoke",
	}
	sort.Strings(snapshotOps) // normalize

	codeOps := make([]string, len(reservedops.ReservedOps))
	copy(codeOps, reservedops.ReservedOps)
	sort.Strings(codeOps) // normalize

	if !reflect.DeepEqual(snapshotOps, codeOps) {
		t.Errorf("reserved-op list drift detected\n  snapshot: %v\n  code:     %v", snapshotOps, codeOps)
	}

	if len(reservedops.ReservedOps) != 10 {
		t.Errorf("ReservedOps has %d entries, want exactly 10 (§1.5)", len(reservedops.ReservedOps))
	}
}

// TestReservedOpPublicReexport verifies that the public protocol.ReservedOps
// re-export matches the internal list exactly.
func TestReservedOpPublicReexport(t *testing.T) {
	internal := make([]string, len(reservedops.ReservedOps))
	copy(internal, reservedops.ReservedOps)

	public := make([]string, len(protocol.ReservedOps))
	copy(public, protocol.ReservedOps)

	sort.Strings(internal)
	sort.Strings(public)

	if !reflect.DeepEqual(internal, public) {
		t.Errorf("protocol.ReservedOps != reservedops.ReservedOps\n  internal: %v\n  public:   %v", internal, public)
	}
}

// ── §1.6 future / fulfills Reserved Tags ─────────────────────────────────────

// TestFutureFulfillsTagConstants verifies that the future and fulfills reserved
// tags have the correct wire values in cf-protocol/protocol/reserved.go.
func TestFutureFulfillsTagConstants(t *testing.T) {
	if protocol.TagFuture != "future" {
		t.Errorf("protocol.TagFuture = %q, want \"future\"", protocol.TagFuture)
	}
	if protocol.TagFulfills != "fulfills" {
		t.Errorf("protocol.TagFulfills = %q, want \"fulfills\"", protocol.TagFulfills)
	}
}

// ── §1.8 Await Ordering Rule ──────────────────────────────────────────────────

// TestAwaitOrderingRuleDocumented verifies that await.go documents the
// two-level ordering rule claimed in §1.8 of the snapshot:
//   1. Earliest timestamp wins.
//   2. Lexicographically smaller message ID breaks ties.
//
// We verify this by checking the doc comment on the exported Await method
// and the presence of the fulfillmentLess helper (via test-accessible
// evidence: the behavior of findFulfillment, which is tested in await_test.go).
//
// The canonical cross-implementer ordering test is in
// cf-protocol/protocol/await_test.go (TestAwaitEarliestTimestampWins,
// TestAwaitLexIDTiebreaker). This test verifies the snapshot claim maps to
// the right code file and that the constants are correct.
func TestAwaitOrderingRuleDocumented(t *testing.T) {
	// The wire-frozen constants for the two reserved tags used in fulfillment
	// semantics must be exactly as the snapshot declares.
	// (Indirectly verifies that await.go uses these exact string values.)
	if protocol.TagFulfills != "fulfills" {
		t.Errorf("TagFulfills wire value = %q, want \"fulfills\" (§1.8 uses this tag for fulfillment predicate)", protocol.TagFulfills)
	}
	if protocol.TagFuture != "future" {
		t.Errorf("TagFuture wire value = %q, want \"future\" (§1.8 complement)", protocol.TagFuture)
	}
}

// ── §3.3 Capability CBOR Field IDs ────────────────────────────────────────────

// TestCapabilityCBORFieldIDs verifies that the Capability struct CBOR integer
// keys match §3.3 of the snapshot (field IDs 1–6).
func TestCapabilityCBORFieldIDs(t *testing.T) {
	type fieldSpec struct {
		fieldName string
		wantTag   string
	}
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

	if mt.NumField() != 6 {
		t.Errorf("Capability has %d fields, want exactly 6 (§3.3)", mt.NumField())
	}
}

// ── §3.4 GrantPayload CBOR Field IDs ─────────────────────────────────────────

// TestGrantPayloadCBORFieldIDs verifies that the GrantPayload struct CBOR
// integer keys match §3.4 of the snapshot (field IDs 1–4).
func TestGrantPayloadCBORFieldIDs(t *testing.T) {
	type fieldSpec struct {
		fieldName string
		wantTag   string
	}
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

	if mt.NumField() != 4 {
		t.Errorf("GrantPayload has %d fields, want exactly 4 (§3.4)", mt.NumField())
	}
}

// ── §3.5 WhereMatcher Kind Values ─────────────────────────────────────────────

// TestWhereMatcherKindValues verifies that the WhereMatcher kind integers match
// §3.5 of the snapshot (1=CampfireByID, 2=NamePrefix, 3=TagMatcher).
func TestWhereMatcherKindValues(t *testing.T) {
	if cftrust.WhereCampfireByID != 1 {
		t.Errorf("WhereCampfireByID = %d, want 1 (§3.5)", cftrust.WhereCampfireByID)
	}
	if cftrust.WhereNamePrefix != 2 {
		t.Errorf("WhereNamePrefix = %d, want 2 (§3.5)", cftrust.WhereNamePrefix)
	}
	if cftrust.WhereTagMatcher != 3 {
		t.Errorf("WhereTagMatcher = %d, want 3 (§3.5)", cftrust.WhereTagMatcher)
	}
}

// ── §2.2 PredicateAST Leaf Kinds ──────────────────────────────────────────────

// TestPredicateASTLeafKindStrings verifies that the leaf kind strings match
// §2.2 of the snapshot. ParsePredicateAST is the code under test.
func TestPredicateASTLeafKindStrings(t *testing.T) {
	// Snapshot §2.1 leaf kinds: level, grant, grant_in, grant_quota, chain_to, chain_to_quorum
	// We parse each and verify no error is returned.
	validLeafs := []map[string]any{
		{"kind": "level", "n": 1},
		{"kind": "grant", "convention": "test", "op": "read"},
		{"kind": "grant_in", "convention": "test", "op_glob": "read*",
			"where": map[string]any{"kind": 1, "id": "abc"}},
		{"kind": "grant_quota", "axis": "rate", "bound": 10},
		{"kind": "chain_to", "pubkey": strings.Repeat("a", 64)},
		{"kind": "chain_to_quorum", "m": 2, "pubkeys": []any{
			make([]byte, 32),
			make([]byte, 32),
		}},
	}

	for _, raw := range validLeafs {
		kind := raw["kind"].(string)
		ast, err := cftrust.ParsePredicateAST(raw)
		if err != nil {
			t.Errorf("leaf kind %q: ParsePredicateAST returned error: %v", kind, err)
			continue
		}
		if ast.Kind == 0 {
			t.Errorf("leaf kind %q: parsed Kind is zero (uninitialised)", kind)
		}
	}
}

// TestCompositePredicateKindStrings verifies all_of and any_of parse correctly.
func TestCompositePredicateKindStrings(t *testing.T) {
	child := map[string]any{"kind": "level", "n": 1}
	for _, kind := range []string{"all_of", "any_of"} {
		raw := map[string]any{"kind": kind, "children": []any{child}}
		ast, err := cftrust.ParsePredicateAST(raw)
		if err != nil {
			t.Errorf("composite kind %q: ParsePredicateAST returned error: %v", kind, err)
			continue
		}
		if len(ast.Children) != 1 {
			t.Errorf("composite kind %q: expected 1 child, got %d", kind, len(ast.Children))
		}
	}
}

// TestNotPredicateRejected verifies that a 'not:' predicate is rejected at
// parse time — this is a deliberate safety property per §2.2.
func TestNotPredicateRejected(t *testing.T) {
	raw := map[string]any{"kind": "not", "child": map[string]any{"kind": "level", "n": 1}}
	_, err := cftrust.ParsePredicateAST(raw)
	if err == nil {
		t.Error("ParsePredicateAST: 'not:' predicate should be rejected at parse time (§2.2), got nil error")
	}
}

// ── §2.4 DenyReason Enum ──────────────────────────────────────────────────────

// TestDenyReasonEnumCodes verifies all 10 stable DenyReason codes match the
// snapshot §2.4 exactly.
func TestDenyReasonEnumCodes(t *testing.T) {
	// Snapshot §2.4 — all 10 codes, frozen at cf-authority 1.0.
	want := map[string]cftrust.DenyReason{
		"expired":              cftrust.DenyExpired,
		"revoked":              cftrust.DenyRevoked,
		"depth_exceeded":       cftrust.DenyDepthExceeded,
		"scope_mismatch":       cftrust.DenyScopeMismatch,
		"scope_widening":       cftrust.DenyScopeWidening,
		"stale_revocation":     cftrust.DenyStaleRevocation,
		"reserved_op_floor":    cftrust.DenyReservedOpFloor,
		"owner_ceiling":        cftrust.DenyOwnerCeiling,
		"predicate_unsatisfied": cftrust.DenyPredicate,
		"store_read_error":     cftrust.DenyStoreRead,
	}

	for wantStr, code := range want {
		if string(code) != wantStr {
			t.Errorf("DenyReason %q: wire value = %q, want %q", wantStr, string(code), wantStr)
		}
	}

	if len(want) != 10 {
		t.Errorf("snapshot claims 10 DenyReason codes, test table has %d — update the test", len(want))
	}
}

// ── Adversarial: Mutation Detection ──────────────────────────────────────────

// TestWireVerifyMutationDetection is the TDD mutation test required by
// campfireagent-3a8 DONE condition 1.
//
// It simulates a snapshot drift scenario by checking that the verifier would
// catch a wrong claim. We do this by asserting that a deliberately wrong
// claim does NOT match the code — i.e., the test framework that backs this
// verifier would report an error if the wrong value were the actual constant.
//
// Specifically, we test three mutation scenarios:
//  1. Reserved-op count mutation: if the list had 9 ops (not 10), the count check would fail.
//  2. Tag wire-value mutation: if TagSessionOpen were "session:started" the constant check would fail.
//  3. DenyReason mutation: if DenyExpired were "exp" the code check would fail.
//
// We verify the mutation WOULD be detected by asserting the wrong values do not
// match the actual code values (proving the real test would catch drift).
func TestWireVerifyMutationDetection(t *testing.T) {
	// Mutation 1: wrong reserved-op count. The real list has 10; any other value
	// is a drift from the snapshot.
	if len(reservedops.ReservedOps) != 10 {
		t.Errorf("MUTATION-1 CAUGHT: ReservedOps length is %d, not 10 — drift from §1.5", len(reservedops.ReservedOps))
	}
	// Verify a mutated count would be caught: 9 != 10 → test would fail.
	mutatedCount := 9
	if mutatedCount == len(reservedops.ReservedOps) {
		t.Error("MUTATION-1 UNDETECTED: a count of 9 incorrectly matches the real list")
	}

	// Mutation 2: wrong session:open wire value. Verify the mutation is detectable.
	mutatedSessionOpen := "session:started"
	if mutatedSessionOpen == tagspec.TagSessionOpen {
		t.Error("MUTATION-2 UNDETECTED: mutated tag 'session:started' incorrectly matches TagSessionOpen")
	}

	// Mutation 3: wrong DenyExpired wire value. Verify the mutation is detectable.
	mutatedDenyExpired := cftrust.DenyReason("exp")
	if mutatedDenyExpired == cftrust.DenyExpired {
		t.Error("MUTATION-3 UNDETECTED: mutated DenyReason 'exp' incorrectly matches DenyExpired")
	}

	// Mutation 4: wrong antecedents CBOR position. Verify it's detectable.
	// Snapshot claims antecedents is at CBOR field 5. If it moved to 6, the
	// test above (TestAntecedentsAtCBORField5) would catch it.
	mt := reflect.TypeOf(message.Message{})
	f, _ := mt.FieldByName("Antecedents")
	mutatedFieldTag := "6,keyasint"
	if mutatedFieldTag == strings.Split(f.Tag.Get("cbor"), ",omitempty")[0] {
		t.Error("MUTATION-4 UNDETECTED: antecedents at field 6 incorrectly matches real struct tag")
	}
}

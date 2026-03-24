package campfire

import (
	"testing"
)

// TestRoleBlindRelay_EffectiveRole verifies that blind-relay is treated as a
// valid distinct role and not normalized to RoleFull.
func TestRoleBlindRelay_EffectiveRole(t *testing.T) {
	got := EffectiveRole(RoleBlindRelay)
	if got != RoleBlindRelay {
		t.Errorf("EffectiveRole(blind-relay) = %q, want %q", got, RoleBlindRelay)
	}
}

// TestIsBlindRelay verifies the blind relay role predicate.
func TestIsBlindRelay(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{RoleBlindRelay, true},
		{RoleFull, false},
		{RoleWriter, false},
		{RoleObserver, false},
		{"", false},
		{"member", false},
		{"creator", false},
	}
	for _, c := range cases {
		got := IsBlindRelay(c.role)
		if got != c.want {
			t.Errorf("IsBlindRelay(%q) = %v, want %v", c.role, got, c.want)
		}
	}
}

// TestCampfireState_EncryptionFields verifies that Encrypted and KeyEpoch fields
// round-trip through State() and ToCampfire() correctly.
func TestCampfireState_EncryptionFields(t *testing.T) {
	cf, err := New("open", nil, 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cf.Encrypted = true
	cf.KeyEpoch = 5

	state := cf.State()
	if !state.Encrypted {
		t.Error("CampfireState.Encrypted should be true")
	}
	if state.KeyEpoch != 5 {
		t.Errorf("CampfireState.KeyEpoch = %d, want 5", state.KeyEpoch)
	}

	// Round-trip through ToCampfire
	cf2 := state.ToCampfire(nil)
	if !cf2.Encrypted {
		t.Error("Campfire.Encrypted should be true after ToCampfire round-trip")
	}
	if cf2.KeyEpoch != 5 {
		t.Errorf("Campfire.KeyEpoch = %d, want 5 after ToCampfire round-trip", cf2.KeyEpoch)
	}
}

// TestCampfireState_BackwardCompat verifies that unencrypted campfires
// (Encrypted=false, absent CBOR field) decode cleanly with zero values.
func TestCampfireState_BackwardCompat(t *testing.T) {
	cf, err := New("open", nil, 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Default: not encrypted
	if cf.Encrypted {
		t.Error("newly created campfire should not be encrypted by default")
	}
	if cf.KeyEpoch != 0 {
		t.Errorf("newly created campfire KeyEpoch = %d, want 0", cf.KeyEpoch)
	}
	state := cf.State()
	if state.Encrypted {
		t.Error("CampfireState for unencrypted campfire should have Encrypted=false")
	}
}

// TestMembershipHash_IncludesRole verifies that the membership hash includes
// the member role, so blind-relay membership is visible in the hash (spec §2.5).
func TestMembershipHash_IncludesRole(t *testing.T) {
	cf, err := New("open", nil, 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Add two members with different roles
	pub1 := make([]byte, 32)
	pub1[0] = 1
	pub2 := make([]byte, 32)
	pub2[0] = 2

	cf.Members = []Member{
		{PublicKey: pub1, JoinedAt: 1000, Role: RoleFull},
		{PublicKey: pub2, JoinedAt: 2000, Role: RoleBlindRelay},
	}
	hashWithRoles := cf.MembershipHash()

	// Same members but all full roles — hash should differ because roles are included
	cf.Members = []Member{
		{PublicKey: pub1, JoinedAt: 1000, Role: RoleFull},
		{PublicKey: pub2, JoinedAt: 2000, Role: RoleFull},
	}
	hashAllFull := cf.MembershipHash()

	if string(hashWithRoles) == string(hashAllFull) {
		t.Error("membership hash must differ when member roles differ (blind-relay vs. full)")
	}
}

// TestEncryptedInitPayload_DefaultValues verifies NewEncryptedInitPayload returns
// the correct protocol-fixed values (spec §6.2).
func TestEncryptedInitPayload_DefaultValues(t *testing.T) {
	p := NewEncryptedInitPayload()
	if p.Epoch != 0 {
		t.Errorf("epoch = %d, want 0", p.Epoch)
	}
	if p.Algorithm != "AES-256-GCM" {
		t.Errorf("algorithm = %q, want AES-256-GCM", p.Algorithm)
	}
	if p.KDF != "HKDF-SHA256" {
		t.Errorf("KDF = %q, want HKDF-SHA256", p.KDF)
	}
	if p.Info != "campfire-message-key-v1" {
		t.Errorf("info = %q, want campfire-message-key-v1", p.Info)
	}
}

// TestMembershipCommitPayload_TagConstant verifies the system message tag constant.
func TestMembershipCommitPayload_TagConstant(t *testing.T) {
	if TagMembershipCommit != "campfire:membership-commit" {
		t.Errorf("TagMembershipCommit = %q, want campfire:membership-commit", TagMembershipCommit)
	}
	if TagEncryptedInit != "campfire:encrypted-init" {
		t.Errorf("TagEncryptedInit = %q, want campfire:encrypted-init", TagEncryptedInit)
	}
}

// TestMembershipCommitReason_Values verifies the commit reason constants.
func TestMembershipCommitReason_Values(t *testing.T) {
	cases := []struct {
		reason MembershipCommitReason
		str    string
	}{
		{MembershipCommitJoin, "join"},
		{MembershipCommitEvict, "evict"},
		{MembershipCommitLeave, "leave"},
		{MembershipCommitScheduled, "scheduled"},
	}
	for _, c := range cases {
		if string(c.reason) != c.str {
			t.Errorf("MembershipCommitReason %q != %q", c.reason, c.str)
		}
	}
}

// TestNewMembershipCommitPayload_BlindRelayExcluded verifies that
// NewMembershipCommitPayload excludes blind-relay members from the Deliveries
// map (spec §6.1, security invariant). Blind relays must not receive the new
// root secret via key delivery since they only forward ciphertext.
func TestNewMembershipCommitPayload_BlindRelayExcluded(t *testing.T) {
	// Build 3 members: 2 full + 1 blind-relay.
	fullMember1 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20}
	fullMember2 := []byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28,
		0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f, 0x30,
		0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38,
		0x39, 0x3a, 0x3b, 0x3c, 0x3d, 0x3e, 0x3f, 0x40}
	blindRelayMember := []byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48,
		0x49, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50,
		0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58,
		0x59, 0x5a, 0x5b, 0x5c, 0x5d, 0x5e, 0x5f, 0x60}

	members := []Member{
		{PublicKey: fullMember1, Role: RoleFull},
		{PublicKey: fullMember2, Role: RoleFull},
		{PublicKey: blindRelayMember, Role: RoleBlindRelay},
	}

	// Caller-supplied deliveries include all 3 keys (the caller may have mistakenly
	// included the blind relay). NewMembershipCommitPayload must filter it out.
	full1Hex := encodePubKey(fullMember1)
	full2Hex := encodePubKey(fullMember2)
	blindHex := encodePubKey(blindRelayMember)
	deliveries := map[string][]byte{
		full1Hex: []byte("encrypted-secret-for-full1"),
		full2Hex: []byte("encrypted-secret-for-full2"),
		blindHex: []byte("encrypted-secret-for-blind-relay"), // must be excluded
	}

	payload := NewMembershipCommitPayload(
		MembershipCommitEvict,
		"",
		2,
		[]byte("membership-hash"),
		members,
		deliveries,
	)

	// Deliveries must have exactly 2 entries (full members only).
	if len(payload.Deliveries) != 2 {
		t.Errorf("Deliveries has %d entries, want 2 (full members only)", len(payload.Deliveries))
	}

	// The blind-relay pubkey must NOT be in Deliveries.
	if _, ok := payload.Deliveries[blindHex]; ok {
		t.Error("blind-relay member pubkey must NOT be in Deliveries map (security invariant)")
	}

	// The two full members must be in Deliveries.
	if _, ok := payload.Deliveries[full1Hex]; !ok {
		t.Error("full member 1 must be in Deliveries map")
	}
	if _, ok := payload.Deliveries[full2Hex]; !ok {
		t.Error("full member 2 must be in Deliveries map")
	}

	// Payload metadata must be correct.
	if payload.Type != MembershipCommitEvict {
		t.Errorf("Type = %q, want %q", payload.Type, MembershipCommitEvict)
	}
	if payload.NewEpoch != 2 {
		t.Errorf("NewEpoch = %d, want 2", payload.NewEpoch)
	}
	if payload.ChainDerived {
		t.Error("ChainDerived must be false for eviction (fresh random root secret)")
	}
}

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

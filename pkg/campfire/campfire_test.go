package campfire

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestNew(t *testing.T) {
	c, err := New("open", []string{"status-update"}, 1)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if c.JoinProtocol != "open" {
		t.Errorf("join_protocol = %s, want open", c.JoinProtocol)
	}
	if len(c.ReceptionRequirements) != 1 || c.ReceptionRequirements[0] != "status-update" {
		t.Errorf("reception_requirements = %v, want [status-update]", c.ReceptionRequirements)
	}
	if len(c.Members) != 0 {
		t.Errorf("members = %d, want 0", len(c.Members))
	}
	if len(c.PublicKeyHex()) != 64 {
		t.Errorf("public key hex length = %d, want 64", len(c.PublicKeyHex()))
	}
}

func TestNewNilReqs(t *testing.T) {
	c, _ := New("open", nil, 1)
	if c.ReceptionRequirements == nil {
		t.Error("reception_requirements should not be nil")
	}
}

func TestAddRemoveMember(t *testing.T) {
	c, _ := New("open", nil, 1)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	c.AddMember(pub)
	if !c.IsMember(pub) {
		t.Error("should be a member after add")
	}
	if len(c.Members) != 1 {
		t.Errorf("members = %d, want 1", len(c.Members))
	}

	if !c.RemoveMember(pub) {
		t.Error("RemoveMember should return true for existing member")
	}
	if c.IsMember(pub) {
		t.Error("should not be a member after remove")
	}

	if c.RemoveMember(pub) {
		t.Error("RemoveMember should return false for non-member")
	}
}

func TestMembershipHash(t *testing.T) {
	c, _ := New("open", nil, 1)
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)

	// Empty membership hash should be deterministic
	h1 := c.MembershipHash()
	h2 := c.MembershipHash()
	if !bytes.Equal(h1, h2) {
		t.Error("empty membership hash should be deterministic")
	}

	// Adding members should change the hash
	c.AddMember(pub1)
	h3 := c.MembershipHash()
	if bytes.Equal(h1, h3) {
		t.Error("hash should change after adding a member")
	}

	// Order-independent: add in different order, same hash
	c2, _ := New("open", nil, 1)
	c2.AddMember(pub2)
	c2.AddMember(pub1)

	c.AddMember(pub2)

	if !bytes.Equal(c.MembershipHash(), c2.MembershipHash()) {
		t.Error("membership hash should be order-independent")
	}
}

func TestState(t *testing.T) {
	c, _ := New("invite-only", []string{"breaking-change"}, 1)
	state := c.State()
	if state.JoinProtocol != "invite-only" {
		t.Errorf("state join_protocol = %s, want invite-only", state.JoinProtocol)
	}
	if len(state.PublicKey) != 32 {
		t.Errorf("state public key length = %d, want 32", len(state.PublicKey))
	}
	if len(state.PrivateKey) != 64 {
		t.Errorf("state private key length = %d, want 64", len(state.PrivateKey))
	}
}

// TestToCampfire verifies that CampfireState.ToCampfire correctly reconstructs
// a live Campfire from on-disk state and a provided member list.
func TestToCampfire(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	state := &CampfireState{
		PublicKey:             pub,
		PrivateKey:            priv,
		JoinProtocol:          "invite-only",
		ReceptionRequirements: []string{"breaking-change", "status-update"},
		CreatedAt:             1234567890,
		Threshold:             2,
	}

	memberPub, _, _ := ed25519.GenerateKey(rand.Reader)
	members := []MemberRecord{
		{PublicKey: memberPub, JoinedAt: 9999},
	}

	cf := state.ToCampfire(members)

	if !bytes.Equal(cf.PublicKey, state.PublicKey) {
		t.Error("PublicKey not copied from state")
	}
	if !bytes.Equal(cf.PrivateKey, state.PrivateKey) {
		t.Error("PrivateKey not copied from state")
	}
	if cf.JoinProtocol != state.JoinProtocol {
		t.Errorf("JoinProtocol = %q, want %q", cf.JoinProtocol, state.JoinProtocol)
	}
	if len(cf.ReceptionRequirements) != len(state.ReceptionRequirements) {
		t.Errorf("ReceptionRequirements len = %d, want %d", len(cf.ReceptionRequirements), len(state.ReceptionRequirements))
	}
	if cf.CreatedAt != state.CreatedAt {
		t.Errorf("CreatedAt = %d, want %d", cf.CreatedAt, state.CreatedAt)
	}
	if cf.Threshold != state.Threshold {
		t.Errorf("Threshold = %d, want %d", cf.Threshold, state.Threshold)
	}
	if len(cf.Members) != 1 {
		t.Fatalf("Members len = %d, want 1", len(cf.Members))
	}
	if !bytes.Equal(cf.Members[0].PublicKey, memberPub) {
		t.Error("Members[0].PublicKey does not match provided member")
	}

	// nil members produces empty Members slice (not nil)
	cfNoMembers := state.ToCampfire(nil)
	if len(cfNoMembers.Members) != 0 {
		t.Errorf("nil members: Members len = %d, want 0", len(cfNoMembers.Members))
	}

	// empty members list also produces empty Members slice
	cfEmptyMembers := state.ToCampfire([]MemberRecord{})
	if len(cfEmptyMembers.Members) != 0 {
		t.Errorf("empty members: Members len = %d, want 0", len(cfEmptyMembers.Members))
	}

	// read-only state (no private key) produces Campfire with nil PrivateKey
	readOnlyState := &CampfireState{
		PublicKey:             pub,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{"status-update"},
		CreatedAt:             1234567890,
		Threshold:             1,
	}
	cfReadOnly := readOnlyState.ToCampfire(nil)
	if cfReadOnly.PrivateKey != nil {
		t.Error("read-only state: PrivateKey should be nil")
	}
}

// TestEffectiveRole verifies workspace-bvg: campfire.EffectiveRole returns correct roles.
func TestEffectiveRole(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{RoleObserver, RoleObserver},
		{RoleWriter, RoleWriter},
		{RoleFull, RoleFull},
		{"", RoleFull},        // empty defaults to full
		{"creator", RoleFull}, // legacy role → full
		{"member", RoleFull},  // legacy role → full
		{"unknown", RoleFull}, // unknown → full
	}

	for _, tc := range tests {
		got := EffectiveRole(tc.input)
		if got != tc.want {
			t.Errorf("EffectiveRole(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

package campfire

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
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
	for i := range cf.ReceptionRequirements {
		if cf.ReceptionRequirements[i] != state.ReceptionRequirements[i] {
			t.Errorf("ReceptionRequirements[%d] = %q, want %q", i, cf.ReceptionRequirements[i], state.ReceptionRequirements[i])
		}
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

// TestDeliveryModesCBORRoundTrip verifies that CampfireState.DeliveryModes
// (CBOR field 9) survives a marshal/unmarshal round-trip intact.
func TestDeliveryModesCBORRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	state := CampfireState{
		PublicKey:  pub,
		PrivateKey: priv,
		JoinProtocol: "open",
		ReceptionRequirements: []string{},
		CreatedAt:  1234567890,
		Threshold:  1,
		DeliveryModes: []string{"pull", "push"},
	}

	data, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got CampfireState
	if err := cfencoding.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(got.DeliveryModes) != 2 {
		t.Fatalf("DeliveryModes len = %d, want 2", len(got.DeliveryModes))
	}
	if got.DeliveryModes[0] != "pull" || got.DeliveryModes[1] != "push" {
		t.Errorf("DeliveryModes = %v, want [pull push]", got.DeliveryModes)
	}
}

// TestDeliveryModesCBORBackwardCompat verifies that an old CampfireState CBOR
// blob (without field 9) unmarshals with a nil/empty DeliveryModes, and that
// EffectiveDeliveryModes returns ["pull"] for backward compatibility.
func TestDeliveryModesCBORBackwardCompat(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	// Old state without DeliveryModes.
	type oldCampfireState struct {
		PublicKey             []byte   `cbor:"1,keyasint"`
		PrivateKey            []byte   `cbor:"2,keyasint"`
		JoinProtocol          string   `cbor:"3,keyasint"`
		ReceptionRequirements []string `cbor:"4,keyasint"`
		CreatedAt             int64    `cbor:"5,keyasint"`
		Threshold             uint     `cbor:"6,keyasint"`
		Encrypted             bool     `cbor:"7,keyasint,omitempty"`
		KeyEpoch              uint64   `cbor:"8,keyasint,omitempty"`
		// No field 9 (DeliveryModes)
	}

	old := oldCampfireState{
		PublicKey:             pub,
		PrivateKey:            priv,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             1234567890,
		Threshold:             1,
	}

	data, err := cfencoding.Marshal(old)
	if err != nil {
		t.Fatalf("Marshal old state: %v", err)
	}

	var got CampfireState
	if err := cfencoding.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal into new CampfireState: %v", err)
	}

	// DeliveryModes should be nil or empty — backward compat.
	if len(got.DeliveryModes) != 0 {
		t.Errorf("DeliveryModes = %v, want nil/empty for old CBOR", got.DeliveryModes)
	}

	// EffectiveDeliveryModes should return ["pull"] as default.
	effective := EffectiveDeliveryModes(got.DeliveryModes)
	if len(effective) != 1 || effective[0] != DeliveryModePull {
		t.Errorf("EffectiveDeliveryModes = %v, want [pull]", effective)
	}
}

// TestEffectiveDeliveryModes verifies all cases of the EffectiveDeliveryModes helper.
func TestEffectiveDeliveryModes(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{"nil input", nil, []string{"pull"}},
		{"empty input", []string{}, []string{"pull"}},
		{"pull only", []string{"pull"}, []string{"pull"}},
		{"push only", []string{"push"}, []string{"push"}},
		{"both modes", []string{"pull", "push"}, []string{"pull", "push"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveDeliveryModes(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("EffectiveDeliveryModes(%v) = %v, want %v", tc.input, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("EffectiveDeliveryModes[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestValidDeliveryMode verifies ValidDeliveryMode accepts only pull/push.
func TestValidDeliveryMode(t *testing.T) {
	tests := []struct {
		mode  string
		valid bool
	}{
		{"pull", true},
		{"push", true},
		{"", false},
		{"webhook", false},
		{"Poll", false}, // case-sensitive
	}

	for _, tc := range tests {
		got := ValidDeliveryMode(tc.mode)
		if got != tc.valid {
			t.Errorf("ValidDeliveryMode(%q) = %v, want %v", tc.mode, got, tc.valid)
		}
	}
}

// TestStateIncludesDeliveryModes verifies Campfire.State() propagates DeliveryModes.
func TestStateIncludesDeliveryModes(t *testing.T) {
	cf, _ := New("open", nil, 1)
	cf.DeliveryModes = []string{"pull", "push"}

	state := cf.State()
	if len(state.DeliveryModes) != 2 {
		t.Fatalf("State().DeliveryModes len = %d, want 2", len(state.DeliveryModes))
	}
	if state.DeliveryModes[0] != "pull" || state.DeliveryModes[1] != "push" {
		t.Errorf("State().DeliveryModes = %v, want [pull push]", state.DeliveryModes)
	}
}

// TestToCampfireDeliveryModes verifies CampfireState.ToCampfire propagates DeliveryModes.
func TestToCampfireDeliveryModes(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	state := &CampfireState{
		PublicKey:     pub,
		PrivateKey:    priv,
		JoinProtocol:  "open",
		CreatedAt:     1234567890,
		Threshold:     1,
		DeliveryModes: []string{"push"},
	}

	cf := state.ToCampfire(nil)
	if len(cf.DeliveryModes) != 1 || cf.DeliveryModes[0] != "push" {
		t.Errorf("ToCampfire().DeliveryModes = %v, want [push]", cf.DeliveryModes)
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

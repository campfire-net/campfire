package campfire

import (
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
	if !equal(h1, h2) {
		t.Error("empty membership hash should be deterministic")
	}

	// Adding members should change the hash
	c.AddMember(pub1)
	h3 := c.MembershipHash()
	if equal(h1, h3) {
		t.Error("hash should change after adding a member")
	}

	// Order-independent: add in different order, same hash
	c2, _ := New("open", nil, 1)
	c2.AddMember(pub2)
	c2.AddMember(pub1)

	c.AddMember(pub2)

	if !equal(c.MembershipHash(), c2.MembershipHash()) {
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

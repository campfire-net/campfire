package convention

import (
	"testing"
)

// TestConnectRequestDeclaration verifies the structure of the connect-request declaration.
func TestConnectRequestDeclaration(t *testing.T) {
	decl := ConnectRequestDeclaration()

	if decl.Convention != SocialConvention {
		t.Errorf("convention: want %q, got %q", SocialConvention, decl.Convention)
	}
	if decl.Version != "0.1" {
		t.Errorf("version: want %q, got %q", "0.1", decl.Version)
	}
	if decl.Operation != "connect-request" {
		t.Errorf("operation: want %q, got %q", "connect-request", decl.Operation)
	}
	if decl.Signing != "member_key" {
		t.Errorf("signing: want %q, got %q", "member_key", decl.Signing)
	}
	if decl.SignerType != SignerMemberKey {
		t.Errorf("signer_type: want %q, got %q", SignerMemberKey, decl.SignerType)
	}

	if len(decl.ProducesTags) != 1 {
		t.Fatalf("produces_tags: want 1, got %d", len(decl.ProducesTags))
	}
	if decl.ProducesTags[0].Tag != SocialConnectRequestTag {
		t.Errorf("produces_tags[0].tag: want %q, got %q", SocialConnectRequestTag, decl.ProducesTags[0].Tag)
	}
	if decl.ProducesTags[0].Cardinality != "exactly_one" {
		t.Errorf("produces_tags[0].cardinality: want %q, got %q", "exactly_one", decl.ProducesTags[0].Cardinality)
	}

	args := argMap(decl.Args)

	requesterIDArg, ok := args["requester_campfire_id"]
	if !ok {
		t.Fatal("missing 'requester_campfire_id' arg")
	}
	if !requesterIDArg.Required {
		t.Error("'requester_campfire_id' should be required")
	}
	if requesterIDArg.Type != "string" {
		t.Errorf("'requester_campfire_id' type: want string, got %q", requesterIDArg.Type)
	}

	nameArg, ok := args["requester_name"]
	if !ok {
		t.Fatal("missing 'requester_name' arg")
	}
	if nameArg.Required {
		t.Error("'requester_name' should be optional")
	}
	if nameArg.MaxLength != 64 {
		t.Errorf("'requester_name' MaxLength: want 64, got %d", nameArg.MaxLength)
	}
}

// TestAcceptConnectionDeclaration verifies the structure of the accept-connection declaration.
func TestAcceptConnectionDeclaration(t *testing.T) {
	decl := AcceptConnectionDeclaration()

	if decl.Convention != SocialConvention {
		t.Errorf("convention: want %q, got %q", SocialConvention, decl.Convention)
	}
	if decl.Operation != "accept-connection" {
		t.Errorf("operation: want %q, got %q", "accept-connection", decl.Operation)
	}
	if decl.Signing != "member_key" {
		t.Errorf("signing: want %q, got %q", "member_key", decl.Signing)
	}
	if decl.SignerType != SignerMemberKey {
		t.Errorf("signer_type: want %q, got %q", SignerMemberKey, decl.SignerType)
	}

	if len(decl.ProducesTags) != 1 {
		t.Fatalf("produces_tags: want 1, got %d", len(decl.ProducesTags))
	}
	if decl.ProducesTags[0].Tag != SocialConnectAcceptedTag {
		t.Errorf("produces_tags[0].tag: want %q, got %q", SocialConnectAcceptedTag, decl.ProducesTags[0].Tag)
	}

	args := argMap(decl.Args)

	requesterIDArg, ok := args["requester_campfire_id"]
	if !ok {
		t.Fatal("missing 'requester_campfire_id' arg")
	}
	if !requesterIDArg.Required {
		t.Error("'requester_campfire_id' should be required")
	}

	channelArg, ok := args["shared_channel_id"]
	if !ok {
		t.Fatal("missing 'shared_channel_id' arg")
	}
	if channelArg.Required {
		t.Error("'shared_channel_id' should be optional")
	}
}

// TestRejectConnectionDeclaration verifies the structure of the reject-connection declaration.
func TestRejectConnectionDeclaration(t *testing.T) {
	decl := RejectConnectionDeclaration()

	if decl.Convention != SocialConvention {
		t.Errorf("convention: want %q, got %q", SocialConvention, decl.Convention)
	}
	if decl.Operation != "reject-connection" {
		t.Errorf("operation: want %q, got %q", "reject-connection", decl.Operation)
	}
	if decl.Signing != "member_key" {
		t.Errorf("signing: want %q, got %q", "member_key", decl.Signing)
	}
	if decl.SignerType != SignerMemberKey {
		t.Errorf("signer_type: want %q, got %q", SignerMemberKey, decl.SignerType)
	}

	if len(decl.ProducesTags) != 1 {
		t.Fatalf("produces_tags: want 1, got %d", len(decl.ProducesTags))
	}
	if decl.ProducesTags[0].Tag != SocialConnectRejectedTag {
		t.Errorf("produces_tags[0].tag: want %q, got %q", SocialConnectRejectedTag, decl.ProducesTags[0].Tag)
	}

	args := argMap(decl.Args)

	reasonArg, ok := args["reason"]
	if !ok {
		t.Fatal("missing 'reason' arg")
	}
	if reasonArg.Required {
		t.Error("'reason' should be optional")
	}
	if reasonArg.MaxLength != 256 {
		t.Errorf("'reason' MaxLength: want 256, got %d", reasonArg.MaxLength)
	}
}

// TestConnectDeclarations verifies ConnectDeclarations returns all three in order.
func TestConnectDeclarations(t *testing.T) {
	decls := ConnectDeclarations()

	if len(decls) != 3 {
		t.Fatalf("ConnectDeclarations: want 3, got %d", len(decls))
	}
	if decls[0].Operation != "connect-request" {
		t.Errorf("decls[0].Operation: want %q, got %q", "connect-request", decls[0].Operation)
	}
	if decls[1].Operation != "accept-connection" {
		t.Errorf("decls[1].Operation: want %q, got %q", "accept-connection", decls[1].Operation)
	}
	if decls[2].Operation != "reject-connection" {
		t.Errorf("decls[2].Operation: want %q, got %q", "reject-connection", decls[2].Operation)
	}

	// All must be in the social convention.
	for _, d := range decls {
		if d.Convention != SocialConvention {
			t.Errorf("decl %q: convention = %q, want %q", d.Operation, d.Convention, SocialConvention)
		}
	}
}

// TestSocialTagConstants verifies the tag constant values follow the convention naming scheme.
func TestSocialTagConstants(t *testing.T) {
	cases := []struct {
		name string
		tag  string
		want string
	}{
		{"SocialConnectRequestTag", SocialConnectRequestTag, "social:connect-request"},
		{"SocialConnectAcceptedTag", SocialConnectAcceptedTag, "social:connect-accepted"},
		{"SocialConnectRejectedTag", SocialConnectRejectedTag, "social:connect-rejected"},
	}
	for _, tc := range cases {
		if tc.tag != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.tag, tc.want)
		}
	}
}

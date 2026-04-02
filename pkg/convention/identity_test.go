package convention

import (
	"testing"
)

// TestIntroduceMeDeclaration verifies the structure of the introduce-me declaration.
func TestIntroduceMeDeclaration(t *testing.T) {
	decl := IntroduceMeDeclaration()

	if decl.Convention != IdentityConvention {
		t.Errorf("convention: want %q, got %q", IdentityConvention, decl.Convention)
	}
	if decl.Version != "0.1" {
		t.Errorf("version: want %q, got %q", "0.1", decl.Version)
	}
	if decl.Operation != "introduce-me" {
		t.Errorf("operation: want %q, got %q", "introduce-me", decl.Operation)
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
	if decl.ProducesTags[0].Tag != IdentityIntroductionTag {
		t.Errorf("produces_tags[0].tag: want %q, got %q", IdentityIntroductionTag, decl.ProducesTags[0].Tag)
	}
	if decl.ProducesTags[0].Cardinality != "exactly_one" {
		t.Errorf("produces_tags[0].cardinality: want %q, got %q", "exactly_one", decl.ProducesTags[0].Cardinality)
	}

	// Check required args
	argByName := argMap(decl.Args)
	pubkeyArg, ok := argByName["pubkey_hex"]
	if !ok {
		t.Fatal("missing 'pubkey_hex' arg")
	}
	if !pubkeyArg.Required {
		t.Error("'pubkey_hex' should be required")
	}
	if pubkeyArg.Type != "string" {
		t.Errorf("'pubkey_hex' type: want string, got %q", pubkeyArg.Type)
	}

	displayArg, ok := argByName["display_name"]
	if !ok {
		t.Fatal("missing 'display_name' arg")
	}
	if displayArg.Required {
		t.Error("'display_name' should not be required (tainted, optional)")
	}
	if displayArg.MaxLength != 64 {
		t.Errorf("'display_name' max_length: want 64, got %d", displayArg.MaxLength)
	}

	homesArg, ok := argByName["home_campfire_ids"]
	if !ok {
		t.Fatal("missing 'home_campfire_ids' arg")
	}
	if homesArg.Required {
		t.Error("'home_campfire_ids' should not be required")
	}
	if !homesArg.Repeated {
		t.Error("'home_campfire_ids' should be repeated")
	}
}

// TestVerifyMeDeclaration verifies the structure of the verify-me declaration.
func TestVerifyMeDeclaration(t *testing.T) {
	decl := VerifyMeDeclaration()

	if decl.Convention != IdentityConvention {
		t.Errorf("convention: want %q, got %q", IdentityConvention, decl.Convention)
	}
	if decl.Operation != "verify-me" {
		t.Errorf("operation: want %q, got %q", "verify-me", decl.Operation)
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
	if decl.ProducesTags[0].Tag != IdentityChallengeRespTag {
		t.Errorf("produces_tags[0].tag: want %q, got %q", IdentityChallengeRespTag, decl.ProducesTags[0].Tag)
	}

	argByName := argMap(decl.Args)
	challengeArg, ok := argByName["challenge"]
	if !ok {
		t.Fatal("missing 'challenge' arg")
	}
	if !challengeArg.Required {
		t.Error("'challenge' should be required")
	}
	if challengeArg.Type != "string" {
		t.Errorf("'challenge' type: want string, got %q", challengeArg.Type)
	}
}

// TestListHomesDeclaration verifies the structure of the list-homes declaration.
func TestListHomesDeclaration(t *testing.T) {
	decl := ListHomesDeclaration()

	if decl.Convention != IdentityConvention {
		t.Errorf("convention: want %q, got %q", IdentityConvention, decl.Convention)
	}
	if decl.Operation != "list-homes" {
		t.Errorf("operation: want %q, got %q", "list-homes", decl.Operation)
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
	if decl.ProducesTags[0].Tag != IdentityHomesTag {
		t.Errorf("produces_tags[0].tag: want %q, got %q", IdentityHomesTag, decl.ProducesTags[0].Tag)
	}

	// list-homes takes no args
	if len(decl.Args) != 0 {
		t.Errorf("args: want 0, got %d", len(decl.Args))
	}
}

// TestDeclareHomeDeclaration verifies the structure of the declare-home declaration.
func TestDeclareHomeDeclaration(t *testing.T) {
	decl := DeclareHomeDeclaration()

	if decl.Convention != IdentityConvention {
		t.Errorf("convention: want %q, got %q", IdentityConvention, decl.Convention)
	}
	if decl.Operation != "declare-home" {
		t.Errorf("operation: want %q, got %q", "declare-home", decl.Operation)
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
	if decl.ProducesTags[0].Tag != IdentityHomeDeclaredTag {
		t.Errorf("produces_tags[0].tag: want %q, got %q", IdentityHomeDeclaredTag, decl.ProducesTags[0].Tag)
	}

	argByName := argMap(decl.Args)
	campfireIDArg, ok := argByName["campfire_id"]
	if !ok {
		t.Fatal("missing 'campfire_id' arg")
	}
	if !campfireIDArg.Required {
		t.Error("'campfire_id' should be required")
	}
	if campfireIDArg.Type != "string" {
		t.Errorf("'campfire_id' type: want string, got %q", campfireIDArg.Type)
	}

	roleArg, ok := argByName["role"]
	if !ok {
		t.Fatal("missing 'role' arg")
	}
	if !roleArg.Required {
		t.Error("'role' should be required")
	}
	// Verify role enum values
	roleValues := map[string]bool{}
	for _, v := range roleArg.Values {
		roleValues[v] = true
	}
	for _, want := range []string{"primary", "secondary", "archive"} {
		if !roleValues[want] {
			t.Errorf("'role' values missing %q", want)
		}
	}
}

// TestIdentityDeclarations verifies that IdentityDeclarations returns all four
// operations in canonical order.
func TestIdentityDeclarations(t *testing.T) {
	decls := IdentityDeclarations()

	if len(decls) != 4 {
		t.Fatalf("want 4 declarations, got %d", len(decls))
	}

	wantOps := []string{"introduce-me", "verify-me", "list-homes", "declare-home"}
	for i, want := range wantOps {
		if decls[i].Operation != want {
			t.Errorf("decls[%d].Operation: want %q, got %q", i, want, decls[i].Operation)
		}
		if decls[i].Convention != IdentityConvention {
			t.Errorf("decls[%d].Convention: want %q, got %q", i, IdentityConvention, decls[i].Convention)
		}
		// All identity operations use member_key signing
		if decls[i].Signing != "member_key" {
			t.Errorf("decls[%d].Signing: want %q, got %q", i, "member_key", decls[i].Signing)
		}
		if decls[i].SignerType != SignerMemberKey {
			t.Errorf("decls[%d].SignerType: want %q, got %q", i, SignerMemberKey, decls[i].SignerType)
		}
	}
}

// TestIdentityTagConstants verifies that tag constants have the expected values.
func TestIdentityTagConstants(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"IdentityConvention", IdentityConvention, "identity"},
		{"IdentityIntroductionTag", IdentityIntroductionTag, "identity:introduction"},
		{"IdentityChallengeRespTag", IdentityChallengeRespTag, "identity:challenge-response"},
		{"IdentityHomesTag", IdentityHomesTag, "identity:homes"},
		{"IdentityHomeDeclaredTag", IdentityHomeDeclaredTag, "identity:home-declared"},
		{"IdentityHomeEchoTag", IdentityHomeEchoTag, "identity:home-echo"},
		{"IdentityBeaconTag", IdentityBeaconTag, "identity:v1"},
	}
	for _, tc := range tests {
		if tc.value != tc.want {
			t.Errorf("%s: want %q, got %q", tc.name, tc.want, tc.value)
		}
	}
}

// argMap converts a slice of ArgDescriptors to a map keyed by name.
// Defined here for use across identity_test.go; seed_test.go has its own
// inline version.
func argMap(args []ArgDescriptor) map[string]ArgDescriptor {
	m := make(map[string]ArgDescriptor, len(args))
	for _, a := range args {
		m[a.Name] = a
	}
	return m
}

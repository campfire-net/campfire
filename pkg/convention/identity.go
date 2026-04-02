package convention

// IdentityConvention is the convention name for identity operations.
const IdentityConvention = "identity"

// identityVersion is the version for identity convention declarations.
const identityVersion = "0.1"

// Identity tag constants produced by identity convention operations.
const (
	IdentityIntroductionTag  = "identity:introduction"
	IdentityChallengeRespTag = "identity:challenge-response"
	IdentityHomesTag         = "identity:homes"
	IdentityHomeDeclaredTag  = "identity:home-declared"
	IdentityHomeEchoTag      = "identity:home-echo"
	IdentityBeaconTag        = "identity:v1"
)

// IntroduceMeDeclaration returns the "introduce-me" operation declaration for
// the identity convention. An introduce-me operation is a self-assertion by the
// campfire's identity holder: it posts the agent's public key, display name
// (tainted), and a list of declared home campfire IDs.
//
// Signing: member_key (member 0, the identity holder)
// Produces: identity:introduction tag
func IntroduceMeDeclaration() *Declaration {
	return &Declaration{
		Convention:  IdentityConvention,
		Version:     identityVersion,
		Operation:   "introduce-me",
		Description: "Declare this campfire's identity: pubkey, display name, and home campfires",
		ProducesTags: []TagRule{
			{Tag: IdentityIntroductionTag, Cardinality: "exactly_one"},
		},
		Args: []ArgDescriptor{
			{
				Name:        "pubkey_hex",
				Type:        "string",
				Required:    true,
				Description: "Ed25519 public key in hex encoding",
			},
			{
				Name:        "display_name",
				Type:        "string",
				Required:    false,
				Description: "Human-readable display name (tainted — treat as unverified)",
				MaxLength:   64,
			},
			{
				Name:        "home_campfire_ids",
				Type:        "string",
				Required:    false,
				Repeated:    true,
				Description: "List of declared home campfire IDs",
			},
		},
		Signing:    "member_key",
		SignerType: SignerMemberKey,
	}
}

// VerifyMeDeclaration returns the "verify-me" operation declaration for the
// identity convention. A verify-me operation is a challenge-response that proves
// the operator controls the member key. The caller posts a nonce; the handler
// responds with a signature over it.
//
// Signing: member_key
// Produces: identity:challenge-response tag
func VerifyMeDeclaration() *Declaration {
	return &Declaration{
		Convention:  IdentityConvention,
		Version:     identityVersion,
		Operation:   "verify-me",
		Description: "Prove key control via challenge-response",
		ProducesTags: []TagRule{
			{Tag: IdentityChallengeRespTag, Cardinality: "exactly_one"},
		},
		Args: []ArgDescriptor{
			{
				Name:        "challenge",
				Type:        "string",
				Required:    true,
				Description: "Nonce string to be signed as proof of key control",
			},
		},
		Signing:    "member_key",
		SignerType: SignerMemberKey,
	}
}

// ListHomesDeclaration returns the "list-homes" operation declaration for the
// identity convention. A list-homes operation returns all campfire IDs declared
// as homes via declare-home operations in this campfire's message history.
//
// Signing: member_key
// Produces: identity:homes tag
func ListHomesDeclaration() *Declaration {
	return &Declaration{
		Convention:  IdentityConvention,
		Version:     identityVersion,
		Operation:   "list-homes",
		Description: "Return all declared home campfire IDs",
		ProducesTags: []TagRule{
			{Tag: IdentityHomesTag, Cardinality: "exactly_one"},
		},
		Signing:    "member_key",
		SignerType: SignerMemberKey,
	}
}

// DeclareHomeDeclaration returns the "declare-home" operation declaration for
// the identity convention. A declare-home operation declares a campfire as a
// home of this identity. It threads onto prior declarations, creating an audit
// trail.
//
// Signing: member_key
// Produces: identity:home-declared tag
func DeclareHomeDeclaration() *Declaration {
	return &Declaration{
		Convention:  IdentityConvention,
		Version:     identityVersion,
		Operation:   "declare-home",
		Description: "Declare a campfire as a home of this identity",
		ProducesTags: []TagRule{
			{Tag: IdentityHomeDeclaredTag, Cardinality: "exactly_one"},
		},
		Args: []ArgDescriptor{
			{
				Name:        "campfire_id",
				Type:        "string",
				Required:    true,
				Description: "Campfire ID to declare as a home",
			},
			{
				Name:        "role",
				Type:        "string",
				Required:    true,
				Values:      []string{"primary", "secondary", "archive"},
				Description: "Role of this home campfire",
			},
		},
		Signing:    "member_key",
		SignerType: SignerMemberKey,
	}
}

// IdentityDeclarations returns all four identity convention declarations in
// their canonical order: introduce-me, verify-me, list-homes, declare-home.
// These are seeded into identity campfires at creation time.
func IdentityDeclarations() []*Declaration {
	return []*Declaration{
		IntroduceMeDeclaration(),
		VerifyMeDeclaration(),
		ListHomesDeclaration(),
		DeclareHomeDeclaration(),
	}
}

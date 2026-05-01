// Package identity provides L3 applied-convention declarations for the identity
// convention. This is a Stage 2 placeholder; Stage 3 will separate declarations
// from implementations and add the full resolver implementations.
//
// Moved from pkg/convention/ in campfireagent-4d7.
package identity

import (
	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

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
	// IdentityProfileTag is the tag for self-declared display name messages.
	// Senders post {"display_name":"..."} payloads tagged identity:profile when
	// joining or creating a campfire. Display names are UNVERIFIED — treat as hints.
	IdentityProfileTag = "identity:profile"

	// IdentityRevokedTag is the tag for revocation messages posted by cf home revoke.
	// Relying parties poll for this tag to learn about revoked presentation claims.
	// TTL-bounded: default 1h before cache expiry closes the revocation gap.
	IdentityRevokedTag = "identity:revoked"
)

// IntroduceMeDeclaration returns the "introduce-me" operation declaration for
// the identity convention. An introduce-me operation is a self-assertion by the
// campfire's identity holder: it posts the agent's public key, display name
// (tainted), and a list of declared home campfire IDs.
//
// Signing: member_key (member 0, the identity holder)
// Produces: identity:introduction tag
func IntroduceMeDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  IdentityConvention,
		Version:     identityVersion,
		Operation:   "introduce-me",
		Description: "Declare this campfire's identity: pubkey, display name, and home campfires",
		ProducesTags: []convention.TagRule{
			{Tag: IdentityIntroductionTag, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
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
		SignerType: convention.SignerMemberKey,
	}
}

// VerifyMeDeclaration returns the "verify-me" operation declaration for the
// identity convention. A verify-me operation is a challenge-response that proves
// the operator controls the member key. The caller posts a nonce; the handler
// responds with a signature over it.
//
// Signing: member_key
// Produces: identity:challenge-response tag
func VerifyMeDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  IdentityConvention,
		Version:     identityVersion,
		Operation:   "verify-me",
		Description: "Prove key control via challenge-response",
		ProducesTags: []convention.TagRule{
			{Tag: IdentityChallengeRespTag, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
			{
				Name:        "challenge",
				Type:        "string",
				Required:    true,
				Description: "Nonce string to be signed as proof of key control",
			},
		},
		Signing:    "member_key",
		SignerType: convention.SignerMemberKey,
	}
}

// ListHomesDeclaration returns the "list-homes" operation declaration for the
// identity convention. A list-homes operation returns all campfire IDs declared
// as homes via declare-home operations in this campfire's message history.
//
// Signing: member_key
// Produces: identity:homes tag
func ListHomesDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  IdentityConvention,
		Version:     identityVersion,
		Operation:   "list-homes",
		Description: "Return all declared home campfire IDs",
		ProducesTags: []convention.TagRule{
			{Tag: IdentityHomesTag, Cardinality: "exactly_one"},
		},
		Signing:    "member_key",
		SignerType: convention.SignerMemberKey,
	}
}

// DeclareHomeDeclaration returns the "declare-home" operation declaration for
// the identity convention. A declare-home operation declares a campfire as a
// home of this identity. It threads onto prior declarations, creating an audit
// trail.
//
// Signing: member_key
// Produces: identity:home-declared tag
func DeclareHomeDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  IdentityConvention,
		Version:     identityVersion,
		Operation:   "declare-home",
		Description: "Declare a campfire as a home of this identity",
		ProducesTags: []convention.TagRule{
			{Tag: IdentityHomeDeclaredTag, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
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
		SignerType: convention.SignerMemberKey,
	}
}

// IdentityDeclarations returns all four identity convention declarations in
// their canonical order: introduce-me, verify-me, list-homes, declare-home.
// These are seeded into identity campfires at creation time.
func IdentityDeclarations() []*convention.Declaration {
	return []*convention.Declaration{
		IntroduceMeDeclaration(),
		VerifyMeDeclaration(),
		ListHomesDeclaration(),
		DeclareHomeDeclaration(),
	}
}

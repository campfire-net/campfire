// Package cfidentity implements the Campfire Identity Convention v0.1 (L3).
//
// This package is the canonical home for identity convention declarations and
// profile management. It absorbs cf-protocol/protocol/profile.go (TRANSITIONAL)
// which was a Stage 3 placeholder pending this package's existence.
//
// # Convention operations
//
//   - introduce-me: self-assertion of pubkey, display name, and home campfires
//   - declare-home: declare a campfire as a home of this identity
//   - verify-me: challenge-response proof of key control
//   - list-homes: return all declared home campfire IDs
//   - echo: cross-campfire echo ceremony (bidirectional home-link proof)
//
// # Tags produced
//
//   - identity:introduction  (introduce-me)
//   - identity:home-declared (declare-home)
//   - identity:challenge-response (verify-me)
//   - identity:homes (list-homes)
//   - identity:home-echo (echo)
//   - identity:revoked (revocation messages)
//   - identity:v1 (beacon tag)
//   - identity:profile (display-name self-declaration)
//
// # Profile management
//
// ProfileFile is the schema for ~/.cf/profile.json (moved from cf-protocol/protocol/profile.go).
// LoadProfile and SaveProfile manage the file.
//
// Layer: L3 — cf-conventions/cf-identity/.
// Depends on: cf-conventions/cf-convention (L2, for Declaration types).
// Does NOT import cf-protocol/ internal packages directly.
// Tracked bead: campfireagent-902.
package cfidentity

import (
	"encoding/json"
	"os"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// Convention name and version.
const (
	IdentityConvention = "identity"
	identityVersion    = "0.1"
)

// Identity tag constants produced by convention operations.
const (
	// IdentityIntroductionTag is produced by introduce-me.
	IdentityIntroductionTag = "identity:introduction"
	// IdentityHomeDeclaredTag is produced by declare-home.
	IdentityHomeDeclaredTag = "identity:home-declared"
	// IdentityChallengeRespTag is produced by verify-me.
	IdentityChallengeRespTag = "identity:challenge-response"
	// IdentityHomesTag is produced by list-homes.
	IdentityHomesTag = "identity:homes"
	// IdentityHomeEchoTag is produced by echo.
	IdentityHomeEchoTag = "identity:home-echo"
	// IdentityRevokedTag is the tag for revocation messages posted by cf home revoke.
	// Relying parties poll for this tag to learn about revoked presentation claims.
	// TTL-bounded: default 1h before cache expiry closes the revocation gap.
	IdentityRevokedTag = "identity:revoked"
	// IdentityBeaconTag is the tag used for identity:v1 beacon publications.
	IdentityBeaconTag = "identity:v1"
	// IdentityProfileTag is the tag for self-declared display name messages.
	// Senders post {"display_name":"..."} payloads tagged identity:profile when
	// joining or creating a campfire. Display names are UNVERIFIED — treat as hints.
	IdentityProfileTag = "identity:profile"
)

// ─────────────────────────────────────────────────────────────────────────────
// Convention declarations
// ─────────────────────────────────────────────────────────────────────────────

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

// EchoDeclaration returns the "echo" operation declaration for the identity
// convention. An echo is a cross-campfire ceremony message that proves the
// operator of campfire B authorized the home link declared in declare-home(A).
//
// The echo message is signed by campfire B's private key over M_B's ID bytes,
// enabling third-party verification of the bidirectional home link.
//
// Signing: member_key (campfire B's key)
// Produces: identity:home-echo tag
func EchoDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  IdentityConvention,
		Version:     identityVersion,
		Operation:   "echo",
		Description: "Cross-campfire echo: prove campfire B authorized the home link",
		ProducesTags: []convention.TagRule{
			{Tag: IdentityHomeEchoTag, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
			{
				Name:        "echo_of",
				Type:        "string",
				Required:    true,
				Description: "Message ID of the declare-home on campfire B (M_B)",
			},
			{
				Name:        "signed_by_b",
				Type:        "string",
				Required:    true,
				Description: "Ed25519 signature by campfire B's private key over M_B's ID bytes (hex)",
			},
			{
				Name:        "campfire_b_pubkey",
				Type:        "string",
				Required:    true,
				Description: "Campfire B's public key in hex encoding (for verifier convenience)",
			},
		},
		Signing:    "member_key",
		SignerType: convention.SignerMemberKey,
	}
}

// IdentityDeclarations returns all five identity convention declarations in
// their canonical order: introduce-me, declare-home, verify-me, list-homes, echo.
// These are seeded into identity campfires at creation time.
func IdentityDeclarations() []*convention.Declaration {
	return []*convention.Declaration{
		IntroduceMeDeclaration(),
		DeclareHomeDeclaration(),
		VerifyMeDeclaration(),
		ListHomesDeclaration(),
		EchoDeclaration(),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Profile management (absorbed from cf-protocol/protocol/profile.go)
// ─────────────────────────────────────────────────────────────────────────────

// ProfileFile is the schema for ~/.cf/profile.json.
//
// Moved from cf-protocol/protocol/profile.go (TRANSITIONAL placeholder removed
// after this package ships — see campfireagent-902 / profile.go header).
type ProfileFile struct {
	DisplayName string `json:"display_name"`
}

// LoadProfile reads ~/.cf/profile.json from cfHome.
// Returns a zero-value ProfileFile if the file does not exist or cannot be parsed.
func LoadProfile(cfHome string) ProfileFile {
	path := profilePath(cfHome)
	data, err := os.ReadFile(path)
	if err != nil {
		return ProfileFile{}
	}
	var p ProfileFile
	if err := json.Unmarshal(data, &p); err != nil {
		return ProfileFile{}
	}
	return p
}

// SaveProfile writes a ProfileFile to ~/.cf/profile.json in cfHome.
func SaveProfile(cfHome string, p ProfileFile) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(profilePath(cfHome), data, 0600)
}

func profilePath(cfHome string) string {
	return cfHome + "/profile.json"
}

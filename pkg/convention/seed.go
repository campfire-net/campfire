package convention

import (
	"github.com/campfire-net/campfire/pkg/naming"
)

// InfrastructureConvention is the convention name for convention-extension operations.
const InfrastructureConvention = "convention-extension"

// infrastructureVersion is the version for built-in convention-extension declarations.
const infrastructureVersion = "0.1"

// PromoteDeclaration returns the built-in "promote" operation declaration for
// convention-extension. A promote operation publishes a validated convention
// declaration to a live convention registry campfire.
//
// This is the ONE declaration embedded in the binary — the bootstrap primitive.
// It is signed by the campfire key (authority-bearing) so that only the campfire
// owner can publish declarations to their registry. All other infrastructure
// declarations (supersede, revoke, naming-register, beacon-register, etc.) come
// from seed beacons, local files, or the network — not the binary.
func PromoteDeclaration() *Declaration {
	return &Declaration{
		Convention:  InfrastructureConvention,
		Version:     infrastructureVersion,
		Operation:   "promote",
		Description: "Publish a validated convention declaration to a convention registry campfire",
		ProducesTags: []TagRule{
			{Tag: ConventionOperationTag, Cardinality: "exactly_one"},
		},
		Args: []ArgDescriptor{
			{
				Name:        "file",
				Type:        "string",
				Required:    true,
				Description: "Path to convention declaration JSON file to publish",
			},
			{
				Name:        "registry",
				Type:        "campfire",
				Required:    true,
				Description: "Convention registry campfire ID to publish to",
			},
		},
		Signing:    "campfire_key",
		SignerType: SignerCampfireKey,
	}
}

// SupersedeDeclaration returns the built-in "supersede" operation declaration for
// convention-extension. A supersede operation replaces an existing declaration with
// a newer version. It is signed by the campfire key (authority-bearing).
func SupersedeDeclaration() *Declaration {
	return &Declaration{
		Convention:  InfrastructureConvention,
		Version:     infrastructureVersion,
		Operation:   "supersede",
		Description: "Replace a convention declaration with a newer version",
		ProducesTags: []TagRule{
			{Tag: ConventionOperationTag, Cardinality: "exactly_one"},
		},
		Args: []ArgDescriptor{
			{
				Name:        "file",
				Type:        "string",
				Required:    true,
				Description: "Path to new declaration JSON",
			},
			{
				Name:        "supersedes",
				Type:        "message_id",
				Required:    true,
				Description: "Message ID of the declaration being replaced",
			},
		},
		Signing:    "campfire_key",
		SignerType: SignerCampfireKey,
	}
}

// RevokeDeclaration returns the built-in "revoke" operation declaration for
// convention-extension. A revoke operation permanently removes a declaration.
// It is signed by the campfire key (authority-bearing).
func RevokeDeclaration() *Declaration {
	return &Declaration{
		Convention:  InfrastructureConvention,
		Version:     infrastructureVersion,
		Operation:   "revoke",
		Description: "Permanently revoke a convention declaration",
		ProducesTags: []TagRule{
			{Tag: conventionRevokeTag, Cardinality: "exactly_one"},
		},
		Args: []ArgDescriptor{
			{
				Name:        "target_id",
				Type:        "message_id",
				Required:    true,
				Description: "Message ID of the declaration to revoke",
			},
		},
		Signing:    "campfire_key",
		SignerType: SignerCampfireKey,
	}
}

// NamingRegisterDeclaration returns the built-in "naming-register" operation
// declaration. This seeds into every new campfire so that name registrations
// are possible from birth.
//
// Signing: campfire_key (authority-bearing — only the campfire owner can register names)
// Produces: naming:name:* tag (via pattern)
// Rate limited: 5/day (MaxRegistrationsPerDay from pkg/naming)
func NamingRegisterDeclaration() *Declaration {
	return &Declaration{
		Convention:  InfrastructureConvention,
		Version:     infrastructureVersion,
		Operation:   "naming-register",
		Description: "Register a named endpoint in this campfire's namespace",
		ProducesTags: []TagRule{
			{Tag: naming.TagNamePrefix, Cardinality: "zero_or_more", Pattern: naming.TagNamePrefix + "*"},
		},
		RateLimit: &RateLimit{
			Max:    5,
			Per:    "campfire",
			Window: "day",
		},
		Args: []ArgDescriptor{
			{
				Name:        "name",
				Type:        "string",
				Required:    true,
				Description: "The name segment to register",
			},
			{
				Name:        "campfire_id",
				Type:        "string",
				Required:    true,
				Description: "The campfire ID this name resolves to",
			},
			{
				Name:        "ttl",
				Type:        "integer",
				Required:    false,
				Description: "Time-to-live in seconds (default 3600, max 86400)",
			},
		},
		Signing:    "campfire_key",
		SignerType: SignerCampfireKey,
	}
}

// infrastructureSeedDeclarations returns all built-in convention-extension
// declarations. These are pre-seeded into convention campfires so that agents
// can use supersede and revoke operations without bootstrapping.
func infrastructureSeedDeclarations() []*Declaration {
	return []*Declaration{
		SupersedeDeclaration(),
		RevokeDeclaration(),
		NamingRegisterDeclaration(),
	}
}

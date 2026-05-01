// Package seed provides the built-in convention-extension declarations seeded
// into every new campfire. This is an L3 (cf-convention-extensions) package.
//
// Moved from cf-conventions/cf-convention/ (L2) in campfireagent-c72 to close
// OPEN-018 carveout: seed.go previously required unexported conventionRevokeTag
// from parser.go and therefore had to live in the same package. That constant
// is now exported as ConventionRevokeTag, enabling this clean L3 placement.
//
// L3 packages may import L2 (cf-convention); the converse is forbidden.
package seed

import (
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/pkg/naming"
)

// PromoteDeclaration returns the built-in "promote" operation declaration for
// convention-extension. A promote operation publishes a validated convention
// declaration to a live convention registry campfire.
//
// This is the ONE declaration embedded in the binary — the bootstrap primitive.
// It is signed by the campfire key (authority-bearing) so that only the campfire
// owner can publish declarations to their registry. All other infrastructure
// declarations (supersede, revoke, naming-register, beacon-register, etc.) come
// from seed beacons, local files, or the network — not the binary.
func PromoteDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  convention.InfrastructureConvention,
		Version:     infrastructureVersion,
		Operation:   "promote",
		Description: "Publish a validated convention declaration to a convention registry campfire",
		ProducesTags: []convention.TagRule{
			{Tag: convention.ConventionOperationTag, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
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
		SignerType: convention.SignerCampfireKey,
	}
}

// SupersedeDeclaration returns the built-in "supersede" operation declaration for
// convention-extension. A supersede operation replaces an existing declaration with
// a newer version. It is signed by the campfire key (authority-bearing).
func SupersedeDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  convention.InfrastructureConvention,
		Version:     infrastructureVersion,
		Operation:   "supersede",
		Description: "Replace a convention declaration with a newer version",
		ProducesTags: []convention.TagRule{
			{Tag: convention.ConventionOperationTag, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
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
		SignerType: convention.SignerCampfireKey,
	}
}

// RevokeDeclaration returns the built-in "revoke" operation declaration for
// convention-extension. A revoke operation permanently removes a declaration.
// It is signed by the campfire key (authority-bearing).
func RevokeDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  convention.InfrastructureConvention,
		Version:     infrastructureVersion,
		Operation:   "revoke",
		Description: "Permanently revoke a convention declaration",
		ProducesTags: []convention.TagRule{
			{Tag: convention.ConventionRevokeTag, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
			{
				Name:        "target_id",
				Type:        "message_id",
				Required:    true,
				Description: "Message ID of the declaration to revoke",
			},
		},
		Signing:    "campfire_key",
		SignerType: convention.SignerCampfireKey,
	}
}

// NamingRegisterDeclaration returns the built-in "naming-register" operation
// declaration. This seeds into every new campfire so that name registrations
// are possible from birth.
//
// Signing: campfire_key (authority-bearing — only the campfire owner can register names)
// Produces: naming:name:* tag (via pattern)
// Rate limited: 5/day (MaxRegistrationsPerDay from pkg/naming)
func NamingRegisterDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  convention.InfrastructureConvention,
		Version:     infrastructureVersion,
		Operation:   "naming-register",
		Description: "Register a named endpoint in this campfire's namespace",
		ProducesTags: []convention.TagRule{
			{Tag: naming.TagNamePrefix, Cardinality: "zero_or_more", Pattern: naming.TagNamePrefix + "*"},
		},
		RateLimit: &convention.RateLimit{
			Max:    5,
			Per:    "campfire",
			Window: "day",
		},
		Args: []convention.ArgDescriptor{
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
		SignerType: convention.SignerCampfireKey,
	}
}

// InfrastructureSeedDeclarations returns all built-in convention-extension
// declarations. These are pre-seeded into convention campfires so that agents
// can use supersede and revoke operations without bootstrapping.
func InfrastructureSeedDeclarations() []*convention.Declaration {
	return []*convention.Declaration{
		SupersedeDeclaration(),
		RevokeDeclaration(),
		NamingRegisterDeclaration(),
	}
}

// infrastructureVersion is the version for built-in convention-extension declarations.
// Defined locally in L3 to avoid the need for L2 to export it.
const infrastructureVersion = "0.1"

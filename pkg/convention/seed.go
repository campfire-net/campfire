package convention

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

// infrastructureSeedDeclarations returns all built-in convention-extension
// declarations. These are pre-seeded into convention campfires so that agents
// can use supersede and revoke operations without bootstrapping.
func infrastructureSeedDeclarations() []*Declaration {
	return []*Declaration{
		SupersedeDeclaration(),
		RevokeDeclaration(),
	}
}

package convention

// DeclarationBuilder provides a fluent API for constructing convention
// Declarations with sane defaults. Use SimpleConvention to create one.
//
// Example:
//
//	decl := convention.SimpleConvention("status", "1.0", "report", "Report agent status").
//	    RequiredArg("agent_id", "string", "Agent identifier").
//	    Arg("details", "string", "Optional details").
//	    Build()
type DeclarationBuilder struct {
	decl Declaration
}

// SimpleConvention creates a DeclarationBuilder with sane defaults for the
// common case: member_key signing, sync response, no rate limiting.
func SimpleConvention(convention, version, operation, description string) *DeclarationBuilder {
	return &DeclarationBuilder{
		decl: Declaration{
			Convention:  convention,
			Version:     version,
			Operation:   operation,
			Description: description,
			Signing:     string(SignerMemberKey),
			Response:    "sync",
		},
	}
}

// Arg adds an optional argument to the convention.
func (b *DeclarationBuilder) Arg(name, typ, description string) *DeclarationBuilder {
	b.decl.Args = append(b.decl.Args, ArgDescriptor{
		Name:        name,
		Type:        typ,
		Description: description,
		Required:    false,
	})
	return b
}

// RequiredArg adds a required argument to the convention.
func (b *DeclarationBuilder) RequiredArg(name, typ, description string) *DeclarationBuilder {
	b.decl.Args = append(b.decl.Args, ArgDescriptor{
		Name:        name,
		Type:        typ,
		Description: description,
		Required:    true,
	})
	return b
}

// ProducesTag adds an entry to the produces_tags list for this convention.
// tag is the tag name (e.g. "status", "convention:operation") and cardinality
// is one of the standard cardinality values (e.g. "exactly_one", "zero_or_more").
func (b *DeclarationBuilder) ProducesTag(tag, cardinality string) *DeclarationBuilder {
	b.decl.ProducesTags = append(b.decl.ProducesTags, TagRule{Tag: tag, Cardinality: cardinality})
	return b
}

// Build returns the final Declaration.
func (b *DeclarationBuilder) Build() Declaration {
	return b.decl
}

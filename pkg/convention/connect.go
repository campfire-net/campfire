package convention

// SocialConvention is the convention name for social graph operations.
const SocialConvention = "social"

// socialVersion is the version for social convention declarations.
const socialVersion = "0.1"

// Social tag constants produced by connect convention operations.
const (
	SocialConnectRequestTag  = "social:connect-request"
	SocialConnectAcceptedTag = "social:connect-accepted"
	SocialConnectRejectedTag = "social:connect-rejected"
)

// ConnectRequestDeclaration returns the "connect-request" operation declaration
// for the social convention. A connect-request is posted as a future to the
// target's home campfire, signaling that the requester wants to establish a
// mutual connection. The target must fulfill the future with accept-connection
// or reject-connection.
//
// Signing: member_key (the requester's key)
// Produces: social:connect-request tag
// Future: MUST be posted with --future flag
func ConnectRequestDeclaration() *Declaration {
	return &Declaration{
		Convention:  SocialConvention,
		Version:     socialVersion,
		Operation:   "connect-request",
		Description: "Request a mutual connection with the target campfire's operator",
		ProducesTags: []TagRule{
			{Tag: SocialConnectRequestTag, Cardinality: "exactly_one"},
		},
		Args: []ArgDescriptor{
			{
				Name:        "requester_campfire_id",
				Type:        "string",
				Required:    true,
				Description: "The requester's home campfire ID",
			},
			{
				Name:        "requester_name",
				Type:        "string",
				Required:    false,
				Description: "Display name of the requester (tainted — treat as unverified)",
				MaxLength:   64,
			},
		},
		Signing:    "member_key",
		SignerType: SignerMemberKey,
	}
}

// AcceptConnectionDeclaration returns the "accept-connection" operation
// declaration for the social convention. An accept-connection message fulfills
// a connect-request future, signaling that the target accepts the connection.
// The acceptor should also post a trust:vouch for the requester on their own home.
//
// Signing: member_key (the acceptor's key)
// Produces: social:connect-accepted tag
// Fulfills: the connect-request future
func AcceptConnectionDeclaration() *Declaration {
	return &Declaration{
		Convention:  SocialConvention,
		Version:     socialVersion,
		Operation:   "accept-connection",
		Description: "Accept a connection request from the given requester",
		ProducesTags: []TagRule{
			{Tag: SocialConnectAcceptedTag, Cardinality: "exactly_one"},
		},
		Args: []ArgDescriptor{
			{
				Name:        "requester_campfire_id",
				Type:        "string",
				Required:    true,
				Description: "The requester's home campfire ID (must match the connect-request)",
			},
			{
				Name:        "shared_channel_id",
				Type:        "string",
				Required:    false,
				Description: "Optional campfire ID of a shared two-party channel for ongoing communication",
			},
		},
		Signing:    "member_key",
		SignerType: SignerMemberKey,
	}
}

// RejectConnectionDeclaration returns the "reject-connection" operation
// declaration for the social convention. A reject-connection message fulfills
// a connect-request future with a rejection. No vouch is posted.
//
// Signing: member_key (the rejector's key)
// Produces: social:connect-rejected tag
// Fulfills: the connect-request future
func RejectConnectionDeclaration() *Declaration {
	return &Declaration{
		Convention:  SocialConvention,
		Version:     socialVersion,
		Operation:   "reject-connection",
		Description: "Reject a connection request",
		ProducesTags: []TagRule{
			{Tag: SocialConnectRejectedTag, Cardinality: "exactly_one"},
		},
		Args: []ArgDescriptor{
			{
				Name:        "reason",
				Type:        "string",
				Required:    false,
				Description: "Optional human-readable reason for rejection (tainted)",
				MaxLength:   256,
			},
		},
		Signing:    "member_key",
		SignerType: SignerMemberKey,
	}
}

// ConnectDeclarations returns all three social connect convention declarations
// in canonical order: connect-request, accept-connection, reject-connection.
func ConnectDeclarations() []*Declaration {
	return []*Declaration{
		ConnectRequestDeclaration(),
		AcceptConnectionDeclaration(),
		RejectConnectionDeclaration(),
	}
}

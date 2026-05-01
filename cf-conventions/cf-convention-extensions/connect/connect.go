// Package connect provides L3 applied-convention declarations for the social
// connect convention. This is a Stage 2 placeholder; Stage 3 will add the
// full handler implementations.
//
// Moved from pkg/convention/ in campfireagent-4d7.
package connect

import (
	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

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
func ConnectRequestDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  SocialConvention,
		Version:     socialVersion,
		Operation:   "connect-request",
		Description: "Request a mutual connection with the target campfire's operator",
		ProducesTags: []convention.TagRule{
			{Tag: SocialConnectRequestTag, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
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
		SignerType: convention.SignerMemberKey,
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
func AcceptConnectionDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  SocialConvention,
		Version:     socialVersion,
		Operation:   "accept-connection",
		Description: "Accept a connection request from the given requester",
		ProducesTags: []convention.TagRule{
			{Tag: SocialConnectAcceptedTag, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
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
		SignerType: convention.SignerMemberKey,
	}
}

// RejectConnectionDeclaration returns the "reject-connection" operation
// declaration for the social convention. A reject-connection message fulfills
// a connect-request future with a rejection. No vouch is posted.
//
// Signing: member_key (the rejector's key)
// Produces: social:connect-rejected tag
// Fulfills: the connect-request future
func RejectConnectionDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  SocialConvention,
		Version:     socialVersion,
		Operation:   "reject-connection",
		Description: "Reject a connection request",
		ProducesTags: []convention.TagRule{
			{Tag: SocialConnectRejectedTag, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
			{
				Name:        "reason",
				Type:        "string",
				Required:    false,
				Description: "Optional human-readable reason for rejection (tainted)",
				MaxLength:   256,
			},
		},
		Signing:    "member_key",
		SignerType: convention.SignerMemberKey,
	}
}

// ConnectDeclarations returns all three social connect convention declarations
// in canonical order: connect-request, accept-connection, reject-connection.
func ConnectDeclarations() []*convention.Declaration {
	return []*convention.Declaration{
		ConnectRequestDeclaration(),
		AcceptConnectionDeclaration(),
		RejectConnectionDeclaration(),
	}
}

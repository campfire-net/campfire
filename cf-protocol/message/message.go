// Package message — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the message substrate from cf-protocol/internal/message.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package message

import "github.com/campfire-net/campfire/cf-protocol/internal/message"

// Re-export all types.
type (
	Message          = message.Message
	ProvenanceHop    = message.ProvenanceHop
	MessageSignInput = message.MessageSignInput
	HopSignInput     = message.HopSignInput
	Signer           = message.Signer
	Ed25519Signer    = message.Ed25519Signer
)

// Re-export all functions.
var (
	NewMessage              = message.NewMessage
	MustNewEd25519Signer    = message.MustNewEd25519Signer
	NewEd25519Signer        = message.NewEd25519Signer
	VerifyHop               = message.VerifyHop
	VerifyMessageSignature  = message.VerifyMessageSignature
)

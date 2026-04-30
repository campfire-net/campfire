// Package crypto — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the crypto substrate from cf-protocol/internal/crypto.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package crypto

import "github.com/campfire-net/campfire/cf-protocol/internal/crypto"

// Re-export types.
type (
	EncryptedPayload = crypto.EncryptedPayload
	PayloadAAD       = crypto.PayloadAAD
)

// Re-export functions.
var (
	AESGCMEncrypt          = crypto.AESGCMEncrypt
	AESGCMDecrypt          = crypto.AESGCMDecrypt
	AESGCMEncryptWithNonce = crypto.AESGCMEncryptWithNonce
	AESGCMDecryptWithNonce = crypto.AESGCMDecryptWithNonce
	HKDFSha256ZeroSalt     = crypto.HKDFSha256ZeroSalt
	HKDFSha256WithSalt     = crypto.HKDFSha256WithSalt
	DeriveEpochCEK         = crypto.DeriveEpochCEK
	NextRootSecret         = crypto.NextRootSecret
	GenerateRootSecret     = crypto.GenerateRootSecret
	MarshalEncryptedPayload   = crypto.MarshalEncryptedPayload
	UnmarshalEncryptedPayload = crypto.UnmarshalEncryptedPayload
	BuildPayloadAAD        = crypto.BuildPayloadAAD
	EncryptPayload         = crypto.EncryptPayload
	DecryptPayload         = crypto.DecryptPayload
	WrapKey                = crypto.WrapKey
	UnwrapKey              = crypto.UnwrapKey
	WrapKeyLegacyHKDF      = crypto.WrapKeyLegacyHKDF
)

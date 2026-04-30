// Package threshold — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the threshold substrate from cf-protocol/internal/threshold.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package threshold

import "github.com/campfire-net/campfire/cf-protocol/internal/threshold"

// Re-export types.
type (
	DKGResult      = threshold.DKGResult
	DKGParticipant = threshold.DKGParticipant
	SigningSession = threshold.SigningSession
)

// RunDKGTimeout is the timeout for a DKG round.
// Re-exported from cf-protocol/internal/threshold.
var RunDKGTimeout = threshold.RunDKGTimeout

// Re-export functions.
var (
	NewDKGParticipant = threshold.NewDKGParticipant
	RunDKG            = threshold.RunDKG
	NewSigningSession = threshold.NewSigningSession
	Sign              = threshold.Sign
	MarshalResult     = threshold.MarshalResult
	UnmarshalResult   = threshold.UnmarshalResult
)

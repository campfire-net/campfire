// Package admission — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the admission substrate from cf-protocol/internal/admission.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package admission

import "github.com/campfire-net/campfire/cf-protocol/internal/admission"

// Re-export types.
type (
	FSTransport      = admission.FSTransport
	Store            = admission.Store
	HTTPTransport    = admission.HTTPTransport
	AdmitterDeps     = admission.AdmitterDeps
	AdmissionRequest = admission.AdmissionRequest
	AdmissionResult  = admission.AdmissionResult
)

// Re-export functions.
var AdmitMember = admission.AdmitMember

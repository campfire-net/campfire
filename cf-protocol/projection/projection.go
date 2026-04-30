// Package projection — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the projection substrate from cf-protocol/internal/projection.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package projection

import "github.com/campfire-net/campfire/cf-protocol/internal/projection"

// Re-export types.
type (
	Class                = projection.Class
	ProjectionMiddleware = projection.ProjectionMiddleware
)

// Class constants.
const (
	ClassIncremental = projection.ClassIncremental
	ClassAlwaysScan  = projection.ClassAlwaysScan
)

// Re-export functions.
var (
	Classify = projection.Classify
	New      = projection.New
)

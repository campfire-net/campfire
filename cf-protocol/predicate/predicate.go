// Package predicate — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the predicate substrate from cf-protocol/internal/predicate.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package predicate

import "github.com/campfire-net/campfire/cf-protocol/internal/predicate"

// Re-export constants.
const (
	MaxDepth    = predicate.MaxDepth
	MaxChildren = predicate.MaxChildren
)

// Re-export error vars.
var ErrDepthExceeded = predicate.ErrDepthExceeded

// Re-export types.
type (
	NodeType       = predicate.NodeType
	Node           = predicate.Node
	MessageContext = predicate.MessageContext
	EvalResult     = predicate.EvalResult
)

// Node type constants.
const (
	NodeAnd         = predicate.NodeAnd
	NodeOr          = predicate.NodeOr
	NodeNot         = predicate.NodeNot
	NodeTag         = predicate.NodeTag
	NodeSender      = predicate.NodeSender
	NodeField       = predicate.NodeField
	NodeTimestamp   = predicate.NodeTimestamp
	NodePayloadSize = predicate.NodePayloadSize
)

// Re-export functions.
var (
	Parse       = predicate.Parse
	Eval        = predicate.Eval
	EvalSafe    = predicate.EvalSafe
	EvalNumeric = predicate.EvalNumeric
)

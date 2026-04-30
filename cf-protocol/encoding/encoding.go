// Package encoding — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the encoding substrate from cf-protocol/internal/encoding.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package encoding

import "github.com/campfire-net/campfire/cf-protocol/internal/encoding"

// Re-export functions.
var (
	Marshal   = encoding.Marshal
	Unmarshal = encoding.Unmarshal
)

// cf-primitives — low-level campfire protocol primitives (sanctioned escape hatch).
//
// This binary exposes the cf-protocol public API surface and nothing more.
// It is the SURFACE CEILING for low-level protocol access:
//
//   - Any addition to the exposed command set requires Pass-1-style adversary review.
//   - Convention-layer features belong in cf, not here.
//   - Higher-level workflows belong in cf, not here.
//
// Exposed primitives (§4.4, §5.3 design v2):
//
//	init       — Init: create or load identity + store
//	create     — Create: create a new campfire
//	join       — Join: join an existing campfire
//	leave      — Leave: leave a campfire
//	disband    — Disband: tear down a campfire (creator only)
//	admit      — Admit: pre-admit a member to an invite-only campfire
//	evict      — Evict: remove a member from a campfire
//	members    — Members: list campfire members
//	send       — Send: send a signed message
//	read       — Read: read messages from a campfire
//	subscribe  — Subscribe: stream messages from a campfire
//	await      — Await: block until a future message is fulfilled
package main

import (
	"os"

	"github.com/campfire-net/campfire/cmd/cf-primitives/internal/primcmd"
)

// dispatch is the core entry-point logic. It calls primcmd.Execute and
// translates errors to exit codes (0 = success, 1 = error). Extracted from
// main so tests can exercise the dispatch path without forking a subprocess.
func dispatch() int {
	if err := primcmd.Execute(); err != nil {
		return 1
	}
	return 0
}

func main() {
	os.Exit(dispatch())
}

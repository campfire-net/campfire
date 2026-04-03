// convention_sweep.go — On-demand fallback sweep endpoint for missed convention dispatches.
//
// Exposes POST /sweep for the Azure Functions timer trigger. The timer fires on a
// configurable schedule (default: every 5 minutes) and calls this endpoint to catch
// messages that the event-driven dispatch path missed (crash, timeout, network glitch).
//
// The endpoint is idempotent: re-dispatching an already-dispatched message is a no-op
// (deduplication via DispatchStore). Re-dispatching a fulfilled or failed message is
// also a no-op (the Sweeper only considers "dispatched" records older than the stale
// threshold).
//
// Architecture: cf-functions proxies /api/sweep -> cf-mcp /sweep. The timer trigger
// function.json fires a POST to /api/sweep on schedule.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// sweepResponse is the JSON response from POST /sweep.
type sweepResponse struct {
	Redispatched int    `json:"redispatched"`
	Status       string `json:"status"` // "ok" or "disabled"
	Error        string `json:"error,omitempty"`
}

// handleSweep runs the fallback dispatch sweep on demand. Called by the Azure
// Functions timer trigger via cf-functions proxy.
//
//	POST /sweep → run sweep, return { redispatched: N, status: "ok" }
//	GET  /sweep → return 405
//
// If the fallback sweep is not wired (convention dispatching disabled), returns
// { redispatched: 0, status: "disabled" } with 200. This is not an error — the
// timer trigger should succeed silently when there's nothing to sweep.
func (s *server) handleSweep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if s.fallbackSweep == nil {
		resp := sweepResponse{Status: "disabled"}
		json.NewEncoder(w).Encode(resp)
		return
	}

	redispatched, err := s.fallbackSweep.Run(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		resp := sweepResponse{
			Status: "error",
			Error:  err.Error(),
		}
		json.NewEncoder(w).Encode(resp)
		fmt.Fprintf(os.Stderr, "cf-mcp: fallback sweep error: %v\n", err)
		return
	}

	if redispatched > 0 {
		fmt.Fprintf(os.Stderr, "cf-mcp: fallback sweep: re-dispatched %d message(s)\n", redispatched)
	}

	resp := sweepResponse{
		Redispatched: redispatched,
		Status:       "ok",
	}
	json.NewEncoder(w).Encode(resp)
}

package cmd

// approve.go — cf approve <grant-request-msg-id>
//
// Design v2 §2.6: "The owner's MCP/CLI surfaces it; owner runs
// `cf approve <future-id>` or `cf approve <future-id> --persist <ttl>`, which
// posts a delegation:grant and fulfills the future."
//
// "cf approve includes scope auto-suggestion: the prompt is pre-filled with
// the scope inferred from the failed predicate. Default --persist 7d."
//
// # Flow
//
//  1. Read the delegation:request message from the owner's identity campfire.
//  2. Parse the request payload: failed_predicate, requested_scope, future_id,
//     agent_pubkey.
//  3. Compute scope auto-suggestion (approve_suggest.go SuggestScope).
//  4. Show the suggested scope as a diff vs. the requested scope.
//  5. Prompt the operator: accept the suggestion, edit it, or enter override.
//     When --accept is given, accept the suggestion without prompting (non-interactive).
//  6. Post identity:granted message to the identity campfire with the approved scope.
//  7. Fulfill the future (post a message fulfilling the future_id).
//
// # Wire format for delegation:request payload
//
//	{
//	  "agent_pubkey":      "hex-ed25519-pubkey",   // requesting agent
//	  "future_id":         "campfire-message-id",  // future to fulfill on approval
//	  "failed_predicate":  { <predicate AST map> }, // the gate that denied
//	  "requested_scope":   "conv:op, conv:op",      // what the agent claims to need
//	  "campfire_id":        "hex-campfire-id"       // campfire the denial occurred on
//	}
//
// # Tags
//
// Incoming: "delegation:request" — the grant request tagged message.
// Outgoing: "delegation:grant"   — the approval message (fulfills the future).
//
// Note: identity:granted (delegation/grant.go PostGrant) is the older per-campfire
// delegation mechanism. delegation:grant here is the operator-approval response to
// a delegation:request future — it fulfills the future and acts as the capability
// grant in the identity campfire.
//
// OPEN-021 implementation.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust/uxmeas"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

// DelegationRequestTag is the tag on delegation:request messages.
const DelegationRequestTag = "delegation:request"

// DelegationGrantTag is the tag on delegation:grant approval messages.
const DelegationGrantTag = "delegation:grant"

// delegationRequestPayload is the JSON payload of a delegation:request message.
// This is the wire format synthesized by the convention executor when an agent
// hits a gate it cannot satisfy (design v2 §2.6).
type delegationRequestPayload struct {
	AgentPubkey     string         `json:"agent_pubkey"`
	FutureID        string         `json:"future_id"`
	FailedPredicate map[string]any `json:"failed_predicate"`
	RequestedScope  string         `json:"requested_scope"` // "conv:op, conv:op" format
	CampfireID      string         `json:"campfire_id"`
}

// delegationGrantPayload is the JSON payload of a delegation:grant approval message.
type delegationGrantPayload struct {
	AgentPubkey   string `json:"agent_pubkey"`
	ApprovedScope string `json:"approved_scope"` // "conv:op, conv:op" format
	PersistTTL    string `json:"persist_ttl"`    // e.g. "168h" (7d)
	CampfireID    string `json:"campfire_id"`
	FutureID      string `json:"future_id"`
	ApprovedAt    int64  `json:"approved_at"`
}

var (
	approvePersist    string
	approveAccept     bool
	approveTelemetry  string
)

var approveCmd = &cobra.Command{
	Use:   "approve <grant-request-msg-id>",
	Short: "Approve a delegation grant request with scope auto-suggestion",
	Long: `Approve a pending delegation:request from an agent.

cf approve reads the delegation:request message from the identity campfire,
infers the minimum scope from the failed gate predicate, and presents the
operator with a scope suggestion. The operator can accept the suggestion, edit
it interactively, or enter an override scope entirely.

On approval, a delegation:grant message is posted to the identity campfire and
the future is fulfilled.

Scope auto-suggestion (design v2 §2.6, OPEN-021):
  The suggested scope is the intersection of what was requested and what the
  failed predicate requires. It is always at least as narrow as the request —
  the operator's default action is least-privilege.

  A diff between requested and suggested scope is shown:
    "  convention:op"  — kept (in both requested and suggested)
    "- convention:op"  — removed (in requested but narrowed out)
    "+ convention:op"  — added (required by predicate, not in request)

Exit codes:
  0  grant posted (approved)
  1  rejected by operator or error

Example:
  cf approve abc123...def456
  cf approve abc123...def456 --persist 24h
  cf approve abc123...def456 --accept`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		msgID := args[0]

		// Capture approve-invoked timestamp (T1 for Budget B) immediately —
		// before any blocking I/O so we measure operator decision latency, not
		// network or disk latency. See uxmeas.BudgetBRecord for the full spec.
		approveInvokedAt := time.Now()

		// Budget B telemetry is enabled by either:
		//   --telemetry uxmeas flag, OR
		//   [telemetry] uxmeas = true in ~/.cf/config.toml
		// The IsEnabled check in uxmeas.RecordApproval handles both paths.
		_ = approveTelemetry // declared to expose the flag; actual gate is in uxmeas.IsEnabled

		// Parse --persist TTL (default 7d = 168h).
		persistTTL, err := parsePersistTTL(approvePersist)
		if err != nil {
			return fmt.Errorf("--persist: %w", err)
		}

		// Initialize protocol client.
		client, initResult, err := protocol.Init(CFHome())
		if err != nil {
			return fmt.Errorf("initializing protocol client: %w", err)
		}
		printInitResultVerbose(initResult)
		defer client.Close()

		// Resolve the identity campfire — the owner's inbox for delegation requests.
		s, err := openStore()
		if err != nil {
			return err
		}
		identityCampfire, err := resolveCampfireID("~home", s)
		s.Close()
		if err != nil {
			return fmt.Errorf("resolving identity campfire (alias ~home): %w\n\nSet up an identity campfire with: cf init", err)
		}

		// Read the delegation:request message by ID.
		result, err := client.Read(protocol.ReadRequest{
			CampfireID: identityCampfire,
			Tags:       []string{DelegationRequestTag},
			SkipSync:   false,
			Limit:      1000,
		})
		if err != nil {
			return fmt.Errorf("reading delegation:request messages: %w", err)
		}

		var reqMsg *protocol.Message
		for i := range result.Messages {
			if result.Messages[i].ID == msgID {
				reqMsg = &result.Messages[i]
				break
			}
		}
		if reqMsg == nil {
			return fmt.Errorf("delegation:request message %q not found in identity campfire %s",
				msgID[:min(12, len(msgID))], identityCampfire[:min(12, len(identityCampfire))])
		}

		// Parse the delegation:request payload.
		var req delegationRequestPayload
		if err := json.Unmarshal(reqMsg.Payload, &req); err != nil {
			return fmt.Errorf("parsing delegation:request payload: %w", err)
		}

		// Budget B telemetry — record T0 (message surfaced) and T1 (approve invoked).
		// RecordApproval is a no-op when telemetry is disabled (default).
		// reqMsg.Timestamp is Unix nanoseconds — the protocol stores message
		// creation time in nanoseconds (cf-protocol/protocol/message.go).
		//
		// Telemetry is enabled when either:
		//   --telemetry uxmeas flag is passed on this invocation, OR
		//   [telemetry] uxmeas = true is set in ~/.cf/config.toml (persistent opt-in).
		uxmeas.RecordApproval(CFHome(), req.FutureID, reqMsg.Timestamp, approveInvokedAt, req.FailedPredicate, approveTelemetry == "uxmeas")

		// Parse the failed predicate AST for scope suggestion.
		var predAST trust.PredicateAST
		if req.FailedPredicate != nil {
			predAST, err = trust.ParsePredicateAST(req.FailedPredicate)
			if err != nil {
				// Predicate parse failure is non-fatal — suggestion degrades to
				// showing only the requested scope with no narrowing.
				fmt.Fprintf(os.Stderr, "note: could not parse failed predicate for suggestion (%v); showing requested scope only\n", err)
			}
		}

		// Parse requested scope.
		requestedScope, err := ParseScopeString(req.RequestedScope)
		if err != nil {
			return fmt.Errorf("parsing requested_scope: %w", err)
		}

		// Compute scope auto-suggestion.
		suggestion := SuggestScope(requestedScope, predAST)

		// Display the request (human mode only).
		short := func(h string) string {
			if len(h) > 12 {
				return h[:12] + "..."
			}
			return h
		}
		if !jsonOutput {
			fmt.Printf("Delegation grant request\n")
			fmt.Printf("  Message : %s\n", short(msgID))
			fmt.Printf("  Agent   : %s\n", short(req.AgentPubkey))
			if req.CampfireID != "" {
				fmt.Printf("  Campfire: %s\n", short(req.CampfireID))
			}
			fmt.Printf("  Persist : %s (use --persist to change)\n", formatTTL(persistTTL))
			fmt.Println()

			// Show the diff.
			diffLines := suggestion.DiffLines()
			if len(diffLines) == 0 {
				fmt.Println("Scope: (empty — no capabilities would be granted)")
			} else {
				fmt.Println("Scope suggestion (- removed, + added vs requested):")
				for _, l := range diffLines {
					fmt.Println(" ", l)
				}
				if suggestion.WiderThanRequested {
					fmt.Println("  (note: suggested scope is wider than requested — predicate requires more)")
				}
			}
			fmt.Println()
		}

		// Determine the scope to approve.
		var approvedScope []ScopeEntry

		if approveAccept {
			// Non-interactive: accept the suggestion.
			approvedScope = suggestion.Suggested
			if !jsonOutput {
				fmt.Printf("Accepted suggestion: %s\n", FormatScope(approvedScope))
			}
		} else {
			// Interactive: show suggestion and prompt.
			suggestedStr := FormatScope(suggestion.Suggested)
			fmt.Printf("Suggested scope: %s\n", suggestedStr)
			fmt.Println("Options:")
			fmt.Println("  [Enter]   accept suggestion")
			fmt.Println("  [e]       edit scope interactively")
			fmt.Println("  [r]       reject (do not approve)")
			fmt.Print("Choice: ")

			reader := bufio.NewReader(os.Stdin)
			choice, _ := reader.ReadString('\n')
			choice = strings.TrimSpace(strings.ToLower(choice))

			switch choice {
			case "", "a":
				// Accept suggestion.
				approvedScope = suggestion.Suggested
			case "e":
				// Edit: show suggested string and let operator modify.
				fmt.Printf("Edit scope (current: %s)\n", suggestedStr)
				fmt.Print("New scope: ")
				edited, _ := reader.ReadString('\n')
				edited = strings.TrimSpace(edited)
				if edited == "" {
					approvedScope = suggestion.Suggested
				} else {
					approvedScope, err = ParseScopeString(edited)
					if err != nil {
						return fmt.Errorf("parsing edited scope: %w", err)
					}
				}
			case "r":
				fmt.Println("Rejected.")
				os.Exit(1)
				return nil
			default:
				// Treat as a direct scope override if it looks like "conv:op, ..."
				if strings.Contains(choice, ":") {
					approvedScope, err = ParseScopeString(choice)
					if err != nil {
						return fmt.Errorf("parsing scope override: %w", err)
					}
				} else {
					fmt.Println("Unrecognized input. Rejected.")
					os.Exit(1)
					return nil
				}
			}
		}

		if len(approvedScope) == 0 {
			return fmt.Errorf("approved scope is empty — cannot issue a zero-capability grant")
		}

		// Post the delegation:grant approval message.
		grantPayload := delegationGrantPayload{
			AgentPubkey:   req.AgentPubkey,
			ApprovedScope: FormatScope(approvedScope),
			PersistTTL:    formatTTL(persistTTL),
			CampfireID:    req.CampfireID,
			FutureID:      req.FutureID,
			ApprovedAt:    time.Now().Unix(),
		}
		payloadBytes, err := json.Marshal(grantPayload)
		if err != nil {
			return fmt.Errorf("marshaling grant payload: %w", err)
		}

		// Tags: delegation:grant + fulfills the future.
		tags := []string{DelegationGrantTag}
		antecedents := []string{}
		if req.FutureID != "" {
			// The approval fulfills the future.
			tags = append(tags, "fulfills")
			antecedents = append(antecedents, req.FutureID)
		}

		_, err = client.Send(protocol.SendRequest{
			CampfireID:  identityCampfire,
			Payload:     payloadBytes,
			Tags:        tags,
			Antecedents: antecedents,
		})
		if err != nil {
			return fmt.Errorf("posting delegation:grant: %w", err)
		}

		// Budget B telemetry — record LandedAt (when the delegation:grant Send returned).
		// This is an optional bookkeeping field; the pass/fail gate only uses InvokedAt.
		uxmeas.UpdateLandedAt(CFHome(), req.FutureID, time.Now(), approveTelemetry == "uxmeas")

		if jsonOutput {
			return emitApproveJSON(msgID, req.AgentPubkey, approvedScope, persistTTL, req.CampfireID, req.FutureID)
		}

		fmt.Printf("Grant approved: agent=%s scope=%q ttl=%s\n",
			short(req.AgentPubkey),
			FormatScope(approvedScope),
			formatTTL(persistTTL),
		)
		if req.FutureID != "" {
			fmt.Printf("  Future %s fulfilled.\n", short(req.FutureID))
		}
		return nil
	},
}

// parsePersistTTL parses the --persist flag value.
// Accepts Go duration strings (e.g. "7d" is not a Go duration; we parse "Nd"
// ourselves and map to hours). Default is 7 days.
func parsePersistTTL(s string) (time.Duration, error) {
	if s == "" {
		return 7 * 24 * time.Hour, nil
	}
	// Handle "Nd" shorthand (N days).
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &days); err == nil && days > 0 {
			d := time.Duration(days) * 24 * time.Hour
			if d > 30*24*time.Hour {
				return 0, fmt.Errorf("persist TTL exceeds 30-day maximum (got %s)", s)
			}
			return d, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (accepted: e.g. 7d, 24h, 168h)", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("persist TTL must be positive")
	}
	if d > 30*24*time.Hour {
		return 0, fmt.Errorf("persist TTL exceeds 30-day maximum (got %s)", s)
	}
	return d, nil
}

// formatTTL formats a duration as a compact human string (e.g. "7d", "24h").
func formatTTL(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days > 0 && d == time.Duration(days)*24*time.Hour {
		return fmt.Sprintf("%dd", days)
	}
	return d.String()
}

// emitApproveJSON emits a JSON result for --json mode.
func emitApproveJSON(msgID, agentPubkey string, scope []ScopeEntry, ttl time.Duration, campfireID, futureID string) error {
	type result struct {
		MsgID       string   `json:"msg_id"`
		AgentPubkey string   `json:"agent_pubkey"`
		Scope       []string `json:"approved_scope"`
		PersistTTL  string   `json:"persist_ttl"`
		CampfireID  string   `json:"campfire_id,omitempty"`
		FutureID    string   `json:"future_id,omitempty"`
	}
	scopeStrs := make([]string, len(scope))
	for i, e := range scope {
		scopeStrs[i] = e.String()
	}
	r := result{
		MsgID:       msgID,
		AgentPubkey: agentPubkey,
		Scope:       scopeStrs,
		PersistTTL:  formatTTL(ttl),
		CampfireID:  campfireID,
		FutureID:    futureID,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func init() {
	approveCmd.Flags().StringVar(&approvePersist, "persist", "7d", "grant TTL (max 30d; e.g. 7d, 24h)")
	approveCmd.Flags().BoolVar(&approveAccept, "accept", false, "accept the scope suggestion without interactive prompt")
	approveCmd.Flags().StringVar(&approveTelemetry, "telemetry", "", "enable telemetry instrumentation (\"uxmeas\" = Budget B Phase 8 Gate 2; also enabled via [telemetry] uxmeas = true in ~/.cf/config.toml)")
	rootCmd.AddCommand(approveCmd)
}

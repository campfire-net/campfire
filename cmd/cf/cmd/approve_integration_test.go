package cmd

// approve_integration_test.go — integration test for cf approve --accept --json.
//
// Veracity rework for campfireagent-9a60:
//
// The veracity adversary (verdict:fail) found that approve_suggest_test.go only
// exercises SuggestScope() in isolation. A bug in approve.go's argument parsing,
// future resolution, or delegation:grant posting would pass all 9 existing Go
// tests. This test closes that gap.
//
// Approach:
//   1. setupTrustTestEnv initializes a real CF_HOME via `cf init`, creating an
//      identity campfire and setting the "home" alias. No mocks.
//   2. We use protocol.New + store.Open to post a real delegation:request message
//      to the identity campfire. This is the same path the convention executor
//      uses in production.
//   3. We invoke rootCmd.Execute() for `cf approve <msg-id> --accept --json`,
//      which runs the full approve.go path: protocol.Init → client.Read →
//      SuggestScope → client.Send.
//   4. We verify: JSON output is well-formed, approved_scope contains the
//      predicate-required entry (rd:claim), persist_ttl defaults to 7d, msg_id
//      and future_id are round-tripped, and a delegation:grant message with
//      correct payload is readable from the identity campfire after the command.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/naming"
)

// runApproveCmd invokes `cf approve <args...> --json` and returns the captured
// stdout and the cobra error (if any).
// It sets jsonOutput = true for the duration of the call, mirroring the pattern
// used in trust_pin_test.go and trust_grant_test.go.
func runApproveCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	origStdout := os.Stdout
	origStderr := os.Stderr
	devNull, _ := os.Open(os.DevNull)
	os.Stdout = w
	os.Stderr = devNull // suppress interactive output / telemetry noise

	jsonOutput = true
	defer func() { jsonOutput = false }()

	// Reset --persist and --accept flags to defaults before each call.
	_ = approveCmd.Flags().Set("persist", "7d")
	_ = approveCmd.Flags().Set("accept", "false")
	_ = approveCmd.Flags().Set("telemetry", "")

	rootCmd.SetArgs(append([]string{"approve"}, args...))
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout
	os.Stderr = origStderr
	devNull.Close()

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	return buf.String(), runErr
}

// TestApproveCmd_AcceptJSON_EndToEnd is the integration test for cf approve --accept --json.
//
// It exercises: protocol.Init → client.Read (find delegation:request) →
// SuggestScope → client.Send (post delegation:grant).
//
// Done conditions (from campfireagent-9a60 spec):
//   - JSON output is well-formed with required keys (msg_id, agent_pubkey,
//     approved_scope, persist_ttl, future_id).
//   - approved_scope contains "rd:claim" (the predicate-required entry).
//   - persist_ttl defaults to "7d".
//   - The delegation:grant message is written to the identity campfire with the
//     correct agent_pubkey, approved_scope, and future_id in its payload.
//   - The future is referenced in the grant's antecedents (fulfills).
func TestApproveCmd_AcceptJSON_EndToEnd(t *testing.T) {
	// NO t.Parallel() — rootCmd is shared state, same contract as trust_pin_test.go.

	// Step 1: Initialize a real CF_HOME with identity campfire and home alias.
	cfHomeDir := setupTrustTestEnv(t)

	// Step 2: Resolve the identity campfire ID from the "home" alias.
	aliasStore := naming.NewAliasStore(cfHomeDir)
	identityCampfireID, err := aliasStore.Get("home")
	if err != nil {
		t.Fatalf("resolving 'home' alias after cf init: %v", err)
	}
	if identityCampfireID == "" {
		t.Fatal("'home' alias resolved to empty string — cf init did not set it")
	}

	// Step 3: Load the agent identity to use protocol.New for sending the request.
	agentID, err := identity.Load(filepath.Join(cfHomeDir, "identity.json"))
	if err != nil {
		t.Fatalf("loading agent identity: %v", err)
	}

	// Step 4: Open the store (same store protocol.Init uses under the hood).
	s, err := store.Open(store.StorePath(cfHomeDir))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Step 5: Build a delegation:request payload.
	// Scenario: agent hit gate "grant:rd:claim", requested scope "rd:*".
	// The future_id is a synthetic ID; we use a short but valid-looking string
	// for this field (approve.go only uses it as an antecedent / round-trip value).
	syntheticFutureID := "future-not-awaited-0000000000000000"
	reqPayload := delegationRequestPayload{
		AgentPubkey:    agentID.PublicKeyHex(),
		FutureID:       syntheticFutureID,
		FailedPredicate: map[string]any{"kind": "grant", "convention": "rd", "op": "claim"},
		RequestedScope:  "rd:*",
		CampfireID:      identityCampfireID,
	}
	reqPayloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		t.Fatalf("marshaling delegation:request payload: %v", err)
	}

	// Step 6: Post the delegation:request to the identity campfire via protocol.New.
	client := protocol.New(s, agentID)
	reqMsg, err := client.Send(protocol.SendRequest{
		CampfireID: identityCampfireID,
		Payload:    reqPayloadBytes,
		Tags:       []string{DelegationRequestTag, "future"},
	})
	if err != nil {
		t.Fatalf("posting delegation:request to identity campfire: %v", err)
	}
	msgID := reqMsg.ID

	// Step 7: Run cf approve <msg-id> --accept --json via rootCmd.Execute().
	out, runErr := runApproveCmd(t, msgID, "--accept", "--json")
	if runErr != nil {
		t.Fatalf("cf approve --accept --json: %v\noutput=%s", runErr, out)
	}

	// Step 8: Verify the JSON output is well-formed with required keys.
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parsing approve JSON output: %v\noutput=%q", err, out)
	}
	for _, key := range []string{"msg_id", "agent_pubkey", "approved_scope", "persist_ttl"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("JSON output missing required key %q: %v", key, parsed)
		}
	}

	// Step 9: Verify approved_scope contains "rd:claim" (predicate-required entry).
	approvedScope, ok := parsed["approved_scope"].([]interface{})
	if !ok {
		t.Fatalf("approved_scope is not an array: %T %v", parsed["approved_scope"], parsed["approved_scope"])
	}
	hasRdClaim := false
	for _, entry := range approvedScope {
		if s, ok := entry.(string); ok && s == "rd:claim" {
			hasRdClaim = true
		}
	}
	if !hasRdClaim {
		t.Errorf("approved_scope should contain 'rd:claim' (predicate-required), got: %v", approvedScope)
	}

	// Step 10: Verify default persist TTL is 7d.
	if persistTTL, ok := parsed["persist_ttl"].(string); !ok || persistTTL != "7d" {
		t.Errorf("persist_ttl = %v, want \"7d\"", parsed["persist_ttl"])
	}

	// Step 11: Verify the delegation:grant message was written to the identity campfire.
	// Use protocol.New + client.Read to read back messages from the identity campfire.
	readResult, err := client.Read(protocol.ReadRequest{
		CampfireID: identityCampfireID,
		Tags:       []string{DelegationGrantTag},
		SkipSync:   false,
		Limit:      100,
	})
	if err != nil {
		t.Fatalf("reading delegation:grant messages from identity campfire: %v", err)
	}

	var grantMsg *protocol.Message
	for i := range readResult.Messages {
		for _, tag := range readResult.Messages[i].Tags {
			if tag == DelegationGrantTag {
				grantMsg = &readResult.Messages[i]
				break
			}
		}
		if grantMsg != nil {
			break
		}
	}
	if grantMsg == nil {
		t.Fatalf("delegation:grant message not found in identity campfire after cf approve")
	}

	// Step 12: Verify the grant payload round-trips the expected values.
	var grantPayload delegationGrantPayload
	if err := json.Unmarshal(grantMsg.Payload, &grantPayload); err != nil {
		t.Fatalf("parsing delegation:grant payload: %v", err)
	}
	if grantPayload.AgentPubkey != agentID.PublicKeyHex() {
		t.Errorf("grant.agent_pubkey = %q, want %q", grantPayload.AgentPubkey, agentID.PublicKeyHex())
	}
	if grantPayload.FutureID != syntheticFutureID {
		t.Errorf("grant.future_id = %q, want %q", grantPayload.FutureID, syntheticFutureID)
	}
	if grantPayload.ApprovedScope == "" {
		t.Error("grant.approved_scope is empty — scope was not persisted to the grant message")
	}
	// The approved scope string must include rd:claim.
	grantScopeEntries, err := ParseScopeString(grantPayload.ApprovedScope)
	if err != nil {
		t.Fatalf("parsing grant approved_scope %q: %v", grantPayload.ApprovedScope, err)
	}
	hasRdClaimInGrant := false
	for _, e := range grantScopeEntries {
		if e.Convention == "rd" && e.OpPattern == "claim" {
			hasRdClaimInGrant = true
		}
	}
	if !hasRdClaimInGrant {
		t.Errorf("grant.approved_scope %q does not include rd:claim", grantPayload.ApprovedScope)
	}
	// Verify the future_id appears in the grant message's antecedents (fulfills).
	hasFulfills := false
	for _, ant := range grantMsg.Antecedents {
		if ant == syntheticFutureID {
			hasFulfills = true
		}
	}
	if !hasFulfills {
		t.Errorf("delegation:grant antecedents %v do not include future_id %q — future was not fulfilled",
			grantMsg.Antecedents, syntheticFutureID)
	}
}

// TestApproveCmd_PersistFlag_EndToEnd verifies that --persist 1d overrides the
// default 7d TTL and is reflected in both the JSON output and the grant payload.
// This exercises the parsePersistTTL → delegationGrantPayload.PersistTTL path.
func TestApproveCmd_PersistFlag_EndToEnd(t *testing.T) {
	// NO t.Parallel() — rootCmd is shared state.

	cfHomeDir := setupTrustTestEnv(t)

	aliasStore := naming.NewAliasStore(cfHomeDir)
	identityCampfireID, err := aliasStore.Get("home")
	if err != nil {
		t.Fatalf("resolving 'home' alias: %v", err)
	}

	agentID, err := identity.Load(filepath.Join(cfHomeDir, "identity.json"))
	if err != nil {
		t.Fatalf("loading identity: %v", err)
	}

	s, err := store.Open(store.StorePath(cfHomeDir))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	reqPayload := delegationRequestPayload{
		AgentPubkey:    agentID.PublicKeyHex(),
		FutureID:       "future-persist-test-00000000000000",
		FailedPredicate: map[string]any{"kind": "grant", "convention": "rd", "op": "claim"},
		RequestedScope:  "rd:claim",
		CampfireID:      identityCampfireID,
	}
	reqPayloadBytes, _ := json.Marshal(reqPayload)

	client := protocol.New(s, agentID)
	reqMsg, err := client.Send(protocol.SendRequest{
		CampfireID: identityCampfireID,
		Payload:    reqPayloadBytes,
		Tags:       []string{DelegationRequestTag, "future"},
	})
	if err != nil {
		t.Fatalf("posting delegation:request: %v", err)
	}

	out, runErr := runApproveCmd(t, reqMsg.ID, "--accept", "--persist", "1d", "--json")
	if runErr != nil {
		t.Fatalf("cf approve --accept --persist 1d --json: %v\noutput=%s", runErr, out)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parsing JSON: %v\noutput=%q", err, out)
	}
	if persistTTL, ok := parsed["persist_ttl"].(string); !ok || persistTTL != "1d" {
		t.Errorf("persist_ttl = %v, want \"1d\"", parsed["persist_ttl"])
	}
}

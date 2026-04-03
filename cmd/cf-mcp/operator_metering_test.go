package main

// operator_metering_test.go — Tests for operator key metering and campfire_init auth fields.
//
// Tests verify:
//   - TestOperatorKeyMetering_BillsToForgeAccount: forge-tk- session's convention
//     metering is attributed to the operator's Forge account (not the convention
//     server's account in the event).
//   - TestOperatorKeyMetering_NormalSessionUnchanged: normal session metering uses
//     the convention server's account from the event (unchanged behaviour).
//   - TestCampfireInitResponse_OperatorKey: campfire_init for a forge-tk- session
//     includes auth_method="operator_key" and the correct operator_account_id.
//   - TestCampfireInitResponse_NormalSession: campfire_init for a normal session
//     includes auth_method="session" and no operator_account_id field.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/forge"
)

// ---------------------------------------------------------------------------
// TestOperatorKeyMetering_BillsToForgeAccount
// ---------------------------------------------------------------------------

// TestOperatorKeyMetering_BillsToForgeAccount verifies that when a Tier 2
// convention meter event is fired in a context carrying a session Forge account
// (forge-tk- path), the UsageEvent is attributed to the operator's account
// rather than the convention server's ForgeAccountID in the event.
func TestOperatorKeyMetering_BillsToForgeAccount(t *testing.T) {
	_, client, events, mu := newCapturingForgeServer(t)
	emitter := forge.NewForgeEmitter(client, 100, nil)
	runEmitter(t, emitter)

	hook := buildConventionMeteringHook(emitter)

	// Simulate a forge-tk- session: inject the operator's account via context.
	const operatorAccountID = "acct-operator-forge"
	const serverAccountID = "acct-convention-server"

	ctx := WithSessionForgeAccount(context.Background(), operatorAccountID)

	event := convention.ConventionMeterEvent{
		CampfireID:     "fire-abc",
		Convention:     "myconv",
		Operation:      "myop",
		Tier:           2,
		ServerID:       "server-xyz",
		ForgeAccountID: serverAccountID, // convention server's account
		MessageID:      "msg-001",
		Status:         "dispatched",
	}
	hook(ctx, event)

	// Wait for delivery.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*events)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	evs := make([]forge.UsageEvent, len(*events))
	copy(evs, *events)
	mu.Unlock()

	if len(evs) != 1 {
		t.Fatalf("expected 1 usage event, got %d", len(evs))
	}
	ev := evs[0]

	// The usage event must bill the operator's account, not the convention server's.
	if ev.AccountID != operatorAccountID {
		t.Errorf("AccountID = %q, want operator account %q", ev.AccountID, operatorAccountID)
	}
	if ev.AccountID == serverAccountID {
		t.Error("AccountID must not be the convention server account for forge-tk- sessions")
	}
	if ev.UnitType != "convention-op-tier2" {
		t.Errorf("UnitType = %q, want %q", ev.UnitType, "convention-op-tier2")
	}
	if ev.ServiceID != "campfire-hosting" {
		t.Errorf("ServiceID = %q, want %q", ev.ServiceID, "campfire-hosting")
	}
}

// ---------------------------------------------------------------------------
// TestOperatorKeyMetering_NormalSessionUnchanged
// ---------------------------------------------------------------------------

// TestOperatorKeyMetering_NormalSessionUnchanged verifies that for normal
// (non-forge-tk-) sessions the metering hook uses the convention server's
// ForgeAccountID from the event, unchanged from prior behavior.
func TestOperatorKeyMetering_NormalSessionUnchanged(t *testing.T) {
	_, client, events, mu := newCapturingForgeServer(t)
	emitter := forge.NewForgeEmitter(client, 100, nil)
	runEmitter(t, emitter)

	hook := buildConventionMeteringHook(emitter)

	const serverAccountID = "acct-convention-server"

	// Normal session: no session forge account in context.
	event := convention.ConventionMeterEvent{
		CampfireID:     "fire-xyz",
		Convention:     "myconv",
		Operation:      "myop",
		Tier:           2,
		ServerID:       "server-abc",
		ForgeAccountID: serverAccountID,
		MessageID:      "msg-002",
		Status:         "dispatched",
	}
	hook(context.Background(), event)

	// Wait for delivery.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*events)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	evs := make([]forge.UsageEvent, len(*events))
	copy(evs, *events)
	mu.Unlock()

	if len(evs) != 1 {
		t.Fatalf("expected 1 usage event, got %d", len(evs))
	}
	// Normal sessions: event.ForgeAccountID (the convention server's account) is used.
	if evs[0].AccountID != serverAccountID {
		t.Errorf("AccountID = %q, want convention server account %q", evs[0].AccountID, serverAccountID)
	}
}

// ---------------------------------------------------------------------------
// campfire_init response helpers
// ---------------------------------------------------------------------------

// extractInitJSON parses the JSON object from a campfire_init response text.
// The text may have "\n\nSession token: ..." appended after the JSON — this
// helper strips that suffix before parsing.
func extractInitJSON(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("cannot extract content from result: %v", string(b))
	}
	text := result.Content[0].Text
	// Strip appended session token text (added by handleMCPSessioned).
	if idx := strings.Index(text, "\n\nSession token:"); idx >= 0 {
		text = text[:idx]
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("parsing init JSON from text: %v\ntext: %s", err, text)
	}
	return m
}

// ---------------------------------------------------------------------------
// TestCampfireInitResponse_OperatorKey
// ---------------------------------------------------------------------------

// TestCampfireInitResponse_OperatorKey verifies that a campfire_init response
// for a forge-tk- session includes auth_method="operator_key" and the correct
// operator_account_id field.
func TestCampfireInitResponse_OperatorKey(t *testing.T) {
	const accountID = "acct-operator-init"

	forgeSrv, _ := newForgeResolveServer(t, accountID, http.StatusOK)
	forgeClient := &forge.Client{
		BaseURL:     forgeSrv.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  forgeSrv.Client(),
		RetryDelays: []time.Duration{0},
	}

	srv := newSessionedServerWithForge(t, forgeClient)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "Bearer forge-tk-testkey", body)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d; body: %s", w.Code, w.Body.String())
	}

	resp := decodeRPCResponse(t, w)
	m := extractInitJSON(t, resp)

	authMethod, _ := m["auth_method"].(string)
	if authMethod != "operator_key" {
		t.Errorf("auth_method = %q, want %q", authMethod, "operator_key")
	}

	operatorAccountID, _ := m["operator_account_id"].(string)
	if operatorAccountID != accountID {
		t.Errorf("operator_account_id = %q, want %q", operatorAccountID, accountID)
	}
}

// ---------------------------------------------------------------------------
// TestCampfireInitResponse_NormalSession
// ---------------------------------------------------------------------------

// TestCampfireInitResponse_NormalSession verifies that a campfire_init response
// for a normal (non-forge-tk-) session includes auth_method="session" and no
// operator_account_id field.
func TestCampfireInitResponse_NormalSession(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionManager(dir)
	defer sm.Stop()

	srv := &server{
		cfHome:         dir,
		beaconDir:      dir,
		sessManager:    sm,
		cfHomeExplicit: true,
	}

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"campfire_init","arguments":{}}}`
	w := postMCPRequest(t, srv, "", body)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d; body: %s", w.Code, w.Body.String())
	}

	resp := decodeRPCResponse(t, w)
	m := extractInitJSON(t, resp)

	authMethod, _ := m["auth_method"].(string)
	if authMethod != "session" {
		t.Errorf("auth_method = %q, want %q", authMethod, "session")
	}

	if _, hasOperatorAcct := m["operator_account_id"]; hasOperatorAcct {
		t.Error("operator_account_id must not be present for normal sessions")
	}
}

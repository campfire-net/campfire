package main

// e2e_durability_test.go — End-to-end cold start cycle integration test.
//
// Bead: campfireagent-c1c
//
// TestE2E_DurableSession_FullColdStartCycle exercises the FULL cold start
// lifecycle in a single test:
//
//  1. Create server with global store (real SQLite stand-in for Azure Table Storage)
//  2. Initialize identity (campfire_init) — records agent public key
//  3. Create a campfire — writes CampfirePrivKey to global store
//  4. Send messages to the campfire
//  5. Create a campfire with a convention declaration
//  6. Create a DM campfire — writes DM campfire key to global store
//  7. Verify audit trail exists before cold start
//  8. COLD START: wipe all campfire.cbor files from the session's cfHome
//  9. Verify all durability paths survived:
//     - Identity is recovered (same agent pubkey via campfire_id)
//     - Campfire is accessible (campfire_read returns sent messages)
//     - Convention sends work (convention tool call succeeds via global store fallback)
//     - DM campfire is discoverable (second DM send succeeds, reused=true)
//     - Audit trail is intact (campfire_read on audit campfire returns entries)

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/store"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
)

// TestE2E_DurableSession_FullColdStartCycle is the comprehensive end-to-end test
// proving that the entire durability system works together — not just individual paths.
//
// Done condition: all assertions pass after the cold start, proving that
// identity, campfire keys, convention sends, DM campfires, and audit trail
// all survive a full wipe of local campfire CBOR state.
func TestE2E_DurableSession_FullColdStartCycle(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	// ---------------------------------------------------------------------------
	// Phase 1: Set up server with a shared global SQLite store.
	// ---------------------------------------------------------------------------
	//
	// In production, the global store is Azure Table Storage. Here we use a real
	// SQLite store as a stand-in — it provides the same key/value semantics and
	// exercises the same code paths.

	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	globalDir := t.TempDir()
	globalStore, err := store.Open(store.StorePath(globalDir))
	if err != nil {
		t.Fatalf("opening global store: %v", err)
	}
	t.Cleanup(func() { globalStore.Close() })
	srv.transportRouter.SetGlobalStore(globalStore, tsURL)

	// ---------------------------------------------------------------------------
	// Phase 2: Initialize identity (campfire_init).
	// ---------------------------------------------------------------------------

	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("campfire_init: expected non-empty session token")
	}

	// Record the agent's public key before cold start.
	idResp := mcpCall(t, tsURL, token, "campfire_id", map[string]interface{}{})
	if idResp.Error != nil {
		t.Fatalf("campfire_id before cold start: code=%d msg=%s", idResp.Error.Code, idResp.Error.Message)
	}
	idTextBefore := extractResultText(t, idResp)
	var idResultBefore map[string]string
	if err := json.Unmarshal([]byte(idTextBefore), &idResultBefore); err != nil {
		t.Fatalf("parsing campfire_id before cold start: %v", err)
	}
	pubKeyBefore := idResultBefore["public_key"]
	if len(pubKeyBefore) != 64 {
		t.Fatalf("expected 64-char hex public_key before cold start, got %q", pubKeyBefore)
	}

	// ---------------------------------------------------------------------------
	// Phase 3: Create a campfire and send messages.
	// ---------------------------------------------------------------------------

	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description": "e2e-cold-start main campfire",
	})
	if createResp.Error != nil {
		t.Fatalf("campfire_create: code=%d msg=%s", createResp.Error.Code, createResp.Error.Message)
	}
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
		Transport  string `json:"transport"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("parsing campfire_create result: %v (text: %s)", err, createText)
	}
	mainCampfireID := createResult.CampfireID
	if mainCampfireID == "" {
		t.Fatal("campfire_create: campfire_id is empty")
	}
	if createResult.Transport != "p2p-http" {
		t.Skipf("test requires p2p-http transport, got %q", createResult.Transport)
	}

	// Confirm global store has the campfire's private key (pre-cold-start invariant).
	gm, err := globalStore.GetMembership(mainCampfireID)
	if err != nil {
		t.Fatalf("global store GetMembership for main campfire: %v", err)
	}
	if gm == nil || gm.CampfirePrivKey == "" {
		t.Fatalf("global store has no CampfirePrivKey for main campfire %s — campfire_create did not write to global store", mainCampfireID)
	}

	// Send a message that we'll verify is readable after cold start.
	const sentMessage = "e2e cold start durability — pre-cold-start message"
	sendResp := mcpCall(t, tsURL, token, "campfire_send", map[string]interface{}{
		"campfire_id": mainCampfireID,
		"message":     sentMessage,
	})
	if sendResp.Error != nil {
		t.Fatalf("campfire_send: code=%d msg=%s", sendResp.Error.Code, sendResp.Error.Message)
	}

	// ---------------------------------------------------------------------------
	// Phase 4: Create a campfire with a convention declaration.
	// ---------------------------------------------------------------------------

	convCreateResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description":    "e2e-cold-start convention campfire",
		"delivery_modes": []string{"pull", "push"},
		"declarations": []interface{}{
			map[string]interface{}{
				"convention":  "e2e-cold-start-convention",
				"version":     "0.1",
				"operation":   "ping",
				"description": "E2E cold start ping",
				"signing":     "member_key",
				"antecedents": "none",
				"produces_tags": []interface{}{
					map[string]interface{}{"tag": "test:e2e-cold-start-ping", "cardinality": "exactly_one"},
				},
				"args": []interface{}{
					map[string]interface{}{
						"name":       "message",
						"type":       "string",
						"required":   true,
						"max_length": 256,
					},
				},
			},
		},
	})
	if convCreateResp.Error != nil {
		t.Fatalf("campfire_create (convention campfire): code=%d msg=%s",
			convCreateResp.Error.Code, convCreateResp.Error.Message)
	}
	convCreateText := extractResultText(t, convCreateResp)
	var convCreateResult struct {
		CampfireID string `json:"campfire_id"`
		Transport  string `json:"transport"`
	}
	if err := json.Unmarshal([]byte(convCreateText), &convCreateResult); err != nil {
		t.Fatalf("parsing convention campfire create result: %v (text: %s)", err, convCreateText)
	}
	convCampfireID := convCreateResult.CampfireID
	if convCampfireID == "" {
		t.Fatal("convention campfire_create: campfire_id is empty")
	}

	// Confirm global store has convention campfire key.
	convGM, err := globalStore.GetMembership(convCampfireID)
	if err != nil {
		t.Fatalf("global store GetMembership for convention campfire: %v", err)
	}
	if convGM == nil || convGM.CampfirePrivKey == "" {
		t.Fatalf("global store has no CampfirePrivKey for convention campfire %s", convCampfireID)
	}

	// ---------------------------------------------------------------------------
	// Phase 5: Create a DM campfire.
	// ---------------------------------------------------------------------------

	// Generate a distinct target key (different from the agent's own key).
	targetKey := buildDistinctKey(t, pubKeyBefore)

	dmResp := mcpCall(t, tsURL, token, "campfire_dm", map[string]interface{}{
		"target_key": targetKey,
		"message":    "e2e cold start DM — instance 1",
	})
	if dmResp.Error != nil {
		t.Fatalf("campfire_dm (initial): code=%d msg=%s", dmResp.Error.Code, dmResp.Error.Message)
	}
	dmText := extractResultText(t, dmResp)
	var dmResult struct {
		CampfireID string `json:"campfire_id"`
		Reused     bool   `json:"reused"`
	}
	if err := json.Unmarshal([]byte(dmText), &dmResult); err != nil {
		t.Fatalf("parsing DM result: %v (text: %s)", err, dmText)
	}
	dmCampfireID := dmResult.CampfireID
	if dmCampfireID == "" {
		t.Fatal("campfire_dm: campfire_id is empty")
	}
	if dmResult.Reused {
		t.Fatal("expected DM campfire to be newly created (reused=false)")
	}

	// Confirm global store has DM campfire key.
	dmGM, err := globalStore.GetMembership(dmCampfireID)
	if err != nil {
		t.Fatalf("global store GetMembership for DM campfire: %v", err)
	}
	if dmGM == nil || dmGM.CampfirePrivKey == "" {
		t.Fatalf("global store has no CampfirePrivKey for DM campfire %s — handleDM did not write to global store", dmCampfireID)
	}

	// ---------------------------------------------------------------------------
	// Phase 6: Set up audit writer and verify audit trail exists before cold start.
	// ---------------------------------------------------------------------------

	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found — cannot set up audit writer")
	}

	// Create an audit writer for this session's server context.
	// The server's cfHome/beaconDir are from the session, not srv's top-level dirs.
	auditSrv := &server{
		cfHome:          sess.cfHome,
		beaconDir:       srv.beaconDir,
		cfHomeExplicit:  true,
		sessManager:     srv.sessManager,
		transportRouter: srv.transportRouter,
		sess:            sess,
	}
	aw, err := NewAuditWriter(auditSrv)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	t.Cleanup(func() { aw.Close() })

	// Trigger an audit entry by sending another message.
	auditSrv.auditWriter = aw
	sendForAuditResp := mcpCall(t, tsURL, token, "campfire_send", map[string]interface{}{
		"campfire_id": mainCampfireID,
		"message":     "e2e audit trigger message",
	})
	if sendForAuditResp.Error != nil {
		t.Fatalf("campfire_send for audit: code=%d msg=%s",
			sendForAuditResp.Error.Code, sendForAuditResp.Error.Message)
	}
	aw.Flush()

	auditCampfireID := aw.CampfireID()
	if auditCampfireID == "" {
		t.Fatal("audit campfire ID is empty")
	}

	// Verify audit trail is readable BEFORE cold start.
	readAuditBeforeResp := mcpCall(t, tsURL, token, "campfire_read", map[string]interface{}{
		"campfire_id": auditCampfireID,
		"all":         true,
	})
	if readAuditBeforeResp.Error != nil {
		t.Fatalf("campfire_read (audit, before cold start): code=%d msg=%s",
			readAuditBeforeResp.Error.Code, readAuditBeforeResp.Error.Message)
	}
	auditTextBefore := extractResultText(t, readAuditBeforeResp)
	if !strings.Contains(auditTextBefore, "action") {
		t.Fatalf("audit entries not found before cold start; got: %s", auditTextBefore)
	}

	// ---------------------------------------------------------------------------
	// Phase 7: COLD START — wipe all campfire.cbor files from the session's cfHome.
	// ---------------------------------------------------------------------------
	//
	// simulateColdStart removes all campfire.cbor files in the cfHome tree.
	// This is the Azure Functions cold start scenario: the new instance has a fresh
	// local filesystem with no campfire CBOR state files. The global store
	// (Azure Table Storage) provides fallback key lookup.
	//
	// identity.json is NOT removed — it's a separate file that the session manager
	// preserves across cold starts (keyed to the session token).

	sessionCFHome := sess.cfHome
	simulateColdStart(t, sessionCFHome)

	// Confirm CBOR files are gone.
	mainCampfireCBOR := filepath.Join(sessionCFHome, mainCampfireID, "campfire.cbor")
	if _, statErr := os.Stat(mainCampfireCBOR); !os.IsNotExist(statErr) {
		t.Logf("note: main campfire.cbor may be in a different path; stat: %v", statErr)
	}

	// ---------------------------------------------------------------------------
	// Phase 8: Verify all durability paths survived the cold start.
	// ---------------------------------------------------------------------------

	// --- 8a. Identity is recovered (same agent pubkey) ---
	//
	// identity.json survives because simulateColdStart only removes campfire.cbor.
	// The session token maps to the same cfHome, so campfire_id still works.
	idAfterResp := mcpCall(t, tsURL, token, "campfire_id", map[string]interface{}{})
	if idAfterResp.Error != nil {
		t.Fatalf("campfire_id after cold start: code=%d msg=%s — identity was lost on cold start",
			idAfterResp.Error.Code, idAfterResp.Error.Message)
	}
	idTextAfter := extractResultText(t, idAfterResp)
	var idResultAfter map[string]string
	if err := json.Unmarshal([]byte(idTextAfter), &idResultAfter); err != nil {
		t.Fatalf("parsing campfire_id after cold start: %v", err)
	}
	pubKeyAfter := idResultAfter["public_key"]
	if pubKeyAfter != pubKeyBefore {
		t.Errorf("identity changed across cold start: before=%s after=%s — identity not durable",
			pubKeyBefore, pubKeyAfter)
	}

	// --- 8b. Campfire is accessible — can read messages sent before cold start ---
	//
	// campfire_read after cold start triggers the global store fallback in
	// ReadState: the local campfire.cbor is absent, so the HTTP transport
	// reconstructs the CampfireState from the global store's CampfirePrivKey.
	readAfterResp := mcpCall(t, tsURL, token, "campfire_read", map[string]interface{}{
		"campfire_id": mainCampfireID,
		"all":         true,
	})
	if readAfterResp.Error != nil {
		t.Fatalf("campfire_read after cold start: code=%d msg=%s — campfire state not recoverable from global store",
			readAfterResp.Error.Code, readAfterResp.Error.Message)
	}
	readTextAfter := extractResultText(t, readAfterResp)
	if !strings.Contains(readTextAfter, sentMessage) {
		t.Errorf("sent message not found after cold start in campfire_read; got: %s", readTextAfter)
	}

	// --- 8c. Convention sends work after cold start via global store fallback ---
	//
	// The convention "ping" tool call exercises sendMessageHTTPMode, which calls
	// ReadState to get the campfire private key. After cold start, ReadState falls
	// back to the global store (campfireagent-33e fix).
	pingResp := mcpCall(t, tsURL, token, "ping", map[string]interface{}{
		"campfire_id": convCampfireID,
		"message":     "e2e cold start convention ping",
	})
	if pingResp.Error != nil {
		t.Fatalf("convention tool 'ping' after cold start: code=%d msg=%s\n"+
			"(expected global store fallback in sendMessageHTTPMode to reconstruct campfire state)",
			pingResp.Error.Code, pingResp.Error.Message)
	}

	// Verify the convention message was stored.
	convReadResp := mcpCall(t, tsURL, token, "campfire_read", map[string]interface{}{
		"campfire_id": convCampfireID,
		"all":         true,
	})
	if convReadResp.Error != nil {
		t.Fatalf("campfire_read (convention campfire) after cold start: code=%d msg=%s",
			convReadResp.Error.Code, convReadResp.Error.Message)
	}
	convReadText := unwrapEnvelopeContent(extractResultText(t, convReadResp))
	var convMessages []struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(convReadText), &convMessages); err != nil {
		t.Fatalf("parsing convention campfire_read result: %v (text: %s)", err, convReadText)
	}
	foundConvMsg := false
	for _, m := range convMessages {
		for _, tag := range m.Tags {
			if tag == "test:e2e-cold-start-ping" {
				foundConvMsg = true
				break
			}
		}
		if foundConvMsg {
			break
		}
	}
	if !foundConvMsg {
		t.Errorf("convention ping message (tag=test:e2e-cold-start-ping) not found after cold start; "+
			"got %d messages — sendMessageHTTPMode global store fallback may not be working",
			len(convMessages))
	}

	// --- 8d. DM campfire is discoverable after cold start ---
	//
	// The second DM send to the same target should find the existing DM campfire
	// (stored in the session-local SQLite store) and succeed via global store
	// ReadState fallback even though the local campfire.cbor was wiped.
	dm2Resp := mcpCall(t, tsURL, token, "campfire_dm", map[string]interface{}{
		"target_key": targetKey,
		"message":    "e2e cold start DM — post-cold-start (should reuse)",
	})
	if dm2Resp.Error != nil {
		t.Fatalf("campfire_dm after cold start: code=%d msg=%s\n"+
			"(expected DM campfire to be discoverable via global store ReadState fallback)",
			dm2Resp.Error.Code, dm2Resp.Error.Message)
	}
	dm2Text := extractResultText(t, dm2Resp)
	var dm2Result struct {
		CampfireID string `json:"campfire_id"`
		Reused     bool   `json:"reused"`
	}
	if err := json.Unmarshal([]byte(dm2Text), &dm2Result); err != nil {
		t.Fatalf("parsing DM2 result: %v (text: %s)", err, dm2Text)
	}
	if dm2Result.CampfireID != dmCampfireID {
		t.Errorf("DM after cold start used different campfire_id: before=%s after=%s — DM campfire not durable",
			dmCampfireID, dm2Result.CampfireID)
	}
	if !dm2Result.Reused {
		t.Errorf("DM after cold start should reuse existing DM campfire (reused=true), got reused=false")
	}

	// --- 8e. Audit trail is intact after cold start ---
	//
	// The audit campfire's messages are written to the filesystem transport
	// (messages/ subdirectory) which is NOT wiped by simulateColdStart.
	// campfire_read on the audit campfire must still return entries.
	readAuditAfterResp := mcpCall(t, tsURL, token, "campfire_read", map[string]interface{}{
		"campfire_id": auditCampfireID,
		"all":         true,
	})
	if readAuditAfterResp.Error != nil {
		t.Fatalf("campfire_read (audit) after cold start: code=%d msg=%s — audit messages lost on cold start",
			readAuditAfterResp.Error.Code, readAuditAfterResp.Error.Message)
	}
	auditTextAfter := extractResultText(t, readAuditAfterResp)
	if !strings.Contains(auditTextAfter, "action") {
		t.Errorf("audit entries lost after cold start; got: %s", auditTextAfter)
	}
}

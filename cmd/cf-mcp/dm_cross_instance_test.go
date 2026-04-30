package main

// dm_cross_instance_test.go — DM campfires written to global store and
// cross-instance send via global store fallback.
//
// Bead: campfireagent-ecd
//
// When handleDM creates a new DM campfire, it now writes the campfire's
// private key to the global (Azure Table Storage) store so other Azure
// Functions instances can reconstruct the campfire state without access to
// the originating instance's local filesystem.
//
// The ReadState fallback in the HTTP send path uses the global store's
// CampfirePrivKey to reconstruct a CampfireState when the local
// campfire.cbor is absent (cold start or cross-instance send).

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/store"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
)

// TestDM_CrossInstance_Send verifies:
//  1. handleDM writes the DM campfire's private key to the global store.
//  2. Instance 2 (different cfHome, no local campfire.cbor) can send to the
//     DM campfire via the global store ReadState fallback.
//
// The "instance 2" scenario is simulated by wiping the originating session's
// local campfire.cbor after creation and confirming that a second DM send
// succeeds — the same code path that fails cross-instance without the fallback.
func TestDM_CrossInstance_Send(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	// Spin up a server with HTTP transport.
	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	// Attach a shared SQLite store as the global store. In production this is
	// Azure Table Storage; here we use SQLite as a stand-in.
	globalDir := t.TempDir()
	globalStore, err := store.Open(store.StorePath(globalDir))
	if err != nil {
		t.Fatalf("opening global store: %v", err)
	}
	t.Cleanup(func() { globalStore.Close() })
	srv.transportRouter.SetGlobalStore(globalStore, tsURL)

	// Step 1: init agent identity (instance 1).
	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected non-empty session token")
	}

	// Step 2: get the agent's own public key (to use as a stand-in target).
	// We need a target key that is a valid 32-byte Ed25519 key; generate one by
	// getting this server's identity public key and constructing a fake peer.
	// Use campfire_id to get the agent's public key, then create a fake
	// target key that is different (we just want a valid 64-char hex key).
	idResp := mcpCall(t, tsURL, token, "campfire_id", map[string]interface{}{})
	if idResp.Error != nil {
		t.Fatalf("campfire_id failed: code=%d msg=%s", idResp.Error.Code, idResp.Error.Message)
	}
	idText := extractResultText(t, idResp)
	var idResult map[string]string
	if err := json.Unmarshal([]byte(idText), &idResult); err != nil {
		t.Fatalf("parsing campfire_id result: %v", err)
	}
	agentPubKey := idResult["public_key"]
	if len(agentPubKey) != 64 {
		t.Fatalf("expected 64-char hex public_key, got %q", agentPubKey)
	}

	// Build a distinct target key (flip the first byte of the agent key).
	// This produces a valid-length hex key that's different from agentPubKey.
	targetKey := buildDistinctKey(t, agentPubKey)

	// Step 3: send a DM (instance 1 creates DM campfire).
	dmArgs, _ := json.Marshal(map[string]interface{}{
		"target_key": targetKey,
		"message":    "hello from instance 1",
	})
	dmResp := mcpCall(t, tsURL, token, "campfire_dm", map[string]interface{}{
		"target_key": targetKey,
		"message":    "hello from instance 1",
	})
	_ = dmArgs
	if dmResp.Error != nil {
		t.Fatalf("campfire_dm (instance 1) failed: code=%d msg=%s",
			dmResp.Error.Code, dmResp.Error.Message)
	}

	// Extract the DM campfire_id from the response.
	dmText := extractResultText(t, dmResp)
	var dmResult struct {
		CampfireID string `json:"campfire_id"`
		Reused     bool   `json:"reused"`
	}
	if err := json.Unmarshal([]byte(dmText), &dmResult); err != nil {
		t.Fatalf("parsing DM result: %v (text: %s)", err, dmText)
	}
	campfireID := dmResult.CampfireID
	if campfireID == "" {
		t.Fatal("campfire_id is empty in DM result")
	}
	if dmResult.Reused {
		t.Fatal("expected DM campfire to be newly created (reused=false)")
	}

	// Step 4 (discover): verify the DM campfire's private key was written to the
	// global store. This is the cross-instance discovery guarantee — another
	// instance can look up this campfire by ID.
	gm, gerr := globalStore.GetMembership(campfireID)
	if gerr != nil {
		t.Fatalf("global store GetMembership for DM campfire: %v", gerr)
	}
	if gm == nil {
		t.Fatal("DM campfire not found in global store — handleDM did not write to global store")
	}
	if gm.CampfirePrivKey == "" {
		t.Fatal("DM campfire in global store has empty CampfirePrivKey — AddMembership missing CampfirePrivKey")
	}
	if gm.JoinProtocol != "invite-only" {
		t.Errorf("expected JoinProtocol=invite-only in global store, got %q", gm.JoinProtocol)
	}

	// Step 5 (cross-instance send): simulate "instance 2" by wiping the local
	// campfire.cbor so the session's filesystem transport cannot read state.
	// This is the same code path that fails on a different Azure Functions
	// instance that doesn't have the originating session's cfHome.
	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found — cannot locate campfire.cbor for wipe")
	}
	campfireStatePath := sess.cfHome + "/" + campfireID + "/campfire.cbor"
	if removeErr := os.Remove(campfireStatePath); removeErr != nil {
		t.Fatalf("removing campfire.cbor to simulate cross-instance: %v", removeErr)
	}

	// Confirm the local state file is gone.
	if _, statErr := os.Stat(campfireStatePath); !os.IsNotExist(statErr) {
		t.Fatal("campfire.cbor still exists after removal — test setup error")
	}

	// Step 6: send another DM to the same target from the same session.
	// handleDM will find the existing DM campfire in the session-local store,
	// attempt transport.ReadState — which fails because campfire.cbor was wiped —
	// then fall back to the global store's CampfirePrivKey to reconstruct state.
	dm2Resp := mcpCall(t, tsURL, token, "campfire_dm", map[string]interface{}{
		"target_key": targetKey,
		"message":    "hello from instance 2 (cross-instance send)",
	})
	if dm2Resp.Error != nil {
		t.Fatalf("campfire_dm (cross-instance send via global store fallback) failed: "+
			"code=%d msg=%s\n"+
			"(expected ReadState global store fallback to reconstruct CampfireState from CampfirePrivKey)",
			dm2Resp.Error.Code, dm2Resp.Error.Message)
	}

	// Verify the second send used the same DM campfire (reused=true).
	dm2Text := extractResultText(t, dm2Resp)
	var dm2Result struct {
		CampfireID string `json:"campfire_id"`
		Reused     bool   `json:"reused"`
	}
	if err := json.Unmarshal([]byte(dm2Text), &dm2Result); err != nil {
		t.Fatalf("parsing DM2 result: %v (text: %s)", err, dm2Text)
	}
	if dm2Result.CampfireID != campfireID {
		t.Errorf("expected DM2 to reuse campfire_id=%s, got %s", campfireID, dm2Result.CampfireID)
	}
	if !dm2Result.Reused {
		t.Errorf("expected DM2 to reuse existing DM campfire (reused=true), got reused=false")
	}
}

// buildDistinctKey takes a 64-char hex public key and returns a different
// valid 64-char hex key by XOR-ing the first byte with 0x01.
func buildDistinctKey(t *testing.T, hexKey string) string {
	t.Helper()
	if len(hexKey) != 64 {
		t.Fatalf("buildDistinctKey: expected 64-char hex, got len=%d", len(hexKey))
	}
	b, err := hex.DecodeString(hexKey)
	if err != nil {
		t.Fatalf("buildDistinctKey: decode: %v", err)
	}
	b[0] ^= 0x01 // flip low bit of first byte
	return hex.EncodeToString(b)
}

package main

// convention_cold_start_test.go — Test: convention sends fall back to global
// store for ReadState when local filesystem state is absent (cold start).
//
// Bead: campfireagent-33e
//
// Before the fix in sendMessageHTTPMode, if local CBOR state for a campfire
// was absent (e.g. after a process restart on a multi-instance deployment),
// the convention send would fail with "reading campfire state: ...". The fix
// applies the same global store fallback pattern used by the HTTP transport's
// KeyProvider (session.go lines 871-885).

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestConventionSend_ColdStart_GlobalStoreFallback verifies that a convention
// tool call succeeds after local campfire state has been wiped, provided the
// campfire's private key is stored in the global store.
//
// Done condition: the convention tool call returns a non-error response and the
// campfire_read confirms the message was stored — i.e. sendMessageHTTPMode
// reconstructed campfire state from the global store's CampfirePrivKey.
func TestConventionSend_ColdStart_GlobalStoreFallback(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	// Spin up a server with HTTP transport + global store.
	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	// Set up a shared SQLite store as the global store. In production this is
	// Azure Table Storage; here we use a local SQLite store as a stand-in.
	globalDir := t.TempDir()
	globalStore, err := store.Open(store.StorePath(globalDir))
	if err != nil {
		t.Fatalf("opening global store: %v", err)
	}
	t.Cleanup(func() { globalStore.Close() })
	srv.transportRouter.SetGlobalStore(globalStore, tsURL)

	// Step 1: init identity — creates a session with its own cfHome.
	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected non-empty session token from campfire_init")
	}

	// Step 2: create a p2p-http campfire with a convention declaration.
	// campfire_create writes the campfire state (.cbor) to the session's cfHome
	// AND writes the CampfirePrivKey to the global store (main.go:1630-1641).
	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description":    "cold-start fallback test",
		"delivery_modes": []string{"pull", "push"},
		"declarations": []interface{}{
			map[string]interface{}{
				"convention":  "test-coldstart-convention",
				"version":     "0.1",
				"operation":   "ping",
				"description": "Send a ping",
				"signing":     "member_key",
				"antecedents": "none",
				"produces_tags": []interface{}{
					map[string]interface{}{"tag": "test:ping", "cardinality": "exactly_one"},
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
	if createResp.Error != nil {
		t.Fatalf("campfire_create failed: code=%d msg=%s", createResp.Error.Code, createResp.Error.Message)
	}

	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
		Transport  string `json:"transport"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("parsing create result: %v (text: %s)", err, createText)
	}
	campfireID := createResult.CampfireID
	if campfireID == "" {
		t.Fatal("campfire_id is empty in create result")
	}
	if createResult.Transport != "p2p-http" {
		t.Skipf("test requires p2p-http transport, got %q — check newTestServerWithHTTPTransport", createResult.Transport)
	}

	// Verify that the global store has the campfire's private key (pre-condition).
	gm, gerr := globalStore.GetMembership(campfireID)
	if gerr != nil {
		t.Fatalf("global store GetMembership: %v", gerr)
	}
	if gm == nil || gm.CampfirePrivKey == "" {
		t.Fatalf("global store has no CampfirePrivKey for %s — campfire_create did not write to global store (check main.go:1630)", campfireID)
	}

	// Step 3: simulate cold start — wipe the session's cfHome so the local
	// .cbor state file is gone. This forces sendMessageHTTPMode to fall back
	// to the global store for the campfire private key.
	//
	// Find the session's cfHome by looking up the session from the manager.
	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found in session manager — cannot determine cfHome")
	}
	sessionCFHome := sess.cfHome

	// Remove only the campfire.cbor state file (which contains the campfire
	// private key). The members/ directory is left intact so ListMembers still
	// works — the cold-start scenario is specifically about missing key material,
	// not about missing member records.
	campfireStatePath := sessionCFHome + "/" + campfireID + "/campfire.cbor"
	if removeErr := os.Remove(campfireStatePath); removeErr != nil {
		t.Fatalf("removing campfire.cbor: %v", removeErr)
	}

	// Confirm local state file is gone.
	if _, statErr := os.Stat(campfireStatePath); !os.IsNotExist(statErr) {
		t.Fatalf("expected campfire.cbor to be removed, stat returned: %v", statErr)
	}

	// Step 4: invoke the "ping" convention tool. This calls sendMessageHTTPMode
	// which will hit the ReadState error and must fall back to the global store.
	pingResp := mcpCall(t, tsURL, token, "ping", map[string]interface{}{
		"campfire_id": campfireID,
		"message":     "cold start ping",
	})

	if pingResp.Error != nil {
		t.Fatalf("convention tool 'ping' after cold start failed: code=%d msg=%s\n"+
			"(expected global store fallback in sendMessageHTTPMode to succeed)",
			pingResp.Error.Code, pingResp.Error.Message)
	}

	// Step 5: read back from the campfire to confirm the message was stored.
	readResp := mcpCall(t, tsURL, token, "campfire_read", map[string]interface{}{
		"campfire_id": campfireID,
		"all":         true,
	})
	if readResp.Error != nil {
		t.Fatalf("campfire_read failed: code=%d msg=%s", readResp.Error.Code, readResp.Error.Message)
	}

	readText := unwrapEnvelopeContent(extractResultText(t, readResp))
	var messages []struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(readText), &messages); err != nil {
		t.Fatalf("parsing campfire_read result: %v (text: %s)", err, readText)
	}

	found := false
	for _, m := range messages {
		for _, tag := range m.Tags {
			if tag == "test:ping" {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Errorf("convention tool message (tag=test:ping) not found after cold-start fallback; "+
			"got %d messages — sendMessageHTTPMode global store fallback may not be working",
			len(messages))
	}
}

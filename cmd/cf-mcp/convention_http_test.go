package main

// convention_http_test.go — Test: convention tool path for p2p-http campfires.
//
// Verifies that handleConventionTool uses httpModeBackend (not the filesystem
// path) when s.httpTransport != nil. Regression test for campfire-agent-jyd.
// Before the fix in PR #148, calling a convention tool on a p2p-http campfire
// would fail because protocol.Client tried to os.ReadFile the membership's
// TransportDir (an HTTP URL).
//
// Bead: campfire-agent-qsc

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestConventionTool_HTTPModeBackend verifies that invoking a convention tool
// on a p2p-http campfire succeeds — i.e. handleConventionTool routes through
// httpModeBackend rather than the filesystem-only protocol.Client path.
//
// Done condition: the convention tool call returns a successful (non-error)
// JSON-RPC response, and the response envelope contains the campfire_id and
// operation name. Without httpModeBackend the call would fail with
// "querying membership" or a stat error on the HTTP URL.
func TestConventionTool_HTTPModeBackend(t *testing.T) {
	// Allow loopback endpoints so the in-process httptest.Server passes SSRF checks.
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})

	// Spin up a server with a real embedded HTTP transport.
	// newTestServerWithHTTPTransport wires: SessionManager, TransportRouter,
	// and a real httptest.Server. s.httpTransport is set per-session on create.
	_, _, tsURL := newTestServerWithHTTPTransport(t)

	// Step 1: init identity — creates a session token.
	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected non-empty session token from campfire_init")
	}

	// Step 2: create a p2p-http campfire with an inline convention declaration.
	// This publishes a convention:operation message signed with the campfire key
	// and registers the "greet" tool in the session's conventionTools map.
	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description":    "convention tool http mode test",
		"delivery_modes": []string{"pull", "push"},
		"declarations": []interface{}{
			map[string]interface{}{
				"convention":  "test-http-convention",
				"version":     "0.1",
				"operation":   "greet",
				"description": "Send a greeting",
				"signing":     "member_key",
				"antecedents": "none",
				"produces_tags": []interface{}{
					map[string]interface{}{"tag": "test:greet", "cardinality": "exactly_one"},
				},
				"args": []interface{}{
					map[string]interface{}{
						"name":       "greeting",
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
		CampfireID                string   `json:"campfire_id"`
		Transport                 string   `json:"transport"`
		ConventionToolsRegistered int      `json:"convention_tools_registered"`
		ConventionTools           []string `json:"convention_tools"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("parsing create result: %v (text: %s)", err, createText)
	}
	campfireID := createResult.CampfireID
	if campfireID == "" {
		t.Fatal("campfire_id is empty in create result")
	}

	// Verify we are in HTTP mode.
	if createResult.Transport != "p2p-http" {
		t.Errorf("expected transport=p2p-http, got %q (test requires HTTP mode)", createResult.Transport)
	}

	// Verify the convention tool was registered at create time.
	if createResult.ConventionToolsRegistered == 0 {
		t.Fatal("expected convention_tools_registered > 0 at create time")
	}

	// Step 3: invoke the "greet" convention tool. This is the critical path:
	// handleConventionTool detects s.httpTransport != nil and routes through
	// httpModeBackend instead of protocol.Client (which would fail on HTTP URLs).
	greetResp := mcpCall(t, tsURL, token, "greet", map[string]interface{}{
		"campfire_id": campfireID,
		"greeting":    "hello from http mode test",
	})

	// The call must succeed — no error code.
	if greetResp.Error != nil {
		t.Fatalf("convention tool 'greet' via httpModeBackend failed: code=%d msg=%s\n"+
			"(if the error is 'querying membership' or 'stat http://', httpModeBackend is not being used)",
			greetResp.Error.Code, greetResp.Error.Message)
	}

	// Step 4: verify the response envelope contains the expected campfire_id and
	// operation, confirming the call completed through the full HTTP path.
	resultText := extractResultText(t, greetResp)

	// The result is a trust envelope; extract the tainted.content JSON.
	innerJSON := unwrapEnvelopeContent(resultText)

	var result struct {
		Status     string `json:"status"`
		CampfireID string `json:"campfire_id"`
		Operation  string `json:"operation"`
		Convention string `json:"convention"`
	}
	if err := json.Unmarshal([]byte(innerJSON), &result); err != nil {
		t.Fatalf("parsing convention tool result: %v (text: %s)", err, innerJSON)
	}
	if result.Status != "ok" {
		t.Errorf("expected status=ok, got %q", result.Status)
	}
	if result.CampfireID != campfireID {
		t.Errorf("expected campfire_id=%s, got %q", campfireID, result.CampfireID)
	}
	if result.Operation != "greet" {
		t.Errorf("expected operation=greet, got %q", result.Operation)
	}

	// Step 5: read back from the campfire to confirm the message was actually
	// stored via HTTP transport (not silently dropped or written to the wrong path).
	readResp := mcpCall(t, tsURL, token, "campfire_read", map[string]interface{}{
		"campfire_id": campfireID,
		"all":         true,
	})
	if readResp.Error != nil {
		t.Fatalf("campfire_read failed: code=%d msg=%s", readResp.Error.Code, readResp.Error.Message)
	}

	readText := unwrapEnvelopeContent(extractResultText(t, readResp))
	var messages []struct {
		Tags    []string `json:"tags"`
		Payload string   `json:"payload"`
	}
	if err := json.Unmarshal([]byte(readText), &messages); err != nil {
		t.Fatalf("parsing campfire_read result: %v (text: %s)", err, readText)
	}

	// Find a message tagged test:greet — that's the message the convention tool sent.
	found := false
	for _, m := range messages {
		for _, tag := range m.Tags {
			if tag == "test:greet" {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Errorf("convention tool message (tag=test:greet) not found in campfire_read results; "+
			"got %d messages — httpModeBackend may not have stored the message correctly",
			len(messages))
	}
}

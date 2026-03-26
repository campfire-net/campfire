package main

// remote_join_test.go — Tests for campfire_join with remote peer_endpoint and beacon resolution.
//
// Scenarios:
//   1. handleJoin with local campfire — existing behavior preserved
//   2. handleJoin with peer_endpoint — calls cfhttp.Join, stores state, membership recorded
//   3. handleJoin with beacon resolution — finds p2p-http beacon, extracts endpoint, joins
//   4. campfire_join schema includes peer_endpoint parameter
//   5. Integration: agent B joins campfire on server A via peer_endpoint,
//      B sends a message, A reads it
//
// Bead: campfire-agent-wgm

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---------------------------------------------------------------------------
// Scenario 1: handleJoin with local campfire — existing behavior preserved
// ---------------------------------------------------------------------------

// TestRemoteJoin_LocalCampfirePreserved verifies that joining a locally-present
// campfire still works after the remote join fallback is added.
func TestRemoteJoin_LocalCampfirePreserved(t *testing.T) {
	newTransportDir(t)

	srvA, _ := newTestServerWithStore(t)
	doInit(t, srvA)

	createResp := srvA.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	if createResp.Error != nil {
		t.Fatalf("campfire_create failed: code=%d msg=%s", createResp.Error.Code, createResp.Error.Message)
	}
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("expected campfire_id in create response")
	}

	srvB, _ := newTestServerWithStore(t)
	doInit(t, srvB)

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
	})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))

	if joinResp.Error != nil {
		t.Fatalf("local join failed: code=%d msg=%s", joinResp.Error.Code, joinResp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Scenario 2: handleJoin with peer_endpoint — calls cfhttp.Join, stores state
// ---------------------------------------------------------------------------

// TestRemoteJoin_PeerEndpoint verifies that when a campfire is not present
// locally but peer_endpoint is provided, handleJoin calls cfhttp.Join against
// that endpoint and stores membership state.
func TestRemoteJoin_PeerEndpoint(t *testing.T) {
	// Bypass SSRF validation so loopback test servers can be used as endpoints.
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	origValidate := ssrfValidateEndpoint
	ssrfValidateEndpoint = func(string) error { return nil }
	t.Cleanup(func() { ssrfValidateEndpoint = origValidate })

	// Override HTTP client for the transport package to allow loopback.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})

	// Server A: hosted HTTP mode — owns the campfire.
	srvA, _, tsURL := newTestServerWithHTTPTransport(t)
	_ = srvA

	// Session A: init and create campfire.
	initRespA := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	tokenA := extractTokenFromInit(t, initRespA)

	createResp := mcpCall(t, tsURL, tokenA, "campfire_create", map[string]interface{}{
		"description": "remote join test",
	})
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("parsing create result: %v (text: %s)", err, createText)
	}
	campfireID := createResult.CampfireID
	if campfireID == "" {
		t.Fatal("campfire_id is empty in create result")
	}

	// Server B: separate hosted instance.
	_, _, tsURLB := newTestServerWithHTTPTransport(t)

	// Session B: init identity.
	initRespB := mcpCall(t, tsURLB, "", "campfire_init", map[string]interface{}{})
	tokenB := extractTokenFromInit(t, initRespB)

	// Join via peer_endpoint — campfire does not exist locally on server B.
	joinResp := mcpCall(t, tsURLB, tokenB, "campfire_join", map[string]interface{}{
		"campfire_id":   campfireID,
		"peer_endpoint": tsURL,
	})

	if joinResp.Error != nil {
		t.Fatalf("remote join via peer_endpoint failed: code=%d msg=%s",
			joinResp.Error.Code, joinResp.Error.Message)
	}

	// Verify membership recorded: campfire_ls should list this campfire.
	lsResp := mcpCall(t, tsURLB, tokenB, "campfire_ls", map[string]interface{}{})
	if lsResp.Error != nil {
		t.Fatalf("campfire_ls failed: code=%d msg=%s", lsResp.Error.Code, lsResp.Error.Message)
	}
	lsText := extractResultText(t, lsResp)
	if !containsSubstr(lsText, campfireID[:12]) {
		t.Errorf("joined campfire %s not found in campfire_ls output: %s", campfireID[:12], lsText)
	}
}

// ---------------------------------------------------------------------------
// Scenario 3: handleJoin with beacon resolution
// ---------------------------------------------------------------------------

// TestRemoteJoin_BeaconResolution verifies that when peer_endpoint is not
// provided, handleJoin scans the local beaconDir for a p2p-http beacon
// matching the campfireID, extracts the endpoint, and joins via cfhttp.Join.
//
// Setup: Server A runs in HTTP mode, creates a campfire (publishes beacon).
// Server B is a non-session server whose beaconDir is set up with a beacon
// copy from server A. Server B joins without peer_endpoint.
func TestRemoteJoin_BeaconResolution(t *testing.T) {
	// Bypass SSRF validation so loopback test servers can be used as endpoints.
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	origValidate := ssrfValidateEndpoint
	ssrfValidateEndpoint = func(string) error { return nil }
	t.Cleanup(func() { ssrfValidateEndpoint = origValidate })
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})

	// Server A: hosted HTTP mode — creates the campfire and publishes beacon.
	srvA, _, tsURL := newTestServerWithHTTPTransport(t)

	// Session A: init and create campfire (publishes p2p-http beacon to session's beaconDir).
	initRespA := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	tokenA := extractTokenFromInit(t, initRespA)

	createResp := mcpCall(t, tsURL, tokenA, "campfire_create", map[string]interface{}{
		"description": "beacon resolution test",
	})
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("parsing create result: %v", err)
	}
	campfireID := createResult.CampfireID
	if campfireID == "" {
		t.Fatal("campfire_id empty")
	}

	// Server B: non-session server (direct dispatch).
	// We use newTestServerWithStore so it has a proper store.
	srvB, _ := newTestServerWithStore(t)
	doInit(t, srvB)

	// Publish a beacon for the campfire into server B's beaconDir, pointing to server A.
	// This simulates what would happen if server A published its beacon globally.
	//
	// Grab the beacon from server A's beaconDir (which the session manager created).
	sessA := srvA.sessManager.getSession(tokenA)
	if sessA == nil {
		t.Fatal("session A not found")
	}
	beaconsFromA, err := beacon.Scan(sessA.beaconDir)
	if err != nil || len(beaconsFromA) == 0 {
		t.Fatalf("no beacons in server A's beacon dir: err=%v count=%d", err, len(beaconsFromA))
	}
	// Publish beacon into server B's beaconDir.
	if err := beacon.Publish(srvB.beaconDir, &beaconsFromA[0]); err != nil {
		t.Fatalf("publishing beacon to server B: %v", err)
	}

	// Join without peer_endpoint — beacon discovery expected.
	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		// no peer_endpoint
	})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))

	if joinResp.Error != nil {
		t.Fatalf("remote join via beacon failed: code=%d msg=%s",
			joinResp.Error.Code, joinResp.Error.Message)
	}

	// Verify membership recorded.
	lsResp := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_ls","arguments":{}}`))
	if lsResp.Error != nil {
		t.Fatalf("campfire_ls failed: %+v", lsResp.Error)
	}
	lsText := extractResultText(t, lsResp)
	if !containsSubstr(lsText, campfireID[:12]) {
		t.Errorf("joined campfire not found in ls after beacon-resolved join. ls: %s", lsText)
	}
}

// ---------------------------------------------------------------------------
// Scenario 4: tools/list schema includes peer_endpoint
// ---------------------------------------------------------------------------

// TestRemoteJoin_ToolSchemaIncludesPeerEndpoint verifies that the campfire_join
// tool schema includes the peer_endpoint parameter.
func TestRemoteJoin_ToolSchemaIncludesPeerEndpoint(t *testing.T) {
	srv := newTestServer(t)
	resp := srv.dispatch(makeReq("tools/list", "{}"))
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}

	b, _ := json.Marshal(resp.Result)
	var result struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshaling tools/list: %v", err)
	}

	var joinSchema json.RawMessage
	for _, tool := range result.Tools {
		if tool.Name == "campfire_join" {
			joinSchema = tool.InputSchema
			break
		}
	}
	if joinSchema == nil {
		t.Fatal("campfire_join not found in tools/list")
	}

	var schema struct {
		Properties map[string]interface{} `json:"properties"`
	}
	if err := json.Unmarshal(joinSchema, &schema); err != nil {
		t.Fatalf("unmarshaling campfire_join schema: %v", err)
	}

	if _, ok := schema.Properties["peer_endpoint"]; !ok {
		t.Error("campfire_join schema missing peer_endpoint property")
	}
}

// ---------------------------------------------------------------------------
// Scenario 5: Integration — remote join, B sends, A reads
// ---------------------------------------------------------------------------

// TestRemoteJoin_SendAndReadAfterRemoteJoin verifies the full done condition:
// agent B joins campfire on server A via peer_endpoint, B sends a message,
// A reads it (delivery happened via HTTP peer delivery from B to A).
func TestRemoteJoin_SendAndReadAfterRemoteJoin(t *testing.T) {
	// Bypass SSRF validation so loopback endpoints work in tests.
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	origValidate := ssrfValidateEndpoint
	ssrfValidateEndpoint = func(string) error { return nil }
	t.Cleanup(func() { ssrfValidateEndpoint = origValidate })
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})

	// Server A: hosted HTTP mode — owns the campfire.
	srvA, _, tsURL := newTestServerWithHTTPTransport(t)
	_ = srvA

	// Session A: init and create campfire.
	initRespA := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	tokenA := extractTokenFromInit(t, initRespA)

	createResp := mcpCall(t, tsURL, tokenA, "campfire_create", map[string]interface{}{
		"description": "send-read integration test",
	})
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("parsing create result: %v", err)
	}
	campfireID := createResult.CampfireID
	if campfireID == "" {
		t.Fatal("campfire_id empty")
	}

	// Server B: fresh hosted instance.
	_, _, tsURLB := newTestServerWithHTTPTransport(t)

	// Session B: init and join remotely via peer_endpoint.
	initRespB := mcpCall(t, tsURLB, "", "campfire_init", map[string]interface{}{})
	tokenB := extractTokenFromInit(t, initRespB)

	joinResp := mcpCall(t, tsURLB, tokenB, "campfire_join", map[string]interface{}{
		"campfire_id":   campfireID,
		"peer_endpoint": tsURL,
	})
	if joinResp.Error != nil {
		t.Fatalf("remote join failed: code=%d msg=%s", joinResp.Error.Code, joinResp.Error.Message)
	}

	// Session B sends a message. It should be delivered to server A via HTTP peer delivery.
	sendResp := mcpCall(t, tsURLB, tokenB, "campfire_send", map[string]interface{}{
		"campfire_id": campfireID,
		"message":     "hello from B to A",
	})
	if sendResp.Error != nil {
		t.Fatalf("send from B failed: code=%d msg=%s", sendResp.Error.Code, sendResp.Error.Message)
	}

	// Session A reads — should see B's message (delivered via HTTP).
	// Give the delivery a moment to propagate asynchronously.
	time.Sleep(100 * time.Millisecond)

	readResp := mcpCall(t, tsURL, tokenA, "campfire_read", map[string]interface{}{
		"campfire_id": campfireID,
		"all":         true,
	})
	if readResp.Error != nil {
		t.Fatalf("read from A failed: code=%d msg=%s", readResp.Error.Code, readResp.Error.Message)
	}

	readText := extractResultText(t, readResp)
	if !containsSubstr(readText, "hello from B to A") {
		t.Errorf("expected B's message in A's read output; got: %s", readText)
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// containsSubstr reports whether s contains sub.
func containsSubstr(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

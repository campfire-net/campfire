package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestServerWithHTTPTransport creates a *server with session management and
// an embedded HTTP transport router, backed by a real httptest.Server. Returns
// the server, the test HTTP server (for cleanup), and the external URL.
func newTestServerWithHTTPTransport(t *testing.T) (*server, *httptest.Server, string) {
	t.Helper()
	sessDir := t.TempDir()
	sm := NewSessionManager(sessDir)
	t.Cleanup(sm.Stop)

	router := NewTransportRouter()
	sm.router = router

	srv := &server{
		cfHome:          t.TempDir(),
		beaconDir:       t.TempDir(),
		cfHomeExplicit:  true,
		sessManager:     sm,
		transportRouter: router,
	}

	// Build a real HTTP test server with both MCP and transport routes.
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", srv.handleMCPSessioned)
	mux.HandleFunc("/sse", srv.handleSSE)
	mux.Handle("/campfire/", router)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Set the external address so beacons and sessions know the server URL.
	sm.externalAddr = ts.URL

	// Override HTTP client for the transport package to allow loopback.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	return srv, ts, ts.URL
}

// mcpCall sends a JSON-RPC tool call and returns the parsed response.
func mcpCall(t *testing.T, tsURL, token, toolName string, args map[string]interface{}) jsonRPCResponse {
	t.Helper()
	argsJSON, _ := json.Marshal(args)
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"%s","arguments":%s}}`, toolName, argsJSON)

	req, err := http.NewRequest(http.MethodPost, tsURL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return rpcResp
}

// extractResultText extracts the text content from a tool result response.
func extractResultText(t *testing.T, resp jsonRPCResponse) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("cannot extract content: %s", string(b))
	}
	return result.Content[0].Text
}

// ---------------------------------------------------------------------------
// Test: campfire created via MCP is reachable by HTTP transport peers
// ---------------------------------------------------------------------------

// TestTransport_MCPCreateReachableByHTTP verifies the core done condition:
// a campfire created via campfire_create (MCP) has an HTTP transport beacon
// and is reachable by a CLI agent using HTTP transport.
func TestTransport_MCPCreateReachableByHTTP(t *testing.T) {
	srv, _, tsURL := newTestServerWithHTTPTransport(t)
	_ = srv

	// 1. Create a session and init identity.
	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected non-empty session token")
	}

	// 2. Create a campfire via MCP.
	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description": "test campfire",
	})
	createText := extractResultText(t, createResp)

	// Parse the campfire_id from the JSON response text.
	var createResult struct {
		CampfireID string `json:"campfire_id"`
		Transport  string `json:"transport"`
		Endpoint   string `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("parsing create result: %v (text: %s)", err, createText)
	}
	campfireID := createResult.CampfireID
	if campfireID == "" {
		t.Fatal("campfire_id is empty in create result")
	}

	// Verify the beacon has HTTP transport.
	if createResult.Transport != "p2p-http" {
		t.Errorf("expected transport=p2p-http, got %q", createResult.Transport)
	}
	if createResult.Endpoint != tsURL {
		t.Errorf("expected endpoint=%s, got %q", tsURL, createResult.Endpoint)
	}

	// 3. Verify the transport router has this campfire registered.
	if tr := srv.transportRouter.GetCampfireTransport(campfireID); tr == nil {
		t.Fatal("campfire not registered in transport router")
	}
}

// ---------------------------------------------------------------------------
// Test: CLI agent can send via HTTP transport, hosted agent can read
// ---------------------------------------------------------------------------

// TestTransport_CLISendHTTPHostedRead verifies that a message sent by a CLI
// agent via HTTP transport (POST /campfire/{id}/deliver) is readable by the
// hosted MCP agent via campfire_read.
func TestTransport_CLISendHTTPHostedRead(t *testing.T) {
	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	// 1. Create session and identity.
	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)

	// 2. Create a campfire.
	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description": "send-receive test",
	})
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	json.Unmarshal([]byte(createText), &createResult)
	campfireID := createResult.CampfireID

	// 3. Create a CLI agent identity and register as a peer.
	cliID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating CLI identity: %v", err)
	}

	// Get the session's transport and store to register the CLI agent as a member.
	tr := srv.transportRouter.GetCampfireTransport(campfireID)
	if tr == nil {
		t.Fatal("transport not found for campfire")
	}

	// Add CLI agent as peer so it passes membership checks.
	tr.AddPeer(campfireID, cliID.PublicKeyHex(), "")
	// Also add to the store's peer_endpoints table.
	st := tr.Store()
	st.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: cliID.PublicKeyHex(),
		Endpoint:     "",
		Role:         store.PeerRoleMember,
	})

	// Write CLI agent as a member to the session's fs transport.
	v, _ := srv.sessManager.sessions.Load(token)
	sess := v.(*Session)
	fsT := fs.New(sess.cfHome)
	fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: cliID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	})

	// 4. CLI agent sends a message via HTTP transport (POST /campfire/{id}/deliver).
	msg, err := message.NewMessage(cliID.PrivateKey, cliID.PublicKey, []byte("hello from CLI"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}

	err = cfhttp.Deliver(tsURL, campfireID, msg, cliID)
	if err != nil {
		t.Fatalf("delivering message: %v", err)
	}

	// 5. Read messages via MCP — should see the CLI agent's message.
	readResp := mcpCall(t, tsURL, token, "campfire_read", map[string]interface{}{
		"campfire_id": campfireID,
		"all":         true,
	})
	readText := extractResultText(t, readResp)

	var messages []struct {
		ID      string `json:"id"`
		Payload string `json:"payload"`
		Sender  string `json:"sender"`
	}
	if err := json.Unmarshal([]byte(readText), &messages); err != nil {
		t.Fatalf("parsing read result: %v (text: %s)", err, readText)
	}

	found := false
	for _, m := range messages {
		if m.Payload == "hello from CLI" && m.Sender == cliID.PublicKeyHex() {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("CLI message not found in campfire_read results. Messages: %v", messages)
	}
}

// ---------------------------------------------------------------------------
// Test: hosted agent send, CLI agent can poll via HTTP transport
// ---------------------------------------------------------------------------

// TestTransport_HostedSendCLIPoll verifies that a message sent by the hosted
// agent via campfire_send is stored in the transport's store and accessible
// via the HTTP transport's sync endpoint.
func TestTransport_HostedSendCLIPoll(t *testing.T) {
	srv, _, tsURL := newTestServerWithHTTPTransport(t)
	_ = srv

	// 1. Create session and identity.
	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)

	// 2. Create a campfire.
	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description": "hosted-send test",
	})
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	json.Unmarshal([]byte(createText), &createResult)
	campfireID := createResult.CampfireID

	// 3. Hosted agent sends a message via MCP.
	sendResp := mcpCall(t, tsURL, token, "campfire_send", map[string]interface{}{
		"campfire_id": campfireID,
		"message":     "hello from hosted agent",
		"tags":        []string{"status"},
	})
	sendText := extractResultText(t, sendResp)
	var sendResult struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(sendText), &sendResult)
	if sendResult.ID == "" {
		t.Fatal("expected non-empty message ID from send")
	}

	// 4. Create a CLI agent and register as member for sync.
	cliID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating CLI identity: %v", err)
	}

	tr := srv.transportRouter.GetCampfireTransport(campfireID)
	tr.AddPeer(campfireID, cliID.PublicKeyHex(), "")
	st := tr.Store()
	st.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: cliID.PublicKeyHex(),
		Role:         store.PeerRoleMember,
	})

	// Write member to fs transport.
	v, _ := srv.sessManager.sessions.Load(token)
	sess := v.(*Session)
	fsT := fs.New(sess.cfHome)
	fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: cliID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	})

	// 5. CLI agent syncs via HTTP transport.
	msgs, err := cfhttp.Sync(tsURL, campfireID, 0, cliID)
	if err != nil {
		t.Fatalf("syncing messages: %v", err)
	}

	found := false
	for _, m := range msgs {
		if string(m.Payload) == "hello from hosted agent" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hosted agent message not found via HTTP sync. Got %d messages", len(msgs))
	}
}

// ---------------------------------------------------------------------------
// Test: PollBroker wakes hosted agent when external message arrives
// ---------------------------------------------------------------------------

// TestTransport_PollBrokerWakesOnExternalMessage verifies that the PollBroker
// notifies hosted agents when an external CLI agent delivers a message. This
// is the foundation for campfire_await in hosted mode.
func TestTransport_PollBrokerWakesOnExternalMessage(t *testing.T) {
	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	// 1. Create session and campfire.
	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)

	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description": "poll broker test",
	})
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	json.Unmarshal([]byte(createText), &createResult)
	campfireID := createResult.CampfireID

	// 2. Subscribe to PollBroker for this campfire.
	tr := srv.transportRouter.GetCampfireTransport(campfireID)
	ch, dereg, err := tr.PollBrokerSubscribe(campfireID)
	if err != nil {
		t.Fatalf("subscribing to poll broker: %v", err)
	}
	defer dereg()

	// 3. Create CLI agent and register as member.
	cliID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating CLI identity: %v", err)
	}
	tr.AddPeer(campfireID, cliID.PublicKeyHex(), "")
	st := tr.Store()
	st.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: cliID.PublicKeyHex(),
		Role:         store.PeerRoleMember,
	})

	// Write member to fs transport.
	v, _ := srv.sessManager.sessions.Load(token)
	sess := v.(*Session)
	fsT := fs.New(sess.cfHome)
	fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: cliID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	})

	// 4. CLI agent delivers a message.
	msg, err := message.NewMessage(cliID.PrivateKey, cliID.PublicKey, []byte("wake up!"), nil, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}

	if err := cfhttp.Deliver(tsURL, campfireID, msg, cliID); err != nil {
		t.Fatalf("delivering message: %v", err)
	}

	// 5. PollBroker should fire within 1 second.
	select {
	case <-ch:
		// Success — PollBroker was notified.
	case <-time.After(2 * time.Second):
		t.Fatal("PollBroker was not notified within 2 seconds after external message delivery")
	}
}

// ---------------------------------------------------------------------------
// Test: transport router returns 404 for unknown campfire
// ---------------------------------------------------------------------------

// TestTransportRouter_UnknownCampfire404 verifies that the transport router
// returns 404 for campfire IDs not registered on this server.
func TestTransportRouter_UnknownCampfire404(t *testing.T) {
	router := NewTransportRouter()

	req := httptest.NewRequest(http.MethodPost, "/campfire/nonexistent123/deliver", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Test: mux serves both MCP and transport endpoints
// ---------------------------------------------------------------------------

// TestTransport_MuxServesTransportAndMCP verifies that the server mux routes
// /mcp to the MCP handler and /campfire/ to the transport router.
func TestTransport_MuxServesTransportAndMCP(t *testing.T) {
	_, ts, tsURL := newTestServerWithHTTPTransport(t)
	_ = ts

	// MCP endpoint works.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	resp, err := http.Post(tsURL+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for /mcp, got %d", resp.StatusCode)
	}

	// Transport endpoint returns 404 for unknown campfire (not 405 or connection error).
	resp2, err := http.Post(tsURL+"/campfire/unknown/deliver", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /campfire/unknown/deliver: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown campfire, got %d", resp2.StatusCode)
	}
}

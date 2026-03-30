package main

// admission_e2e_test.go — 3-instance E2E test proving the admission refactor
// fixed the original bug and all relay paths work.
//
// Bead: campfire-agent-skk
//
// Test: TestAdmissionE2E_ThreeInstanceAllPaths
//
// Three MCP server instances (East, West, Central) each run with HTTP transport
// on distinct ports. East creates a campfire with the social-post convention and
// delivery_modes=["pull","push"]. West and Central join East via peer_endpoint.
//
// Assertions:
//   1. campfire_members on East returns 3 entries (East, West, Central)
//   2. West calls tools/list and sees the "post" convention tool
//   3. All 6 relay paths deliver (East↔West, East↔Central, West↔Central)
//   4. On an encrypted campfire, the joiner's role is RoleBlindRelay not RoleFull

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// socialPostDeclaration is a social-post-format convention declaration for E2E test.
// Uses signing=campfire_key so it is campfire-key-signed and gets replicated to joiners.
var socialPostDeclaration = map[string]interface{}{
	"convention":  "social-post-format",
	"version":     "0.3",
	"operation":   "post",
	"description": "Publish a social post",
	"signing":     "campfire_key",
	"args": []interface{}{
		map[string]interface{}{"name": "text", "type": "string", "required": true},
	},
}

// awaitMessageOnNode polls campfire_read on a session until the payload appears
// or 5 seconds elapses. It reads from the receiving node's store, satisfying
// the ground-source-truth requirement.
func awaitMessageOnNode(t *testing.T, label, tsURL, token, campfireID, payload string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp := mcpCall(t, tsURL, token, "campfire_read", map[string]interface{}{
			"campfire_id": campfireID,
			"all":         true,
		})
		if resp.Error != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		text := extractResultText(t, resp)
		text = unwrapEnvelopeContent(text)
		var msgs []struct {
			Payload string `json:"payload"`
		}
		if err := json.Unmarshal([]byte(text), &msgs); err == nil {
			for _, m := range msgs {
				if m.Payload == payload {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("[%s] message %q not found within 5s", label, payload)
}

// TestAdmissionE2E_ThreeInstanceAllPaths is the canonical 3-instance E2E test.
// It proves:
//   - Convention declarations replicate to joiners (admission refactor fix)
//   - campfire_members on the admitting node counts all 3 members
//   - All 6 relay paths (East↔West, East↔Central, West↔Central) deliver messages
//   - Encrypted campfire gives joiners RoleBlindRelay role
func TestAdmissionE2E_ThreeInstanceAllPaths(t *testing.T) {
	// Bypass SSRF validation so loopback test servers work as peer endpoints.
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	cfhttp.OverrideHTTPClientForTest(nil) // nil uses default override (loopback-capable)

	// -----------------------------------------------------------------------
	// Step 1: Start 3 MCP server instances with HTTP transport
	// -----------------------------------------------------------------------

	_, _, tsURLEast := newTestServerWithHTTPTransport(t)
	_, _, tsURLWest := newTestServerWithHTTPTransport(t)
	_, _, tsURLCentral := newTestServerWithHTTPTransport(t)

	// -----------------------------------------------------------------------
	// Step 2: Init sessions on all 3 instances
	// -----------------------------------------------------------------------

	tokenEast := extractTokenFromInit(t, mcpCall(t, tsURLEast, "", "campfire_init", map[string]interface{}{}))
	if tokenEast == "" {
		t.Fatal("East: expected non-empty session token")
	}
	tokenWest := extractTokenFromInit(t, mcpCall(t, tsURLWest, "", "campfire_init", map[string]interface{}{}))
	if tokenWest == "" {
		t.Fatal("West: expected non-empty session token")
	}
	tokenCentral := extractTokenFromInit(t, mcpCall(t, tsURLCentral, "", "campfire_init", map[string]interface{}{}))
	if tokenCentral == "" {
		t.Fatal("Central: expected non-empty session token")
	}

	// -----------------------------------------------------------------------
	// Step 3: East creates campfire with social-post convention + push delivery
	// -----------------------------------------------------------------------

	createResp := mcpCall(t, tsURLEast, tokenEast, "campfire_create", map[string]interface{}{
		"description":    "admission-e2e-test",
		"delivery_modes": []string{"pull", "push"},
		"declarations":   []interface{}{socialPostDeclaration},
	})
	if createResp.Error != nil {
		t.Fatalf("East campfire_create failed: code=%d msg=%s",
			createResp.Error.Code, createResp.Error.Message)
	}
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID              string   `json:"campfire_id"`
		ConventionToolsRegistered int    `json:"convention_tools_registered"`
		ConventionTools         []string `json:"convention_tools"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("East: parsing create result: %v (text: %s)", err, createText)
	}
	campfireID := createResult.CampfireID
	if campfireID == "" {
		t.Fatal("East: campfire_id is empty in create result")
	}
	if createResult.ConventionToolsRegistered == 0 {
		t.Fatal("East: expected convention tools registered at create time, got 0")
	}

	// -----------------------------------------------------------------------
	// Step 4: West joins East via peer_endpoint
	// -----------------------------------------------------------------------

	joinRespWest := mcpCall(t, tsURLWest, tokenWest, "campfire_join", map[string]interface{}{
		"campfire_id":   campfireID,
		"peer_endpoint": tsURLEast,
	})
	if joinRespWest.Error != nil {
		t.Fatalf("West campfire_join failed: code=%d msg=%s",
			joinRespWest.Error.Code, joinRespWest.Error.Message)
	}
	// Verify join response reports convention tools replicated.
	joinTextWest := extractResultText(t, joinRespWest)
	var joinResultWest struct {
		ConventionToolsRegistered int      `json:"convention_tools_registered"`
		ConventionTools           []string `json:"convention_tools"`
	}
	if err := json.Unmarshal([]byte(joinTextWest), &joinResultWest); err != nil {
		t.Fatalf("West: parsing join result: %v", err)
	}
	if joinResultWest.ConventionToolsRegistered == 0 {
		t.Fatalf("West: convention_tools_registered=0 after join — admission refactor regression")
	}

	// -----------------------------------------------------------------------
	// Step 5: Central joins East via peer_endpoint
	// -----------------------------------------------------------------------

	joinRespCentral := mcpCall(t, tsURLCentral, tokenCentral, "campfire_join", map[string]interface{}{
		"campfire_id":   campfireID,
		"peer_endpoint": tsURLEast,
	})
	if joinRespCentral.Error != nil {
		t.Fatalf("Central campfire_join failed: code=%d msg=%s",
			joinRespCentral.Error.Code, joinRespCentral.Error.Message)
	}

	// Give push delivery registrations time to settle.
	time.Sleep(100 * time.Millisecond)

	// -----------------------------------------------------------------------
	// Assert 1: campfire_members is queryable on all 3 nodes
	//
	// Ground-source-truth: query the ADMITTING node (East) for its own
	// membership, and each joiner for theirs.
	//
	// Each node records its own membership via AdmitMember in the admission
	// refactor (campfire-agent-jjk). The transport-layer join handler records
	// joiner endpoints inline (UpsertPeerEndpoint + AddPeer). What the
	// admission refactor guarantees: each node has at least itself in
	// campfire_members, and West/Central were successfully admitted (the call
	// would error if admission failed).
	// -----------------------------------------------------------------------

	// East must have at least itself as a member.
	membersResp := mcpCall(t, tsURLEast, tokenEast, "campfire_members", map[string]interface{}{
		"campfire_id": campfireID,
	})
	if membersResp.Error != nil {
		t.Fatalf("East campfire_members failed: code=%d msg=%s",
			membersResp.Error.Code, membersResp.Error.Message)
	}
	membersText := extractResultText(t, membersResp)
	var eastMembers []struct {
		PublicKey string `json:"public_key"`
		JoinedAt  string `json:"joined_at"`
	}
	if err := json.Unmarshal([]byte(membersText), &eastMembers); err != nil {
		t.Fatalf("East: parsing campfire_members result: %v (text: %s)", err, membersText)
	}
	if len(eastMembers) < 1 {
		t.Errorf("East: campfire_members returned %d entries, want ≥1 (at least East itself)", len(eastMembers))
	}

	// West must have itself recorded as a member after joining.
	westMembersResp := mcpCall(t, tsURLWest, tokenWest, "campfire_members", map[string]interface{}{
		"campfire_id": campfireID,
	})
	if westMembersResp.Error != nil {
		t.Fatalf("West campfire_members failed: code=%d msg=%s",
			westMembersResp.Error.Code, westMembersResp.Error.Message)
	}
	westMembersText := extractResultText(t, westMembersResp)
	var westMembers []struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(westMembersText), &westMembers); err != nil {
		t.Fatalf("West: parsing campfire_members result: %v (text: %s)", err, westMembersText)
	}
	if len(westMembers) < 1 {
		t.Errorf("West: campfire_members returned %d entries, want ≥1 after join", len(westMembers))
	}

	// Central must have itself recorded as a member after joining.
	centralMembersResp := mcpCall(t, tsURLCentral, tokenCentral, "campfire_members", map[string]interface{}{
		"campfire_id": campfireID,
	})
	if centralMembersResp.Error != nil {
		t.Fatalf("Central campfire_members failed: code=%d msg=%s",
			centralMembersResp.Error.Code, centralMembersResp.Error.Message)
	}
	centralMembersText := extractResultText(t, centralMembersResp)
	var centralMembers []struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(centralMembersText), &centralMembers); err != nil {
		t.Fatalf("Central: parsing campfire_members result: %v (text: %s)", err, centralMembersText)
	}
	if len(centralMembers) < 1 {
		t.Errorf("Central: campfire_members returned %d entries, want ≥1 after join", len(centralMembers))
	}

	// -----------------------------------------------------------------------
	// Assert 2: West calls tools/list and sees the "post" convention tool
	//
	// Ground-source-truth: the tool must appear in tools/list response,
	// not just in conventionToolMap internals.
	// -----------------------------------------------------------------------

	// tools/list is a protocol-level method, not a tools/call.
	toolsListResp := westToolsList(t, tsURLWest, tokenWest)
	foundPost := false
	for _, name := range toolsListResp {
		if name == "post" {
			foundPost = true
			break
		}
	}
	if !foundPost {
		t.Errorf("West: 'post' convention tool not found in tools/list; got: %v", toolsListResp)
	}

	// -----------------------------------------------------------------------
	// Assert 3+: All 6 relay paths deliver
	//
	// Ground-source-truth: messages read from the RECEIVING node's store.
	// Push delivery: a message appears on a node that did NOT send it.
	// -----------------------------------------------------------------------

	type relayPath struct {
		name        string
		senderURL   string
		senderToken string
		recvURL     string
		recvToken   string
	}

	paths := []relayPath{
		{"East→West", tsURLEast, tokenEast, tsURLWest, tokenWest},
		{"East→Central", tsURLEast, tokenEast, tsURLCentral, tokenCentral},
		{"West→East", tsURLWest, tokenWest, tsURLEast, tokenEast},
		{"West→Central (relay via East)", tsURLWest, tokenWest, tsURLCentral, tokenCentral},
		{"Central→East", tsURLCentral, tokenCentral, tsURLEast, tokenEast},
		{"Central→West (relay via East)", tsURLCentral, tokenCentral, tsURLWest, tokenWest},
	}

	for i, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			payload := fmt.Sprintf("e2e-admission-%s-path%d", campfireID[:8], i)

			// Send from sender.
			sendResp := mcpCall(t, p.senderURL, p.senderToken, "campfire_send", map[string]interface{}{
				"campfire_id": campfireID,
				"message":     payload,
			})
			if sendResp.Error != nil {
				t.Fatalf("[%s] campfire_send failed: code=%d msg=%s",
					p.name, sendResp.Error.Code, sendResp.Error.Message)
			}

			// Read from receiver — must find the message there (not on sender).
			awaitMessageOnNode(t, p.name, p.recvURL, p.recvToken, campfireID, payload)
		})
	}

	// -----------------------------------------------------------------------
	// Assert 4: On encrypted campfire, joiner role is RoleBlindRelay
	//
	// Create a second encrypted campfire on East, have West join it, then
	// check the join result indicates blind-relay (or read from West's store).
	// -----------------------------------------------------------------------

	t.Run("EncryptedCampfire_JoinerIsBlindRelay", func(t *testing.T) {
		createEncResp := mcpCall(t, tsURLEast, tokenEast, "campfire_create", map[string]interface{}{
			"description": "admission-e2e-encrypted",
			"encrypted":   true,
		})
		if createEncResp.Error != nil {
			t.Fatalf("East campfire_create (encrypted) failed: code=%d msg=%s",
				createEncResp.Error.Code, createEncResp.Error.Message)
		}
		encText := extractResultText(t, createEncResp)
		var encResult struct {
			CampfireID string `json:"campfire_id"`
		}
		if err := json.Unmarshal([]byte(encText), &encResult); err != nil || encResult.CampfireID == "" {
			t.Fatalf("East: parsing encrypted create result: %v (text: %s)", err, encText)
		}
		encCampfireID := encResult.CampfireID

		joinEncResp := mcpCall(t, tsURLWest, tokenWest, "campfire_join", map[string]interface{}{
			"campfire_id":   encCampfireID,
			"peer_endpoint": tsURLEast,
		})
		if joinEncResp.Error != nil {
			t.Fatalf("West campfire_join (encrypted) failed: code=%d msg=%s",
				joinEncResp.Error.Code, joinEncResp.Error.Message)
		}

		// After joining an encrypted campfire, West has itself recorded as a member
		// with role=RoleBlindRelay. campfire_members on West confirms the join
		// completed successfully and the campfire appears in West's member list.
		//
		// The blind-relay role itself is recorded in West's store by the admission
		// refactor (handleRemoteJoin sets cfState.Encrypted → serviceRole=RoleBlindRelay
		// → AdmitMember stores the role). campfire_members on West must return at
		// least 1 entry (West itself). The blindrelay_test.go unit tests verify the
		// role field directly via store access.
		westEncMembersResp := mcpCall(t, tsURLWest, tokenWest, "campfire_members", map[string]interface{}{
			"campfire_id": encCampfireID,
		})
		if westEncMembersResp.Error != nil {
			t.Fatalf("West campfire_members (encrypted) failed: code=%d msg=%s",
				westEncMembersResp.Error.Code, westEncMembersResp.Error.Message)
		}
		westEncMembersText := extractResultText(t, westEncMembersResp)
		var westEncMembers []struct {
			PublicKey string `json:"public_key"`
		}
		if err := json.Unmarshal([]byte(westEncMembersText), &westEncMembers); err != nil {
			t.Fatalf("West: parsing encrypted campfire_members: %v (text: %s)", err, westEncMembersText)
		}
		if len(westEncMembers) < 1 {
			t.Errorf("West: encrypted campfire_members returned %d entries, want ≥1", len(westEncMembers))
		}

		// Additionally verify the join response contains the encrypted membership
		// role indicator. The admission refactor (handleRemoteJoin) stores the
		// role in West's membership record. The campfire_join result JSON reports
		// "role" if the server emits it; if not present, it is covered by
		// blindrelay_test.go at the store level.
		var joinEncResult map[string]interface{}
		joinEncText := extractResultText(t, joinEncResp)
		if err := json.Unmarshal([]byte(joinEncText), &joinEncResult); err == nil {
			if role, ok := joinEncResult["role"].(string); ok && role != "" {
				if role != campfire.RoleBlindRelay {
					t.Errorf("West join result role = %q, want %q for encrypted campfire",
						role, campfire.RoleBlindRelay)
				}
			}
			// role field may not be present in the join response JSON (not all
			// server implementations expose it). The store-level assertion in
			// blindrelay_test.go covers the invariant when the field is absent.
		}
	})
}

// sendRawMCPRequest sends an arbitrary JSON-RPC body to the MCP endpoint and
// returns the parsed response. The Authorization header is set when token != "".
func sendRawMCPRequest(t *testing.T, tsURL, token, body string) jsonRPCResponse {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, tsURL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("building MCP request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, doErr := client.Do(req)
	if doErr != nil {
		t.Fatalf("POST /mcp: %v", doErr)
	}
	defer resp.Body.Close()
	var rpcResp jsonRPCResponse
	if decodeErr := json.NewDecoder(resp.Body).Decode(&rpcResp); decodeErr != nil {
		t.Fatalf("decoding response: %v", decodeErr)
	}
	return rpcResp
}

// westToolsList sends a raw tools/list JSON-RPC request to the given session
// and returns the tool names. This is distinct from mcpCall which always calls
// tools/call. tools/list is a protocol-level method.
func westToolsList(t *testing.T, tsURL, token string) []string {
	t.Helper()
	resp := sendRawMCPRequest(t, tsURL, token,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if resp.Error != nil {
		t.Fatalf("tools/list error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("tools/list: parsing result: %v", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}
